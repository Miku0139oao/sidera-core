package box

import (
	"testing"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/stretchr/testify/require"
)

func TestValidateDashboardServicesRejectsMultipleControllers(t *testing.T) {
	services := []option.Service{
		{Type: C.TypeAPI, Options: &option.APIServiceOptions{Dashboard: &option.APIDashboardOptions{Enabled: true}}},
		{Type: C.TypeAPI, Options: &option.APIServiceOptions{Dashboard: &option.APIDashboardOptions{Enabled: true}}},
	}
	require.ErrorContains(t, validateDashboardServices(services), "only one")
	services[1].Options.(*option.APIServiceOptions).Dashboard.Enabled = false
	require.NoError(t, validateDashboardServices(services))
}

func TestCloneDashboardOptionsDoesNotShareMergeState(t *testing.T) {
	dashboard := &option.APIDashboardOptions{Enabled: true}
	options := option.Options{Services: []option.Service{{
		Type: C.TypeAPI, Options: &option.APIServiceOptions{Dashboard: dashboard},
	}}}
	cloned := cloneDashboardOptions(options)
	clonedDashboard := cloned.Services[0].Options.(*option.APIServiceOptions).Dashboard
	clonedDashboard.AppliedServerRevisions = map[string]int64{"server": 1}
	cloned.Inbounds = append(cloned.Inbounds, option.Inbound{Type: C.TypeSOCKS})
	require.Nil(t, dashboard.AppliedServerRevisions)
	require.Empty(t, options.Inbounds)
}
