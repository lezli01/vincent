package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/taskstate"
)

// newDoctorCmd builds `vincent doctor` — the one command that answers "why is
// nothing running?" without needing four other surfaces and a hand-extracted
// bearer token (§17, task 006).
//
// It is the only data subcommand that still produces a full report when no
// daemon answers, the way `workflow validate` deliberately works offline: the
// daemon being down is one of the answers, and a diagnostic that refuses to
// speak until the thing it diagnoses is healthy would be useless.
func newDoctorCmd() *cobra.Command {
	var (
		fix   bool
		force bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose this vincent installation (exit 0 healthy, 1 problems found, 2 no daemon)",
		Long: "Report paths, daemon liveness, the log tail, database health, agent CLIs,\n" +
			"storage and task counts in one pass.\n\n" +
			"--fix reclaims orphaned directories (the same scan `vincent gc` runs) and\n" +
			"compacts the database. Both are writes, so the daemon performs them:\n" +
			"repair is unavailable when no daemon answers.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd, fix, force)
		},
	}
	jsonFlag(cmd)
	cmd.Flags().BoolVar(&fix, "fix", false,
		"reclaim orphaned directories and compact the database (needs a running daemon)")
	cmd.Flags().BoolVar(&force, "force", false,
		"with --fix, also reclaim orphans whose dirty check is unclear or unavailable")
	return cmd
}

func runDoctor(cmd *cobra.Command, fix, force bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	dirs, err := config.ResolveDirs()
	if err != nil {
		return err
	}
	c, unreachable := doctorClient(cmd, dirs)
	if unreachable != nil {
		// Exit 2 wins over any exit-1 finding in the local report (decision
		// 7) — but the report is still printed in full, because "the daemon
		// is down" is rarely the whole story and the rest of it is what the
		// user pastes into a bug report.
		rep := localDoctorReport(cmd.Context(), dirs)
		if fix {
			_, _ = fmt.Fprintln(errOut,
				"--fix needs a running daemon: every repair is a write, and only the daemon writes")
		}
		if err := renderDoctor(cmd, out, rep, nil); err != nil {
			return err
		}
		return errDaemonUnreachable
	}

	var actions []apiclient.DoctorFixAction
	var rep *apiclient.DoctorReport
	if fix {
		res, err := c.DoctorFix(cmd.Context(), force)
		if err != nil {
			return fmt.Errorf("%s", apiMessage(err))
		}
		actions, rep = res.Actions, res.Report
	} else {
		rep, err = c.Doctor(cmd.Context())
		if err != nil {
			return fmt.Errorf("%s", apiMessage(err))
		}
	}
	if err := renderDoctor(cmd, out, rep, actions); err != nil {
		return err
	}
	if !rep.Healthy() {
		return exitError{code: 1}
	}
	return nil
}

// doctorClient resolves a live daemon, returning a non-nil second value when
// none answered. Unlike the shared client helper it prints nothing: "no
// daemon" is a row in the report doctor is about to render, not an error
// message that pre-empts it.
func doctorClient(cmd *cobra.Command, dirs config.Dirs) (*apiclient.Client, error) {
	c, err := apiclient.Discover(dirs.Data)
	if err != nil {
		return nil, err
	}
	if _, err := c.Health(cmd.Context()); err != nil {
		return nil, err
	}
	return c, nil
}

// localDoctorReport composes the degraded report: everything that needs no
// database (§12.2 paths, config parse, §9.5 adapter detection, the log, disk
// free, the worktree scan), with the daemon rows read from its on-disk
// discovery records and the database and task rows left unknown.
//
// The unknowns are the point. "Only the daemon opens SQLite" is an ownership
// invariant, and a diagnostic is not a reason to carve an exception into it —
// so doctor reports what it cannot know rather than opening the file
// read-only behind the daemon's back.
func localDoctorReport(ctx context.Context, dirs config.Dirs) *doctor.Report {
	return doctor.Compose(ctx, doctor.Options{
		Dirs:    dirs,
		LogPath: daemon.LogPath(dirs.Data),
		TailLog: daemon.TailFile,
		Daemon:  localDaemonStatus(ctx, dirs),
		// ScanOrphans is nil: an orphan is an entry no task row claims, and the
		// claim set is in a database only the daemon opens. So the report
		// counts and sizes the worktrees it can see on disk and says the orphan
		// question is unanswered, rather than guessing from directory names
		// (decision 3).
	})
}

// localDaemonStatus mirrors `vincent daemon status`, which is the command
// doctor most resembles, and reports the same three outcomes.
func localDaemonStatus(ctx context.Context, dirs config.Dirs) doctor.Daemon {
	d := doctor.Daemon{Status: doctor.StatusNotRunning}
	running, err := daemon.ProbeRunning(dirs.Data)
	if err != nil {
		d.Detail = err.Error()
		return d
	}
	ri, riErr := daemon.ReadRuntimeInfo(dirs.Data)
	if !running {
		if riErr == nil {
			d.Detail = "a stale daemon.json is present from an unclean shutdown"
		}
		return d
	}
	if riErr != nil {
		// The lock is the liveness authority, so something is running; there
		// is simply no way to reach it.
		d.Status = doctor.StatusUnresponsive
		d.Detail = "the daemon holds its lock but daemon.json is unreadable"
		return d
	}
	started := ri.StartedAt.UTC()
	d.PID, d.Port, d.StartedAt = ri.PID, ri.Port, &started
	d.UptimeSeconds = int64(time.Since(ri.StartedAt).Seconds())
	h, err := daemon.CheckHealth(ctx, ri.Port)
	if err != nil {
		d.Status = doctor.StatusUnresponsive
		d.Detail = err.Error()
		return d
	}
	// Reachable but not through this code path's client — which only happens
	// when the token is unreadable, so the report is honest about the process
	// being healthy and the request never having been made.
	d.Status = doctor.StatusRunning
	d.Version = h.Version
	return d
}

// renderDoctor writes the report as JSON or as grouped tables.
func renderDoctor(cmd *cobra.Command, w io.Writer, rep *apiclient.DoctorReport, actions []apiclient.DoctorFixAction) error {
	if wantJSON(cmd) {
		if actions == nil {
			return emitJSON(w, rep)
		}
		return emitJSON(w, apiclient.DoctorFixResult{Actions: actions, Report: rep})
	}
	if actions != nil {
		rows := make([][]string, 0, len(actions))
		for _, a := range actions {
			rows = append(rows, []string{a.Action, a.Status, dash(a.Target), dash(a.Detail)})
		}
		if err := table(w, []string{"REPAIR", "RESULT", "TARGET", "DETAIL"}, rows); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	for _, group := range doctorGroups(rep) {
		if err := table(w, []string{group.name, ""}, group.rows); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return doctorProblems(w, rep)
}

type doctorGroup struct {
	name string
	rows [][]string
}

func doctorGroups(rep *apiclient.DoctorReport) []doctorGroup {
	return []doctorGroup{
		{"PATHS", doctorPathRows(rep.Paths)},
		{"DAEMON", doctorDaemonRows(rep.Daemon)},
		{"LOG", doctorLogRows(rep.Log)},
		{"DATABASE", doctorDatabaseRows(rep.Database)},
		{"AGENTS", doctorAgentRows(rep.Agents)},
		{"STORAGE", doctorStorageRows(rep.Storage)},
		{"TASKS", doctorTaskRows(rep.Tasks)},
	}
}

func doctorPathRows(p apiclient.DoctorPaths) [][]string {
	state := "ok"
	switch {
	case !p.ConfigFileExists:
		state = "not created yet (defaults apply)"
	case !p.ConfigParses:
		state = "DOES NOT PARSE: " + p.ConfigError
	case p.ConfigError != "":
		state = p.ConfigError
	}
	rows := [][]string{
		{"config dir", p.ConfigDir},
		{"data dir", p.DataDir},
		{"config file", p.ConfigFile},
		{"config", state},
	}
	// A config.yaml readable by other local accounts, with the chmod that
	// fixes it: the file can hold literal environment.set values (§12.3).
	for _, w := range p.ConfigPermissions {
		rows = append(rows, []string{"permissions", fmt.Sprintf(
			"%s is %s, want %s — run: %s", w.Path, w.Mode, w.ExpectedMode, w.Remediation)})
	}
	return rows
}

func doctorDaemonRows(d apiclient.DoctorDaemon) [][]string {
	rows := [][]string{{"status", d.Status}}
	if d.PID != 0 {
		rows = append(rows, []string{"pid", strconv.Itoa(d.PID)})
	}
	if d.Port != 0 {
		rows = append(rows, []string{"port", strconv.Itoa(d.Port)})
	}
	if d.Version != "" {
		rows = append(rows, []string{"version", d.Version})
	}
	if d.StartedAt != nil {
		rows = append(rows,
			[]string{"started", d.StartedAt.Local().Format(time.RFC3339)},
			[]string{"uptime", (time.Duration(d.UptimeSeconds) * time.Second).String()})
	}
	if d.Detail != "" {
		rows = append(rows, []string{"detail", d.Detail})
	}
	return rows
}

func doctorLogRows(l apiclient.DoctorLog) [][]string {
	rows := [][]string{{"path", l.Path}}
	if !l.Exists {
		rows = append(rows, []string{"state", "no log yet"})
	} else {
		rows = append(rows, []string{"size", humanBytes(l.SizeBytes)})
		if l.ModTime != nil {
			rows = append(rows, []string{"modified", l.ModTime.Local().Format(time.RFC3339)})
		}
	}
	if l.Error != "" {
		rows = append(rows, []string{"error", l.Error})
	}
	for _, line := range l.Tail {
		rows = append(rows, []string{"", line})
	}
	return rows
}

func doctorDatabaseRows(d apiclient.DoctorDatabase) [][]string {
	rows := [][]string{{"path", d.Path}}
	if !d.Known {
		// Deliberately not read from a second process: only the daemon opens
		// SQLite (§4), and a diagnostic does not get an exception.
		rows = append(rows, []string{"state", "unknown — daemon not running"})
		return rows
	}
	rows = append(rows,
		[]string{"size", humanBytes(d.SizeBytes)},
		[]string{"schema version", fmt.Sprintf("%d (binary embeds %d)", d.SchemaVersion, d.NewestMigration)},
		[]string{"integrity_check", dash(d.IntegrityCheck)})
	if d.Error != "" {
		rows = append(rows, []string{"error", d.Error})
	}
	return rows
}

func doctorAgentRows(agents []apiclient.DoctorAgent) [][]string {
	rows := make([][]string, 0, len(agents))
	for _, a := range agents {
		var parts []string
		if a.Available {
			parts = append(parts, "found")
			if a.Version != "" {
				parts = append(parts, a.Version)
			}
			parts = append(parts, "auth "+loggedInWord(a.LoggedIn))
			if a.Path != "" {
				parts = append(parts, a.Path)
			}
		} else {
			parts = append(parts, "not found")
			if a.Error != "" {
				parts = append(parts, a.Error)
			}
		}
		rows = append(rows, []string{a.Name, strings.Join(parts, "  ")})
	}
	return rows
}

// loggedInWord renders the §9.5 tri-state. "unknown" is a real answer, not a
// hedge: claude's CLI exposes no non-interactive auth surface, so vincent
// declines to accuse it either way.
func loggedInWord(v *bool) string {
	switch {
	case v == nil:
		return "unknown"
	case *v:
		return "ok"
	default:
		return "NOT LOGGED IN"
	}
}

func doctorStorageRows(s apiclient.DoctorStorage) [][]string {
	free := "unknown"
	if s.DiskError != "" {
		free = s.DiskError
	} else if s.DiskTotalBytes > 0 {
		free = fmt.Sprintf("%s free of %s",
			humanBytes(int64(s.DiskFreeBytes)), humanBytes(int64(s.DiskTotalBytes)))
	}
	rows := [][]string{
		{"disk", free},
		{"worktrees", fmt.Sprintf("%d using %s", s.WorktreeCount, humanBytes(s.WorktreeBytes))},
	}
	if !s.OrphansKnown {
		rows = append(rows, []string{"orphaned", "unknown — daemon not running"})
	} else {
		rows = append(rows, []string{"orphaned", strconv.Itoa(len(s.Orphans))})
		for _, o := range s.Orphans {
			note := o.Path + "  " + humanBytes(o.SizeBytes)
			// The skip reason is the daemon's own string, the same way
			// `vincent gc` prints it — one vocabulary for one classification.
			switch o.Skip {
			case "":
			case apiclient.SkipNotADirectory:
				note += "  (not a directory — never removed)"
			default:
				note += "  (" + o.Skip + " — needs --force)"
			}
			rows = append(rows, []string{"", note})
		}
	}
	if s.ScanError != "" {
		rows = append(rows, []string{"scan error", s.ScanError})
	}
	return rows
}

func doctorTaskRows(t apiclient.DoctorTasks) [][]string {
	if !t.Known {
		return [][]string{{"state", "unknown — daemon not running"}}
	}
	rows := make([][]string, 0, len(taskstate.All)+1)
	// Walked in §6 order rather than map order, so two runs of doctor produce
	// diffable output.
	for _, s := range taskstate.All {
		rows = append(rows, []string{string(s), strconv.Itoa(t.Counts[string(s)])})
	}
	rows = append(rows, []string{"total", strconv.Itoa(t.Total)})
	if t.Error != "" {
		rows = append(rows, []string{"error", t.Error})
	}
	return rows
}

func doctorProblems(w io.Writer, rep *apiclient.DoctorReport) error {
	if rep.Healthy() {
		_, err := fmt.Fprintln(w, "no problems found")
		return err
	}
	rows := make([][]string, 0, len(rep.Problems))
	for _, p := range rep.Problems {
		rows = append(rows, []string{p.Group, p.Message})
	}
	return table(w, []string{"PROBLEMS", ""}, rows)
}
