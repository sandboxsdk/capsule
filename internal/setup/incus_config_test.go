package setup

import (
	"strings"
	"testing"
)

func TestCapsuleIncusEnvAlwaysTargetsCapsuleConfigDir(t *testing.T) {
	t.Parallel()

	env, err := CapsuleIncusEnv()
	if err != nil {
		t.Fatalf("CapsuleIncusEnv returned an error: %v", err)
	}

	if len(env) != 1 {
		t.Fatalf("expected one env entry, got %d", len(env))
	}

	if !strings.HasPrefix(env[0], "INCUS_CONF=") {
		t.Fatalf("expected INCUS_CONF entry, got %q", env[0])
	}

	if !strings.HasSuffix(env[0], "/capsule/incus") {
		t.Fatalf("expected Capsule Incus config suffix, got %q", env[0])
	}
}
