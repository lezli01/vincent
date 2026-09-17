package backupsched

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/version"
)

// CheckInterval is how often Run asks whether a backup is due. It bounds how
// late a run starts and how long a config edit waits to take effect; a check
// that finds nothing due is one directory read.
const CheckInterval = time.Minute

// RetryDelay is how long a failed attempt waits before the next one (decision
// 2). Without it a failure would leave the newest archive stale and every
// check would try again — a hot loop holding the store's only connection.
const RetryDelay = time.Hour

// Archive names are `vincent-backup-<UTC YYYYMMDDTHHMMSSZ>.tar.gz`. The
// timestamp is fixed-width UTC, so sorting names sorts by time, and the
// pattern is the only thing that makes a file this timer's to count or delete.
const (
	namePrefix     = "vincent-backup-"
	nameSuffix     = ".tar.gz"
	nameTimeLayout = "20060102T150405Z"
)

var namePattern = regexp.MustCompile(`^vincent-backup-\d{8}T\d{6}Z\.tar\.gz$`)

// ArchiveName is the file name the timer gives an archive taken at t.
func ArchiveName(t time.Time) string {
	return namePrefix + t.UTC().Format(nameTimeLayout) + nameSuffix
}

// archiveTime parses a timer-written archive name, reporting false for any
// other file — including a manual backup that happens to sit beside them.
func archiveTime(name string) (time.Time, bool) {
	if !namePattern.MatchString(name) {
		return time.Time{}, false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, namePrefix), nameSuffix)
	t, err := time.Parse(nameTimeLayout, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Deps are the timer's inputs.
type Deps struct {
	// Config returns the current configuration; it is read on every check.
	Config func() config.Config
	// DataDir and ConfigDir are the resolved §12.2 directories the archive
	// reads, and DataDir is what `backup.dir: ""` resolves under.
	DataDir   string
	ConfigDir string
	// CopyDatabase writes a consistent database copy (store.BackupTo).
	CopyDatabase func(ctx context.Context, dst string) error
	// SchemaVersion is the manifest's schema_version (store.SchemaVersion).
	SchemaVersion func(ctx context.Context) (int, error)
	Logger        *slog.Logger
}

// Scheduler is the timer. Its status sits behind a mutex because the API
// reads it from request goroutines while Run writes it.
type Scheduler struct {
	deps Deps

	mu     sync.Mutex
	status Status
	// failedAt is when the most recent attempt failed; zero once one
	// succeeds. It is what holds the next attempt back by RetryDelay.
	failedAt time.Time
}

// New returns a timer over deps.
func New(deps Deps) *Scheduler {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	return &Scheduler{deps: deps}
}

// Status returns a copy of what the most recent check learned.
func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Run checks once, then on every tick until ctx is done. It is meant to be
// started in a goroutine by the daemon, after SweepStaging.
func (s *Scheduler) Run(ctx context.Context) {
	s.Check(ctx, time.Now())
	t := time.NewTicker(CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Check(ctx, time.Now())
		}
	}
}

// SweepStaging removes `.vincent-backup-*` directories left in the backup
// directory by a run that never finished. It is called at timer start
// **only**: at that moment nothing can be writing one, because a manual
// backup goes through this same daemon, which has not started serving yet.
// A sweep between runs could delete a manual backup's staging copy in flight.
func (s *Scheduler) SweepStaging() {
	dir := s.deps.Config().Backup.ResolveDir(s.deps.DataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.deps.Logger.Warn("backup: sweep staging directories", "dir", dir, "error", err)
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), backup.StagingPrefix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(path); err != nil {
			s.deps.Logger.Warn("backup: remove leftover staging directory", "path", path, "error", err)
			continue
		}
		s.deps.Logger.Info("backup: removed leftover staging directory", "path", path)
	}
}

// Check runs one pass: it works out when the next backup is due and, when
// that is now or earlier, takes one and prunes. It reports whether a backup
// was attempted. now is a parameter so tests can move the clock without
// sleeping.
func (s *Scheduler) Check(ctx context.Context, now time.Time) bool {
	cfg := s.deps.Config().Backup
	dir := cfg.ResolveDir(s.deps.DataDir)

	archives, listErr := listArchives(dir)
	s.mu.Lock()
	s.status.Dir = dir
	s.status.Retained = len(archives)
	s.status.LastSuccessAt = time.Time{}
	if len(archives) > 0 {
		s.status.LastSuccessAt = archives[0].at
	}
	if !cfg.Enabled() {
		s.status.NextDueAt = time.Time{}
		s.mu.Unlock()
		return false
	}
	due := s.nextDueLocked(cfg.Interval.Std())
	s.status.NextDueAt = due
	s.mu.Unlock()

	if now.Before(due) {
		return false
	}
	// A directory that cannot be listed is a failed attempt, not a silent
	// skip: it is the same fault a run would hit, and it must reach doctor.
	if listErr != nil {
		s.recordFailure(now, cfg.Interval.Std(), listErr)
		return true
	}
	s.run(ctx, now, cfg, dir)
	return true
}

// nextDueLocked is newest archive + interval, held back by RetryDelay after a
// failure. With no archive and no failure it is the zero time: due now.
func (s *Scheduler) nextDueLocked(interval time.Duration) time.Time {
	var due time.Time
	if !s.status.LastSuccessAt.IsZero() {
		due = s.status.LastSuccessAt.Add(interval)
	}
	if !s.failedAt.IsZero() {
		if retry := s.failedAt.Add(RetryDelay); retry.After(due) {
			due = retry
		}
	}
	return due
}

func (s *Scheduler) run(ctx context.Context, now time.Time, cfg config.Backup, dir string) {
	s.mu.Lock()
	s.status.LastAttemptAt = now
	s.mu.Unlock()

	res, err := s.take(ctx, now, dir)
	if err != nil {
		s.recordFailure(now, cfg.Interval.Std(), err)
		return
	}
	s.deps.Logger.Info("backup: scheduled backup written", "path", res.Path, "bytes", res.Bytes)

	// Prune only after a success: a failure never lowers the number of good
	// archives (decision 3).
	pruneErr := prune(dir, cfg.Keep)
	if pruneErr != nil {
		s.deps.Logger.Warn("backup: prune old scheduled backups", "dir", dir, "error", pruneErr)
	}
	archives, _ := listArchives(dir)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedAt = time.Time{}
	s.status.LastError = ""
	s.status.LastBytes = res.Bytes
	s.status.PruneError = ""
	if pruneErr != nil {
		s.status.PruneError = pruneErr.Error()
	}
	s.status.Retained = len(archives)
	s.status.LastSuccessAt = archiveNameTime(res.Path, now)
	s.status.NextDueAt = s.nextDueLocked(cfg.Interval.Std())
}

func (s *Scheduler) take(ctx context.Context, now time.Time, dir string) (backup.Result, error) {
	// A directory inside either tree the archive walks would be read by the
	// archive being written into it, and every run would carry the ones
	// before it. The handler refuses the transcript half of this too.
	for _, tree := range []string{
		filepath.Join(s.deps.DataDir, backup.TranscriptsPrefix),
		filepath.Join(s.deps.ConfigDir, "workflows"),
	} {
		if under(tree, dir) {
			return backup.Result{}, fmt.Errorf("backup.dir %s is inside %s, which the backup itself reads", dir, tree)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return backup.Result{}, fmt.Errorf("create backup directory: %w", err)
	}
	schema, err := s.deps.SchemaVersion(ctx)
	if err != nil {
		return backup.Result{}, fmt.Errorf("read schema version: %w", err)
	}
	return backup.Take(ctx, filepath.Join(dir, ArchiveName(now)), backup.Snapshot{
		CopyDatabase: s.deps.CopyDatabase,
		DataDir:      s.deps.DataDir,
		ConfigDir:    s.deps.ConfigDir,
		Manifest: backup.Manifest{
			VincentVersion: version.Version(),
			SchemaVersion:  schema,
			CreatedAt:      backup.FormatTime(now),
		},
		Atomic: true,
	})
}

func (s *Scheduler) recordFailure(now time.Time, interval time.Duration, err error) {
	s.deps.Logger.Error("backup: scheduled backup failed", "error", err)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedAt = now
	s.status.LastAttemptAt = now
	s.status.LastError = err.Error()
	s.status.NextDueAt = s.nextDueLocked(interval)
}

// archiveNameTime is the time a written archive's name carries — second
// precision, which is what the next check will read back — or fallback.
func archiveNameTime(path string, fallback time.Time) time.Time {
	if t, ok := archiveTime(filepath.Base(path)); ok {
		return t
	}
	return fallback
}

type archive struct {
	name string
	at   time.Time
}

// listArchives returns the timer-written archives in dir, newest first. A
// directory that does not exist yet holds none and is not an error.
func listArchives(dir string) ([]archive, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []archive
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if at, ok := archiveTime(e.Name()); ok {
			out = append(out, archive{name: e.Name(), at: at})
		}
	}
	slices.SortFunc(out, func(a, b archive) int { return strings.Compare(b.name, a.name) })
	return out, nil
}

// prune removes timer-written archives past the newest keep. keep <= 0 keeps
// everything. Every removal is attempted; the errors are joined.
func prune(dir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	archives, err := listArchives(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range archives[min(keep, len(archives)):] {
		if err := os.Remove(filepath.Join(dir, a.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// under reports whether p sits at or under root.
func under(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
