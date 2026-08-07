package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Environment variables overriding platform-native directory resolution,
// used by tests and scripted setups (phase 1 decision).
const (
	EnvConfigDir = "VINCENT_CONFIG_DIR"
	EnvDataDir   = "VINCENT_DATA_DIR"
)

// Dirs holds the resolved vincent directories (spec §12.2).
type Dirs struct {
	// Config holds config.yaml and workflows/.
	Config string
	// Data holds vincent.db, token, daemon.json, daemon.lock, worktrees/,
	// transcripts/, and logs/.
	Data string
}

// ResolveDirs returns the platform-native config and data directories,
// honoring the VINCENT_CONFIG_DIR / VINCENT_DATA_DIR overrides. It does not
// create the directories.
func ResolveDirs() (Dirs, error) {
	d := Dirs{
		Config: os.Getenv(EnvConfigDir),
		Data:   os.Getenv(EnvDataDir),
	}
	if d.Config == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Dirs{}, fmt.Errorf("resolve config dir: %w", err)
		}
		d.Config = filepath.Join(base, "vincent")
	}
	if d.Data == "" {
		dir, err := dataDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
		if err != nil {
			return Dirs{}, err
		}
		d.Data = dir
	}
	return d, nil
}

// dataDir resolves the platform data directory (spec §12.2). It is factored
// over its inputs so every platform branch is unit-testable on any OS.
func dataDir(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	switch goos {
	case "windows":
		base := getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("resolve data dir: LOCALAPPDATA is not set")
		}
		return filepath.Join(base, "vincent"), nil
	case "darwin":
		h, err := home()
		if err != nil {
			return "", fmt.Errorf("resolve data dir: %w", err)
		}
		return filepath.Join(h, "Library", "Application Support", "vincent", "data"), nil
	default: // linux and other unixes follow XDG
		if base := getenv("XDG_DATA_HOME"); base != "" {
			return filepath.Join(base, "vincent"), nil
		}
		h, err := home()
		if err != nil {
			return "", fmt.Errorf("resolve data dir: %w", err)
		}
		return filepath.Join(h, ".local", "share", "vincent"), nil
	}
}
