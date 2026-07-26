package dashboardstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/stretchr/testify/require"
)

func TestMergeProfilesFailureIsTransactional(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	require.NoError(t, os.WriteFile(dataPath, []byte(`{"version":3`), 0o600))
	dashboard := &option.APIDashboardOptions{Enabled: true, DataPath: dataPath}
	options := option.Options{
		Inbounds: []option.Inbound{{Type: C.TypeSOCKS, Tag: "base", Options: new(option.SocksInboundOptions)}},
		Services: []option.Service{{
			Type: C.TypeAPI, Options: &option.APIServiceOptions{Dashboard: dashboard},
		}},
	}
	require.Error(t, MergeProfiles(context.Background(), &options))
	require.Nil(t, dashboard.AppliedServerRevisions)
	require.Len(t, options.Inbounds, 1)
	require.Equal(t, "base", options.Inbounds[0].Tag)
}

func TestMergeProfilesRejectsDeletedTagCollision(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "dashboard.json")
	store := `{"version":3,"servers":{"0":{"kind":"inbound","type":"socks","revision":2,"deleted":true}}}`
	require.NoError(t, os.WriteFile(dataPath, []byte(store), 0o600))
	options := option.Options{
		Inbounds: []option.Inbound{{Type: C.TypeSOCKS, Options: new(option.SocksInboundOptions)}},
		Services: []option.Service{{
			Type: C.TypeAPI,
			Options: &option.APIServiceOptions{Dashboard: &option.APIDashboardOptions{
				Enabled: true, DataPath: dataPath,
			}},
		}},
	}
	require.ErrorContains(t, MergeProfiles(context.Background(), &options), "collides with base configuration")
}
