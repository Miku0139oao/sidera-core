package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
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
	"github.com/sagernet/sing/service/filemanager"

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
	if err = validateRuntimeConfigPath(resolvedRuntimeConfigPath(), optionsList, options); err != nil {
		return option.Options{}, err
	}
	if err = apiService.MergeDashboardProfiles(globalCtx, &options); err != nil {
		return option.Options{}, E.Cause(err, "load dashboard server profiles")
	}
	if resolvedRuntimeConfigPath() != "" {
		enableProcessSignalReload(&options)
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

func create(options option.Options, runtimePath string, persistRuntime bool) (*box.Box, context.CancelFunc, error) {
	if disableColor {
		if options.Log == nil {
			options.Log = &option.LogOptions{}
		}
		options.Log.DisableColor = true
	}
	ctx, cancel := context.WithCancel(globalCtx)
	var beforeRuntimeCommit func() (func() error, error)
	if persistRuntime && runtimePath != "" {
		beforeRuntimeCommit = func() (func() error, error) {
			snapshot, err := captureRuntimeConfigFiles(runtimePath)
			if err != nil {
				return nil, E.Cause(err, "snapshot runtime configuration before commit")
			}
			if err = writeRuntimeConfig(runtimePath, options); err != nil {
				restoreErr := restoreDashboardStoreFiles(snapshot)
				verifyErr := verifyDashboardStoreFiles(snapshot)
				return nil, errors.Join(err, causeRuntimeCommitError(restoreErr, "restore runtime configuration"), causeRuntimeCommitError(verifyErr, "verify restored runtime configuration"))
			}
			return func() error {
				restoreErr := restoreDashboardStoreFiles(snapshot)
				verifyErr := verifyDashboardStoreFiles(snapshot)
				return errors.Join(causeRuntimeCommitError(restoreErr, "restore runtime configuration"), causeRuntimeCommitError(verifyErr, "verify restored runtime configuration"))
			}, nil
		}
	}
	instance, err := box.New(box.Options{
		Context:                    ctx,
		Options:                    options,
		NetworkNamespaceHolderArgs: []string{"/proc/self/exe", commandNetnsHolder.Use},
		BeforeRuntimeCommit:        beforeRuntimeCommit,
	})
	if err != nil {
		cancel()
		return nil, nil, E.Cause(err, "create service")
	}

	osSignals := make(chan os.Signal, 1)
	// SIGHUP belongs exclusively to the run loop so a reload request cannot
	// cancel the live instance before its replacement has been validated.
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM)
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

func causeRuntimeCommitError(err error, message string) error {
	if err == nil {
		return nil
	}
	return E.Cause(err, message)
}

func run() error {
	runtimePath := resolvedRuntimeConfigPath()
	optionsList, err := readConfig()
	var options option.Options
	if err == nil {
		options, err = mergeOptionsListWithDashboard(optionsList)
	}
	usingRuntimeFallback := false
	var desiredLoadErr error
	if err != nil {
		desiredLoadErr = err
		options, _, err = readRuntimeConfig(runtimePath)
		if err != nil {
			return errors.Join(desiredLoadErr, E.Cause(err, "load last-known-good runtime configuration"))
		}
		optionsList = nil
		usingRuntimeFallback = true
	}
	namespaceOptions := includeRuntimeFallbackNetworkNamespaces(options, runtimePath)
	err = runInUserNamespaceIfNeeded(namespaceOptions, optionsList)
	if err != nil {
		return err
	}
	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(osSignals)
	var instance *box.Box
	var cancel context.CancelFunc
	if usingRuntimeFallback {
		var loadedPath string
		var priorErr error
		instance, cancel, options, loadedPath, priorErr, err = startRuntimeFallback(runtimePath)
		if err == nil {
			log.Error(errors.Join(E.Cause(desiredLoadErr, "load desired configuration; using last-known-good runtime configuration at ", loadedPath), priorErr))
		}
	} else {
		instance, cancel, options, err = startDesiredOrRuntimeFallback(options, runtimePath)
	}
	if err != nil {
		return err
	}
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
			instance, cancel, err = create(options, "", false)
			if err != nil {
				return errors.Join(E.Cause(snapshotErr, "snapshot dashboard store"), E.Cause(err, "restore previous service"))
			}
			log.Error(E.Cause(snapshotErr, "reload service; previous service restored"))
			continue
		}

		candidateInstance, candidateCancel, candidateErr := create(candidate, runtimePath, true)
		if candidateErr == nil {
			options = candidate
			instance = candidateInstance
			cancel = candidateCancel
			continue
		}

		restoreStoreErr := restoreDashboardStoreFiles(storeSnapshot)
		verifyStoreErr := verifyDashboardStoreFiles(storeSnapshot)
		if verifyStoreErr != nil {
			return errors.Join(E.Cause(candidateErr, "start reloaded service"), restoreStoreErr, E.Cause(verifyStoreErr, "verify restored dashboard store"))
		}
		instance, cancel, err = create(options, "", false)
		if err != nil {
			return errors.Join(E.Cause(candidateErr, "start reloaded service"), restoreStoreErr, E.Cause(err, "restore previous service"))
		}
		if restoreStoreErr != nil {
			log.Error(errors.Join(E.Cause(candidateErr, "reload service; previous service restored"), E.Cause(restoreStoreErr, "restore dashboard store")))
		} else {
			log.Error(E.Cause(candidateErr, "reload service; previous service restored"))
		}
	}
}

func includeRuntimeFallbackNetworkNamespaces(options option.Options, runtimePath string) option.Options {
	if runtimePath == "" {
		return options
	}
	options.NetworkNamespaces = append([]option.NetworkNamespace(nil), options.NetworkNamespaces...)
	for _, candidatePath := range []string{runtimePath, runtimePath + ".bak"} {
		fallbackOptions, err := readRuntimeConfigAt(candidatePath)
		if err == nil && validateRuntimeConfigPath(runtimePath, nil, fallbackOptions) == nil {
			options.NetworkNamespaces = append(options.NetworkNamespaces, fallbackOptions.NetworkNamespaces...)
		}
	}
	return options
}

func startDesiredOrRuntimeFallback(options option.Options, runtimePath string) (*box.Box, context.CancelFunc, option.Options, error) {
	storeSnapshot, err := captureDashboardStoreFiles(options)
	if err != nil {
		return nil, nil, option.Options{}, E.Cause(err, "snapshot dashboard store")
	}
	instance, cancel, desiredErr := create(options, runtimePath, true)
	if desiredErr == nil {
		return instance, cancel, options, nil
	}

	restoreErr := restoreDashboardStoreFiles(storeSnapshot)
	verifyErr := verifyDashboardStoreFiles(storeSnapshot)
	if verifyErr != nil {
		return nil, nil, option.Options{}, errors.Join(E.Cause(desiredErr, "start desired configuration"), restoreErr, E.Cause(verifyErr, "verify restored activation files"))
	}
	instance, cancel, fallback, loadedPath, priorErr, fallbackErr := startRuntimeFallback(runtimePath)
	if fallbackErr != nil {
		return nil, nil, option.Options{}, errors.Join(E.Cause(desiredErr, "start desired configuration"), restoreErr, fallbackErr)
	}
	if restoreErr != nil {
		log.Error(errors.Join(E.Cause(desiredErr, "start desired configuration; using last-known-good runtime configuration at ", loadedPath), E.Cause(restoreErr, "restore activation files"), priorErr))
	} else {
		log.Error(errors.Join(E.Cause(desiredErr, "start desired configuration; using last-known-good runtime configuration at ", loadedPath), priorErr))
	}
	return instance, cancel, fallback, nil
}

func startRuntimeFallback(runtimePath string) (*box.Box, context.CancelFunc, option.Options, string, error, error) {
	if runtimePath == "" {
		return nil, nil, option.Options{}, "", nil, E.New("last-known-good runtime configuration path is not configured")
	}
	var attemptErrors error
	for _, candidatePath := range []string{runtimePath, runtimePath + ".bak"} {
		options, err := readRuntimeConfigAt(candidatePath)
		if err == nil {
			err = validateRuntimeConfigPath(runtimePath, nil, options)
		}
		if err != nil {
			attemptErrors = errors.Join(attemptErrors, E.Cause(err, "load runtime configuration at ", candidatePath))
			continue
		}
		storeSnapshot, err := captureDashboardStoreFiles(options)
		if err != nil {
			attemptErrors = errors.Join(attemptErrors, E.Cause(err, "snapshot dashboard store for runtime configuration at ", candidatePath))
			continue
		}
		instance, cancel, startErr := create(options, "", false)
		if startErr == nil {
			return instance, cancel, options, candidatePath, attemptErrors, nil
		}
		restoreErr := restoreDashboardStoreFiles(storeSnapshot)
		verifyErr := verifyDashboardStoreFiles(storeSnapshot)
		attemptErrors = errors.Join(attemptErrors, E.Cause(startErr, "start runtime configuration at ", candidatePath), restoreErr)
		if verifyErr != nil {
			return nil, nil, option.Options{}, "", nil, errors.Join(attemptErrors, E.Cause(verifyErr, "verify restored dashboard store"))
		}
	}
	return nil, nil, option.Options{}, "", nil, E.Cause(attemptErrors, "start last-known-good runtime configuration")
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
	return captureFiles(filePaths)
}

func captureRuntimeConfigFiles(path string) ([]dashboardFileSnapshot, error) {
	if path == "" {
		return nil, nil
	}
	return captureFiles([]string{path, path + ".bak"})
}

func captureFiles(filePaths []string) ([]dashboardFileSnapshot, error) {
	sort.Strings(filePaths)
	snapshots := make([]dashboardFileSnapshot, 0, len(filePaths))
	for _, path := range filePaths {
		info, err := filemanager.Stat(globalCtx, path)
		if errors.Is(err, os.ErrNotExist) {
			snapshots = append(snapshots, dashboardFileSnapshot{path: path})
			continue
		}
		if err != nil {
			return nil, err
		}
		content, err := filemanager.ReadFile(globalCtx, path)
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
		content, err := filemanager.ReadFile(globalCtx, snapshot.path)
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
		if err := filemanager.Remove(globalCtx, snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	parent := filepath.Dir(snapshot.path)
	if err := filemanager.MkdirAll(globalCtx, parent, 0o700); err != nil {
		return err
	}
	tempPath := snapshot.path + ".restore.tmp"
	if err := filemanager.Remove(globalCtx, tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tempFile, err := filemanager.OpenFile(globalCtx, tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, snapshot.mode)
	if err != nil {
		return err
	}
	defer filemanager.Remove(globalCtx, tempPath)
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
	if err = secureStateFile(tempPath); err != nil {
		return err
	}
	return replaceStateFile(tempPath, snapshot.path)
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
