package main

import "github.com/spf13/cobra"

func newSetupCommand(d deps) *cobra.Command {
	return &cobra.Command{
		Use:     "setup",
		Short:   "Run interactive setup",
		Args:    cobra.NoArgs,
		GroupID: "core",
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.newSetupService().Run(cmd.Context())
		},
	}
}
