package main

import (
	"context"
	"io"

	"github.com/sandboxsdk/capsule/internal/setup"
)

type deps struct {
	in        io.Reader
	out       io.Writer
	errOut    io.Writer
	execIncus func(context.Context, []string, io.Reader, io.Writer, io.Writer) error
}

func newDeps(in io.Reader, out, errOut io.Writer) deps {
	return deps{
		in:        in,
		out:       out,
		errOut:    errOut,
		execIncus: execIncus,
	}
}

func (d deps) newSetupService() *setup.Service {
	return setup.NewService(
		setup.NewConsolePrompter(d.in, d.out),
		setup.NewExecRunner(d.out, d.errOut),
		setup.NewHostDetector(),
		d.out,
	)
}

func (d deps) runIncus(ctx context.Context, args []string) error {
	return d.execIncus(ctx, args, d.in, d.out, d.errOut)
}
