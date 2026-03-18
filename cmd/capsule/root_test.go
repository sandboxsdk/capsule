package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunWithoutCommandShowsHelp(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var errOut bytes.Buffer

	err := run(context.Background(), nil, strings.NewReader(""), &out, &errOut)
	if err == nil || err.Error() != errMissingCommand.Error() {
		t.Fatalf("run() error = %v, want %v", err, errMissingCommand)
	}

	got := out.String()
	if !strings.Contains(got, "Usage:") {
		t.Fatalf("help output %q does not contain usage", got)
	}
	if !strings.Contains(got, "setup") {
		t.Fatalf("help output %q does not list setup command", got)
	}
	if strings.Contains(got, "__bootstrap-local-linux-server") {
		t.Fatalf("help output %q unexpectedly lists hidden bootstrap command", got)
	}
}

func TestRunVersionCommand(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var errOut bytes.Buffer

	err := run(context.Background(), []string{"version"}, strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := out.String(); got != "dev\n" {
		t.Fatalf("version output = %q, want %q", got, "dev\n")
	}
	if got := errOut.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestRunSetupRejectsUnexpectedArguments(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var errOut bytes.Buffer

	err := run(context.Background(), []string{"setup", "extra"}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("run() error = nil, want argument validation error")
	}
	if !strings.Contains(err.Error(), "unknown command \"extra\" for \"capsule setup\"") {
		t.Fatalf("run() error = %q, want argument validation message", err)
	}
}
