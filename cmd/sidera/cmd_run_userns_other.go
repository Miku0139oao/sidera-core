//go:build !linux

package main

import "github.com/Miku0139oao/sidera-core/option"

func runInUserNamespaceIfNeeded(options option.Options, optionsList []*OptionsEntry) error {
	return nil
}
