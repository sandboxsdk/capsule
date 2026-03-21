package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/sandboxsdk/capsule/internal/setup"
	"github.com/spf13/cobra"
)

var incusLookPath = exec.LookPath

func newIncusCommand(d deps) *cobra.Command {
	return &cobra.Command{
		Use:                "incus",
		Short:              "Run the Incus CLI through Capsule",
		GroupID:            "core",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.runIncus(cmd.Context(), args)
		},
	}
}

func execIncus(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	binary, err := incusLookPath("incus")
	if err != nil {
		return errors.New("incus is not installed; run capsule setup first")
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut

	env, err := setup.CapsuleIncusEnv()
	if err != nil {
		return err
	}

	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running incus: %w", err)
	}

	return nil
}
