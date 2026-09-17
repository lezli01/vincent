package apiclient_test

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// importLive is the real import handler over a real store in a real data dir,
// with one project named "repo" (id 1) already registered.
type importLive struct {
	client *apiclient.Client
	store  *store.Store
	dirs   config.Dirs
}

func newImportLive(t *testing.T) *importLive {
	t.Helper()
	dirs := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	st, err := store.Open(filepath.Join(dirs.Data, backup.DatabaseEntry))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(t.Context(),
		&store.Project{Name: "repo", Path: t.TempDir(), DefaultBranch: "main"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		Dirs:        dirs,
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &importLive{client: apiclient.New(ts.URL, testToken), store: st, dirs: dirs}
}

// backedUp describes the task a test archive carries.
type backedUp struct {
	project string          // the source project's name; "repo" when empty
	state   store.TaskState // archived when empty
	lane    bool            // a fan-out lane of a parent in the same backup
	schema  int             // the manifest's schema version; the binary's when zero
}

const transcriptBody = `{"type":"system","subtype":"init"}` + "\n"

// makeImportArchive builds a real `daemon backup` archive from a source
// installation holding one task with two step runs and their transcripts, and
// returns it with the task's id.
func makeImportArchive(t *testing.T, b backedUp) (archive string, taskID int64) {
	t.Helper()
	ctx := t.Context()
	src := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	st, err := store.Open(filepath.Join(src.Data, backup.DatabaseEntry))
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	defer func() { _ = st.Close() }()
	name := b.project
	if name == "" {
		name = "repo"
	}
	p := &store.Project{Name: name, Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	mk := func(title string, parent *int64) *store.Task {
		task := &store.Task{
			ProjectID: p.ID, Title: title, WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
			BaseBranch: "main", BranchName: "vincent/" + title, State: store.TaskDone, ParentTaskID: parent,
		}
		if err := st.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task
	}
	var parent *int64
	if b.lane {
		pt := mk("parent", nil)
		parent = &pt.ID
	}
	task := mk("imported", parent)
	for i := range 2 {
		transcript := filepath.Join(src.Data, backup.TranscriptsPrefix,
			strconv.FormatInt(task.ID, 10), strconv.Itoa(i)+"-1.jsonl")
		if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(transcript, []byte(transcriptBody), 0o600); err != nil {
			t.Fatal(err)
		}
		run := &store.StepRun{
			TaskID: task.ID, StepIndex: i, StepID: "s" + strconv.Itoa(i), StepType: "agent",
			Attempt: 1, State: store.StepSucceeded, TranscriptPath: transcript,
		}
		if err := st.CreateStepRun(ctx, run); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
	}
	state := b.state
	if state == "" {
		state = store.TaskArchived
	}
	if state == store.TaskArchived {
		if _, _, err := st.TransitionTask(ctx, task.ID,
			store.TaskDone, store.TaskArchived, store.TaskChange{}); err != nil {
			t.Fatalf("archive: %v", err)
		}
	}
	dbCopy := filepath.Join(t.TempDir(), backup.DatabaseEntry)
	if err := st.BackupTo(ctx, dbCopy); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	schema := b.schema
	if schema == 0 {
		schema = store.NewestMigration()
	}
	archive = filepath.Join(t.TempDir(), "backup.tar.gz")
	createArchive(t, archive, dbCopy, src, schema)
	return archive, task.ID
}

func createArchive(t *testing.T, dst, db string, src config.Dirs, schema int) {
	t.Helper()
	if _, err := backup.Create(dst, backup.Source{
		Database: db, DataDir: src.Data, ConfigDir: src.Config,
		Manifest: backup.Manifest{
			VincentVersion: "v0.0.0-test", SchemaVersion: schema, CreatedAt: backup.FormatTime(time.Now()),
		},
	}); err != nil {
		t.Fatalf("create archive: %v", err)
	}
}

// wantImportError asserts a refusal arrived as the §13.1 envelope with the
// reason a client branches on, and returns its details.
func wantImportError(t *testing.T, err error, status int, reason string) map[string]string {
	t.Helper()
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("ImportTask = %v, want *apiclient.Error", err)
	}
	if apiErr.Status != status {
		t.Fatalf("status = %d (%s), want %d", apiErr.Status, apiErr.Message, status)
	}
	if reason != "" && apiErr.Details["reason"] != reason {
		t.Fatalf("reason = %q (%s), want %q", apiErr.Details["reason"], apiErr.Message, reason)
	}
	return apiErr.Details
}

func TestImportTaskOverTheWire(t *testing.T) {
	archive, id := makeImportArchive(t, backedUp{})
	live := newImportLive(t)
	ctx := t.Context()

	res, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id})
	if err != nil {
		t.Fatalf("ImportTask: %v", err)
	}
	if res.TaskID != id || res.ProjectID != 1 || res.Title != "imported" || res.StepRuns != 2 ||
		res.StepRunsRenumbered || res.TranscriptFiles != 2 ||
		res.TranscriptBytes != int64(2*len(transcriptBody)) ||
		res.BackupSchemaVersion != store.NewestMigration() || res.ArchivedAt == "" {
		t.Errorf("result = %+v", res)
	}

	got, err := live.client.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != string(store.TaskArchived) || len(got.Steps) != 2 {
		t.Fatalf("imported task = state %s, %d step run(s); want archived with 2", got.State, len(got.Steps))
	}
	// The transcript path was rewritten into this data dir, so the route
	// serves the original bytes.
	data, _, err := live.client.TranscriptRaw(ctx, id, got.Steps[0].ID, apiclient.TranscriptOptions{})
	if err != nil {
		t.Fatalf("TranscriptRaw: %v", err)
	}
	if string(data) != transcriptBody {
		t.Errorf("transcript = %q, want %q", data, transcriptBody)
	}
	runs, err := live.store.ListStepRuns(ctx, id)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}
	wantDir := filepath.Join(live.dirs.Data, backup.TranscriptsPrefix, strconv.FormatInt(id, 10))
	for _, r := range runs {
		if filepath.Dir(r.TranscriptPath) != wantDir {
			t.Errorf("transcript_path = %s, want one under %s", r.TranscriptPath, wantDir)
		}
	}

	// Decision 2: a second import refuses rather than replacing the row.
	_, err = live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id})
	wantImportError(t, err, http.StatusConflict, apiclient.ImportRefusedTaskExists)
}

func TestImportTaskRefusalsOverTheWire(t *testing.T) {
	ctx := t.Context()

	t.Run("not archived", func(t *testing.T) {
		archive, id := makeImportArchive(t, backedUp{state: store.TaskDone})
		live := newImportLive(t)
		_, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id})
		details := wantImportError(t, err, http.StatusConflict, apiclient.ImportRefusedNotArchived)
		if details["state"] != string(store.TaskDone) {
			t.Errorf("details = %v, want the backed-up state", details)
		}
	})

	t.Run("lane without its parent", func(t *testing.T) {
		archive, id := makeImportArchive(t, backedUp{lane: true})
		live := newImportLive(t)
		_, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id})
		wantImportError(t, err, http.StatusConflict, apiclient.ImportRefusedParentMissing)
	})

	t.Run("same project id, another name, then --project", func(t *testing.T) {
		archive, id := makeImportArchive(t, backedUp{project: "elsewhere"})
		live := newImportLive(t)
		_, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id})
		wantImportError(t, err, http.StatusConflict, apiclient.ImportRefusedProjectMismatch)

		_, err = live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id, ProjectID: 99})
		wantImportError(t, err, http.StatusNotFound, apiclient.ImportProjectNotFound)

		res, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id, ProjectID: 1})
		if err != nil {
			t.Fatalf("ImportTask with project_id: %v", err)
		}
		if res.ProjectID != 1 {
			t.Errorf("ProjectID = %d, want 1", res.ProjectID)
		}
	})

	t.Run("task not in the backup", func(t *testing.T) {
		archive, _ := makeImportArchive(t, backedUp{})
		live := newImportLive(t)
		_, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: 404})
		wantImportError(t, err, http.StatusNotFound, apiclient.ImportTaskNotInBackup)
	})

	t.Run("stray transcripts", func(t *testing.T) {
		archive, id := makeImportArchive(t, backedUp{})
		live := newImportLive(t)
		stray := filepath.Join(live.dirs.Data, backup.TranscriptsPrefix, strconv.FormatInt(id, 10), "stray")
		if err := os.MkdirAll(filepath.Dir(stray), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stray, []byte("mine"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id})
		wantImportError(t, err, http.StatusConflict, apiclient.ImportRefusedTranscriptsPresent)
		if b, err := os.ReadFile(stray); err != nil || string(b) != "mine" {
			t.Errorf("the stray was touched: %q, %v", b, err)
		}
		if _, err := live.store.GetTask(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetTask after the refusal = %v, want ErrNotFound", err)
		}
	})

	t.Run("schema from the future", func(t *testing.T) {
		archive, id := makeImportArchive(t, backedUp{schema: store.NewestMigration() + 1})
		live := newImportLive(t)
		_, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id})
		wantImportError(t, err, http.StatusBadRequest, apiclient.ImportSchemaTooNew)
	})

	t.Run("bad paths", func(t *testing.T) {
		live := newImportLive(t)
		notABackup := filepath.Join(t.TempDir(), "notes.tar.gz")
		if err := os.WriteFile(notABackup, []byte("just text"), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, p := range map[string]string{
			"relative":     "backup.tar.gz",
			"directory":    t.TempDir(),
			"missing":      filepath.Join(t.TempDir(), "gone.tar.gz"),
			"not a backup": notABackup,
		} {
			_, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: p, TaskID: 1})
			var apiErr *apiclient.Error
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
				t.Errorf("%s: ImportTask = %v, want a 400", name, err)
			}
		}
	})
}

// TestImportTaskFromAnOlderSchema: the staged copy is migrated, never the live
// file. The archive's database stops at migration 0001, so every later
// migration runs over a row that predates it.
func TestImportTaskFromAnOlderSchema(t *testing.T) {
	ctx := t.Context()
	src := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	dbPath := filepath.Join(t.TempDir(), backup.DatabaseEntry)
	schema, err := os.ReadFile(filepath.Join("..", "store", "migrations", "0001_init.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := backup.FormatTime(time.Now())
	for _, q := range []string{
		string(schema),
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations VALUES (1, '` + now + `')`,
		`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
			VALUES (1, 'repo', '/nowhere', 'main', '` + now + `', '` + now + `')`,
		`INSERT INTO tasks (id, project_id, title, workflow_name, workflow_snapshot, base_branch,
			branch_name, state, created_at, updated_at, archived_at)
			VALUES (7, 1, 'old', 'adhoc', 'steps: []', 'main', 'vincent/7-old', 'archived',
			'` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO step_runs (task_id, step_index, step_id, step_type, attempt, state, started_at)
			VALUES (7, 0, 's', 'agent', 1, 'succeeded', '` + now + `')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			_ = db.Close()
			t.Fatalf("seed old schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "old.tar.gz")
	createArchive(t, archive, dbPath, src, 1)

	live := newImportLive(t)
	res, err := live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: 7})
	if err != nil {
		t.Fatalf("ImportTask from schema 1: %v", err)
	}
	if res.TaskID != 7 || res.StepRuns != 1 || res.BackupSchemaVersion != 1 {
		t.Errorf("result = %+v", res)
	}
	// The archive itself was not migrated in place: the staged copy was.
	m, err := backup.ReadManifest(archive)
	if err != nil || m.SchemaVersion != 1 {
		t.Errorf("manifest after import = %+v, %v", m, err)
	}
}

// TestImportTaskFailedCommitLeavesNoTranscripts: the transcript directory is
// placed before the rows are inserted, so an insert that fails has to take it
// back out. The failure is a trigger added through a second connection.
func TestImportTaskFailedCommitLeavesNoTranscripts(t *testing.T) {
	ctx := t.Context()
	archive, id := makeImportArchive(t, backedUp{})
	live := newImportLive(t)
	db, err := sql.Open("sqlite", live.store.Path())
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	_, err = db.ExecContext(ctx, `CREATE TRIGGER refuse_runs BEFORE INSERT ON step_runs
		BEGIN SELECT RAISE(ABORT, 'refused by test'); END`)
	_ = db.Close()
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, err = live.client.ImportTask(ctx, apiclient.TaskImportRequest{Path: archive, TaskID: id})
	wantImportError(t, err, http.StatusInternalServerError, "")
	dir := filepath.Join(live.dirs.Data, backup.TranscriptsPrefix, strconv.FormatInt(id, 10))
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s after a failed import = %v, want not exist", dir, err)
	}
	if _, err := live.store.GetTask(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTask after a failed import = %v, want ErrNotFound", err)
	}
	// Staging is gone too: nothing .vincent-import-* is left in the data dir.
	matches, _ := filepath.Glob(filepath.Join(live.dirs.Data, ".vincent-import-*"))
	if len(matches) != 0 {
		t.Errorf("staging left behind: %v", matches)
	}
}
