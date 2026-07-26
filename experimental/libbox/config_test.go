package libbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const xrayDirectConfig = `{"outbounds":[{"tag":"direct","protocol":"freedom","settings":{}}]}`

func TestCheckConfigAcceptsXray(t *testing.T) {
	require.NoError(t, CheckConfig(xrayDirectConfig))
}

func TestFormatConfigRejectsXray(t *testing.T) {
	_, err := FormatConfig(xrayDirectConfig)
	require.ErrorContains(t, err, "formatting Xray configuration files is not implemented")
}
