package setup

import (
	"fmt"
	"os"
	"path/filepath"
)

const capsuleIncusConfigSubdir = "capsule/incus"

// CapsuleIncusConfigDir returns the Capsule-managed Incus config directory.
func CapsuleIncusConfigDir() (string, error) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("determining the user config directory: %w", err)
	}

	return filepath.Join(configRoot, capsuleIncusConfigSubdir), nil
}

// CapsuleIncusEnv returns the Incus environment override for the Capsule-managed Incus profile.
func CapsuleIncusEnv() ([]string, error) {
	configDir, err := CapsuleIncusConfigDir()
	if err != nil {
		return nil, err
	}

	return []string{"INCUS_CONF=" + configDir}, nil
}
