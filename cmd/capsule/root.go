package main

import (
	"errors"

	"github.com/spf13/cobra"
)

var errMissingCommand = errors.New("missing command")

func newRootCommand(d deps) *cobra.Command {
	root := &cobra.Command{
		Use:           "capsule",
		Short:         "Capsule CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmd.Help(); err != nil {
				return err
			}
			return errMissingCommand
		},
	}

	root.AddGroup(
		&cobra.Group{ID: "core", Title: "Core Commands"},
		&cobra.Group{ID: "inspect", Title: "Inspection Commands"},
	)

	root.AddCommand(
		newSetupCommand(d),
		newVersionCommand(d),
		newBootstrapCommand(),
	)

	return root
}
