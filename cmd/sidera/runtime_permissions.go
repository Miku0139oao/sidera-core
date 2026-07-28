//go:build !windows

package main

import (
	"os"

	"github.com/sagernet/sing/service/filemanager"
)

func secureStateFile(path string) error {
	return os.Chmod(path, 0o600)
}

func replaceStateFile(source string, destination string) error {
	return filemanager.Rename(globalCtx, source, destination)
}
