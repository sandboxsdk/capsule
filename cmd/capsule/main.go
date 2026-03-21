package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}

		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	d := newDeps(in, out, errOut)
	cmd := newRootCommand(d)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}
