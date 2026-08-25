package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/version"
)

// handleDoctor serves GET /v1/doctor: the whole §17 diagnostic in one body —
// paths, daemon, log, database, agents, storage, tasks — composed from
// internal/doctor plus the rows only the daemon can answer.
//
// It is strictly read-only. Repair is POST /v1/doctor/fix (task 005 decision
// 5): the router allow-lists methods per route, and a GET that deletes
// directories would be wrong in a table clients read as a contract.
//
// `?probe=false` serves adapter availability from the §9.6 binary-identity
// cache instead of forcing a re-probe (task 029 decision 4).
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.doctorReport(r.Context(), wantsProbe(r)))
}

// wantsProbe reads `?probe`, defaulting to true.
//
// The default is what task 005 decision 2 decided and is unchanged for every
// caller that decision was written about: `vincent doctor` is a command a
// human ran deliberately, and a cached `logged_in: false` would break the loop
// — run doctor, log in, run doctor again, still told you are logged out. What
// the flag adds is the caller that decision was *not* about: a TUI panel that
// opens on a keypress has no such loop, and spawning three subprocesses every
// time someone presses `6` would be a regression in a view that is cheap today
// (task 029 decision 4).
func wantsProbe(r *http.Request) bool {
	if !r.URL.Query().Has("probe") {
		return true
	}
	v := r.URL.Query().Get("probe")
	return v != "false" && v != "0"
}

// doctorReport composes the full report. Every daemon-only group degrades to
// its own error string rather than failing the request: a diagnostic that
// answers 500 because one of its seven questions could not be answered is
// exactly the tool that is no use on the day it is needed.
func (s *Server) doctorReport(ctx context.Context, probe bool) *doctor.Report {
	// Nil, not a method value, when there is no reclaimer: internal/doctor
	// reads a nil scan as "nobody could answer the orphan question", which is
	// the same state a client gets when no daemon answered at all.
	var scan func(context.Context) ([]doctor.Orphan, error)
	if s.deps.Reclaimer != nil {
		scan = s.doctorScanOrphans
	}
	rep := doctor.Compose(ctx, doctor.Options{
		Dirs:        s.deps.Dirs,
		LogPath:     s.deps.LogPath,
		TailLog:     s.deps.TailLog,
		Daemon:      s.doctorDaemon(),
		Agents:      s.doctorAgents(ctx, probe),
		ScanOrphans: scan,
	})
	s.fillDatabase(ctx, rep)
	s.fillTasks(ctx, rep)
	rep.Evaluate()
	return rep
}

// doctorDaemon is the liveness group. The handler is running inside the
// daemon, so "running" is not a probe result but a fact — which is also why
// internal/doctor takes it as a parameter (decision 6).
func (s *Server) doctorDaemon() doctor.Daemon {
	started := s.deps.StartedAt
	d := doctor.Daemon{
		Status:        doctor.StatusRunning,
		PID:           os.Getpid(),
		StartedAt:     &started,
		UptimeSeconds: int64(time.Since(started).Seconds()),
		Version:       version.Version(),
	}
	if _, port, err := net.SplitHostPort(s.deps.ListenAddr); err == nil {
		if n, err := strconv.Atoi(port); err == nil {
			d.Port = n
		}
	}
	return d
}

// doctorAgents reads §9.5 availability from the §9.6 cache, with refresh
// forced unless the caller asked otherwise (decision 2, narrowed by task 029
// decision 4).
//
// Auth state is not a pure function of the binary, so the binary-identity key
// is a floor for it, not a guarantee: a cached `logged_in: false` would
// otherwise survive the user logging in until the CLI was upgraded, breaking
// doctor in the exact loop it exists for — run doctor, log in, run doctor
// again, still told you are logged out. The cost is one probe per adapter per
// invocation of a command the user ran deliberately, bounded by the adapters'
// own probe timeouts. probe=false is for the caller with no such loop, and it
// is never the default.
func (s *Server) doctorAgents(ctx context.Context, probe bool) []doctor.Agent {
	out := []doctor.Agent{}
	if s.deps.Catalog == nil {
		return out
	}
	for _, name := range s.deps.Catalog.Names() {
		e, ok := s.deps.Catalog.Entry(ctx, name, probe)
		if !ok {
			continue
		}
		out = append(out, doctor.Agent{
			Name:      name,
			Available: e.Availability.Found,
			Path:      e.Availability.Path,
			Version:   e.Availability.Version,
			LoggedIn:  e.Availability.LoggedIn,
			Error:     e.Availability.Error,
		})
	}
	return out
}

// doctorScanOrphans is gc's read-only scan (task 005), translated into the
// doctor report's shape. There is one classifier and doctor is a reader of
// it: an orphan is an entry no task row *claims*, which is a question only the
// daemon's task table can answer, and answering it a second time from
// directory names would miss the crash case §10 exists for.
//
// The scan runs without force, so its skip reasons are the ones a plain
// `vincent gc` would report; `--fix --force` re-runs the whole thing with
// force rather than reinterpreting these.
func (s *Server) doctorScanOrphans(ctx context.Context) ([]doctor.Orphan, error) {
	rep, err := s.deps.Reclaimer.Scan(ctx)
	if err != nil {
		return nil, err
	}
	return doctorOrphans(rep), nil
}

// doctorOrphans maps a reclaim report onto the wire type, keeping gc's own
// vocabulary for the skip reasons (§18's snake_case names).
func doctorOrphans(rep taskrun.Report) []doctor.Orphan {
	out := make([]doctor.Orphan, 0, len(rep.Orphans))
	for _, o := range rep.Orphans {
		out = append(out, doctor.Orphan{
			Name:      filepath.Base(o.Path),
			Path:      o.Path,
			Kind:      o.Kind,
			SizeBytes: o.Bytes,
			TaskID:    o.TaskID,
			Skip:      o.Skip,
		})
	}
	return out
}

// fillDatabase fills the §14 group, including task 029's footprint, row counts
// and retention span. The scans live here rather than on /v1/info because this
// endpoint is the deliberately cold one: it already forces three adapter
// probes, an integrity_check and a worktree walk, so a COUNT(*) per table
// costs it nothing new (task 029 decision 1).
func (s *Server) fillDatabase(ctx context.Context, rep *doctor.Report) {
	if s.deps.Store == nil {
		return
	}
	db := &rep.Database
	db.Known = true
	db.Path = s.deps.Store.Path()
	db.NewestMigration = store.NewestMigration()
	if sizes, err := s.deps.Store.FileSizes(); err == nil {
		db.SizeBytes = sizes.MainBytes
		db.WALBytes, db.SHMBytes = sizes.WALBytes, sizes.SHMBytes
		db.TotalBytes = sizes.TotalBytes
	} else {
		db.Error = err.Error()
	}
	v, err := s.deps.Store.SchemaVersion(ctx)
	if err != nil {
		db.Error = err.Error()
		return
	}
	db.SchemaVersion = v
	check, err := s.deps.Store.IntegrityCheck(ctx)
	if err != nil {
		db.Error = err.Error()
		return
	}
	db.IntegrityCheck = check
	rows, err := s.deps.Store.TableRows(ctx)
	if err != nil {
		db.Error = err.Error()
		return
	}
	db.TableRows = rows
	oldest, err := s.deps.Store.OldestEventAt(ctx)
	if err != nil {
		db.Error = err.Error()
		return
	}
	db.OldestEventAt = oldest
	snapshots, err := s.deps.Store.WorkflowSnapshotBytes(ctx)
	if err != nil {
		db.Error = err.Error()
		return
	}
	db.WorkflowSnapshotBytes = snapshots
}

func (s *Server) fillTasks(ctx context.Context, rep *doctor.Report) {
	if s.deps.Store == nil {
		return
	}
	counts, err := s.deps.Store.CountTasksByState(ctx)
	if err != nil {
		rep.Tasks.Error = err.Error()
		return
	}
	byName := make(map[string]int, len(counts))
	for state, n := range counts {
		byName[string(state)] = n
	}
	rep.SetTaskCounts(byName)
	// The §12.4 contradiction (issue #142). It is asked for separately from
	// the tally because it is not one: a count of `queued` tasks is
	// information, and a `queued` task whose previous attempt is still open
	// is a defect that stops that task running.
	bad, err := s.deps.Store.UnreconciledTasks(ctx)
	if err != nil {
		rep.Tasks.Error = err.Error()
		return
	}
	rep.Tasks.Unreconciled = make([]doctor.UnreconciledTask, 0, len(bad))
	for _, u := range bad {
		rep.Tasks.Unreconciled = append(rep.Tasks.Unreconciled, doctor.UnreconciledTask{
			TaskID: u.TaskID, State: string(u.State), OpenStepRuns: u.OpenStepRuns,
		})
	}
}

// doctorFixRequest is the POST /v1/doctor/fix body. `?force` is accepted too,
// matching how archive takes its own confirmation (§13.2).
type doctorFixRequest struct {
	Force bool `json:"force"`
}

// handleDoctorFix serves POST /v1/doctor/fix: it removes orphaned worktrees
// and compacts the database, then answers with what it did plus a fresh
// report.
//
// The daemon performs every write, so the one-writer invariant holds and
// repair is simply unavailable when no daemon answers — which is what
// `vincent doctor --fix` reports rather than doing it locally.
func (s *Server) handleDoctorFix(w http.ResponseWriter, r *http.Request) {
	var req doctorFixRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	force := req.Force || hasForce(r)
	// Both reports force the re-probe: repair is the deliberate-command path
	// decision 2 was written about, so `?probe` is a GET-only flag.
	before := s.doctorReport(r.Context(), true)
	actions := s.reclaimOrphans(r.Context(), force)
	actions = append(actions, s.compactDatabase(r.Context(), before))
	writeJSON(w, http.StatusOK, doctor.FixResult{
		Actions: actions,
		Report:  s.doctorReport(r.Context(), true),
	})
}

// reclaimOrphans hands the §10 residue to gc and reports what it did.
//
// `--fix` runs the same `Reclaim` that `vincent gc` runs, under the same
// reclaim lock, with the same containment check and the same without-force
// gate on a worktree git calls dirty — or cannot judge at all. Doctor owns
// none of that policy; duplicating it is how the two commands would come to
// delete different sets.
func (s *Server) reclaimOrphans(ctx context.Context, force bool) []doctor.FixAction {
	actions := []doctor.FixAction{}
	if s.deps.Reclaimer == nil {
		return actions
	}
	rep, err := s.deps.Reclaimer.Reclaim(ctx, force, false)
	if err != nil {
		return append(actions, doctor.FixAction{
			Action: doctor.ActionRemoveWorktree,
			Status: doctor.FixFailed,
			Detail: err.Error(),
		})
	}
	for _, o := range rep.Orphans {
		a := doctor.FixAction{Action: doctor.ActionRemoveWorktree, Target: o.Path}
		switch {
		case o.Removed:
			a.Status, a.FreedBytes = doctor.FixDone, o.Bytes
			if o.Kind == taskrun.KindWorktree {
				// The registration in the project repo is deliberately left
				// alone: doctor's scope is the data dir.
				a.Detail = "run `git worktree prune` in the project repo to clear its stale registration"
			}
		case o.Error != "":
			a.Status, a.Detail = doctor.FixFailed, o.Error
		case o.Skip == taskrun.SkipNotADirectory:
			a.Status = doctor.FixSkipped
			a.Detail = "not a directory; vincent did not create it"
		case o.Skip != "":
			a.Status = doctor.FixSkipped
			a.Detail = o.Skip + "; confirm with --force"
		default:
			a.Status = doctor.FixSkipped
		}
		actions = append(actions, a)
	}
	return actions
}

// compactDatabase runs a real VACUUM, unless work is in flight.
//
// The daemon holds SQLite open on one WAL connection and a full VACUUM
// rewrites the file under an exclusive lock, so compacting while a step is
// mid-write would stall it (decision 4). Reporting the refusal beats imposing
// the stall — and a checkpoint alone was rejected, because it does not
// reclaim the pages transcript pruning and task deletion freed, which is the
// growth this exists for.
func (s *Server) compactDatabase(ctx context.Context, rep *doctor.Report) doctor.FixAction {
	a := doctor.FixAction{Action: doctor.ActionCompactDatabase, Target: rep.Database.Path}
	if s.deps.Store == nil {
		a.Status, a.Detail = doctor.FixSkipped, "no database is open"
		return a
	}
	inFlight, err := s.deps.Store.CountSlotHolders(ctx)
	if err != nil {
		a.Status, a.Detail = doctor.FixFailed, err.Error()
		return a
	}
	if inFlight > 0 {
		a.Status = doctor.FixSkipped
		a.Detail = strconv.Itoa(inFlight) + " task(s) in flight; a VACUUM would stall them mid-step"
		return a
	}
	before := rep.Database.SizeBytes
	if err := s.deps.Store.Vacuum(ctx); err != nil {
		a.Status, a.Detail = doctor.FixFailed, err.Error()
		return a
	}
	a.Status = doctor.FixDone
	if fi, err := os.Stat(rep.Database.Path); err == nil && before > fi.Size() {
		a.FreedBytes = before - fi.Size()
	}
	return a
}
