package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	if !strings.Contains(got, "incus") {
		t.Fatalf("help output %q does not list incus command", got)
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

func TestRunIncusDelegatesToIncusCLI(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	var errOut bytes.Buffer

	d := newDeps(strings.NewReader(""), &out, &errOut)
	d.execIncus = func(ctx context.Context, args []string, in io.Reader, stdout, stderr io.Writer) error {
		if len(args) != 2 || args[0] != "list" || args[1] != "--format=json" {
			t.Fatalf("unexpected args: %#v", args)
		}

		_, err := stdout.Write([]byte("ok\n"))
		return err
	}

	cmd := newRootCommand(d)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"incus", "list", "--format=json"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext returned an error: %v", err)
	}

	if got := out.String(); got != "ok\n" {
		t.Fatalf("stdout = %q, want %q", got, "ok\n")
	}
}

func TestExecIncusWrapsMissingBinary(t *testing.T) {
	t.Parallel()

	originalLookPath := incusLookPath
	incusLookPath = func(string) (string, error) {
		return "", errors.New("not found")
	}
	defer func() {
		incusLookPath = originalLookPath
	}()

	err := execIncus(context.Background(), nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected execIncus to fail when incus is unavailable")
	}

	if !strings.Contains(err.Error(), "incus is not installed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
