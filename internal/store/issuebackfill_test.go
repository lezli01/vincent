package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// Snapshots exactly as task 035 wrote them: the bytes are compared after the
// migration, so they are spelled out rather than marshalled.
const (
	// o/r#7 in p1, an older and a newer snapshot of the same issue.
	backfillOldSnap = `{"repo":"o/r","number":7,"title":"old title","url":"https://github.com/o/r/issues/7","state":"OPEN","labels":["bug"]}`
	backfillNewSnap = `{"repo":"o/r","number":7,"title":"new title","body":"the body","url":"https://github.com/o/r/issues/7","state":"CLOSED","labels":["BUG","area/ui"],"author":"ann"}`
	// The same number in another repo of the same project.
	backfillOtherRepoSnap = `{"repo":"o/other","number":7,"title":"other repo","url":"https://github.com/o/other/issues/7","state":"OPEN","labels":["bug"]}`
	// The same repo and number in a second project.
	backfillP2Snap = `{"repo":"o/r","number":7,"title":"p2 copy","url":"https://github.com/o/r/issues/7","state":"OPEN"}`
	// Already imported under its node_id by 130.8.
	backfillImportedSnap = `{"repo":"o/r","number":9,"title":"stale snapshot","url":"https://github.com/o/r/issues/9","state":"OPEN","labels":["dup"]}`
)

// backfillAt is the n-th minute after a fixed instant, in store.TimeFormat.
func backfillAt(n int) string {
	return formatTime(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute))
}

// seedBackfillFixture writes, at schema 0039, every shape of task 035
// snapshot the backfill must group.
func seedBackfillFixture(t *testing.T, path string) {
	t.Helper()
	migrateTo(t, path, 39)
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open at 0039: %v", err)
	}
	defer func() { _ = db.Close() }()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, p := range []struct {
		id   int
		name string
	}{{1, "p1"}, {2, "p2"}} {
		exec(`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
			VALUES (?, ?, ?, 'main', ?, ?)`, p.id, p.name, "/"+p.name, backfillAt(0), backfillAt(0))
	}
	// The issue the importer already brought in for o/r#9, under its node_id.
	exec(`INSERT INTO issues (id, project_id, title, state, created_at, updated_at)
		VALUES (1, 1, 'imported', 'open', ?, ?)`, backfillAt(0), backfillAt(0))
	exec(`INSERT INTO issue_remotes (issue_id, project_id, provider, remote_key, repo, number, url, synced_at)
		VALUES (1, 1, 'github', 'I_kwNODE9', 'o/r', 9, 'https://github.com/o/r/issues/9', ?)`, backfillAt(0))
	exec(`INSERT INTO labels (project_id, name, source) VALUES (1, 'Bug', 'local')`)

	task := func(id, project int, state string, created int, snap any, parent any, archived any) {
		t.Helper()
		exec(`INSERT INTO tasks (id, project_id, title, workflow_name, workflow_snapshot, base_branch,
			branch_name, state, created_at, updated_at, github_issue_json, parent_task_id, archived_at)
			VALUES (?, ?, 't', 'adhoc', 'steps: []', 'main', 'b', ?, ?, ?, ?, ?, ?)`,
			id, project, state, backfillAt(created), backfillAt(created+100), snap, parent, archived)
	}
	task(1, 1, "done", 1, backfillOldSnap, nil, nil)
	task(2, 1, "running", 5, backfillNewSnap, nil, nil)
	task(3, 1, "running", 6, backfillNewSnap, 2, nil) // lanes: verbatim copies of 2's
	task(4, 1, "done", 6, backfillNewSnap, 2, nil)
	task(5, 1, "archived", 3, backfillOtherRepoSnap, nil, backfillAt(50)) // archived-only
	task(6, 2, "queued", 2, backfillP2Snap, nil, nil)                     // never started
	task(7, 1, "done", 4, backfillImportedSnap, nil, nil)
	task(8, 1, "done", 4, nil, nil, nil) // no snapshot at all
}

type backfillTask struct {
	issueID   sql.NullInt64
	issueJSON sql.NullString
	snap      sql.NullString
}

func readBackfillTasks(t *testing.T, s *Store) map[int64]backfillTask {
	t.Helper()
	rows, err := s.db.Query(`SELECT id, issue_id, issue_json, github_issue_json FROM tasks`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]backfillTask{}
	for rows.Next() {
		var id int64
		var bt backfillTask
		if err := rows.Scan(&id, &bt.issueID, &bt.issueJSON, &bt.snap); err != nil {
			t.Fatal(err)
		}
		out[id] = bt
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMigration0040BackfillsSnapshotsIntoIssues: opening a 0039 database
// gives each distinct (project, repo, number) snapshot one issue, reuses an
// already imported one, and links every snapshot task, lanes included
// (task 130.11). It also proves modernc's JSON1 (json_extract, json_each).
func TestMigration0040BackfillsSnapshotsIntoIssues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	seedBackfillFixture(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 39 -> 40): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := t.Context()

	var issues, remotes, events int
	for q, n := range map[string]*int{
		`SELECT COUNT(*) FROM issues`:        &issues,
		`SELECT COUNT(*) FROM issue_remotes`: &remotes,
		`SELECT COUNT(*) FROM events`:        &events,
	} {
		if err := s.db.QueryRow(q).Scan(n); err != nil {
			t.Fatal(err)
		}
	}
	if issues != 4 || remotes != 4 {
		t.Errorf("issues = %d, remotes = %d; want 4 and 4 (one pre-imported + three backfilled)", issues, remotes)
	}
	if events != 0 {
		t.Errorf("events = %d; the backfill writes none", events)
	}

	tasks := readBackfillTasks(t, s)
	want := map[int64]string{
		1: backfillOldSnap, 2: backfillNewSnap, 3: backfillNewSnap, 4: backfillNewSnap,
		5: backfillOtherRepoSnap, 6: backfillP2Snap, 7: backfillImportedSnap,
	}
	for id, snap := range want {
		bt := tasks[id]
		if !bt.issueID.Valid {
			t.Errorf("task %d not linked", id)
		}
		if bt.issueJSON.Valid {
			t.Errorf("task %d issue_json = %q; want NULL", id, bt.issueJSON.String)
		}
		if bt.snap.String != snap {
			t.Errorf("task %d github_issue_json changed:\n got %s\nwant %s", id, bt.snap.String, snap)
		}
	}
	if tasks[8].issueID.Valid {
		t.Errorf("task 8 has no snapshot but issue_id = %d", tasks[8].issueID.Int64)
	}
	if tasks[7].issueID.Int64 != 1 {
		t.Errorf("task 7 linked to issue %d; want the pre-imported issue 1", tasks[7].issueID.Int64)
	}
	group := tasks[1].issueID.Int64
	for _, id := range []int64{2, 3, 4} {
		if tasks[id].issueID.Int64 != group {
			t.Errorf("task %d issue %d; want %d, its group's", id, tasks[id].issueID.Int64, group)
		}
	}
	distinct := map[int64]bool{group: true, tasks[5].issueID.Int64: true, tasks[6].issueID.Int64: true, 1: true}
	if len(distinct) != 4 {
		t.Errorf("issue ids %v are not four distinct issues", distinct)
	}

	// The o/r#7 issue in p1: newest snapshot's content, copied timestamps.
	iss, err := s.GetIssue(ctx, group)
	if err != nil {
		t.Fatalf("GetIssue(%d): %v", group, err)
	}
	if iss.ProjectID != 1 || iss.Title != "new title" || iss.Body != "the body" || iss.Author != "ann" ||
		iss.State != issuestate.Closed || iss.CloseReason != issuestate.Completed ||
		iss.Kind != "" || iss.Priority != 0 || iss.Version != 1 {
		t.Errorf("backfilled issue = %+v", iss)
	}
	var created, updated string
	var closed sql.NullString
	if err := s.db.QueryRow(`SELECT created_at, updated_at, closed_at FROM issues WHERE id = ?`, group).
		Scan(&created, &updated, &closed); err != nil {
		t.Fatal(err)
	}
	if created != backfillAt(1) || updated != backfillAt(6) || closed.String != backfillAt(6) {
		t.Errorf("timestamps = %s, %s, %v; want %s, %s, %s", created, updated, closed, backfillAt(1), backfillAt(6), backfillAt(6))
	}
	for _, v := range []string{created, updated, closed.String} {
		if _, err := time.Parse(TimeFormat, v); err != nil {
			t.Errorf("timestamp %q does not parse with TimeFormat: %v", v, err)
		}
	}
	// "BUG" folds into the project's existing "Bug" label; "area/ui" is new.
	if want := []string{"area/ui", "Bug"}; !reflect.DeepEqual(iss.Labels, want) {
		t.Errorf("labels = %v, want %v", iss.Labels, want)
	}
	var source string
	if err := s.db.QueryRow(`SELECT source FROM labels WHERE project_id = 1 AND name = 'area/ui'`).Scan(&source); err != nil || source != "github" {
		t.Errorf("area/ui label source = %q, %v; want github", source, err)
	}
	r := iss.Remote
	if r == nil {
		t.Fatal("backfilled issue has no remote")
	}
	if r.Provider != "github" || r.RemoteKey != "legacy:o/r#7" || r.Repo != "o/r" || r.Number != 7 ||
		r.URL != "https://github.com/o/r/issues/7" || r.SyncedAt != nil || r.RemoteJSON != "" || r.RemoteUpdatedAt != nil {
		t.Errorf("remote = %+v; want an unsynced legacy placeholder", r)
	}

	// Archived-only: the snapshot's open state, the archived task's times.
	other, err := s.GetIssue(ctx, tasks[5].issueID.Int64)
	if err != nil {
		t.Fatal(err)
	}
	if other.Title != "other repo" || other.State != issuestate.Open || other.ClosedAt != nil ||
		other.Remote == nil || other.Remote.RemoteKey != "legacy:o/other#7" ||
		!other.CreatedAt.Equal(other.UpdatedAt) || formatTime(other.CreatedAt) != backfillAt(3) {
		t.Errorf("archived-only issue = %+v remote %+v", other, other.Remote)
	}
	if want := []string{"Bug"}; !reflect.DeepEqual(other.Labels, want) {
		t.Errorf("archived-only labels = %v, want %v", other.Labels, want)
	}

	// The second project's copy of o/r#7 is its own issue.
	p2, err := s.GetIssue(ctx, tasks[6].issueID.Int64)
	if err != nil {
		t.Fatal(err)
	}
	if p2.ProjectID != 2 || p2.Title != "p2 copy" || len(p2.Labels) != 0 || p2.Remote == nil || p2.Remote.RemoteKey != "legacy:o/r#7" {
		t.Errorf("p2 issue = %+v remote %+v", p2, p2.Remote)
	}

	// The pre-imported issue is untouched: no snapshot label, real key.
	imported, err := s.GetIssue(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Title != "imported" || len(imported.Labels) != 0 || imported.Remote.RemoteKey != "I_kwNODE9" {
		t.Errorf("pre-imported issue = %+v remote %+v", imported, imported.Remote)
	}

	// The next issue the store creates continues past the derived ids.
	next := mustCreateIssue(t, s, NewIssue{ProjectID: 1, Title: "after"})
	if next.ID != 5 {
		t.Errorf("next issue id = %d; want 5", next.ID)
	}
	fk, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fk.Close() }()
	if fk.Next() {
		t.Error("PRAGMA foreign_key_check reports a violation after 0040")
	}
	if err := fk.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestMigration0040OnTaskImportStaging: task import opens the backup's
// database with Open (task 117), so a backup written before 0040 exports a
// task already carrying its backfilled issue link, snapshot unchanged.
func TestMigration0040OnTaskImportStaging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "staged.db")
	seedBackfillFixture(t, path)
	src, err := Open(path)
	if err != nil {
		t.Fatalf("Open staged copy: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	exp, err := src.ExportTask(t.Context(), 5)
	if err != nil {
		t.Fatalf("ExportTask: %v", err)
	}
	id, ok := exp.Task.Int64("issue_id")
	if !ok {
		t.Fatal("exported task carries no backfilled issue_id")
	}
	var key string
	if err := src.db.QueryRow(`SELECT remote_key FROM issue_remotes WHERE issue_id = ?`, id).Scan(&key); err != nil || key != "legacy:o/other#7" {
		t.Errorf("staged issue %d remote = %q, %v", id, key, err)
	}
	if got := exp.Task.String("github_issue_json"); got != backfillOtherRepoSnap {
		t.Errorf("exported github_issue_json = %s", got)
	}

	// The live store backfilled o/other#7 under an id of its own; the staged
	// id names an unrelated live issue. The import follows the snapshot's
	// repo and number, never the staged id.
	live := openTest(t)
	p := testProject(t, live, "repo")
	want := int64(1)
	if id == want {
		want = 2
	}
	for range 5 {
		testIssue(t, live, p.ID, "unrelated")
	}
	if _, err := live.db.Exec(`INSERT INTO issue_remotes (issue_id, project_id, provider, remote_key, repo, number, url)
		VALUES (?, ?, 'github', 'legacy:o/other#7', 'o/other', 7, 'https://github.com/o/other/issues/7')`, want, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := live.ImportTask(t.Context(), exp, ImportOptions{ProjectID: p.ID}); err != nil {
		t.Fatalf("ImportTask: %v", err)
	}
	got, err := live.GetTask(t.Context(), 5)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.IssueID == nil || *got.IssueID != want {
		t.Errorf("imported issue_id = %v, want %d (staged id was %d)", got.IssueID, want, id)
	}
}
