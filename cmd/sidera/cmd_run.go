package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	runtimeDebug "runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Miku0139oao/sidera-core"
	"github.com/Miku0139oao/sidera-core/config"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	apiService "github.com/Miku0139oao/sidera-core/service/api"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badjson"

	"github.com/spf13/cobra"
)

var commandRun = &cobra.Command{
	Use:   "run",
	Short: "Run service",
	Run: func(cmd *cobra.Command, args []string) {
		err := run()
		if err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	mainCommand.AddCommand(commandRun)
	mainCommand.Run = commandRun.Run
}

type OptionsEntry struct {
	content []byte
	path    string
	options option.Options
	dialect config.Dialect
}

func readConfigAt(path string) (*OptionsEntry, error) {
	var (
		configContent []byte
		err           error
	)
	if path == "stdin" {
		configContent, err = io.ReadAll(os.Stdin)
	} else {
		configContent, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, E.Cause(err, "read config at ", path)
	}
	options, dialect, err := config.Decode(globalCtx, configContent)
	if err != nil {
		return nil, E.Cause(err, "decode config at ", path)
	}
	return &OptionsEntry{
		content: configContent,
		path:    path,
		options: options,
		dialect: dialect,
	}, nil
}

func readConfig() ([]*OptionsEntry, error) {
	var optionsList []*OptionsEntry
	for _, path := range configPaths {
		optionsEntry, err := readConfigAt(path)
		if err != nil {
			return nil, err
		}
		optionsList = append(optionsList, optionsEntry)
	}
	for _, directory := range configDirectories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, E.Cause(err, "read config directory at ", directory)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") || entry.IsDir() {
				continue
			}
			optionsEntry, err := readConfigAt(filepath.Join(directory, entry.Name()))
			if err != nil {
				return nil, err
			}
			optionsList = append(optionsList, optionsEntry)
		}
	}
	sort.Slice(optionsList, func(i, j int) bool {
		return optionsList[i].path < optionsList[j].path
	})
	return optionsList, nil
}

func readConfigAndMerge() (option.Options, error) {
	optionsList, err := readConfig()
	if err != nil {
		return option.Options{}, err
	}
	return mergeOptionsListWithDashboard(optionsList)
}

func mergeOptionsListWithDashboard(optionsList []*OptionsEntry) (option.Options, error) {
	options, err := mergeOptionsList(optionsList)
	if err != nil {
		return option.Options{}, err
	}
	if err = mergeXrayDashboardSidecar(&options, optionsList); err != nil {
		return option.Options{}, err
	}
	if err = apiService.MergeDashboardProfiles(globalCtx, &options); err != nil {
		return option.Options{}, E.Cause(err, "load dashboard server profiles")
	}
	return options, nil
}

func mergeXrayDashboardSidecar(options *option.Options, optionsList []*OptionsEntry) error {
	if len(optionsList) != 1 || optionsList[0].dialect != config.DialectXray || optionsList[0].path == "stdin" {
		return nil
	}
	sidecarPath := optionsList[0].path + ".sidera.json"
	content, err := os.ReadFile(sidecarPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return E.Cause(err, "read Sidera dashboard sidecar at ", sidecarPath)
	}
	sidecar, dialect, err := config.Decode(globalCtx, content)
	if err != nil {
		return E.Cause(err, "decode Sidera dashboard sidecar at ", sidecarPath)
	}
	if dialect != config.DialectSingBox {
		return E.New("Sidera dashboard sidecar must use native configuration syntax: ", sidecarPath)
	}
	if sidecar.Log != nil || sidecar.DNS != nil || sidecar.NTP != nil || sidecar.Certificate != nil ||
		len(sidecar.CertificateProviders) > 0 || len(sidecar.HTTPClients) > 0 || len(sidecar.NetworkNamespaces) > 0 ||
		len(sidecar.Endpoints) > 0 || len(sidecar.Inbounds) > 0 || len(sidecar.Outbounds) > 0 || sidecar.Route != nil || sidecar.Experimental != nil {
		return E.New("Xray-adjacent Sidera sidecar may only contain the dashboard API service: ", sidecarPath)
	}
	usedTags := make(map[string]bool, len(options.Services))
	for _, current := range options.Services {
		if current.Tag != "" {
			usedTags[current.Tag] = true
		}
	}
	for _, serviceOptions := range sidecar.Services {
		if serviceOptions.Type != C.TypeAPI {
			return E.New("Xray-adjacent Sidera sidecar only supports service type api")
		}
		if serviceOptions.Tag == "" {
			return E.New("Sidera dashboard API service requires an explicit tag")
		}
		if usedTags[serviceOptions.Tag] {
			return E.New("duplicate service tag in Sidera dashboard sidecar: ", serviceOptions.Tag)
		}
		usedTags[serviceOptions.Tag] = true
		options.Services = append(options.Services, serviceOptions)
	}
	return nil
}

func mergeOptionsList(optionsList []*OptionsEntry) (option.Options, error) {
	if len(optionsList) == 0 {
		return option.Options{}, E.New("no configuration files")
	}
	if len(optionsList) == 1 {
		return optionsList[0].options, nil
	}
	dialect := optionsList[0].dialect
	for _, options := range optionsList[1:] {
		if options.dialect != dialect {
			return option.Options{}, E.New("cannot merge ", dialect, " and ", options.dialect, " configuration files")
		}
	}
	if dialect == config.DialectXray {
		return option.Options{}, E.New("merging multiple Xray configuration files is not implemented")
	}
	var (
		mergedMessage json.RawMessage
		err           error
	)
	for _, options := range optionsList {
		mergedMessage, err = badjson.MergeJSON(globalCtx, options.options.RawMessage, mergedMessage, false)
		if err != nil {
			return option.Options{}, E.Cause(err, "merge config at ", options.path)
		}
	}
	var mergedOptions option.Options
	err = mergedOptions.UnmarshalJSONContext(globalCtx, mergedMessage)
	if err != nil {
		return option.Options{}, E.Cause(err, "unmarshal merged config")
	}
	return mergedOptions, nil
}

func rejectXrayConfigOutput(optionsList []*OptionsEntry, operation string) error {
	for _, optionsEntry := range optionsList {
		if optionsEntry.dialect == config.DialectXray {
			return E.New(operation, " Xray configuration files is not implemented: ", optionsEntry.path)
		}
	}
	return nil
}

func create(options option.Options) (*box.Box, context.CancelFunc, error) {
	if disableColor {
		if options.Log == nil {
			options.Log = &option.LogOptions{}
		}
		options.Log.DisableColor = true
	}
	ctx, cancel := context.WithCancel(globalCtx)
	instance, err := box.New(box.Options{
		Context:                    ctx,
		Options:                    options,
		NetworkNamespaceHolderArgs: []string{"/proc/self/exe", commandNetnsHolder.Use},
	})
	if err != nil {
		cancel()
		return nil, nil, E.Cause(err, "create service")
	}

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer func() {
		signal.Stop(osSignals)
		close(osSignals)
	}()
	startCtx, finishStart := context.WithCancel(context.Background())
	go func() {
		_, loaded := <-osSignals
		if loaded {
			cancel()
			closeMonitor(startCtx)
		}
	}()
	err = instance.Start()
	finishStart()
	if err != nil {
		cancel()
		return nil, nil, E.Cause(err, "start service")
	}
	return instance, cancel, nil
}

func run() error {
	optionsList, err := readConfig()
	if err != nil {
		return err
	}
	options, err := mergeOptionsListWithDashboard(optionsList)
	if err != nil {
		return err
	}
	err = runInUserNamespaceIfNeeded(options, optionsList)
	if err != nil {
		return err
	}
	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(osSignals)
	for {
		instance, cancel, createErr := create(options)
		if createErr != nil {
			return createErr
		}
		runtimeDebug.FreeOSMemory()
		for {
			osSignal := <-osSignals
			if osSignal == syscall.SIGHUP {
				err = check()
				if err != nil {
					log.Error(E.Cause(err, "reload service"))
					continue
				}
			}
			cancel()
			closeCtx, closed := context.WithCancel(context.Background())
			go closeMonitor(closeCtx)
			err = instance.Close()
			closed()
			if osSignal != syscall.SIGHUP {
				if err != nil {
					log.Error(E.Cause(err, "Sidera Core did not close properly"))
				}
				return nil
			}
			break
		}
		options, err = readConfigAndMerge()
		if err != nil {
			return err
		}
	}
}

func closeMonitor(ctx context.Context) {
	time.Sleep(C.FatalStopTimeout)
	select {
	case <-ctx.Done():
		return
	default:
	}
	log.Fatal("Sidera Core did not close!")
}
