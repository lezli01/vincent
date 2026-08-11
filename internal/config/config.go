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

// ByteSize is a byte count written in YAML as a human string ("512MB").
//
// It is its own type for the same reason Duration is: a bare integer of bytes
// in a config file is unreadable and invites off-by-1024 errors, and the
// alternative — a plain int with the unit baked into the key name — cannot be
// changed later without renaming the key.
type ByteSize int64

// Bytes returns the value as a byte count.
func (b ByteSize) Bytes() int64 { return int64(b) }

// byteUnits are accepted suffixes, longest first so "MB" wins over "B".
// Only binary multiples are offered: a transcript cap is a disk-space
// question, and disk space is quoted in powers of two by every tool that
// reports it.
var byteUnits = []struct {
	suffix string
	mult   int64
}{
	{"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30}, {"TB", 1 << 40}, {"B", 1},
}

// String renders the largest unit that divides the value exactly, so a
// round-trip through the config file preserves how a human wrote it.
func (b ByteSize) String() string {
	n := int64(b)
	if n == 0 {
		return "0B"
	}
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}} {
		if n%u.mult == 0 {
			return strconv.FormatInt(n/u.mult, 10) + u.suffix
		}
	}
	return strconv.FormatInt(n, 10) + "B"
}

// UnmarshalYAML implements yaml.BytesUnmarshaler. A bare number is bytes.
func (b *ByteSize) UnmarshalYAML(raw []byte) error {
	var s string
	if err := yaml.Unmarshal(raw, &s); err != nil {
		// A bare integer is valid YAML but not a string; accept it as bytes
		// rather than rejecting a config that is unambiguous.
		var n int64
		if err2 := yaml.Unmarshal(raw, &n); err2 == nil {
			*b = ByteSize(n)
			return nil
		}
		return fmt.Errorf("size must be a string like \"512MB\": %w", err)
	}
	v, err := ParseByteSize(s)
	if err != nil {
		return err
	}
	*b = v
	return nil
}

// MarshalYAML implements yaml.BytesMarshaler.
func (b ByteSize) MarshalYAML() ([]byte, error) { return []byte(b.String()), nil }

// ParseByteSize parses "512MB", "1GB", "4096" (bytes). It is exported so the
// same spelling works wherever a size is accepted.
func ParseByteSize(s string) (ByteSize, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(s))
	for _, u := range byteUnits {
		if !strings.HasSuffix(trimmed, u.suffix) {
			continue
		}
		num := strings.TrimSpace(strings.TrimSuffix(trimmed, u.suffix))
		n, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid size %q: %w", s, err)
		}
		if n < 0 {
			return 0, fmt.Errorf("invalid size %q: must not be negative", s)
		}
		return ByteSize(n * u.mult), nil
	}
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: want a number with an optional KB/MB/GB suffix", s)
	}
	return ByteSize(n), nil
}

// Config is the daemon configuration (spec §12.3, plus the agents section —
// a phase 1 spec addition).
type Config struct {
	Listen                  string   `yaml:"listen"`
	MaxParallelTasks        int      `yaml:"max_parallel_tasks"`
	Defaults                Defaults `yaml:"defaults"`
	TranscriptRetentionDays int      `yaml:"transcript_retention_days"`
	// TranscriptMaxBytes caps one attempt's transcript (§12.3, §18). Past it
	// the step fails `transcript_limit` rather than filling the disk. It sits
	// at the top level beside its retention sibling, not under `defaults:`,
	// which is timeouts only (PR V decision).
	TranscriptMaxBytes ByteSize `yaml:"transcript_max_bytes"`
	LogLevel           string   `yaml:"log_level"`
	// Debug records, in every step's transcript, the exact conditions the
	// step ran under: the resolved agent/model/effort, the permission mode,
	// the working directory, and the full argv of the process spawned.
	//
	// It exists because none of that was visible when a run misbehaved. A
	// step that asked for permissions gave no way to see it had resolved to
	// `restricted`, and diagnosing it meant reading the stored snapshot out
	// of the database. Off by default: argv can carry a prompt, and a
	// transcript is something people paste into issues.
	Debug  bool   `yaml:"debug"`
	Agents Agents `yaml:"agents"`
}

// Defaults holds fallback step timeouts, applied when a workflow step does
// not declare its own.
type Defaults struct {
	AgentTimeout   Duration `yaml:"agent_timeout"`
	CommandTimeout Duration `yaml:"command_timeout"`
	// InputTimeout bounds each wait in awaiting_input (§7.4); overridable
	// in workflow defaults and per step.
	InputTimeout Duration `yaml:"input_timeout"`
}

// Agents configures how agent CLIs are located.
type Agents struct {
	Claude Agent `yaml:"claude"`
	Codex  Agent `yaml:"codex"`
	// Cursor's empty path resolves the `cursor-agent` binary, never `cursor`
	// — that one is the editor launcher (spec §9.7).
	Cursor Agent `yaml:"cursor"`
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
			InputTimeout:   Duration(24 * time.Hour),
		},
		TranscriptRetentionDays: 90,
		TranscriptMaxBytes:      512 << 20, // 512MB (§12.3)
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
	if c.Defaults.InputTimeout <= 0 {
		return fmt.Errorf("defaults.input_timeout must be positive, got %s", c.Defaults.InputTimeout)
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
