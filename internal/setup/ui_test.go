package setup

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTaskUIRunNonTerminalSuccessUsesCheckmark(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	ui := newTaskUI(&out)

	if err := ui.Run("Installing Incus", func() (string, error) {
		return "", nil
	}); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}

	rendered := out.String()
	if !strings.Contains(rendered, "Installing Incus...\n") {
		t.Fatalf("expected non-terminal prelude, got %q", rendered)
	}
	if !strings.Contains(rendered, "✓") {
		t.Fatalf("expected success output to contain a checkmark, got %q", rendered)
	}
	if strings.Contains(rendered, "[ok]") {
		t.Fatalf("expected success output to avoid legacy marker, got %q", rendered)
	}
}

func TestTaskUIRunNonTerminalFailureUsesCross(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	ui := newTaskUI(&out)

	runErr := ui.Run("Installing Incus", func() (string, error) {
		return "permission denied", errors.New("boom")
	})
	if runErr == nil {
		t.Fatal("expected Run to return an error")
	}

	rendered := out.String()
	if !strings.Contains(rendered, "✗") {
		t.Fatalf("expected failure output to contain a cross, got %q", rendered)
	}
	if strings.Contains(rendered, "[x]") {
		t.Fatalf("expected failure output to avoid legacy marker, got %q", rendered)
	}
	if !strings.Contains(rendered, "permission denied") {
		t.Fatalf("expected failure output to include logs, got %q", rendered)
	}
}
