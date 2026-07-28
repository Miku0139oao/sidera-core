package main

import (
	"context"
	stdjson "encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/Miku0139oao/sidera-core/common/dashboardstore"
	"github.com/Miku0139oao/sidera-core/config"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/include"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

func TestRuntimeDocumentPreservesEffectiveMetadata(t *testing.T) {
	setRuntimeTestContext(t)
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	options := runtimeTestOptions(dataPath, 7)

	content, err := encodeRuntimeDocument(options)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "runtime.json")
	require.NoError(t, writeRuntimeConfigFile(path, content))

	loaded, err := readRuntimeConfigAt(path)
	require.NoError(t, err)
	dashboard := loaded.Services[0].Options.(*option.APIServiceOptions).Dashboard
	require.Equal(t, map[string]int64{"managed": 7}, dashboard.AppliedServerRevisions)
	require.True(t, dashboard.ProcessSignalReload)
	vless := loaded.Outbounds[0].Options.(*option.VLESSOutboundOptions)
	require.True(t, vless.XrayPacketEncoding)
}

func TestRuntimeConfigFallsBackToRetainedBackup(t *testing.T) {
	setRuntimeTestContext(t)
	path := filepath.Join(t.TempDir(), "runtime.json")
	require.NoError(t, writeRuntimeConfig(path, runtimeTestOptions("dashboard.json", 1)))
	require.NoError(t, writeRuntimeConfig(path, runtimeTestOptions("dashboard.json", 2)))
	require.FileExists(t, path+".bak")
	require.NoError(t, os.WriteFile(path, []byte("corrupt"), 0o600))

	loaded, loadedPath, err := readRuntimeConfig(path)
	require.NoError(t, err)
	require.Equal(t, path+".bak", loadedPath)
	dashboard := loaded.Services[0].Options.(*option.APIServiceOptions).Dashboard
	require.EqualValues(t, 1, dashboard.AppliedServerRevisions["managed"])
}

func TestRuntimeDocumentChecksumCoversCompatibilityMetadata(t *testing.T) {
	setRuntimeTestContext(t)
	content, err := encodeRuntimeDocument(runtimeTestOptions("dashboard.json", 1))
	require.NoError(t, err)
	var document runtimeDocument
	require.NoError(t, stdjson.Unmarshal(content, &document))
	document.Compatibility.XrayVLESSPacketEncodingOutbounds = append(document.Compatibility.XrayVLESSPacketEncodingOutbounds, 2)
	content, err = stdjson.Marshal(document)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "runtime.json")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	require.ErrorContains(t, func() error {
		_, err := readRuntimeConfigAt(path)
		return err
	}(), "checksum mismatch")
}

func TestRuntimeFallbackTriesBackupAndRollsBackFailedPrimary(t *testing.T) {
	setRuntimeTestContext(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "runtime.json")
	dataPath := filepath.Join(directory, "dashboard.json")
	require.NoError(t, writeRuntimeConfig(path, option.Options{}))

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { occupied.Close() })
	address := badoption.Addr(netip.MustParseAddr("127.0.0.1"))
	port := uint16(occupied.Addr().(*net.TCPAddr).Port)
	badOptions := option.Options{Services: []option.Service{{
		Type: C.TypeAPI,
		Tag:  "dashboard",
		Options: &option.APIServiceOptions{
			ListenOptions: option.ListenOptions{Listen: &address, ListenPort: port},
			Secret:        "test-secret",
			Dashboard:     &option.APIDashboardOptions{Enabled: true, DataPath: dataPath, AppliedServerRevisions: map[string]int64{}},
		},
	}}}
	require.NoError(t, writeRuntimeConfig(path, badOptions))

	instance, cancel, active, loadedPath, priorErr, err := startRuntimeFallback(path)
	require.NoError(t, err)
	require.Error(t, priorErr)
	require.Equal(t, path+".bak", loadedPath)
	require.Empty(t, active.Services)
	require.NoFileExists(t, dataPath)
	require.NoError(t, closeInstance(instance, cancel))
}

func TestRuntimeConfigPathRejectsStateCollisions(t *testing.T) {
	setRuntimeTestContext(t)
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	entry := &OptionsEntry{path: configPath, dialect: config.DialectSingBox}
	require.ErrorContains(t, validateRuntimeConfigPath(configPath, []*OptionsEntry{entry}, option.Options{}), "source configuration")

	dataPath := filepath.Join(directory, "dashboard.json")
	options := runtimeTestOptions(dataPath, 1)
	require.ErrorContains(t, validateRuntimeConfigPath(dataPath+".bak", nil, options), "dashboard data path")

	previousDirectories := configDirectories
	configDirectories = []string{directory}
	t.Cleanup(func() { configDirectories = previousDirectories })
	require.ErrorContains(t, validateRuntimeConfigPath(filepath.Join(directory, "runtime.json"), nil, option.Options{}), "configuration directory")
	require.NoError(t, validateRuntimeConfigPath(filepath.Join(directory, "runtime.state"), nil, option.Options{}))
}

func TestRuntimeConfigPathRejectsSymlinkAliases(t *testing.T) {
	setRuntimeTestContext(t)
	directory := t.TempDir()
	stateDirectory := filepath.Join(directory, "state")
	require.NoError(t, os.Mkdir(stateDirectory, 0o700))
	aliasDirectory := filepath.Join(directory, "alias")
	if err := os.Symlink(stateDirectory, aliasDirectory); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	configPath := filepath.Join(stateDirectory, "config.json")
	entry := &OptionsEntry{path: configPath, dialect: config.DialectSingBox}
	require.ErrorContains(t, validateRuntimeConfigPath(filepath.Join(aliasDirectory, "config.json"), []*OptionsEntry{entry}, option.Options{}), "source configuration")
}

func TestLoadedRuntimeRejectsDashboardPathCollision(t *testing.T) {
	setRuntimeTestContext(t)
	path := filepath.Join(t.TempDir(), "runtime.state")
	content, err := encodeRuntimeDocument(runtimeTestOptions(path, 1))
	require.NoError(t, err)
	require.NoError(t, writeRuntimeConfigFile(path, content))

	_, _, err = readRuntimeConfig(path)
	require.ErrorContains(t, err, "dashboard data path")
}

func TestRuntimeConfigPathRequiresExplicitFlag(t *testing.T) {
	setRuntimeTestContext(t)
	previousRuntimePath := runtimeConfigPath
	previousConfigPaths := configPaths
	runtimeConfigPath = ""
	configPaths = []string{"config.json"}
	t.Cleanup(func() {
		runtimeConfigPath = previousRuntimePath
		configPaths = previousConfigPaths
	})
	require.Empty(t, resolvedRuntimeConfigPath())
}

func TestRuntimeFallbackNamespacesIncludeBackup(t *testing.T) {
	setRuntimeTestContext(t)
	path := filepath.Join(t.TempDir(), "runtime.json")
	backupOptions := option.Options{NetworkNamespaces: []option.NetworkNamespace{{Type: C.NetNsTypeUnshare, Tag: "fallback"}}}
	require.NoError(t, writeRuntimeConfig(path, backupOptions))
	require.NoError(t, writeRuntimeConfig(path, option.Options{}))

	options := includeRuntimeFallbackNetworkNamespaces(option.Options{}, path)
	require.Len(t, options.NetworkNamespaces, 1)
	require.Equal(t, C.NetNsTypeUnshare, options.NetworkNamespaces[0].Type)
}

func TestLoadedRuntimeSkipsPendingDashboardProfiles(t *testing.T) {
	setRuntimeTestContext(t)
	directory := t.TempDir()
	dataPath := filepath.Join(directory, "dashboard.json")
	store := `{
  "version": 6,
  "servers": {
    "pending": {
      "kind": "inbound",
      "type": "socks",
      "config": {"type":"socks","tag":"wrong-identity"},
      "revision": 8
    }
  }
}`
	require.NoError(t, os.WriteFile(dataPath, []byte(store), 0o600))
	path := filepath.Join(directory, "runtime.json")
	require.NoError(t, writeRuntimeConfig(path, runtimeTestOptions(dataPath, 7)))
	loaded, err := readRuntimeConfigAt(path)
	require.NoError(t, err)

	require.NoError(t, dashboardstore.MergeProfiles(globalCtx, &loaded))
	require.Empty(t, loaded.Inbounds)
	dashboard := loaded.Services[0].Options.(*option.APIServiceOptions).Dashboard
	require.EqualValues(t, 7, dashboard.AppliedServerRevisions["managed"])
}

func runtimeTestOptions(dataPath string, revision int64) option.Options {
	vless := &option.VLESSOutboundOptions{UUID: "00000000-0000-4000-8000-000000000001", XrayPacketEncoding: true}
	return option.Options{
		Outbounds: []option.Outbound{{Type: C.TypeVLESS, Tag: "xray-vless", Options: vless}},
		Services: []option.Service{{
			Type: C.TypeAPI,
			Tag:  "dashboard",
			Options: &option.APIServiceOptions{
				Secret: "test-secret",
				Dashboard: &option.APIDashboardOptions{
					Enabled: true, DataPath: dataPath,
					AppliedServerRevisions: map[string]int64{"managed": revision},
				},
			},
		}},
	}
}

func setRuntimeTestContext(t *testing.T) {
	t.Helper()
	previous := globalCtx
	globalCtx = include.Context(context.Background())
	t.Cleanup(func() { globalCtx = previous })
}
