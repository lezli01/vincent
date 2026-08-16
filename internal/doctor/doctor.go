package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// DefaultLogTail is how many trailing daemon-log lines a report carries. Wide
// enough to hold a startup failure with its stack of causes, narrow enough
// that the whole report still pastes into an issue.
const DefaultLogTail = 20

// Daemon status values (§12.1, mirroring `vincent daemon status`).
const (
	// StatusRunning: the process holds the lock and answered /v1/health.
	StatusRunning = "running"
	// StatusNotRunning: nothing holds the lock.
	StatusNotRunning = "not_running"
	// StatusUnresponsive: the process is alive but did not answer. This is a
	// problem; "not running" is not.
	StatusUnresponsive = "unresponsive"
)

// Problem groups, matching the report's own sections.
const (
	GroupPaths    = "paths"
	GroupDaemon   = "daemon"
	GroupDatabase = "database"
	GroupStorage  = "storage"
)

// Report is one complete diagnostic. Every group is always present: a row
// that could not be determined says so in its own fields rather than being
// omitted, because "absent" and "unknown" are different answers to a user
// asking why nothing is running.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Paths       Paths     `json:"paths"`
	Daemon      Daemon    `json:"daemon"`
	Log         Log       `json:"log"`
	Database    Database  `json:"database"`
	Agents      []Agent   `json:"agents"`
	Storage     Storage   `json:"storage"`
	Tasks       Tasks     `json:"tasks"`
	// Problems is the closed set of findings that make `vincent doctor` exit
	// 1 (task 005 decision 7). It is computed by Evaluate, on the server as
	// well as locally, so a client never re-derives the verdict.
	Problems []Problem `json:"problems"`
}

// Paths is where vincent reads and writes (§12.2), plus whether the one file
// a user edits by hand is currently legal.
type Paths struct {
	ConfigDir  string `json:"config_dir"`
	DataDir    string `json:"data_dir"`
	ConfigFile string `json:"config_file"`
	// ConfigFileExists is false before the first daemon start writes the
	// commented default file. That is not a fault: defaults apply.
	ConfigFileExists bool `json:"config_file_exists"`
	// ConfigParses is true when the file loaded and validated. A file that
	// exists and does not parse is a problem — the daemon refuses to start on
	// it (§12.3) — which is exactly the "why is nothing running?" the command
	// is for.
	ConfigParses bool   `json:"config_parses"`
	ConfigError  string `json:"config_error,omitempty"`
}

// Daemon is the liveness picture. It is supplied by the caller rather than
// probed here (decision 6): the API handler knows its own identity, and the
// CLI's no-daemon path reads it from internal/daemon.
type Daemon struct {
	Status        string     `json:"status"`
	PID           int        `json:"pid,omitempty"`
	Port          int        `json:"port,omitempty"`
	StartedAt     *time.Time `json:"started_at"`
	UptimeSeconds int64      `json:"uptime_seconds"`
	Version       string     `json:"version,omitempty"`
	// Detail explains a status the fields alone cannot — a stale daemon.json
	// left by an unclean shutdown, say.
	Detail string `json:"detail,omitempty"`
}

// Log is the daemon log's stat plus its tail. The tail is the reason the TUI
// used to be the only way to diagnose a daemon that would not start.
type Log struct {
	Path      string     `json:"path"`
	Exists    bool       `json:"exists"`
	SizeBytes int64      `json:"size_bytes"`
	ModTime   *time.Time `json:"mod_time"`
	Tail      []string   `json:"tail"`
	// Error is set when the log could not be read. A log that is merely
	// absent is not an error — a daemon that has never run has never written
	// one — so Exists carries that instead.
	Error string `json:"error,omitempty"`
}

// Database is the §14 store as seen from outside. Known is false when no
// daemon answered: only the daemon opens SQLite, and a diagnostic does not
// change that.
type Database struct {
	Path            string `json:"path"`
	Known           bool   `json:"known"`
	SizeBytes       int64  `json:"size_bytes"`
	SchemaVersion   int    `json:"schema_version"`
	NewestMigration int    `json:"newest_migration"`
	IntegrityCheck  string `json:"integrity_check,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Agent is one adapter's §9.5 availability, trimmed to what a diagnostic
// needs. The option catalogs of §9.6 are `GET /v1/agents`' job.
type Agent struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	// LoggedIn is null where the CLI exposes no non-interactive auth surface
	// — claude today (§9.5). It is never a guess.
	LoggedIn *bool  `json:"logged_in"`
	Error    string `json:"error,omitempty"`
}

// Storage is the data dir's footprint (§17) and the §10 residue.
type Storage struct {
	// WorktreesDir is {data_dir}/worktrees, which the count and byte total
	// below describe. Orphans span both data roots, because gc does (task 005)
	// and doctor reports gc's answer rather than a second one.
	WorktreesDir   string `json:"worktrees_dir"`
	DiskFreeBytes  uint64 `json:"disk_free_bytes"`
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
	DiskError      string `json:"disk_error,omitempty"`

	WorktreeCount int   `json:"worktree_count"`
	WorktreeBytes int64 `json:"worktree_bytes"`

	// OrphansKnown is false when no daemon answered: an orphan is defined by
	// what the task table claims (task 005), so without a daemon there is
	// nothing to diff the directories against and none of them is accused.
	OrphansKnown bool     `json:"orphans_known"`
	Orphans      []Orphan `json:"orphans"`
	ScanError    string   `json:"scan_error,omitempty"`
}

// Orphan is one entry under a data root that no task row claims — gc's
// classification verbatim (task 005; spec §10), not a second definition.
// Doctor reports it and `--fix` reclaims it through the same code path, so
// `vincent gc` and `vincent doctor` can never disagree about what an orphan
// is.
type Orphan struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Kind is which data root the entry sits under — worktree or transcript.
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	// TaskID is the id the directory name carries, or 0 when the name is not
	// a task id at all. It is informational: the claim decides, not the name.
	TaskID int64 `json:"task_id"`
	// Skip is why gc would leave this entry alone — `worktree_dirty`,
	// `dirty_unknown`, `not_a_directory` — or "" when it is eligible. The
	// first two clear under `--force`; the third never does, because vincent
	// only ever creates directories under a data root.
	Skip string `json:"skip,omitempty"`
}

// Tasks is the §6 state tally. Known is false without a daemon, for the same
// reason Database.Known is.
type Tasks struct {
	Known bool `json:"known"`
	// Counts is keyed by §6 state name and carries a zero for every state, so
	// a reader sees the whole vocabulary rather than only what happens to be
	// populated.
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
	Error  string         `json:"error,omitempty"`
}

// Problem is one finding from the closed unhealthy set (decision 7). A
// missing or logged-out agent CLI is deliberately not one: most machines have
// one of three adapters installed, and a doctor that exits 1 almost
// everywhere is useless in a script.
type Problem struct {
	Group   string `json:"group"`
	Message string `json:"message"`
}

// Options are the inputs Compose cannot obtain for itself.
type Options struct {
	// Dirs are the resolved §12.2 directories.
	Dirs config.Dirs
	// LogPath is {data_dir}/logs/daemon.log. It arrives as a parameter
	// because daemon.LogPath lives in a package this one must not import
	// (decision 6); both callers pass its result in.
	LogPath string
	// LogTail is how many trailing lines to carry; <= 0 means DefaultLogTail.
	LogTail int
	// TailLog reads those trailing lines. It arrives as a function for the
	// same reason LogPath arrives as a string: the one implementation lives
	// in internal/daemon, which this package must not import, and a second
	// copy of a routine with subtleties (a window that starts mid-line, a
	// rotation that renames the file underneath the reader) would drift.
	// Both real callers pass daemon.TailFile. Nil means the report carries
	// the log's stat but no tail.
	TailLog func(path string, n int) ([]string, error)
	// Daemon is the liveness picture the caller already established.
	Daemon Daemon
	// Agents, when non-nil, is used verbatim instead of detecting locally.
	// The API handler passes the §9.6 catalog's answer this way so the
	// endpoint and `GET /v1/agents` can never disagree.
	Agents []Agent
	// ScanOrphans is gc's read-only scan (task 005): the one classifier, run
	// by the daemon that owns the task table. It arrives as a function for the
	// same reason TailLog does — the implementation lives in internal/taskrun,
	// which this package must not import — and nil means no daemon answered,
	// so the report says orphans are unknown rather than guessing from
	// directory names.
	ScanOrphans func(ctx context.Context) ([]Orphan, error)
}

// Compose builds every group this package can answer without a database and
// evaluates the problem set over it. The API handler fills the Database and
// Tasks groups afterwards and calls Evaluate again — which is idempotent, and
// rebuilds Problems from scratch each time.
func Compose(ctx context.Context, opts Options) *Report {
	r := &Report{GeneratedAt: time.Now().UTC(), Daemon: opts.Daemon}
	cfg, paths := inspectPaths(opts.Dirs)
	r.Paths = paths
	r.Log = inspectLog(opts.LogPath, opts.LogTail, opts.TailLog)
	r.Database = Database{
		Path:            filepath.Join(opts.Dirs.Data, "vincent.db"),
		NewestMigration: store.NewestMigration(),
	}
	r.Tasks = Tasks{Counts: zeroStateCounts()}
	if opts.Agents != nil {
		r.Agents = opts.Agents
	} else {
		r.Agents = DetectAgents(ctx, cfg)
	}
	r.Storage = inspectStorage(ctx, opts)
	r.Evaluate()
	return r
}

// inspectPaths resolves §12.2 and answers the one question about config.yaml
// a user can act on: does it parse? The loaded configuration is returned too,
// because adapter detection needs the configured binary paths (§12.3) and
// re-reading the file would be a second chance to disagree with this row.
func inspectPaths(dirs config.Dirs) (config.Config, Paths) {
	file := filepath.Join(dirs.Config, config.FileName)
	p := Paths{ConfigDir: dirs.Config, DataDir: dirs.Data, ConfigFile: file}
	cfg := config.Default()
	if _, err := os.Stat(file); err != nil {
		// Absent is not a fault: the daemon writes the commented default on
		// first start, and until then the defaults are what is in force.
		if !errors.Is(err, os.ErrNotExist) {
			p.ConfigError = err.Error()
		}
		return cfg, p
	}
	p.ConfigFileExists = true
	loaded, err := config.Load(file)
	if err != nil {
		p.ConfigError = err.Error()
		return cfg, p
	}
	p.ConfigParses = true
	return loaded, p
}

// inspectLog stats the daemon log and reads its tail. A log that is simply
// not there is not an error — a daemon that has never started has never
// written one — which is why Exists is a field rather than Error being set.
func inspectLog(path string, n int, tail func(string, int) ([]string, error)) Log {
	if n <= 0 {
		n = DefaultLogTail
	}
	l := Log{Path: path, Tail: []string{}}
	fi, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			l.Error = err.Error()
		}
		return l
	}
	l.Exists = true
	l.SizeBytes = fi.Size()
	mt := fi.ModTime().UTC()
	l.ModTime = &mt
	if tail == nil {
		return l
	}
	lines, err := tail(path, n)
	if err != nil {
		l.Error = err.Error()
		return l
	}
	if lines != nil {
		l.Tail = lines
	}
	return l
}

// DetectAgents probes every adapter's §9.5 availability against the
// configured binary paths.
//
// It builds the three adapters here rather than taking a registry, because
// the caller that needs this — `vincent doctor` with no daemon — has no
// registry to hand: internal/daemon builds the production one, and this
// package must not import it (decision 6). The construction is deliberately
// the same shape as internal/daemon's, with a fixed configuration instead of
// a hot-reloading accessor: nothing is going to reload underneath a report
// that takes one snapshot.
func DetectAgents(ctx context.Context, cfg config.Config) []Agent {
	reg := agent.NewRegistry(
		claude.New(func() string { return cfg.Agents.Claude.Path }),
		codex.New(func() string { return cfg.Agents.Codex.Path }),
		cursor.New(func() string { return cfg.Agents.Cursor.Path }),
	)
	out := make([]Agent, 0, len(reg.Names()))
	for _, name := range reg.Names() {
		a, ok := reg.Get(name)
		if !ok {
			continue
		}
		av, err := a.Detect(ctx)
		if err != nil {
			av = agent.Availability{Error: err.Error()}
		}
		out = append(out, Agent{
			Name:      name,
			Available: av.Found,
			Path:      av.Path,
			Version:   av.Version,
			LoggedIn:  av.LoggedIn,
			Error:     av.Error,
		})
	}
	return out
}

// zeroStateCounts is the §6 vocabulary with every count at zero, so a report
// shows "blocked 0" rather than nothing at all.
func zeroStateCounts() map[string]int {
	counts := make(map[string]int, len(taskstate.All))
	for _, s := range taskstate.All {
		counts[string(s)] = 0
	}
	return counts
}

// SetTaskCounts fills the Tasks group from a state tally. It is the daemon's
// half of the report; the map it is given may omit empty states, and the
// §6 vocabulary is filled in around it.
func (r *Report) SetTaskCounts(counts map[string]int) {
	r.Tasks = Tasks{Known: true, Counts: zeroStateCounts()}
	for state, n := range counts {
		r.Tasks.Counts[state] += n
		r.Tasks.Total += n
	}
}

// Evaluate recomputes Problems from the report's current contents. It is
// idempotent and safe to call after filling in the daemon-only groups.
//
// The set is closed on purpose (decision 7). Task counts never appear here:
// twelve blocked tasks is information, not a defect. Nor does a missing or
// logged-out agent CLI — it is reported plainly in its own group.
func (r *Report) Evaluate() {
	r.Problems = []Problem{}
	if r.Paths.ConfigFileExists && !r.Paths.ConfigParses {
		r.Problems = append(r.Problems, Problem{
			Group:   GroupPaths,
			Message: fmt.Sprintf("%s does not parse: %s", r.Paths.ConfigFile, r.Paths.ConfigError),
		})
	}
	if r.Daemon.Status == StatusUnresponsive {
		r.Problems = append(r.Problems, Problem{
			Group:   GroupDaemon,
			Message: "the daemon process is alive but not answering; stop it and start it again",
		})
	}
	if r.Database.Known {
		if r.Database.Error != "" {
			r.Problems = append(r.Problems, Problem{
				Group: GroupDatabase, Message: r.Database.Error,
			})
		}
		if r.Database.IntegrityCheck != "" && r.Database.IntegrityCheck != "ok" {
			r.Problems = append(r.Problems, Problem{
				Group:   GroupDatabase,
				Message: "PRAGMA integrity_check reports: " + r.Database.IntegrityCheck,
			})
		}
		if r.Database.NewestMigration > 0 && r.Database.SchemaVersion > r.Database.NewestMigration {
			r.Problems = append(r.Problems, Problem{
				Group: GroupDatabase,
				Message: fmt.Sprintf(
					"the database is at schema version %d but this binary embeds only %d — it was written by a newer vincent",
					r.Database.SchemaVersion, r.Database.NewestMigration),
			})
		}
	}
	if r.Storage.OrphansKnown && len(r.Storage.Orphans) > 0 {
		r.Problems = append(r.Problems, Problem{
			Group: GroupStorage,
			Message: fmt.Sprintf(
				"%d orphaned director(ies) under the data roots; reclaim them with `vincent doctor --fix` or `vincent gc`",
				len(r.Storage.Orphans)),
		})
	}
}

// Healthy reports whether nothing in the closed unhealthy set fired.
func (r *Report) Healthy() bool { return len(r.Problems) == 0 }

// Repair actions and their outcomes (§13.2, `POST /v1/doctor/fix`).
const (
	ActionRemoveWorktree  = "remove_worktree"
	ActionCompactDatabase = "compact_database"

	FixDone    = "done"
	FixSkipped = "skipped"
	FixFailed  = "failed"
)

// FixAction is one repair the daemon attempted, and what came of it. A
// skipped action is reported with the reason it was skipped rather than
// silently omitted: `--fix` refusing to compact while work is in flight is
// something the user has to be told, not something to hide (decision 4).
type FixAction struct {
	Action     string `json:"action"`
	Target     string `json:"target,omitempty"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	FreedBytes int64  `json:"freed_bytes,omitempty"`
}

// FixResult is what `POST /v1/doctor/fix` answers: what it did, plus a fresh
// report taken afterwards, so a client never has to make a second call to see
// the state it just changed.
type FixResult struct {
	Actions []FixAction `json:"actions"`
	Report  *Report     `json:"report"`
}
