package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
	for _, serviceOptions := range options.Services {
		if serviceOptions.Type != C.TypeAPI {
			continue
		}
		apiOptions, loaded := serviceOptions.Options.(*option.APIServiceOptions)
		if loaded && apiOptions.Dashboard != nil && apiOptions.Dashboard.Enabled {
			apiOptions.Dashboard.ProcessSignalReload = true
		}
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
	instance, cancel, err := create(options)
	if err != nil {
		return err
	}
	runtimeDebug.FreeOSMemory()
	for {
		osSignal := <-osSignals
		if osSignal != syscall.SIGHUP {
			err = closeInstance(instance, cancel)
			if err != nil {
				log.Error(E.Cause(err, "Sidera Core did not close properly"))
			}
			return nil
		}

		candidate, reloadErr := readConfigAndMerge()
		if reloadErr == nil {
			reloadErr = validateConfigOptions(candidate)
		}
		if reloadErr != nil {
			log.Error(E.Cause(reloadErr, "reload service"))
			continue
		}

		closeErr := closeInstance(instance, cancel)
		if closeErr != nil {
			log.Error(E.Cause(closeErr, "close previous service during reload"))
		}
		storeSnapshot, snapshotErr := captureDashboardStoreFiles(options, candidate)
		if snapshotErr != nil {
			instance, cancel, err = create(options)
			if err != nil {
				return errors.Join(E.Cause(snapshotErr, "snapshot dashboard store"), E.Cause(err, "restore previous service"))
			}
			log.Error(E.Cause(snapshotErr, "reload service; previous service restored"))
			continue
		}

		candidateInstance, candidateCancel, candidateErr := create(candidate)
		if candidateErr == nil {
			options = candidate
			instance = candidateInstance
			cancel = candidateCancel
			runtimeDebug.FreeOSMemory()
			continue
		}

		restoreStoreErr := restoreDashboardStoreFiles(storeSnapshot)
		verifyStoreErr := verifyDashboardStoreFiles(storeSnapshot)
		if verifyStoreErr != nil {
			return errors.Join(E.Cause(candidateErr, "start reloaded service"), restoreStoreErr, E.Cause(verifyStoreErr, "verify restored dashboard store"))
		}
		instance, cancel, err = create(options)
		if err != nil {
			return errors.Join(E.Cause(candidateErr, "start reloaded service"), restoreStoreErr, E.Cause(err, "restore previous service"))
		}
		if restoreStoreErr != nil {
			log.Error(errors.Join(E.Cause(candidateErr, "reload service; previous service restored"), E.Cause(restoreStoreErr, "restore dashboard store")))
		} else {
			log.Error(E.Cause(candidateErr, "reload service; previous service restored"))
		}
		runtimeDebug.FreeOSMemory()
	}
}

func closeInstance(instance *box.Box, cancel context.CancelFunc) error {
	cancel()
	closeCtx, closed := context.WithCancel(context.Background())
	go closeMonitor(closeCtx)
	err := instance.Close()
	closed()
	return err
}

type dashboardFileSnapshot struct {
	path    string
	content []byte
	mode    os.FileMode
	exists  bool
}

func captureDashboardStoreFiles(optionsList ...option.Options) ([]dashboardFileSnapshot, error) {
	paths := make(map[string]bool)
	for _, options := range optionsList {
		for _, serviceOptions := range options.Services {
			if serviceOptions.Type != C.TypeAPI {
				continue
			}
			apiOptions, loaded := serviceOptions.Options.(*option.APIServiceOptions)
			if !loaded || apiOptions.Dashboard == nil || !apiOptions.Dashboard.Enabled {
				continue
			}
			dataPath := apiService.ResolveDashboardDataPath(globalCtx, apiOptions.Dashboard.DataPath)
			paths[dataPath] = true
		}
	}
	filePaths := make([]string, 0, len(paths)*3)
	for dataPath := range paths {
		filePaths = append(filePaths, dataPath, dataPath+".bak", dataPath+".tmp")
	}
	sort.Strings(filePaths)
	snapshots := make([]dashboardFileSnapshot, 0, len(filePaths))
	for _, path := range filePaths {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			snapshots = append(snapshots, dashboardFileSnapshot{path: path})
			continue
		}
		if err != nil {
			return nil, err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, dashboardFileSnapshot{
			path: path, content: content, mode: info.Mode().Perm(), exists: true,
		})
	}
	return snapshots, nil
}

func restoreDashboardStoreFiles(snapshots []dashboardFileSnapshot) error {
	var result error
	for _, snapshot := range snapshots {
		result = errors.Join(result, restoreDashboardStoreFile(snapshot))
	}
	return result
}

func verifyDashboardStoreFiles(snapshots []dashboardFileSnapshot) error {
	var result error
	for _, snapshot := range snapshots {
		content, err := os.ReadFile(snapshot.path)
		if !snapshot.exists {
			if err == nil || !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, E.New("unexpected restored dashboard file: ", snapshot.path))
			}
			continue
		}
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if !bytes.Equal(content, snapshot.content) {
			result = errors.Join(result, E.New("restored dashboard file content mismatch: ", snapshot.path))
		}
	}
	return result
}

func restoreDashboardStoreFile(snapshot dashboardFileSnapshot) error {
	if !snapshot.exists {
		if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	parent := filepath.Dir(snapshot.path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tempFile, err := os.CreateTemp(parent, ".sidera-dashboard-restore-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	if err = tempFile.Chmod(snapshot.mode); err == nil {
		_, err = tempFile.Write(snapshot.content)
	}
	if err == nil {
		err = tempFile.Sync()
	}
	closeErr := tempFile.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if !strings.HasSuffix(snapshot.path, ".bak") && !strings.HasSuffix(snapshot.path, ".tmp") {
		backupPath := snapshot.path + ".bak"
		if err = os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = os.Rename(tempPath, backupPath); err != nil {
			return err
		}
		if err = os.Rename(backupPath, snapshot.path); err == nil {
			return nil
		}
		if runtime.GOOS != "windows" {
			return err
		}
		if removeErr := os.Remove(snapshot.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		if renameErr := os.Rename(backupPath, snapshot.path); renameErr != nil {
			return errors.Join(err, renameErr)
		}
		return nil
	}

	var backupPath string
	if _, statErr := os.Stat(snapshot.path); statErr == nil {
		backupFile, createErr := os.CreateTemp(parent, ".sidera-dashboard-backup-*")
		if createErr != nil {
			return createErr
		}
		backupPath = backupFile.Name()
		if closeErr = backupFile.Close(); closeErr != nil {
			os.Remove(backupPath)
			return closeErr
		}
		if err = os.Remove(backupPath); err != nil {
			return err
		}
		if err = os.Rename(snapshot.path, backupPath); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err = os.Rename(tempPath, snapshot.path); err != nil {
		if backupPath != "" {
			err = errors.Join(err, os.Rename(backupPath, snapshot.path))
		}
		return err
	}
	if backupPath != "" {
		return os.Remove(backupPath)
	}
	return nil
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
