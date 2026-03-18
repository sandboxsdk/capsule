package main

import (
	"github.com/MSch/capsule/internal/setup"
	"github.com/spf13/cobra"
)

func newBootstrapCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "__bootstrap-local-linux-server",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return setup.BootstrapLocalLinuxServer(cmd.Context())
		},
	}
}
