//go:build with_utls

package tls

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRealityClientVersion(t *testing.T) {
	version, err := parseRealityClientVersion("26.3.27")
	require.NoError(t, err)
	require.Equal(t, []byte{26, 3, 27}, version)

	version, err = parseRealityClientVersion("1.8")
	require.NoError(t, err)
	require.Equal(t, []byte{1, 8, 0}, version)

	_, err = parseRealityClientVersion("26.3.27.1")
	require.Error(t, err)
}
