package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// defaultConfigYAML is written on first daemon start when no config.yaml
// exists (phase 1 decision). It must always parse and equal Default();
// config_test.go enforces that.
const defaultConfigYAML = `# vincent daemon configuration (spec §12.3).
# Generated with defaults on first start. Edit freely: the daemon watches this
# file and hot-reloads valid changes. Invalid edits are logged and rejected,
# and the last good configuration stays active. Changes to "listen" take
# effect on the next daemon restart.

# Address the HTTP API binds to. Loopback only. Port 0 picks an ephemeral
# port, published for clients in {data_dir}/daemon.json.
listen: 127.0.0.1:0

# Global cap on concurrently running tasks. Per-project caps are configured
# on each project.
max_parallel_tasks: 3

# Fallback step timeouts, used when a workflow step declares none.
# input_timeout bounds each wait for an answer to an agent's input request
# (awaiting_input, §7.4); on expiry the attempt fails under the retry policy.
defaults:
  agent_timeout: 60m
  command_timeout: 15m
  input_timeout: 24h

# Transcripts of archived tasks older than this many days are pruned.
transcript_retention_days: 90

# Per-attempt transcript cap. A step whose transcript passes this fails with
# transcript_limit rather than filling the disk.
transcript_max_bytes: 512MB

# Daemon log verbosity: debug | info | warn | error.
log_level: info

# Record how every step was actually invoked in its transcript: resolved
# agent/model/effort, permission mode, working directory, and the full argv.
# Turn this on when a run does something you cannot explain, then paste the
# transcript. Off by default because argv includes the rendered prompt.
debug: false

# Agent CLI locations. An empty path resolves the binary from PATH.
agents:
  claude:
    path: ""
  codex:
    path: ""
  # cursor resolves the "cursor-agent" binary — not "cursor", which is the
  # editor launcher (§9.7).
  cursor:
    path: ""
`

// EnsureDefaultFile writes the commented default config.yaml into dir when
// none exists, creating dir if needed. It reports whether the file was
// created. An existing file is never touched.
func EnsureDefaultFile(dir string) (created bool, err error) {
	path := filepath.Join(dir, FileName)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil // lost a create race; the existing file wins
		}
		return false, fmt.Errorf("write default config: %w", err)
	}
	if _, err := f.WriteString(defaultConfigYAML); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write default config: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("write default config: %w", err)
	}
	return true, nil
}
