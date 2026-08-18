//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeConfigSecuresRetainedBackup(t *testing.T) {
	setRuntimeTestContext(t)
	path := filepath.Join(t.TempDir(), "runtime.json")
	require.NoError(t, os.WriteFile(path, []byte("old runtime"), 0o644))
	require.NoError(t, os.Chmod(path, 0o644))

	require.NoError(t, writeRuntimeConfigFile(path, []byte("new runtime")))

	primary, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), primary.Mode().Perm())
	backup, err := os.Stat(path + ".bak")
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), backup.Mode().Perm())
}
