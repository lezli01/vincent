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
	Listen           string `yaml:"listen"`
	MaxParallelTasks int    `yaml:"max_parallel_tasks"`
	// BranchTemplate is the global branch-naming convention, the level between
	// the built-in `vincent/{id}-{slug}` and a project's own template (task 001,
	// §5.3). Empty means the built-in name.
	//
	// Its *syntax* is not checked here: validating it needs the branch template
	// context, which lives in internal/worktree, and this package is a leaf that
	// imports nothing internal. internal/daemon validates it instead — at startup
	// as a hard failure, and on hot-reload by keeping the previous value with a
	// warning, because a daemon that quietly ignored the configured convention
	// would create every branch under the wrong name.
	BranchTemplate string   `yaml:"branch_template"`
	Defaults       Defaults `yaml:"defaults"`
	// DeleteEmptyBranchOnArchive deletes a task's branch at archive time when
	// it carries no commits past its recorded base (§10, task 008). Default
	// true: a workflow that never writes to the repository leaves a ref behind
	// on every run, and a branch whose tip is an ancestor of its base holds
	// nothing to lose. Anything carrying a commit object is never touched.
	//
	// A plain bool is right here, unlike the pointer a tri-state would need:
	// Load unmarshals into Default(), so an absent key keeps true and an
	// explicit `false` restores the pre-008 behaviour exactly.
	DeleteEmptyBranchOnArchive bool `yaml:"delete_empty_branch_on_archive"`
	// DeleteRemoteBranchOnArchive additionally deletes the upstream
	// counterpart of a branch that qualified. Default **false**, and honoured
	// only by POST /v1/tasks/{id}/archive — a forge is shared with other
	// people and the deletion is unrecoverable, so it happens only when a
	// human asked for this archive and only when they opted in. It has no
	// effect while DeleteEmptyBranchOnArchive is false: the remote leg runs
	// after a local delete that succeeded.
	DeleteRemoteBranchOnArchive bool `yaml:"delete_remote_branch_on_archive"`
	TranscriptRetentionDays     int  `yaml:"transcript_retention_days"`
	// TranscriptMaxBytes caps one attempt's transcript (§12.3, §18). Past it
	// the step fails `transcript_limit` rather than filling the disk. It sits
	// at the top level beside its retention sibling, not under `defaults:`,
	// which is timeouts only (PR V decision).
	TranscriptMaxBytes ByteSize `yaml:"transcript_max_bytes"`
	// UsageLimitRecheckInterval is how long a task waits before trying again
	// after its agent reported a spent usage quota *without* a reset time
	// (task 003, §11). When the CLI does report one, that timestamp wins and
	// this is unused.
	//
	// 15m bounds a five-hour window at roughly twenty wasted spawns, and a
	// user who knows their plan can tighten or widen it. There is deliberately
	// no exponential backoff: that is per-task state the row would have to
	// carry, and a second retry-ish concept beside §7.2's.
	UsageLimitRecheckInterval Duration `yaml:"usage_limit_recheck_interval"`
	LogLevel                  string   `yaml:"log_level"`
	// Debug records, in every step's transcript, the exact conditions the
	// step ran under: the resolved agent/model/effort, the permission mode,
	// the working directory, and the full argv of the process spawned.
	//
	// It exists because none of that was visible when a run misbehaved. A
	// step that asked for permissions gave no way to see it had resolved to
	// `restricted`, and diagnosing it meant reading the stored snapshot out
	// of the database. Off by default: argv can carry a prompt, and a
	// transcript is something people paste into issues.
	Debug bool `yaml:"debug"`
	// Environment decides what child processes inherit (§12.3, T4.23). Its
	// zero value is not the default — Default() sets inherit: all, which is
	// what every version before it did implicitly.
	Environment Environment `yaml:"environment"`
	Agents      Agents      `yaml:"agents"`
	// Parallel bounds a `type: parallel` step group (task 014). It sits in its
	// own block rather than under `defaults:`, which holds timeouts a step may
	// override per-step; this one is not step-overridable in the same sense —
	// a group's `max_parallel:` replaces it outright rather than inheriting
	// from it (task 014 decision 30).
	Parallel Parallel `yaml:"parallel"`
	// FanOut bounds a `type: fan_out` tree (task 014). Both values are read
	// in the task-creation path, so a reload governs the next task rather
	// than anything already running (decision 30).
	FanOut FanOut `yaml:"fan_out"`
	// Loop bounds a `type: loop` step (§7.8, task 016). It sits beside
	// Parallel and FanOut for the same reason they do: it is a ceiling on
	// what one step may do, not a timeout a step inherits.
	Loop Loop `yaml:"loop"`
	// TUI is view preference, not daemon behaviour: the daemon validates it,
	// hot-reloads it and serves it on `GET /v1/config`, and does nothing else
	// with it. It lives in this file rather than one of the TUI's own because
	// the TUI is a pure API client (§15) — it reads no configuration from
	// disk, and a second file would be a second path, a second reload story
	// and a second `vincent doctor` line for one setting.
	TUI TUI `yaml:"tui"`
}

// TUI holds the settings clients read for themselves (§15).
type TUI struct {
	Board BoardView `yaml:"board"`
}

// BoardView configures the task table — the board's Tasks panel.
type BoardView struct {
	// GroupBy nests the rows under group headers, outermost level first.
	//
	// The default groups by project and then by workflow: a board is read
	// project by project, and within one project the workflow is what says
	// what a task is *doing* — the same reason those are the two columns the
	// width budget sheds last (boardcols.go). An empty list — `group_by: []`
	// — is the flat table every version before this one rendered.
	GroupBy []BoardGroup `yaml:"group_by"`
}

// BoardGroup is one grouping level of the task table.
type BoardGroup string

// The grouping vocabulary. Deliberately not `state`: the board's band sort
// already orders by state and pins the tasks waiting on a human above
// everything (§15), so a state grouping would fight the one ordering rule
// the board is not allowed to lose.
const (
	BoardGroupProject  BoardGroup = "project"
	BoardGroupWorkflow BoardGroup = "workflow"
)

func (b BoardView) validate() error {
	seen := make(map[BoardGroup]bool, len(b.GroupBy))
	for _, g := range b.GroupBy {
		switch g {
		case BoardGroupProject, BoardGroupWorkflow:
		default:
			return fmt.Errorf(
				"tui.board.group_by: unknown level %q; want project or workflow, or [] for a flat table", g)
		}
		if seen[g] {
			return fmt.Errorf("tui.board.group_by: %q listed twice", g)
		}
		seen[g] = true
	}
	return nil
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

// Parallel configures `type: parallel` step groups (spec §7, §11 — task 014).
//
// MaxParallel is a genuine second concurrency dimension: a group runs its
// sub-steps inside **one** task's slot, and the §11 caps count tasks in
// slot-holding states, so they never see it. One task can therefore keep
// MaxParallel processes busy while the board reads a single running task —
// which is why this has a default at all rather than being unbounded.
type Parallel struct {
	MaxParallel int `yaml:"max_parallel"`
}

// FanOut configures `type: fan_out` step trees (spec §7.6 — task 014).
//
// Both bounds are enforced at task creation, which is possible because the
// whole tree's shape is static there: lane lists live in the snapshot. That
// is what turns a depth-3 explosion into a 400 in front of the person typing
// rather than two hundred worktrees discovered six hours later.
//
// Depth is unlimited by design and bounded by a default: deeper trees are a
// config edit, not a code change.
type FanOut struct {
	// MaxDepth is how many fan-out levels one tree may have.
	MaxDepth int `yaml:"max_depth"`
	// MaxTasks bounds the child tasks one creation may produce, excluding
	// the root itself.
	MaxTasks int `yaml:"max_tasks"`
}

// Loop configures `type: loop` steps (spec §7.8 — task 016).
//
// MaxIterations is both the default for a step that declares no
// `max_iterations:` and the ceiling `count:` is validated against at load, so
// `count: 5000` is refused in front of the person typing rather than
// discovered on iteration 300. It is deliberately low: an agent step is
// minutes and dollars, and ten iterations of a three-step body is already
// thirty agent runs (decision 5).
//
// It is read per loop rather than cached, so a hot reload (§12.3) governs
// the next loop — including one already running, which blocks with
// `loop_limit` if the lowered ceiling is already behind it.
type Loop struct {
	MaxIterations int `yaml:"max_iterations"`
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
		DeleteEmptyBranchOnArchive: true,
		// Deliberately not defaulted true beside its local sibling: this one
		// writes to a forge (§10, task 008).
		DeleteRemoteBranchOnArchive: false,
		TranscriptRetentionDays:     90,
		TranscriptMaxBytes:          512 << 20, // 512MB (§12.3)
		UsageLimitRecheckInterval:   Duration(15 * time.Minute),
		LogLevel:                    "info",
		// Inherit everything: exactly what the daemon did before the policy
		// existed, so nothing changes for anyone who does not ask.
		Environment: Environment{Inherit: InheritAll()},
		// Four independent verifications is the shape this feature was
		// designed around (test, lint, typecheck, build); past that a group
		// is competing with the task caps for the same cores.
		Parallel: Parallel{MaxParallel: 4},
		// Three levels is deep enough to compose real workflows and shallow
		// enough that a mistake is visible; 64 descendants is more than the
		// caps will run concurrently anyway, so it bounds the explosion
		// rather than the throughput.
		FanOut: FanOut{MaxDepth: 3, MaxTasks: 64},
		Loop:   Loop{MaxIterations: 10},
		TUI: TUI{Board: BoardView{
			GroupBy: []BoardGroup{BoardGroupProject, BoardGroupWorkflow},
		}},
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
	if c.Parallel.MaxParallel < 1 {
		return fmt.Errorf("parallel.max_parallel must be at least 1, got %d", c.Parallel.MaxParallel)
	}
	if c.FanOut.MaxDepth < 1 {
		return fmt.Errorf("fan_out.max_depth must be at least 1, got %d", c.FanOut.MaxDepth)
	}
	if c.FanOut.MaxTasks < 1 {
		return fmt.Errorf("fan_out.max_tasks must be at least 1, got %d", c.FanOut.MaxTasks)
	}
	if c.Loop.MaxIterations < 1 {
		return fmt.Errorf("loop.max_iterations must be at least 1, got %d", c.Loop.MaxIterations)
	}
	if c.TranscriptRetentionDays < 0 {
		return fmt.Errorf("transcript_retention_days must not be negative, got %d", c.TranscriptRetentionDays)
	}
	// Positive, not merely non-negative: zero would re-admit a quota-held task
	// on the very next tick, which is the tight respawn loop the hold exists
	// to stop (task 003).
	if c.UsageLimitRecheckInterval <= 0 {
		return fmt.Errorf("usage_limit_recheck_interval must be positive, got %s", c.UsageLimitRecheckInterval)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level must be one of debug, info, warn, error; got %q", c.LogLevel)
	}
	if err := c.TUI.Board.validate(); err != nil {
		return err
	}
	return c.Environment.validate()
}

// Warnings are settings that parse, load and take effect, but do not do what
// they look like they ask for. The daemon logs them at startup and on any
// reload that changes them.
//
// They are separate from validate() on purpose: none of these refuses the file.
// A key that is merely unreachable is not an invalid one, and failing the load
// over it would revert every unrelated edit in the same save (§12.3).
func (c Config) Warnings() []string {
	var out []string
	// A silent no-op key is worse than a line in the log: the remote leg runs
	// only after a local delete that succeeded (§10, task 008), so this pair
	// asks for something that cannot happen.
	if c.DeleteRemoteBranchOnArchive && !c.DeleteEmptyBranchOnArchive {
		out = append(out, "delete_remote_branch_on_archive is true while "+
			"delete_empty_branch_on_archive is false: the remote counterpart is only "+
			"deleted after the local branch, so no remote branch will ever be deleted")
	}
	return out
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
