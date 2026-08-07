package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// FileName is the name of the daemon configuration file inside the config
// directory.
const FileName = "config.yaml"

// Duration is a time.Duration that unmarshals from YAML strings in Go
// duration syntax (e.g. "60m", "1h30m").
type Duration time.Duration

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String returns the Go duration string form.
func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML implements yaml.BytesUnmarshaler.
func (d *Duration) UnmarshalYAML(b []byte) error {
	var s string
	if err := yaml.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"60m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML implements yaml.BytesMarshaler.
func (d Duration) MarshalYAML() ([]byte, error) {
	return []byte(d.String()), nil
}

// Config is the daemon configuration (spec §12.3, plus the agents section —
// a phase 1 spec addition).
type Config struct {
	Listen                  string   `yaml:"listen"`
	MaxParallelTasks        int      `yaml:"max_parallel_tasks"`
	Defaults                Defaults `yaml:"defaults"`
	TranscriptRetentionDays int      `yaml:"transcript_retention_days"`
	LogLevel                string   `yaml:"log_level"`
	Agents                  Agents   `yaml:"agents"`
}

// Defaults holds fallback step timeouts, applied when a workflow step does
// not declare its own.
type Defaults struct {
	AgentTimeout   Duration `yaml:"agent_timeout"`
	CommandTimeout Duration `yaml:"command_timeout"`
}

// Agents configures how agent CLIs are located.
type Agents struct {
	Claude Agent `yaml:"claude"`
	Codex  Agent `yaml:"codex"`
}

// Agent is the per-adapter configuration. An empty Path means the binary is
// resolved from PATH.
type Agent struct {
	Path string `yaml:"path"`
}

// Default returns the built-in configuration defaults (spec §12.3).
func Default() Config {
	return Config{
		Listen:           "127.0.0.1:0",
		MaxParallelTasks: 3,
		Defaults: Defaults{
			AgentTimeout:   Duration(60 * time.Minute),
			CommandTimeout: Duration(15 * time.Minute),
		},
		TranscriptRetentionDays: 90,
		LogLevel:                "info",
	}
}

// Load reads and validates the config file at path. A missing file is not an
// error: the built-in defaults apply. Keys omitted from the file keep their
// default values; unknown keys are rejected (strict decoding).
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.UnmarshalWithOptions(raw, &cfg, yaml.DisallowUnknownField()); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen %q: %w", c.Listen, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("listen %q: host must be loopback (127.0.0.1, ::1, or localhost)", c.Listen)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 0 || p > 65535 {
		return fmt.Errorf("listen %q: invalid port %q", c.Listen, port)
	}
	if c.MaxParallelTasks < 1 {
		return fmt.Errorf("max_parallel_tasks must be at least 1, got %d", c.MaxParallelTasks)
	}
	if c.Defaults.AgentTimeout <= 0 {
		return fmt.Errorf("defaults.agent_timeout must be positive, got %s", c.Defaults.AgentTimeout)
	}
	if c.Defaults.CommandTimeout <= 0 {
		return fmt.Errorf("defaults.command_timeout must be positive, got %s", c.Defaults.CommandTimeout)
	}
	if c.TranscriptRetentionDays < 0 {
		return fmt.Errorf("transcript_retention_days must not be negative, got %d", c.TranscriptRetentionDays)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level must be one of debug, info, warn, error; got %q", c.LogLevel)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
