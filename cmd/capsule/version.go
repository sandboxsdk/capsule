package main

import (
	"fmt"

	"github.com/MSch/capsule/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCommand(d deps) *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print Capsule version",
		Args:    cobra.NoArgs,
		GroupID: "inspect",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintln(d.out, version.Version)
			return err
		},
	}
}
