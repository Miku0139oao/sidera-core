package main

import (
	"context"

	"github.com/Miku0139oao/sidera-core"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/service"

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

var checkActiveRuntime bool

func init() {
	commandCheck.Flags().BoolVar(&checkActiveRuntime, "active-runtime", false, "check the last-known-good runtime configuration")
	mainCommand.AddCommand(commandCheck)
}

func check() error {
	if checkActiveRuntime {
		options, _, err := readRuntimeConfig(resolvedRuntimeConfigPath())
		if err != nil {
			return err
		}
		return validateConfigOptions(options)
	}
	options, err := readConfigAndMerge()
	if err != nil {
		return err
	}
	return validateConfigOptions(options)
}

func validateConfigOptions(options option.Options) error {
	ctx, cancel := context.WithCancel(service.ExtendContext(globalCtx))
	defer cancel()
	instance, err := box.New(box.Options{
		Context:        ctx,
		Options:        options,
		ValidationOnly: true,
	})
	if err != nil {
		return err
	}
	// Validation-only constructors may own workers whose Close waits for the
	// validation context. Cancel admission before asking those workers to join.
	cancel()
	return instance.Close()
}
