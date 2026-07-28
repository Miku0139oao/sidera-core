//go:build !windows

package api

import (
	"context"
	"os"

	"github.com/sagernet/sing/service/filemanager"
)

func secureAdminStateFile(_ context.Context, path string) error {
	return os.Chmod(path, 0o600)
}

func replaceAdminStateFile(ctx context.Context, source string, destination string) error {
	return filemanager.Rename(ctx, source, destination)
}

func syncAdminStateDirectory(ctx context.Context, path string) error {
	directory, err := filemanager.Open(ctx, path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
