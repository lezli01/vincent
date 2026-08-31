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

	"github.com/lezli01/vincent/internal/taskstate"
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
	// MaxParallelChats bounds how many chats may hold a live agent process
	// at once (§11, task 063 decision 1). It is a *separate* cap, not a
	// share of MaxParallelTasks: a chat turn is a foreground reply to a
	// person and must never wait behind batch work.
	//
	// It exists because the §11 amendment of 2026-08-29 (task 057) named the
	// cost of an uncapped agent process verbatim — live-but-uncounted agent
	// CLIs accumulating — and that reasoning does not stop applying because
	// the noun changed. A turn over the cap is refused with 409 immediately;
	// it is never queued, so internal/scheduler's "the only place
	// queued → running happens" invariant is untouched.
	MaxParallelChats int `yaml:"max_parallel_chats"`
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
	// FetchBaseBranch refreshes a task's base branch from its own configured
	// upstream before the worktree is created, and starts the task branch at
	// the fetched commit (§10, task 056). Default true: without it every task
	// builds on whatever the human's last `git pull` left behind, which on a
	// daemon that runs for days over projects receiving merged pull requests
	// is arbitrarily stale.
	//
	// Nothing local is mutated — the user's base branch keeps its SHA and its
	// working tree — and nothing blocks: no remote, no upstream, an
	// unreachable host or an auth failure all fall back to the local base with
	// a log line. `false` restores the pre-056 behaviour exactly, for a
	// repository where fetching is slow or needs interactive auth.
	//
	// A plain bool for the same reason its Delete* siblings are: Load
	// unmarshals into Default(), so an absent key keeps true and an explicit
	// `false` is honoured.
	FetchBaseBranch         bool `yaml:"fetch_base_branch"`
	TranscriptRetentionDays int  `yaml:"transcript_retention_days"`
	// TranscriptMaxBytes caps one attempt's transcript (§12.3, §18). Past it
	// the step fails `transcript_limit` rather than filling the disk. It sits
	// at the top level beside its retention sibling, not under `defaults:`,
	// which is timeouts only (PR V decision).
	TranscriptMaxBytes ByteSize `yaml:"transcript_max_bytes"`
	// MaxTaskCostUSD caps what **one task** may spend before the engine stops
	// it (§12.3, §17, §18 — task 033). Past it the task blocks `cost_limit` at
	// the next attempt boundary. Zero — the default — is no cap, so nothing
	// changes for anyone who does not ask.
	//
	// It sits at the top level beside TranscriptMaxBytes rather than under
	// `defaults:`, which is timeouts a step may override (PR V decision): a
	// budget is not something a step inherits.
	//
	// A plain float64, unlike Duration and ByteSize: those exist because a
	// bare number of nanoseconds or bytes is unreadable in a file, and USD is
	// already the unit — it is in the key name.
	//
	// Per task, not per daemon and not per tree: a `fan_out` lane is an
	// ordinary task row (task 014 decision 1), so a tree of twenty lanes may
	// spend twenty times this before any single row trips.
	MaxTaskCostUSD float64 `yaml:"max_task_cost_usd"`
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
	// Include bounds a `type: include` expansion (§7.9, task 019). It is read
	// in the task-creation path, like FanOut, so a reload governs the next
	// task rather than anything already running.
	Include Include `yaml:"include"`
	// GitHub governs the GitHub integration (§12.3, task 035, task 069):
	// whether the daemon may talk to GitHub about a project whose `origin` is
	// a github.com repository, so a task can be created from an issue — and,
	// since task 069, so a human can push a task's branch and open its pull
	// request from inside vincent.
	//
	// It is read per use, so a hot reload reaches the next call rather than
	// requiring a restart — the same rule the rest of §12.3 follows.
	GitHub GitHub `yaml:"github"`
	// Notify is the outward signal: a command the daemon spawns when a task
	// enters one of the listed states (§12.3, task 046). Its zero value is
	// off, so a daemon nobody configured spawns nothing.
	Notify Notify `yaml:"notify"`
	// Update governs the release check (§12.3, task 055): whether the daemon
	// asks GitHub for the latest stable release on a timer, and how often.
	//
	// It is read per tick, so a hot reload governs the next one — including
	// a reload that switches the poller off, which takes effect without a
	// restart.
	Update Update `yaml:"update"`
	// MCP governs §13.4's Model Context Protocol server (task 057): whether
	// the daemon wires its own agent steps to it, and how deep an agent may
	// create tasks that create tasks.
	MCP MCP `yaml:"mcp"`
	// Container governs §16's container execution mode (task 061): the image
	// a task's steps run in, and how that container is wired. `image: ""` is
	// the default and means today's behaviour — every step on the host.
	Container Container `yaml:"container"`
	// TUI is view preference, not daemon behaviour: the daemon validates it,
	// hot-reloads it and serves it on `GET /v1/config`, and does nothing else
	// with it. It lives in this file rather than one of the TUI's own because
	// the TUI is a pure API client (§15) — it reads no configuration from
	// disk, and a second file would be a second path, a second reload story
	// and a second `vincent doctor` line for one setting.
	TUI TUI `yaml:"tui"`
}

// GitHub configures the GitHub integration (spec §12.3 — task 035, task 069).
//
// It was read-only until task 069, which gave it **one** write path:
// pull-request creation, from a human pressing a key in vincent. There is
// deliberately no second key gating that write (task 069 decision 2) — the
// consent is the keypress and the editable popup in front of it, not a line
// in config.yaml nobody would turn on — so `enabled: false` turns the write
// off with everything else, and is the one switch there is.
//
// There is deliberately no token key here either. vincent stores no
// credential of its own: it drives `gh`, or reads GITHUB_TOKEN/GH_TOKEN from
// the environment the daemon already inherited, which is what keeps §2's
// "secret management" non-goal intact (decision 1). A credential with no
// write scope is not a misconfiguration — the create falls back to GitHub's
// own compare page, which is what vincent did before task 069.
type GitHub struct {
	// Enabled turns the integration on. It defaults to **true** and is an
	// opt-*out*: it is inert on every project whose origin is not a
	// github.com repository, and makes no call at all until a human opens the
	// issue picker, names an issue, or asks for a pull request, so
	// on-by-default costs nothing unasked for (decision 6).
	//
	// It is also the **only** gate on task 069's one write path. Nothing is
	// pushed and no pull request is opened while this is false.
	//
	// A plain bool is right here, as it is for
	// DeleteEmptyBranchOnArchive: Load unmarshals into Default(), so an
	// absent key keeps true and an explicit `false` turns the integration
	// off. There is nothing to validate — a bool is either spelling or a
	// parse error the strict decoder already refuses — which is why
	// validate() has no clause for this block.
	Enabled bool `yaml:"enabled"`
	// PollInterval is how often the daemon reconciles task↔pull-request links
	// (task 052, §12.3): it lists each GitHub-based project's open pull
	// requests and links the ones whose head branch is a task's branch.
	//
	// This is the daemon's **first standing outbound network traffic**, which
	// is why it is a key rather than a constant, and why `0` disables the
	// reconciler while leaving the rest of the integration on. A user who
	// wants the picker but no background calls must be able to say so without
	// turning `enabled` off.
	//
	// The default is conservative — a pull request appearing a few minutes
	// late costs nothing, and the reconciler is a convenience over a fact the
	// branch already carries.
	PollInterval Duration `yaml:"poll_interval"`
}

// Polls reports whether the reconciler should run at all.
func (g GitHub) Polls() bool { return g.Enabled && g.PollInterval > 0 }

// Update configures the release check (spec §12.3 — task 055).
//
// There is no `auto_apply` here and there never will be: agents already run
// full-auto (§16), and swapping the orchestrator's own binary underneath
// running tasks with no human in the loop is not something vincent does
// quietly. Checking is automatic; `vincent update` is the human act.
type Update struct {
	// Check turns the background poll on. It defaults to **true** and is an
	// opt-*out*, like GitHub.Enabled — but it carries a stronger promise
	// than that one does, because unlike the pull-request reconciler this
	// check fires for **every** install rather than only for projects whose
	// origin is a github.com repository. With `check: false` the daemon
	// makes no outbound request for this feature at all; only an explicit
	// `vincent update` does, and that one is the CLI's own call and never
	// goes through the daemon (decision 3).
	//
	// A plain bool for the same reason GitHub.Enabled is one: Load
	// unmarshals into Default(), so an absent key keeps true and an explicit
	// `false` turns it off.
	Check bool `yaml:"check"`
	// PollInterval is how often the daemon re-asks. `0` stops the poller
	// while leaving the key legible, mirroring `github.poll_interval`.
	//
	// The default is a day. A release is not news that goes stale in
	// minutes, and the endpoint is unauthenticated — a tighter interval buys
	// nothing and spends the shared rate limit.
	PollInterval Duration `yaml:"poll_interval"`
}

// Polls reports whether the release check should run at all.
func (u Update) Polls() bool { return u.Check && u.PollInterval > 0 }

// Notify configures the daemon's outward signal (spec §12.3 — task 046): a
// command spawned when a task enters one of the listed states, with a JSON
// envelope on its stdin.
//
// It exists because the daemon is designed to run with zero clients attached
// (§2), and the one thing it could not do with zero clients attached was say
// it needed a human: the only alert in the tree is the TUI's terminal bell,
// which rings on `awaiting_input` and only while a board is open.
//
// The selector is target *states*, not event types: §13.3's durable
// vocabulary has one transition event, `task.state_changed`, and this reads
// its `to` field — exactly what the TUI bell keys off. No event type is
// introduced for this.
//
// Global rather than per-project: projects are database rows, not YAML, so a
// per-project override would need a column and API surface for a case nobody
// has asked for.
type Notify struct {
	// On lists the states whose arrival fires Command. Empty — the default —
	// fires nothing.
	On []taskstate.State `yaml:"on"`
	// Command is argv, never a shell string: there is no portable shell to
	// assume, and a string would invite quoting bugs that differ per platform.
	// Empty means the hook is off.
	Command []string `yaml:"command"`
}

// Enabled reports whether the hook can fire at all.
func (n Notify) Enabled() bool { return len(n.On) > 0 && len(n.Command) > 0 }

// Fires reports whether a transition into s should spawn the command.
func (n Notify) Fires(s taskstate.State) bool {
	if len(n.Command) == 0 {
		return false
	}
	for _, want := range n.On {
		if want == s {
			return true
		}
	}
	return false
}

// validate rejects a state name that is not in §6's vocabulary, so a typo is
// refused at load and the last good configuration stays active (§12.3).
//
// This is why internal/config imports internal/taskstate — the one internal
// import this package has (task 046 decision 4). taskstate is itself a leaf
// (it imports only `sort`), so there is no cycle, and the alternative is a
// second copy of §6's ten state names drifting from the first. The
// branch_template precedent of validating in internal/daemon does not apply:
// that one exists because the branch-template context lives in
// internal/worktree, a package with real dependencies.
func (n Notify) validate() error {
	seen := make(map[taskstate.State]bool, len(n.On))
	for _, s := range n.On {
		if !taskstate.Valid(s) {
			return fmt.Errorf("notify.on: unknown task state %q; want one of %s",
				s, strings.Join(stateNames(), ", "))
		}
		if seen[s] {
			return fmt.Errorf("notify.on: %q listed twice", s)
		}
		seen[s] = true
	}
	for i, arg := range n.Command {
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("notify.command: element %d is empty; argv elements must be non-empty", i)
		}
	}
	return nil
}

// stateNames renders §6's vocabulary for an error message.
func stateNames() []string {
	out := make([]string, 0, len(taskstate.All))
	for _, s := range taskstate.All {
		out = append(out, string(s))
	}
	return out
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

// MCP configures the daemon's Model Context Protocol server (spec §13.4 —
// task 057).
//
// There is no `enabled` key. The endpoint is part of the API surface the way
// `/v1` is, behind the same bearer token on the same loopback listener, so
// "serving MCP" is not a mode the daemon is in — what a user can meaningfully
// turn off is vincent wiring the server into its *own* agent steps, which is
// what WireSteps is.
type MCP struct {
	// WireSteps registers the per-step endpoint with the agent CLI the daemon
	// spawns for an agent step, so a step's agent gets the tool list with no
	// user configuration (decision 10).
	//
	// It defaults to **true** and is an opt-*out*, the way github.enabled is
	// (task 035 decision 6): the acceptance criterion this work exists for is
	// that a step's agent has the tools by default, and one line turns it
	// off. A plain bool is right here for the reason it is there — Load
	// unmarshals into Default(), so an absent key keeps true and an explicit
	// `false` restores the pre-057 behaviour exactly.
	WireSteps bool `yaml:"wire_steps"`
	// MaxDepth bounds a chain of tasks created through MCP: a step's agent
	// creates a task, whose step's agent creates a task, and so on. The depth
	// is discovered at run time, so neither §7.6's fan_out bounds nor §7.9's
	// include bound covers it — both are creation-time checks over a static
	// snapshot (decision 7).
	MaxDepth int `yaml:"max_depth"`
	// MaxTasks bounds how many tasks one MCP-created ancestry chain may
	// contain in total. It is the count bound beside the depth bound, for the
	// same reason fan_out has both: a shallow chain that is wide is the same
	// runaway as a deep one.
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

// Include configures `type: include` expansion (spec §7.9 — task 019).
//
// MaxDepth is enforced at task creation, which is possible for the reason
// FanOut's bounds are: the callee is resolved into the snapshot there, so the
// whole expanded shape is static in the insert path (task 019 decision 2).
//
// There is deliberately no bound on the expanded *step count*. Step ids are
// unique across an expansion (decision 5), so a callee reached twice is a 400
// rather than a doubling, and an expansion cannot multiply silently — depth is
// the only dimension left to bound.
type Include struct {
	// MaxDepth is how many include levels one expansion may have. A root
	// including a workflow is depth 1; that workflow's own include is depth 2.
	MaxDepth int `yaml:"max_depth"`
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
		MaxParallelChats: 3,
		Defaults: Defaults{
			AgentTimeout:   Duration(60 * time.Minute),
			CommandTimeout: Duration(15 * time.Minute),
			InputTimeout:   Duration(24 * time.Hour),
		},
		DeleteEmptyBranchOnArchive: true,
		// Deliberately not defaulted true beside its local sibling: this one
		// writes to a forge (§10, task 008).
		DeleteRemoteBranchOnArchive: false,
		// Default-on outbound traffic needs no separate argument: github.enabled
		// already defaults true and §26 settled that posture. A fetch reads.
		FetchBaseBranch:           true,
		TranscriptRetentionDays:   90,
		TranscriptMaxBytes:        512 << 20, // 512MB (§12.3)
		UsageLimitRecheckInterval: Duration(15 * time.Minute),
		LogLevel:                  "info",
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
		// Three levels and 32 tasks, a little tighter than fan_out's tree:
		// this chain is discovered at run time, so a mistake is only visible
		// once it has already spawned, and the bound is what stops it.
		MCP: MCP{WireSteps: true, MaxDepth: 3, MaxTasks: 32},
		// Five levels, where fan-out gets three: an include costs a splice
		// rather than a worktree, so the bound is about keeping a mistake
		// legible rather than about what the machine can afford.
		Include: Include{MaxDepth: 5},
		// On by default: an opt-out, per task 035 decision 6.
		GitHub: GitHub{Enabled: true, PollInterval: Duration(5 * time.Minute)},
		// On by default with a day between calls (task 055 decision 3).
		Update: Update{Check: true, PollInterval: Duration(24 * time.Hour)},
		// Runtime named, mounts and network on: inert until an image is set,
		// and the shape a container user wants when they set one (§16).
		Container: Container{Runtime: "docker", MountAgentConfig: true, Network: true},
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
	// G304: Load is called with ConfigPath(), or with a path a test chose. The
	// config file belongs to the invoking user, so reading it crosses no
	// boundary (§16); the file is what says who the user is, not the reverse.
	raw, err := os.ReadFile(path) //nolint:gosec // G304: see above
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	cfg, err = Decode(raw)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Decode parses and validates config.yaml bytes that are not (yet) on disk.
// It is what lets PATCH /v1/config reject an edit before anything is written
// (task 060): the candidate file is decoded through exactly the path Load
// takes, so a patch that would not survive a restart is refused now.
func Decode(raw []byte) (Config, error) {
	cfg := Default()
	if err := yaml.UnmarshalWithOptions(raw, &cfg, yaml.DisallowUnknownField()); err != nil {
		return Config{}, fmt.Errorf("parse: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// Validate is validate() for callers outside the package. The API needs to
// reject a configuration before writing it, and the rule it applies has to be
// the same one Load applies on the next start.
func (c Config) Validate() error { return c.validate() }

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
	if c.MaxParallelChats < 1 {
		return fmt.Errorf("max_parallel_chats must be at least 1, got %d", c.MaxParallelChats)
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
	if c.MCP.MaxDepth < 1 {
		return fmt.Errorf("mcp.max_depth must be at least 1, got %d", c.MCP.MaxDepth)
	}
	if c.MCP.MaxTasks < 1 {
		return fmt.Errorf("mcp.max_tasks must be at least 1, got %d", c.MCP.MaxTasks)
	}
	if c.Include.MaxDepth < 1 {
		return fmt.Errorf("include.max_depth must be at least 1, got %d", c.Include.MaxDepth)
	}
	if c.Loop.MaxIterations < 1 {
		return fmt.Errorf("loop.max_iterations must be at least 1, got %d", c.Loop.MaxIterations)
	}
	if c.TranscriptRetentionDays < 0 {
		return fmt.Errorf("transcript_retention_days must not be negative, got %d", c.TranscriptRetentionDays)
	}
	// Non-negative, not positive: zero is the documented "no cap" (task 033),
	// unlike usage_limit_recheck_interval below where zero would mean a
	// respawn loop. A negative budget cannot be honoured by any run.
	if c.MaxTaskCostUSD < 0 {
		return fmt.Errorf("max_task_cost_usd must not be negative, got %v", c.MaxTaskCostUSD)
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
	if err := c.Notify.validate(); err != nil {
		return err
	}
	// Non-negative, not positive: `0` is the documented "do not poll", and a
	// negative interval is a typo that would otherwise round to it silently
	// and look like it worked.
	if c.Update.PollInterval < 0 {
		return fmt.Errorf("update.poll_interval must not be negative, got %s", c.Update.PollInterval)
	}
	if err := c.Container.Validate(); err != nil {
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
	// Both halves of `notify:` are required for it to fire, and a half-written
	// block looks exactly like a working one until a task blocks at 3am and
	// nothing happens (task 046 decision 10). Neither refuses the file: a user
	// commenting `command` out for an afternoon should not have the same save
	// revert an unrelated log_level edit.
	if len(c.Notify.Command) > 0 && len(c.Notify.On) == 0 {
		out = append(out, "notify.command is set while notify.on is empty: "+
			"no state fires the hook, so the command will never run")
	}
	if len(c.Notify.On) > 0 && len(c.Notify.Command) == 0 {
		out = append(out, "notify.on lists states while notify.command is empty: "+
			"there is nothing to run, so no notification will be delivered")
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
