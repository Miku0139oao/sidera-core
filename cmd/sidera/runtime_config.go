package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Miku0139oao/sidera-core/config"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	apiService "github.com/Miku0139oao/sidera-core/service/api"
	E "github.com/sagernet/sing/common/exceptions"
	SJSON "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service/filemanager"
)

const runtimeDocumentVersion = 1

type runtimeDocument struct {
	Version       int                  `json:"version"`
	CreatedAt     time.Time            `json:"created_at"`
	PayloadSHA256 string               `json:"sha256"`
	Config        stdjson.RawMessage   `json:"config"`
	Dashboard     *runtimeDashboard    `json:"dashboard,omitempty"`
	Compatibility runtimeCompatibility `json:"compatibility,omitempty"`
}

type runtimeDashboard struct {
	AppliedServerRevisions map[string]int64 `json:"applied_server_revisions"`
}

type runtimeCompatibility struct {
	XrayVLESSPacketEncodingOutbounds []int `json:"xray_vless_packet_encoding_outbounds,omitempty"`
}

func resolvedRuntimeConfigPath() string {
	if runtimeConfigPath == "" {
		return ""
	}
	return filepath.Clean(filemanager.BasePath(globalCtx, os.ExpandEnv(runtimeConfigPath)))
}

func validateRuntimeConfigPath(path string, optionsList []*OptionsEntry, options option.Options) error {
	if path == "" {
		return nil
	}
	runtimeFiles, err := normalizedPathSet(path, path+".bak", path+".tmp")
	if err != nil {
		return err
	}
	for _, entry := range optionsList {
		if entry.path == "stdin" {
			continue
		}
		paths := []string{entry.path}
		if entry.dialect == config.DialectXray {
			paths = append(paths, entry.path+".sidera.json")
		}
		for _, sourcePath := range paths {
			normalized, normalizeErr := normalizeRuntimePath(sourcePath)
			if normalizeErr != nil {
				return normalizeErr
			}
			if runtimeFiles[normalized] {
				return E.New("runtime configuration path collides with source configuration: ", sourcePath)
			}
		}
	}
	primaryPath, err := normalizeRuntimePath(path)
	if err != nil {
		return err
	}
	for _, directory := range configDirectories {
		normalizedDirectory, normalizeErr := normalizeRuntimePath(directory)
		if normalizeErr != nil {
			return normalizeErr
		}
		if sameRuntimePath(filepath.Dir(primaryPath), normalizedDirectory) && strings.HasSuffix(strings.ToLower(primaryPath), ".json") {
			return E.New("runtime configuration must not be a JSON file in a configuration directory: ", path)
		}
	}
	for _, serviceOptions := range options.Services {
		if serviceOptions.Type != C.TypeAPI {
			continue
		}
		apiOptions, loaded := serviceOptions.Options.(*option.APIServiceOptions)
		if !loaded || apiOptions.Dashboard == nil || !apiOptions.Dashboard.Enabled {
			continue
		}
		dataPath := apiService.ResolveDashboardDataPath(globalCtx, apiOptions.Dashboard.DataPath)
		dashboardFiles, setErr := normalizedPathSet(dataPath, dataPath+".bak", dataPath+".tmp")
		if setErr != nil {
			return setErr
		}
		for runtimeFile := range runtimeFiles {
			if dashboardFiles[runtimeFile] {
				return E.New("runtime configuration path collides with dashboard data path: ", dataPath)
			}
		}
	}
	return nil
}

func normalizedPathSet(paths ...string) (map[string]bool, error) {
	result := make(map[string]bool, len(paths))
	for _, path := range paths {
		normalized, err := normalizeRuntimePath(path)
		if err != nil {
			return nil, err
		}
		result[normalized] = true
	}
	return result, nil
}

func normalizeRuntimePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	candidate := absolute
	var suffix []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr == nil {
			for _, component := range slices.Backward(suffix) {
				resolved = filepath.Join(resolved, component)
			}
			absolute = filepath.Clean(resolved)
			break
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", resolveErr
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
		suffix = append(suffix, filepath.Base(candidate))
		candidate = parent
	}
	if runtime.GOOS == "windows" {
		absolute = strings.ToLower(absolute)
	}
	return absolute, nil
}

func sameRuntimePath(left string, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func writeRuntimeConfig(path string, options option.Options) error {
	if path == "" {
		return nil
	}
	document, err := encodeRuntimeDocument(options)
	if err != nil {
		return err
	}
	rotateCurrent := true
	if _, primaryErr := readRuntimeConfigAt(path); primaryErr != nil {
		if _, backupErr := readRuntimeConfigAt(path + ".bak"); backupErr == nil {
			// A corrupt or incomplete primary must never displace the validated
			// fallback generation. The new durable primary is sufficient until a
			// later validated commit can rotate it normally.
			rotateCurrent = false
		}
	}
	return writeRuntimeConfigFileWithBackup(path, document, rotateCurrent)
}

func encodeRuntimeDocument(options option.Options) ([]byte, error) {
	configContent, err := SJSON.MarshalContext(globalCtx, options)
	if err != nil {
		return nil, E.Cause(err, "encode effective runtime configuration")
	}
	document := runtimeDocument{
		Version:   runtimeDocumentVersion,
		CreatedAt: time.Now().UTC(),
		Config:    configContent,
	}
	for index, outbound := range options.Outbounds {
		if outbound.Type != C.TypeVLESS {
			continue
		}
		vlessOptions, loaded := outbound.Options.(*option.VLESSOutboundOptions)
		if loaded && vlessOptions.XrayPacketEncoding {
			document.Compatibility.XrayVLESSPacketEncodingOutbounds = append(document.Compatibility.XrayVLESSPacketEncodingOutbounds, index)
		}
	}
	for _, serviceOptions := range options.Services {
		if serviceOptions.Type != C.TypeAPI {
			continue
		}
		apiOptions, loaded := serviceOptions.Options.(*option.APIServiceOptions)
		if !loaded || apiOptions.Dashboard == nil || !apiOptions.Dashboard.Enabled {
			continue
		}
		if document.Dashboard != nil {
			return nil, E.New("only one dashboard-enabled API service is allowed")
		}
		revisions := make(map[string]int64, len(apiOptions.Dashboard.AppliedServerRevisions))
		maps.Copy(revisions, apiOptions.Dashboard.AppliedServerRevisions)
		document.Dashboard = &runtimeDashboard{AppliedServerRevisions: revisions}
	}
	checksum, err := checksumRuntimeDocument(document)
	if err != nil {
		return nil, E.Cause(err, "checksum runtime document")
	}
	document.PayloadSHA256 = hex.EncodeToString(checksum)
	content, err := stdjson.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, E.Cause(err, "encode runtime document")
	}
	return append(content, '\n'), nil
}

func readRuntimeConfig(path string) (option.Options, string, error) {
	if path == "" {
		return option.Options{}, "", E.New("last-known-good runtime configuration path is not configured")
	}
	var result error
	for _, candidatePath := range []string{path, path + ".bak"} {
		options, err := readRuntimeConfigAt(candidatePath)
		if err == nil {
			err = validateRuntimeConfigPath(path, nil, options)
		}
		if err == nil {
			return options, candidatePath, nil
		}
		result = errors.Join(result, E.Cause(err, "load runtime configuration at ", candidatePath))
	}
	return option.Options{}, "", result
}

func readRuntimeConfigAt(path string) (option.Options, error) {
	content, err := filemanager.ReadFile(globalCtx, path)
	if err != nil {
		return option.Options{}, err
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var document runtimeDocument
	if err = decoder.Decode(&document); err != nil {
		return option.Options{}, E.Cause(err, "decode runtime document")
	}
	if err = ensureJSONEOF(decoder); err != nil {
		return option.Options{}, err
	}
	if document.Version != runtimeDocumentVersion {
		return option.Options{}, E.New("unsupported runtime document version: ", document.Version)
	}
	if document.CreatedAt.IsZero() || len(document.Config) == 0 {
		return option.Options{}, E.New("incomplete runtime document")
	}
	checksumDocument := document
	checksumDocument.PayloadSHA256 = ""
	checksum, checksumErr := checksumRuntimeDocument(checksumDocument)
	if checksumErr != nil {
		return option.Options{}, E.Cause(checksumErr, "checksum runtime document")
	}
	if !equalHexChecksum(document.PayloadSHA256, checksum) {
		return option.Options{}, E.New("runtime configuration checksum mismatch")
	}
	var options option.Options
	if err = options.UnmarshalJSONContext(globalCtx, document.Config); err != nil {
		return option.Options{}, E.Cause(err, "decode effective runtime configuration")
	}
	if err = restoreRuntimeMetadata(&options, document); err != nil {
		return option.Options{}, err
	}
	enableProcessSignalReload(&options)
	return options, nil
}

func restoreRuntimeMetadata(options *option.Options, document runtimeDocument) error {
	var dashboardOptions *option.APIDashboardOptions
	for _, serviceOptions := range options.Services {
		if serviceOptions.Type != C.TypeAPI {
			continue
		}
		apiOptions, loaded := serviceOptions.Options.(*option.APIServiceOptions)
		if !loaded || apiOptions.Dashboard == nil || !apiOptions.Dashboard.Enabled {
			continue
		}
		if dashboardOptions != nil {
			return E.New("only one dashboard-enabled API service is allowed")
		}
		dashboardOptions = apiOptions.Dashboard
	}
	if (dashboardOptions == nil) != (document.Dashboard == nil) {
		return E.New("runtime dashboard metadata does not match configuration")
	}
	if dashboardOptions != nil {
		dashboardOptions.AppliedServerRevisions = make(map[string]int64, len(document.Dashboard.AppliedServerRevisions))
		for tag, revision := range document.Dashboard.AppliedServerRevisions {
			if revision <= 0 {
				return E.New("invalid applied dashboard server revision: ", tag)
			}
			dashboardOptions.AppliedServerRevisions[tag] = revision
		}
	}
	indexes := append([]int(nil), document.Compatibility.XrayVLESSPacketEncodingOutbounds...)
	sort.Ints(indexes)
	for index, outboundIndex := range indexes {
		if outboundIndex < 0 || outboundIndex >= len(options.Outbounds) || index > 0 && outboundIndex == indexes[index-1] {
			return E.New("invalid Xray VLESS compatibility outbound index: ", outboundIndex)
		}
		outbound := &options.Outbounds[outboundIndex]
		vlessOptions, loaded := outbound.Options.(*option.VLESSOutboundOptions)
		if outbound.Type != C.TypeVLESS || !loaded {
			return E.New("Xray VLESS compatibility metadata does not match outbound index: ", outboundIndex)
		}
		vlessOptions.XrayPacketEncoding = true
	}
	return nil
}

func enableProcessSignalReload(options *option.Options) {
	for _, serviceOptions := range options.Services {
		if serviceOptions.Type != C.TypeAPI {
			continue
		}
		apiOptions, loaded := serviceOptions.Options.(*option.APIServiceOptions)
		if loaded && apiOptions.Dashboard != nil && apiOptions.Dashboard.Enabled {
			apiOptions.Dashboard.ProcessSignalReload = true
		}
	}
}

func equalHexChecksum(encoded string, expected []byte) bool {
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == len(expected) && bytes.Equal(decoded, expected)
}

func checksumRuntimeDocument(document runtimeDocument) ([]byte, error) {
	content, err := stdjson.Marshal(document)
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(content)
	return checksum[:], nil
}

func ensureJSONEOF(decoder *stdjson.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return E.New("runtime document contains trailing JSON data")
	}
	return E.Cause(err, "decode runtime document trailer")
}

func writeRuntimeConfigFile(path string, content []byte) error {
	return writeRuntimeConfigFileWithBackup(path, content, true)
}

func writeRuntimeConfigFileWithBackup(path string, content []byte, rotateCurrent bool) error {
	parent := filepath.Dir(path)
	if err := filemanager.MkdirAll(globalCtx, parent, 0o700); err != nil {
		return err
	}
	candidatePath, err := writeStateTempFile(path, "candidate", content, 0o600)
	if err != nil {
		return err
	}
	defer filemanager.Remove(globalCtx, candidatePath)

	_, statErr := filemanager.Stat(globalCtx, path)
	hasCurrent := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	var stagedBackupPath string
	if hasCurrent && rotateCurrent {
		if err = secureStateFile(path); err != nil {
			return err
		}
		currentContent, readErr := filemanager.ReadFile(globalCtx, path)
		if readErr != nil {
			return readErr
		}
		stagedBackupPath, err = writeStateTempFile(path+".bak", "backup", currentContent, 0o600)
		if err != nil {
			return err
		}
		defer filemanager.Remove(globalCtx, stagedBackupPath)
	}

	// Install the validated candidate before rotating the previous primary.
	// A crash at every point before the second rename therefore retains the
	// existing backup generation.
	if err = replaceStateFile(candidatePath, path); err != nil {
		return err
	}
	if err = syncStateDirectory(parent); err != nil {
		return err
	}
	if stagedBackupPath != "" {
		if err = replaceStateFile(stagedBackupPath, path+".bak"); err != nil {
			return err
		}
		if err = syncStateDirectory(parent); err != nil {
			return err
		}
	}
	return nil
}

func writeStateTempFile(destination string, purpose string, content []byte, mode os.FileMode) (string, error) {
	temporary, temporaryPath, err := createExclusiveStateTempFile(destination, purpose, mode)
	if err != nil {
		return "", err
	}
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if err != nil {
			_ = filemanager.Remove(globalCtx, temporaryPath)
		}
	}()
	if err = temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	closed = true
	if err != nil || closeErr != nil {
		err = errors.Join(err, closeErr)
		return "", err
	}
	if err = secureStateFile(temporaryPath); err != nil {
		return "", err
	}
	return temporaryPath, nil
}

func createExclusiveStateTempFile(destination string, purpose string, mode os.FileMode) (*os.File, string, error) {
	parent := filepath.Dir(destination)
	prefix := "." + filepath.Base(destination) + "." + purpose + "-"
	var randomBytes [16]byte
	for attempt := 0; attempt < 128; attempt++ {
		if _, err := rand.Read(randomBytes[:]); err != nil {
			return nil, "", err
		}
		temporaryPath := filepath.Join(parent, prefix+hex.EncodeToString(randomBytes[:]))
		temporary, err := filemanager.OpenFile(globalCtx, temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return temporary, temporaryPath, nil
	}
	return nil, "", E.New("allocate exclusive state temporary file for ", destination)
}

func syncStateDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := filemanager.Open(globalCtx, path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
