package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("got %+v, want defaults %+v", cfg, Default())
	}
}

func TestLoadPartialOverridesKeepDefaults(t *testing.T) {
	path := writeConfig(t, "max_parallel_tasks: 5\nlog_level: debug\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxParallelTasks != 5 {
		t.Errorf("MaxParallelTasks = %d, want 5", cfg.MaxParallelTasks)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.Listen != Default().Listen {
		t.Errorf("Listen = %q, want default %q", cfg.Listen, Default().Listen)
	}
	if cfg.Defaults.AgentTimeout != Default().Defaults.AgentTimeout {
		t.Errorf("AgentTimeout = %s, want default %s", cfg.Defaults.AgentTimeout, Default().Defaults.AgentTimeout)
	}
}

func TestLoadFullOverrides(t *testing.T) {
	path := writeConfig(t, strings.TrimSpace(`
listen: localhost:7777
max_parallel_tasks: 9
defaults:
  agent_timeout: 1h30m
  command_timeout: 90s
  input_timeout: 36h
transcript_retention_days: 7
transcript_max_bytes: 32MB
log_level: warn
agents:
  claude:
    path: /opt/bin/claude
  codex:
    path: C:\tools\codex.exe
`))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Listen:           "localhost:7777",
		MaxParallelTasks: 9,
		Defaults: Defaults{
			AgentTimeout:   Duration(90 * time.Minute),
			CommandTimeout: Duration(90 * time.Second),
			InputTimeout:   Duration(36 * time.Hour),
		},
		TranscriptRetentionDays: 7,
		TranscriptMaxBytes:      32 << 20,
		LogLevel:                "warn",
		Agents: Agents{
			Claude: Agent{Path: "/opt/bin/claude"},
			Codex:  Agent{Path: `C:\tools\codex.exe`},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown key":            "max_parallel_jobs: 5\n",
		"unknown nested key":     "defaults:\n  agent_timeut: 60m\n",
		"bad duration":           "defaults:\n  agent_timeout: sixty minutes\n",
		"duration missing unit":  "defaults:\n  agent_timeout: 60\n",
		"negative duration":      "defaults:\n  command_timeout: -5m\n",
		"zero cap":               "max_parallel_tasks: 0\n",
		"bad log level":          "log_level: verbose\n",
		"non-loopback listen":    "listen: 0.0.0.0:8080\n",
		"listen without port":    "listen: 127.0.0.1\n",
		"bad port":               "listen: 127.0.0.1:notaport\n",
		"negative retention":     "transcript_retention_days: -1\n",
		"not yaml":               "{{{\n",
		"wrong type for integer": "max_parallel_tasks: many\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Errorf("Load accepted invalid config %q", content)
			}
		})
	}
}

func TestEnsureDefaultFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg") // exercise dir creation too
	created, err := EnsureDefaultFile(dir)
	if err != nil {
		t.Fatalf("EnsureDefaultFile: %v", err)
	}
	if !created {
		t.Error("created = false on first call, want true")
	}

	// The generated file must parse strictly and equal the built-in defaults.
	cfg, err := Load(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("Load(generated default): %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("generated file loads to %+v, want defaults %+v", cfg, Default())
	}

	// A second call must not touch the existing file.
	custom := "max_parallel_tasks: 42\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureDefaultFile(dir)
	if err != nil {
		t.Fatalf("EnsureDefaultFile (second): %v", err)
	}
	if created {
		t.Error("created = true on second call, want false")
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != custom {
		t.Error("EnsureDefaultFile overwrote an existing config file")
	}
}
