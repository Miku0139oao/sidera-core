package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Miku0139oao/sidera-core/config"
	"github.com/Miku0139oao/sidera-core/include"
	"github.com/Miku0139oao/sidera-core/option"

	"github.com/stretchr/testify/require"
)

func TestMergeXrayDashboardSidecar(t *testing.T) {
	previousContext := globalCtx
	globalCtx = include.Context(context.Background())
	t.Cleanup(func() { globalCtx = previousContext })

	configPath := filepath.Join(t.TempDir(), "config.json")
	sidecar := `{
  "services": [{
    "type": "api",
    "tag": "dashboard",
    "listen": "127.0.0.1",
    "listen_port": 9090,
    "secret": "test-secret",
    "dashboard": {"enabled": true, "data_path": "dashboard.json"}
  }]
}`
	require.NoError(t, os.WriteFile(configPath+".sidera.json", []byte(sidecar), 0o600))
	options := option.Options{}
	require.NoError(t, mergeXrayDashboardSidecar(&options, []*OptionsEntry{{
		path: configPath, dialect: config.DialectXray,
	}}))
	require.Len(t, options.Services, 1)
	require.Equal(t, "api", options.Services[0].Type)
	require.Equal(t, "dashboard", options.Services[0].Tag)
}

func TestMergeXrayDashboardSidecarRejectsRuntimeConfig(t *testing.T) {
	previousContext := globalCtx
	globalCtx = include.Context(context.Background())
	t.Cleanup(func() { globalCtx = previousContext })

	configPath := filepath.Join(t.TempDir(), "config.json")
	sidecar := `{
  "inbounds": [{"type":"socks","tag":"extra","listen":"127.0.0.1","listen_port":1080}]
}`
	require.NoError(t, os.WriteFile(configPath+".sidera.json", []byte(sidecar), 0o600))
	require.ErrorContains(t, mergeXrayDashboardSidecar(&option.Options{}, []*OptionsEntry{{
		path: configPath, dialect: config.DialectXray,
	}}), "may only contain the dashboard API service")
}

func TestDashboardStoreSnapshotRestore(t *testing.T) {
	previousContext := globalCtx
	globalCtx = include.Context(context.Background())
	t.Cleanup(func() { globalCtx = previousContext })

	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	require.NoError(t, os.WriteFile(dataPath, []byte("before"), 0o600))
	options := option.Options{Services: []option.Service{{
		Type: "api",
		Options: &option.APIServiceOptions{Dashboard: &option.APIDashboardOptions{
			Enabled: true, DataPath: dataPath,
		}},
	}}}
	snapshot, err := captureDashboardStoreFiles(options)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dataPath, []byte("after"), 0o600))
	require.NoError(t, os.WriteFile(dataPath+".bak", []byte("unexpected"), 0o600))
	require.NoError(t, restoreDashboardStoreFiles(snapshot))
	content, err := os.ReadFile(dataPath)
	require.NoError(t, err)
	require.Equal(t, "before", string(content))
	_, err = os.Stat(dataPath + ".bak")
	require.ErrorIs(t, err, os.ErrNotExist)
}
