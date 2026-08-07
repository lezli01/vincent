package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const runtimeFile = "daemon.json"

// RuntimeInfo is the client-discovery record published as daemon.json
// (spec §12.2). It is written after the listener is bound and removed on
// graceful shutdown; a crash leaves it behind, which `daemon status` reports
// as stale (the lock, not this file, is the liveness authority).
type RuntimeInfo struct {
	Port      int       `json:"port"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// RuntimePath returns the daemon.json path inside dataDir.
func RuntimePath(dataDir string) string { return filepath.Join(dataDir, runtimeFile) }

// WriteRuntimeInfo atomically publishes ri as daemon.json.
func WriteRuntimeInfo(dataDir string, ri RuntimeInfo) error {
	b, err := json.MarshalIndent(ri, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal daemon.json: %w", err)
	}
	if err := writeFileAtomic(RuntimePath(dataDir), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write daemon.json: %w", err)
	}
	return nil
}

// ReadRuntimeInfo reads daemon.json. A missing file yields os.ErrNotExist.
func ReadRuntimeInfo(dataDir string) (RuntimeInfo, error) {
	b, err := os.ReadFile(RuntimePath(dataDir))
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("read daemon.json: %w", err)
	}
	var ri RuntimeInfo
	if err := json.Unmarshal(b, &ri); err != nil {
		return RuntimeInfo{}, fmt.Errorf("parse daemon.json: %w", err)
	}
	return ri, nil
}

// RemoveRuntimeInfo deletes daemon.json; a missing file is not an error.
func RemoveRuntimeInfo(dataDir string) error {
	if err := os.Remove(RuntimePath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove daemon.json: %w", err)
	}
	return nil
}
