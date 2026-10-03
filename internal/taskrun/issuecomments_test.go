package taskrun

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// The issue thread a task carries (task 130 decision 24, 130.16): frozen in
// the snapshot at creation, rendered as `.Issue.Comments` and written into
// $VINCENT_ISSUE_FILE in `gh issue view --json comments`'s element shape.

func threadedSnapshot() *store.IssueSnapshot {
	snap := importedSnapshot()
	snap.Comments = []store.IssueSnapshotComment{
		{Author: "octo", Body: "Seen on Windows too.", CreatedAt: time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)},
		{Author: "lezli01", Body: "Repro attached.", CreatedAt: time.Date(2026, 9, 3, 9, 30, 0, 0, time.FixedZone("CEST", 2*3600))},
	}
	return snap
}

const threadTemplate = `{{ range .Issue.Comments }}[{{ .Author }} {{ .CreatedAt.UTC.Format "2006-01-02" }}] {{ .Body }}
{{ end }}`

func TestIssueCommentsRenderInATemplate(t *testing.T) {
	got, err := workflow.Render("prompt", threadTemplate,
		workflow.RenderContext{Issue: issueContext(&store.Task{Issue: threadedSnapshot()})})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	const want = "[octo 2026-09-02] Seen on Windows too.\n[lezli01 2026-09-03] Repro attached.\n"
	if got != want {
		t.Errorf("rendered %q, want %q", got, want)
	}

	// No thread, a legacy GitHub snapshot and no issue at all range over
	// nothing rather than failing the render.
	for name, task := range map[string]*store.Task{
		"no comments": {Issue: localSnapshot()},
		"legacy":      legacyTask(storedIssue()),
		"unlinked":    {},
	} {
		got, err := workflow.Render("prompt", threadTemplate, workflow.RenderContext{Issue: issueContext(task)})
		if err != nil || got != "" {
			t.Errorf("%s: rendered %q, %v; want nothing", name, got, err)
		}
	}
}

func TestIssueFileCommentsAreGhShaped(t *testing.T) {
	f, _, ok := issueFileOf(&store.Task{Issue: threadedSnapshot()})
	if !ok {
		t.Fatal("a linked task produced no file")
	}
	b, err := json.Marshal(f.Comments)
	if err != nil {
		t.Fatal(err)
	}
	// Oldest first, createdAt in UTC RFC3339 as gh prints it.
	const want = `[{"author":{"login":"octo"},"body":"Seen on Windows too.","createdAt":"2026-09-02T08:00:00Z"},` +
		`{"author":{"login":"lezli01"},"body":"Repro attached.","createdAt":"2026-09-03T07:30:00Z"}]`
	if string(b) != want {
		t.Errorf("comments =\n %s\nwant\n %s", b, want)
	}

	// An empty thread and a legacy snapshot are `[]`, never null.
	for name, task := range map[string]*store.Task{
		"no comments": {Issue: localSnapshot()},
		"legacy":      legacyTask(storedIssue()),
	} {
		f, _, _ := issueFileOf(task)
		b, _ := json.Marshal(f.Comments)
		if string(b) != "[]" {
			t.Errorf("%s: comments = %s, want []", name, b)
		}
	}
}

// TestIssueThreadIsFrozenAtCreation drives the real store: the task's thread
// is what the issue held when the task was created, and a comment written
// afterwards reaches neither the render nor the file. Nothing here has a
// GitHub client to call — the snapshot is filled from issue_comments inside
// the create transaction (decision 24.5).
func TestIssueThreadIsFrozenAtCreation(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(filepath.Join(t.TempDir(), "thread.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	iss, err := st.CreateIssue(ctx, store.NewIssue{ProjectID: project.ID, Title: "Lock file leaks", Author: "me"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	for _, body := range []string{"first", "second"} {
		if _, err := st.AddIssueComment(ctx, iss.ID, "me", body, "", issuestate.Human); err != nil {
			t.Fatalf("AddIssueComment: %v", err)
		}
	}
	task := &store.Task{
		ProjectID: project.ID, Title: "fix", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/0-fix", State: store.TaskQueued,
		IssueID: &iss.ID, Issue: store.NewIssueSnapshot(iss, time.Now()),
	}
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	snapshot := func() (string, []issueFileComment) {
		t.Helper()
		got, err := st.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		r, err := workflow.Render("prompt", `{{ range .Issue.Comments }}{{ .Author }}:{{ .Body }};{{ end }}`,
			workflow.RenderContext{Issue: issueContext(got)})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		f, _, _ := issueFileOf(got)
		return r, f.Comments
	}
	render, file := snapshot()
	if render != "me:first;me:second;" {
		t.Fatalf("rendered %q, want the creation-time thread", render)
	}
	if len(file) != 2 || file[0].Body != "first" || file[1].Body != "second" || file[0].Author.Login != "me" {
		t.Fatalf("file comments = %+v", file)
	}

	if _, err := st.AddIssueComment(ctx, iss.ID, "me", "too late", "", issuestate.Human); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	render2, file2 := snapshot()
	if render2 != render || !reflect.DeepEqual(file2, file) {
		t.Errorf("a later comment changed the task: %q %+v", render2, file2)
	}
}
