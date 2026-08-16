package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
)

// dirs returns two isolated directories, so no test ever reads the
// developer's real §12.2 locations.
func dirs(t *testing.T) config.Dirs {
	t.Helper()
	return config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestConfigStates pins the three outcomes a user can act on differently. A
// file that is merely absent is not a fault — the daemon writes the commented
// default on first start — while one that exists and does not parse stops the
// daemon dead, which is exactly the "why is nothing running?" this command is
// for.
func TestConfigStates(t *testing.T) {
	tests := []struct {
		name        string
		content     string // "" = do not create the file
		wantExists  bool
		wantParses  bool
		wantProblem bool
	}{
		{name: "absent", wantExists: false, wantParses: false, wantProblem: false},
		{name: "valid", content: "max_parallel_tasks: 2\n", wantExists: true, wantParses: true},
		{
			name:        "unparseable",
			content:     "max_parallel_tasks: [this is not a number\n",
			wantExists:  true,
			wantProblem: true,
		},
		{
			name:        "parses but invalid",
			content:     "max_parallel_tasks: -3\n",
			wantExists:  true,
			wantProblem: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := dirs(t)
			if tc.content != "" {
				write(t, filepath.Join(d.Config, config.FileName), tc.content)
			}
			rep := Compose(t.Context(), Options{Dirs: d})
			if rep.Paths.ConfigFileExists != tc.wantExists {
				t.Errorf("ConfigFileExists = %v, want %v", rep.Paths.ConfigFileExists, tc.wantExists)
			}
			if rep.Paths.ConfigParses != tc.wantParses {
				t.Errorf("ConfigParses = %v, want %v (error %q)",
					rep.Paths.ConfigParses, tc.wantParses, rep.Paths.ConfigError)
			}
			if got := hasProblem(rep, GroupPaths); got != tc.wantProblem {
				t.Errorf("paths problem = %v, want %v (problems %v)", got, tc.wantProblem, rep.Problems)
			}
			if tc.wantProblem && rep.Paths.ConfigError == "" {
				t.Error("an unhealthy config reported no error text")
			}
			if rep.Paths.ConfigFile != filepath.Join(d.Config, config.FileName) {
				t.Errorf("ConfigFile = %q", rep.Paths.ConfigFile)
			}
		})
	}
}

// TestMissingLogIsNotAnError separates "no daemon has ever run here" from "the
// log is unreadable". Only the second is worth a line of a user's attention.
func TestMissingLogIsNotAnError(t *testing.T) {
	d := dirs(t)
	rep := Compose(t.Context(), Options{
		Dirs:    d,
		LogPath: filepath.Join(d.Data, "logs", "daemon.log"),
		TailLog: func(string, int) ([]string, error) { return nil, errors.New("must not be called") },
	})
	if rep.Log.Exists {
		t.Error("Log.Exists is true for a log that was never written")
	}
	if rep.Log.Error != "" {
		t.Errorf("Log.Error = %q, want empty for a merely absent log", rep.Log.Error)
	}
	if rep.Log.Tail == nil {
		t.Error("Log.Tail is nil; an empty list keeps `| jq '.log.tail[]'` working")
	}
	if !rep.Healthy() {
		t.Errorf("a missing log made the report unhealthy: %v", rep.Problems)
	}
}

func TestLogStatAndTail(t *testing.T) {
	d := dirs(t)
	logPath := filepath.Join(d.Data, "logs", "daemon.log")
	write(t, logPath, "first\nsecond\nthird\n")
	rep := Compose(t.Context(), Options{
		Dirs:    d,
		LogPath: logPath,
		LogTail: 2,
		TailLog: func(path string, n int) ([]string, error) {
			if path != logPath {
				t.Errorf("tail path = %q, want %q", path, logPath)
			}
			if n != 2 {
				t.Errorf("tail n = %d, want 2", n)
			}
			return []string{"second", "third"}, nil
		},
	})
	if !rep.Log.Exists || rep.Log.SizeBytes == 0 || rep.Log.ModTime == nil {
		t.Fatalf("log stat incomplete: %+v", rep.Log)
	}
	if strings.Join(rep.Log.Tail, ",") != "second,third" {
		t.Errorf("Log.Tail = %v", rep.Log.Tail)
	}
}

// TestDiskFreeIsReported guards the platform split: the unix and windows
// implementations are separate files, and a report with a zero disk row would
// mean neither ran.
func TestDiskFreeIsReported(t *testing.T) {
	rep := Compose(t.Context(), Options{Dirs: dirs(t)})
	if rep.Storage.DiskError != "" {
		t.Fatalf("DiskError = %q", rep.Storage.DiskError)
	}
	if rep.Storage.DiskFreeBytes == 0 || rep.Storage.DiskTotalBytes == 0 {
		t.Fatalf("disk free = %d of %d; want non-zero on a running test machine",
			rep.Storage.DiskFreeBytes, rep.Storage.DiskTotalBytes)
	}
	if rep.Storage.DiskFreeBytes > rep.Storage.DiskTotalBytes {
		t.Errorf("free (%d) exceeds total (%d)", rep.Storage.DiskFreeBytes, rep.Storage.DiskTotalBytes)
	}
}

// TestDiskFreeSurvivesAnAbsentDataDir covers the fresh-machine case: nothing
// has created the data dir yet, and the free space of the volume it will live
// on is a better answer than "cannot stat".
func TestDiskFreeSurvivesAnAbsentDataDir(t *testing.T) {
	d := config.Dirs{Config: t.TempDir(), Data: filepath.Join(t.TempDir(), "not", "created", "yet")}
	rep := Compose(t.Context(), Options{Dirs: d})
	if rep.Storage.DiskError != "" || rep.Storage.DiskFreeBytes == 0 {
		t.Fatalf("disk row = %d free, error %q", rep.Storage.DiskFreeBytes, rep.Storage.DiskError)
	}
	if rep.Storage.WorktreeCount != 0 || rep.Storage.ScanError != "" {
		t.Errorf("scan of an absent tree: count %d, error %q",
			rep.Storage.WorktreeCount, rep.Storage.ScanError)
	}
}

// TestOrphansComeFromTheScan pins what this package is now responsible for:
// the footprint is measured here, the classification is not. gc owns the
// definition of an orphan (task 005) and its own tests cover the legs — what
// matters here is that the scan's answer reaches the report unaltered, spanning
// both data roots.
func TestOrphansComeFromTheScan(t *testing.T) {
	d := dirs(t)
	root := filepath.Join(d.Data, WorktreesDirName)
	for _, name := range []string{"1", "2", "notatask"} {
		write(t, filepath.Join(root, name, "file.txt"), "contents\n")
	}
	scanned := []Orphan{
		{Name: "2", Path: filepath.Join(root, "2"), Kind: "worktree", TaskID: 2, SizeBytes: 9},
		{Name: "notatask", Path: filepath.Join(root, "notatask"), Kind: "worktree", Skip: "worktree_dirty"},
		{Name: "40", Path: filepath.Join(d.Data, "transcripts", "40"), Kind: "transcript", TaskID: 40},
	}

	rep := Compose(t.Context(), Options{
		Dirs:        d,
		ScanOrphans: func(context.Context) ([]Orphan, error) { return scanned, nil },
	})
	s := rep.Storage
	if !s.OrphansKnown {
		t.Fatal("OrphansKnown is false with a scan supplied")
	}
	// The footprint counts every worktree on disk, orphaned or not: it answers
	// "how much is this costing me", not "what may be deleted".
	if s.WorktreeCount != 3 || s.WorktreeBytes == 0 {
		t.Errorf("footprint = %d dirs using %d bytes, want 3 and non-zero",
			s.WorktreeCount, s.WorktreeBytes)
	}
	if !reflect.DeepEqual(s.Orphans, scanned) {
		t.Errorf("orphans = %+v, want the scan's answer verbatim %+v", s.Orphans, scanned)
	}
	if !hasProblem(rep, GroupStorage) {
		t.Errorf("orphans present but no storage problem: %v", rep.Problems)
	}
}

// TestOrphansUnknownWithoutTheTaskTable is the no-daemon path. An orphan is
// defined by what the task table claims, so with no daemon there is nothing to
// diff against — and a classifier that guessed from directory names would
// accuse every worktree, including the ones holding work in progress.
func TestOrphansUnknownWithoutTheTaskTable(t *testing.T) {
	d := dirs(t)
	write(t, filepath.Join(d.Data, WorktreesDirName, "7", "file.txt"), "contents\n")
	rep := Compose(t.Context(), Options{Dirs: d}) // ScanOrphans is nil
	if rep.Storage.OrphansKnown {
		t.Error("OrphansKnown is true without a scan")
	}
	if len(rep.Storage.Orphans) != 0 {
		t.Errorf("orphans = %v, want none accused", rep.Storage.Orphans)
	}
	if rep.Storage.WorktreeCount != 1 || rep.Storage.WorktreeBytes == 0 {
		t.Errorf("worktrees still get counted: %d using %d bytes",
			rep.Storage.WorktreeCount, rep.Storage.WorktreeBytes)
	}
	if !rep.Healthy() {
		t.Errorf("unknown orphans must not set the exit code: %v", rep.Problems)
	}
}

// TestScanErrorLeavesOrphansUnknown keeps a failed scan distinct from a clean
// one. "I looked and found nothing" and "I could not look" are different
// answers, and only the first should ever reassure a user.
func TestScanErrorLeavesOrphansUnknown(t *testing.T) {
	d := dirs(t)
	write(t, filepath.Join(d.Data, WorktreesDirName, "9", "file.txt"), "x\n")
	rep := Compose(t.Context(), Options{
		Dirs: d,
		ScanOrphans: func(context.Context) ([]Orphan, error) {
			return nil, errors.New("database is locked")
		},
	})
	if rep.Storage.OrphansKnown {
		t.Error("OrphansKnown is true after a scan that failed")
	}
	if !strings.Contains(rep.Storage.ScanError, "database is locked") {
		t.Errorf("ScanError = %q, want the scan's error", rep.Storage.ScanError)
	}
}

// TestStrayFilesAreNotWorktrees keeps the footprint to what §10 says a
// worktree is: a directory named after a task. gc reports the stray file
// separately, with `not_a_directory`, and removes it never.
func TestStrayFilesAreNotWorktrees(t *testing.T) {
	d := dirs(t)
	write(t, filepath.Join(d.Data, WorktreesDirName, "stray.txt"), "not a worktree\n")
	rep := Compose(t.Context(), Options{
		Dirs:        d,
		ScanOrphans: func(context.Context) ([]Orphan, error) { return nil, nil },
	})
	if rep.Storage.WorktreeCount != 0 || rep.Storage.WorktreeBytes != 0 {
		t.Errorf("a stray file was counted: %+v", rep.Storage)
	}
}

// TestUnhealthyIsAClosedSet is decision 7. A doctor that exits 1 on nearly
// every machine is useless in a script, so a missing or logged-out agent CLI
// and a pile of blocked tasks are reported and do not count.
func TestUnhealthyIsAClosedSet(t *testing.T) {
	no := false
	rep := &Report{
		Agents: []Agent{
			{Name: "claude", Available: false, Error: "claude not found on PATH"},
			{Name: "codex", Available: true, LoggedIn: &no},
		},
		Tasks: Tasks{Known: true, Counts: map[string]int{"blocked": 12}, Total: 12},
	}
	rep.Evaluate()
	if !rep.Healthy() {
		t.Errorf("missing/logged-out agents or blocked tasks set the exit code: %v", rep.Problems)
	}

	rep.Daemon = Daemon{Status: StatusUnresponsive}
	rep.Database = Database{Known: true, IntegrityCheck: "ok", SchemaVersion: 6, NewestMigration: 6}
	rep.Evaluate()
	if len(rep.Problems) != 1 || rep.Problems[0].Group != GroupDaemon {
		t.Fatalf("unresponsive daemon problems = %v", rep.Problems)
	}

	rep.Daemon = Daemon{Status: StatusRunning}
	rep.Database = Database{Known: true, IntegrityCheck: "*** in database main ***\npage 3 is never used"}
	rep.Evaluate()
	if !hasProblem(rep, GroupDatabase) {
		t.Errorf("a failed integrity_check is not a problem: %v", rep.Problems)
	}

	// A database written by a newer vincent: the binary would have to invent
	// migrations it does not embed to understand it.
	rep.Database = Database{Known: true, IntegrityCheck: "ok", SchemaVersion: 9, NewestMigration: 6}
	rep.Evaluate()
	if !hasProblem(rep, GroupDatabase) {
		t.Errorf("schema ahead of the binary is not a problem: %v", rep.Problems)
	}

	// The other direction is normal: this binary simply has migrations to apply.
	rep.Database = Database{Known: true, IntegrityCheck: "ok", SchemaVersion: 4, NewestMigration: 6}
	rep.Evaluate()
	if !rep.Healthy() {
		t.Errorf("a database behind the binary is not unhealthy: %v", rep.Problems)
	}
}

// TestDatabaseAndTasksUnknownWithoutADaemon: the local report must never
// pretend to know what only the daemon can read.
func TestDatabaseAndTasksUnknownWithoutADaemon(t *testing.T) {
	d := dirs(t)
	rep := Compose(t.Context(), Options{Dirs: d, Daemon: Daemon{Status: StatusNotRunning}})
	if rep.Database.Known || rep.Tasks.Known {
		t.Errorf("database/tasks claimed known without a daemon: %+v %+v", rep.Database, rep.Tasks)
	}
	if rep.Database.Path != filepath.Join(d.Data, "vincent.db") {
		t.Errorf("Database.Path = %q", rep.Database.Path)
	}
	if rep.Database.NewestMigration == 0 {
		t.Error("NewestMigration = 0; the embedded ceiling is known without opening anything")
	}
	// "not running" is not a fault — it is the answer, and exit 2 carries it.
	if !rep.Healthy() {
		t.Errorf("a stopped daemon made the report unhealthy: %v", rep.Problems)
	}
}

// TestTaskCountsCarryTheWholeVocabulary: a reader should see "blocked 0"
// rather than nothing at all.
func TestTaskCountsCarryTheWholeVocabulary(t *testing.T) {
	rep := &Report{}
	rep.SetTaskCounts(map[string]int{"blocked": 2, "done": 1})
	if !rep.Tasks.Known || rep.Tasks.Total != 3 {
		t.Fatalf("tasks = %+v", rep.Tasks)
	}
	for _, state := range []string{
		"queued", "running", "awaiting_gate", "awaiting_input",
		"blocked", "paused", "done", "aborted", "archived",
	} {
		if _, ok := rep.Tasks.Counts[state]; !ok {
			t.Errorf("state %q missing from the tally", state)
		}
	}
	if rep.Tasks.Counts["blocked"] != 2 || rep.Tasks.Counts["queued"] != 0 {
		t.Errorf("counts = %v", rep.Tasks.Counts)
	}
}

func hasProblem(r *Report, group string) bool {
	for _, p := range r.Problems {
		if p.Group == group {
			return true
		}
	}
	return false
}
