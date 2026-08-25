package store

import (
	"os"
	"regexp"
	"sort"
	"testing"
	"time"
	"unicode/utf8"
)

// createTable matches the migrations' own `CREATE TABLE name (` form.
var createTable = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"?(\w+)"?`)

// migratedTables is the table set read out of the embedded migrations, plus
// the one the migration runner creates for itself.
//
// The expectation is derived rather than listed on purpose: TableRows exists
// to enumerate the schema, so a test that pinned today's six names would pass
// against an implementation that hardcoded the same six, and would need an
// edit for every future migration — which is exactly the maintenance the
// enumeration was chosen to avoid.
func migratedTables(t *testing.T) []string {
	t.Helper()
	names := []string{"schema_migrations"}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	for _, e := range entries {
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range createTable.FindAllStringSubmatch(string(body), -1) {
			names = append(names, m[1])
		}
	}
	sort.Strings(names)
	return names
}

func TestTableRowsEnumeratesEveryMigratedTable(t *testing.T) {
	s := openTest(t)

	rows, err := s.TableRows(t.Context())
	if err != nil {
		t.Fatalf("TableRows: %v", err)
	}
	for _, name := range migratedTables(t) {
		if _, ok := rows[name]; !ok {
			t.Errorf("table %q is in the schema but not in the counts: %v", name, rows)
		}
	}
	if len(rows) != len(migratedTables(t)) {
		t.Errorf("counted %d tables, the schema has %d: %v",
			len(rows), len(migratedTables(t)), rows)
	}
	// The issue's own list named an `actions` table. There is none — actions
	// are columns on tasks (0003_actions.sql) — which is why the set is read
	// from the database rather than written down.
	if _, ok := rows["actions"]; ok {
		t.Error("counts claim an `actions` table; actions are columns on tasks")
	}
	for name, n := range rows {
		if name == "schema_migrations" {
			continue
		}
		if n != 0 {
			t.Errorf("fresh database: %s = %d rows, want 0", name, n)
		}
	}
}

func TestTableRowsCountsSeededRows(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	p := testProject(t, s, "counted")
	for i := range 3 {
		if err := s.CreateTask(ctx, newTask(p.ID, "t"+string(rune('a'+i)), TaskQueued), nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}
	rows, err := s.TableRows(ctx)
	if err != nil {
		t.Fatalf("TableRows: %v", err)
	}
	if rows["projects"] != 1 {
		t.Errorf("projects = %d, want 1", rows["projects"])
	}
	if rows["tasks"] != 3 {
		t.Errorf("tasks = %d, want 3", rows["tasks"])
	}
	// Creation writes its own events, so the growth driver §17 names is
	// non-zero without the test appending any by hand.
	if rows["events"] == 0 {
		t.Errorf("events = 0 after three task creations: %v", rows)
	}
}

func TestOldestEventAtIsNilOnAnEmptyTable(t *testing.T) {
	s := openTest(t)

	at, err := s.OldestEventAt(t.Context())
	if err != nil {
		t.Fatalf("OldestEventAt: %v", err)
	}
	if at != nil {
		t.Errorf("OldestEventAt = %v on a fresh install, want nil", *at)
	}
}

func TestOldestEventAtIsTheFirstEvent(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	first := time.Date(2026, 1, 2, 3, 4, 5, 600000000, time.UTC)
	for i := range 3 {
		e := &Event{Type: "daemon.started", TS: first.Add(time.Duration(i) * time.Hour)}
		if err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	at, err := s.OldestEventAt(ctx)
	if err != nil {
		t.Fatalf("OldestEventAt: %v", err)
	}
	if at == nil {
		t.Fatal("OldestEventAt = nil after three events")
	}
	if !at.Equal(first) {
		t.Errorf("OldestEventAt = %v, want %v", *at, first)
	}
}

// TestWorkflowSnapshotBytesCountsBytesNotCharacters is the CAST's regression
// test: SQLite's LENGTH() counts characters on TEXT, so the same query
// without `CAST(… AS BLOB)` under-reports any snapshot with non-ASCII in it
// while still looking right on an ASCII fixture.
func TestWorkflowSnapshotBytesCountsBytesNotCharacters(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	zero, err := s.WorkflowSnapshotBytes(ctx)
	if err != nil {
		t.Fatalf("WorkflowSnapshotBytes: %v", err)
	}
	if zero != 0 {
		t.Errorf("WorkflowSnapshotBytes = %d with no tasks, want 0", zero)
	}

	p := testProject(t, s, "snapshots")
	snapshots := []string{
		"name: ascii\nsteps: []\n",
		"name: ünïcödé\ndescription: 中文の説明 ✓\nsteps: []\n",
	}
	var wantBytes, wantRunes int
	for i, snap := range snapshots {
		task := newTask(p.ID, "s"+string(rune('a'+i)), TaskQueued)
		task.WorkflowSnapshot = snap
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		wantBytes += len(snap)
		wantRunes += utf8.RuneCountInString(snap)
	}
	if wantBytes == wantRunes {
		t.Fatal("the fixture is pure ASCII; it cannot tell bytes from characters")
	}
	got, err := s.WorkflowSnapshotBytes(ctx)
	if err != nil {
		t.Fatalf("WorkflowSnapshotBytes: %v", err)
	}
	if got != int64(wantBytes) {
		t.Errorf("WorkflowSnapshotBytes = %d, want %d bytes (%d characters)",
			got, wantBytes, wantRunes)
	}
}

func TestFileSizesTotalsTheSidecars(t *testing.T) {
	s := openTest(t)

	// A write, so the WAL has something in it: the main file alone is what
	// this figure exists to stop a reader quoting.
	if err := s.AppendEvent(t.Context(), &Event{Type: "daemon.started"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	sizes, err := s.FileSizes()
	if err != nil {
		t.Fatalf("FileSizes: %v", err)
	}
	if sizes.MainBytes <= 0 {
		t.Errorf("MainBytes = %d, want the file's real size", sizes.MainBytes)
	}
	if sizes.TotalBytes < sizes.MainBytes {
		t.Errorf("TotalBytes = %d < MainBytes = %d", sizes.TotalBytes, sizes.MainBytes)
	}
	if sizes.TotalBytes != sizes.MainBytes+sizes.WALBytes+sizes.SHMBytes {
		t.Errorf("total %d != %d + %d + %d",
			sizes.TotalBytes, sizes.MainBytes, sizes.WALBytes, sizes.SHMBytes)
	}
	// Whatever is on disk is what is reported: an auto-checkpoint may have
	// emptied the WAL, so the assertion is against the file, not a guess.
	for _, c := range []struct {
		suffix string
		got    int64
	}{{"-wal", sizes.WALBytes}, {"-shm", sizes.SHMBytes}} {
		var want int64
		if fi, err := os.Stat(s.Path() + c.suffix); err == nil {
			want = fi.Size()
		}
		if c.got != want {
			t.Errorf("%s = %d, want %d", c.suffix, c.got, want)
		}
	}
}

// TestFileSizesToleratesAbsentSidecars pins the "missing is zero, not an
// error" rule: WAL and SHM are removed on a clean checkpoint, so a database
// nobody has open normally has neither.
func TestFileSizesToleratesAbsentSidecars(t *testing.T) {
	s := openTest(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(s.Path() + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", suffix, err)
		}
	}
	sizes, err := s.FileSizes()
	if err != nil {
		t.Fatalf("FileSizes with no sidecars: %v", err)
	}
	if sizes.WALBytes != 0 || sizes.SHMBytes != 0 {
		t.Errorf("absent sidecars reported as %d/%d bytes", sizes.WALBytes, sizes.SHMBytes)
	}
	if sizes.TotalBytes != sizes.MainBytes {
		t.Errorf("TotalBytes = %d, want MainBytes = %d", sizes.TotalBytes, sizes.MainBytes)
	}
}
