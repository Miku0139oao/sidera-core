package main

import (
	"context"

	"github.com/Miku0139oao/sidera-core"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"

	"github.com/spf13/cobra"
)

var commandCheck = &cobra.Command{
	Use:   "check",
	Short: "Check configuration",
	Run: func(cmd *cobra.Command, args []string) {
		err := check()
		if err != nil {
			log.Fatal(err)
		}
	},
	Args: cobra.NoArgs,
}

func init() {
	mainCommand.AddCommand(commandCheck)
}

func check() error {
	options, err := readConfigAndMerge()
	if err != nil {
		return err
	}
	return validateConfigOptions(options)
}

func validateConfigOptions(options option.Options) error {
	ctx, cancel := context.WithCancel(globalCtx)
	instance, err := box.New(box.Options{
		Context:        ctx,
		Options:        options,
		ValidationOnly: true,
	})
	if err == nil {
		instance.Close()
	}
	cancel()
	return err
}
