package backupsched

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/config"
)

// harness is a timer over throwaway directories, a fake database copier and a
// config the test can change between checks.
type harness struct {
	t       *testing.T
	dataDir string
	cfgDir  string
	dir     string

	mu      sync.Mutex
	cfg     config.Config
	copyErr error
	copies  int
}

func newHarness(t *testing.T, interval time.Duration, keep int) *harness {
	t.Helper()
	h := &harness{t: t, dataDir: t.TempDir(), cfgDir: t.TempDir()}
	h.dir = filepath.Join(t.TempDir(), "backups")
	h.cfg = config.Default()
	h.cfg.Backup = config.Backup{Interval: config.Duration(interval), Keep: keep, Dir: h.dir}
	if err := os.MkdirAll(filepath.Join(h.dataDir, backup.TranscriptsPrefix, "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dataDir, backup.TranscriptsPrefix, "1", "step.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.cfgDir, config.FileName), []byte("log_level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) scheduler() *Scheduler {
	return New(Deps{
		Config: func() config.Config {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.cfg
		},
		DataDir:   h.dataDir,
		ConfigDir: h.cfgDir,
		CopyDatabase: func(_ context.Context, dst string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.copies++
			if h.copyErr != nil {
				// A copy that dies part-way leaves a partial file behind.
				_ = os.WriteFile(dst, []byte("partial"), 0o600)
				return h.copyErr
			}
			return os.WriteFile(dst, []byte("SQLite format 3\x00"), 0o600)
		},
		SchemaVersion: func(context.Context) (int, error) { return 7, nil },
	})
}

func (h *harness) setConfig(f func(*config.Backup)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(&h.cfg.Backup)
}

func (h *harness) setCopyErr(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.copyErr = err
}

func (h *harness) copyCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.copies
}

// names lists every entry in the backup directory, sorted.
func (h *harness) names() []string {
	h.t.Helper()
	entries, err := os.ReadDir(h.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		h.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func (h *harness) touch(name string) {
	h.t.Helper()
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, name), []byte("x"), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

var t0 = time.Date(2026, 9, 17, 3, 0, 0, 0, time.UTC)

func TestCheckOffRunsNothing(t *testing.T) {
	h := newHarness(t, 0, 7)
	s := h.scheduler()
	if s.Check(context.Background(), t0) {
		t.Fatal("Check attempted a backup with backups off")
	}
	if h.copyCount() != 0 || len(h.names()) != 0 {
		t.Fatalf("off wrote something: copies=%d names=%v", h.copyCount(), h.names())
	}
	if st := s.Status(); !st.NextDueAt.IsZero() || st.LastError != "" {
		t.Errorf("off status = %+v", st)
	}
}

func TestCheckEmptyDirRunsAtFirstCheck(t *testing.T) {
	h := newHarness(t, 24*time.Hour, 7)
	s := h.scheduler()
	if !s.Check(context.Background(), t0) {
		t.Fatal("an empty directory did not run at the first check")
	}
	want := ArchiveName(t0)
	if got := h.names(); !slices.Equal(got, []string{want}) {
		t.Fatalf("backup dir = %v, want only %s", got, want)
	}
	if fi, err := os.Stat(h.dir); err != nil || !fi.IsDir() {
		t.Fatalf("backup dir not created: %v", err)
	}
	m, err := backup.ReadManifest(filepath.Join(h.dir, want))
	if err != nil {
		t.Fatalf("the archive does not pass ReadManifest: %v", err)
	}
	if m.SchemaVersion != 7 || m.CreatedAt != backup.FormatTime(t0) {
		t.Errorf("manifest = %+v", m)
	}
	st := s.Status()
	if !st.LastSuccessAt.Equal(t0) || !st.LastAttemptAt.Equal(t0) || st.LastError != "" ||
		st.LastBytes <= 0 || st.Retained != 1 || !st.NextDueAt.Equal(t0.Add(24*time.Hour)) {
		t.Errorf("status after success = %+v", st)
	}
	// Not due again until the interval has passed.
	if s.Check(context.Background(), t0.Add(23*time.Hour)) {
		t.Error("ran again before the interval passed")
	}
	if !s.Check(context.Background(), t0.Add(24*time.Hour)) {
		t.Error("did not run once the interval passed")
	}
}

// The clock comes from the newest archive name, so a restarted daemon over an
// existing directory does not re-run early — and an overdue one runs at once.
func TestCheckDueFromNewestArchiveName(t *testing.T) {
	h := newHarness(t, 24*time.Hour, 7)
	h.touch(ArchiveName(t0.Add(-48 * time.Hour)))
	h.touch(ArchiveName(t0.Add(-2 * time.Hour)))

	s := h.scheduler()
	if s.Check(context.Background(), t0) {
		t.Fatal("a new instance re-ran early over a recent archive")
	}
	st := s.Status()
	if !st.LastSuccessAt.Equal(t0.Add(-2*time.Hour)) || !st.NextDueAt.Equal(t0.Add(22*time.Hour)) || st.Retained != 2 {
		t.Errorf("status = %+v", st)
	}

	overdue := newHarness(t, 24*time.Hour, 7)
	overdue.touch(ArchiveName(t0.Add(-30 * time.Hour)))
	if !overdue.scheduler().Check(context.Background(), t0) {
		t.Error("an overdue backup did not run at the first check")
	}
}

func TestCheckFailureRetriesAfterAnHour(t *testing.T) {
	h := newHarness(t, 24*time.Hour, 7)
	h.setCopyErr(errors.New("disk full"))
	s := h.scheduler()

	if !s.Check(context.Background(), t0) {
		t.Fatal("did not attempt")
	}
	st := s.Status()
	if !strings.Contains(st.LastError, "disk full") || !st.LastAttemptAt.Equal(t0) ||
		!st.NextDueAt.Equal(t0.Add(RetryDelay)) {
		t.Fatalf("status after failure = %+v", st)
	}
	// No file under a final name, and no staging directory left behind.
	if got := h.names(); len(got) != 0 {
		t.Errorf("a failed run left %v", got)
	}

	if s.Check(context.Background(), t0.Add(time.Minute)) {
		t.Error("retried on the next check rather than an hour later")
	}
	if h.copyCount() != 1 {
		t.Errorf("copies = %d, want 1", h.copyCount())
	}

	h.setCopyErr(nil)
	later := t0.Add(RetryDelay)
	if !s.Check(context.Background(), later) {
		t.Fatal("did not retry after RetryDelay")
	}
	st = s.Status()
	if st.LastError != "" || !st.LastSuccessAt.Equal(later) || !st.NextDueAt.Equal(later.Add(24*time.Hour)) {
		t.Errorf("a later success did not clear the failure: %+v", st)
	}
}

func TestCheckConfigChangeTakesEffectAtNextCheck(t *testing.T) {
	h := newHarness(t, 0, 7)
	s := h.scheduler()
	if s.Check(context.Background(), t0) {
		t.Fatal("ran while off")
	}
	h.setConfig(func(b *config.Backup) { b.Interval = config.Duration(24 * time.Hour) })
	if !s.Check(context.Background(), t0.Add(time.Minute)) {
		t.Fatal("turning backups on did not take effect at the next check")
	}

	// A shorter interval makes the existing archive overdue at once.
	h.setConfig(func(b *config.Backup) { b.Interval = config.Duration(time.Hour) })
	if !s.Check(context.Background(), t0.Add(time.Hour+time.Minute)) {
		t.Error("a shorter interval did not take effect at the next check")
	}

	// A new directory is read at the next check too.
	other := filepath.Join(t.TempDir(), "elsewhere")
	h.setConfig(func(b *config.Backup) { b.Dir = other })
	s.Check(context.Background(), t0.Add(2*time.Hour))
	if st := s.Status(); st.Dir != other {
		t.Errorf("status dir = %s, want %s", st.Dir, other)
	}
	if _, err := os.Stat(filepath.Join(other, ArchiveName(t0.Add(2*time.Hour)))); err != nil {
		t.Errorf("no archive in the new directory: %v", err)
	}
}

func TestPruneKeepsNewestTimerArchivesOnly(t *testing.T) {
	h := newHarness(t, time.Hour, 2)
	older := []string{
		ArchiveName(t0.Add(-4 * time.Hour)),
		ArchiveName(t0.Add(-3 * time.Hour)),
		ArchiveName(t0.Add(-2 * time.Hour)),
	}
	for _, n := range older {
		h.touch(n)
	}
	manual := []string{
		"vincent-backup-manual.tar.gz",       // a hand-made archive
		"vincent-backup-20200101.tar.gz",     // nearly the pattern
		"my-backup.tar.gz",                   // unrelated
		"vincent-backup-20200101T000000Z.gz", // wrong suffix
	}
	for _, n := range manual {
		h.touch(n)
	}

	s := h.scheduler()
	if !s.Check(context.Background(), t0) {
		t.Fatal("did not run")
	}
	want := append([]string{ArchiveName(t0), older[2]}, manual...)
	slices.Sort(want)
	if got := h.names(); !slices.Equal(got, want) {
		t.Errorf("after prune = %v\nwant %v", got, want)
	}
	if st := s.Status(); st.Retained != 2 || st.PruneError != "" {
		t.Errorf("status = %+v", st)
	}
}

func TestPruneKeepZeroKeepsEverything(t *testing.T) {
	h := newHarness(t, time.Hour, 0)
	for i := range 10 {
		h.touch(ArchiveName(t0.Add(-time.Duration(i+2) * time.Hour)))
	}
	s := h.scheduler()
	if !s.Check(context.Background(), t0) {
		t.Fatal("did not run")
	}
	if got := len(h.names()); got != 11 {
		t.Errorf("keep 0 left %d archives, want 11", got)
	}
}

func TestFailedRunPrunesNothing(t *testing.T) {
	h := newHarness(t, time.Hour, 1)
	for i := range 3 {
		h.touch(ArchiveName(t0.Add(-time.Duration(i+2) * time.Hour)))
	}
	h.setCopyErr(errors.New("boom"))
	if !h.scheduler().Check(context.Background(), t0) {
		t.Fatal("did not attempt")
	}
	if got := len(h.names()); got != 3 {
		t.Errorf("a failed run left %d archives, want all 3", got)
	}
}

// A copy that fails part-way — here the database copy "succeeds" without
// writing anything, so the archive dies after its manifest — never leaves a
// file under the final name for the next check to count as a success.
func TestFailureMidArchiveLeavesNoFinalName(t *testing.T) {
	h := newHarness(t, time.Hour, 7)
	s := New(Deps{
		Config:        func() config.Config { return h.cfg },
		DataDir:       h.dataDir,
		ConfigDir:     h.cfgDir,
		CopyDatabase:  func(context.Context, string) error { return nil },
		SchemaVersion: func(context.Context) (int, error) { return 1, nil },
	})
	if !s.Check(context.Background(), t0) {
		t.Fatal("did not attempt")
	}
	if s.Status().LastError == "" {
		t.Fatal("the run did not fail")
	}
	if got := h.names(); len(got) != 0 {
		t.Errorf("a run that died mid-archive left %v", got)
	}
}

func TestSweepStagingAtStart(t *testing.T) {
	h := newHarness(t, time.Hour, 7)
	stale := filepath.Join(h.dir, backup.StagingPrefix+"12345")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, ArchiveName(t0)), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.touch(ArchiveName(t0.Add(-time.Minute)))
	h.touch(".vincent-backup-a-file-not-a-dir")

	h.scheduler().SweepStaging()
	want := []string{".vincent-backup-a-file-not-a-dir", ArchiveName(t0.Add(-time.Minute))}
	slices.Sort(want)
	if got := h.names(); !slices.Equal(got, want) {
		t.Errorf("after sweep = %v, want %v", got, want)
	}
}

func TestBackupDirInsideAnArchivedTreeFails(t *testing.T) {
	h := newHarness(t, time.Hour, 7)
	for _, dir := range []string{
		filepath.Join(h.dataDir, backup.TranscriptsPrefix, "bk"),
		filepath.Join(h.cfgDir, "workflows", "bk"),
	} {
		h.setConfig(func(b *config.Backup) { b.Dir = dir })
		s := h.scheduler()
		if !s.Check(context.Background(), t0) {
			t.Fatalf("%s: did not attempt", dir)
		}
		if !strings.Contains(s.Status().LastError, "inside") {
			t.Errorf("%s: last error = %q", dir, s.Status().LastError)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%s was created", dir)
		}
	}
}

func TestDefaultDirIsUnderDataDir(t *testing.T) {
	h := newHarness(t, time.Hour, 7)
	h.setConfig(func(b *config.Backup) { b.Dir = "" })
	s := h.scheduler()
	if !s.Check(context.Background(), t0) {
		t.Fatal("did not attempt")
	}
	want := filepath.Join(h.dataDir, config.BackupDirName)
	if _, err := os.Stat(filepath.Join(want, ArchiveName(t0))); err != nil {
		t.Errorf("no archive under %s: %v (status %+v)", want, err, s.Status())
	}
}
