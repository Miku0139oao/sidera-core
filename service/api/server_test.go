package api

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterCloseError(t *testing.T) {
	require.NoError(t, filterCloseError(nil))
	require.NoError(t, filterCloseError(net.ErrClosed))

	other := errors.New("close failed")
	require.ErrorIs(t, filterCloseError(other), other)

	joined := errors.Join(net.ErrClosed, other)
	filtered := filterCloseError(joined)
	require.NotNil(t, filtered)
	require.ErrorIs(t, filtered, other)
	require.False(t, errors.Is(filtered, net.ErrClosed))

	multiClosed := errors.Join(net.ErrClosed, net.ErrClosed)
	require.NoError(t, filterCloseError(multiClosed))
}
