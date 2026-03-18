package main

import (
	"io"

	"github.com/MSch/capsule/internal/setup"
)

type deps struct {
	in     io.Reader
	out    io.Writer
	errOut io.Writer
}

func newDeps(in io.Reader, out, errOut io.Writer) deps {
	return deps{
		in:     in,
		out:    out,
		errOut: errOut,
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
