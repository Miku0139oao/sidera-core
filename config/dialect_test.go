package config_test

import (
	"testing"

	"github.com/Miku0139oao/sidera-core/config"

	"github.com/stretchr/testify/require"
)

func TestDetectNativeEndpointProtocol(t *testing.T) {
	testCases := []string{
		`{"inbounds":[{"type":"cloudflared","token":"test","protocol":"quic"}]}`,
		`{"outbounds":[{"type":"shadowsocksr","server":"127.0.0.1","server_port":443,"protocol":"auth_sha1_v4"}]}`,
	}
	for _, content := range testCases {
		dialect, err := config.Detect([]byte(content))
		require.NoError(t, err)
		require.Equal(t, config.DialectSingBox, dialect)
	}
}

func TestDetectStillRejectsMixedEndpointFields(t *testing.T) {
	_, err := config.Detect([]byte(`{"outbounds":[{"type":"direct","protocol":"freedom"}]}`))
	require.ErrorContains(t, err, "ambiguous config dialect")
}
