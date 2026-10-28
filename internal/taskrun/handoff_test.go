package taskrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
)

// handoffFixture is a store holding one handoff over two delivered issues,
// and the handoff task, whose worktree is a real checkout.
func handoffFixture(t *testing.T) (*store.Store, *store.Task, []*store.Issue) {
	t.Helper()
	ctx := t.Context()
	st, err := storetest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repo := testrepo.Init(t, "main")
	p := &store.Project{Name: "p", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	var issues []*store.Issue
	for _, title := range []string{"first", "second"} {
		iss, err := st.CreateIssue(ctx, store.NewIssue{ProjectID: p.ID, Title: title, Kind: "bug"}, issuestate.Human)
		if err != nil {
			t.Fatal(err)
		}
		work := &store.Task{
			ProjectID: p.ID, Title: title, WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
			BaseBranch: "main", BranchName: "vincent/" + title, State: store.TaskRunning,
			IssueID: &iss.ID, IssueWorktree: store.IssueWorktreeMain,
		}
		if err := st.CreateTask(ctx, work, nil); err != nil {
			t.Fatal(err)
		}
		d := &store.Delivery{
			Kind: store.DeliveryChange, Title: title, HeadSHA: "sha-" + title, Branch: "vincent/" + title,
			Base: "main", RecordedAt: time.Now().UTC(),
		}
		if _, _, err := st.TransitionTask(ctx, work.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: d}); err != nil {
			t.Fatal(err)
		}
		issues = append(issues, iss)
	}
	task := &store.Task{
		ProjectID: p.ID, Title: "integrate", WorkflowName: "integrate", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/integrate", State: store.TaskQueued,
	}
	h := &store.Handoff{CreatedBy: store.HandoffByHuman}
	if err := st.CreateHandoff(ctx, h, []int64{issues[0].ID, issues[1].ID}, task, nil); err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = repo
	return st, task, issues
}

// `.Handoff` is built from the claim-time rows: an issue edited after the
// claim is not reflected, ItemLines has one JSON object per item equal to the
// file's elements, and each item's issue is byte-compatible with
// $VINCENT_ISSUE_FILE.
func TestHandoffContextAndFile(t *testing.T) {
	st, task, issues := handoffFixture(t)
	ctx := t.Context()
	cur, err := st.GetIssue(ctx, issues[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	renamed := "renamed after the claim"
	if _, err := st.UpdateIssue(ctx, cur.ID, cur.Version, store.IssuePatch{Title: &renamed}, issuestate.Human); err != nil {
		t.Fatal(err)
	}

	d, ok, err := loadHandoff(ctx, st, task)
	if err != nil || !ok {
		t.Fatalf("loadHandoff = %v, %v", ok, err)
	}
	hc := d.ctx
	if hc.ID == 0 || len(hc.Items) != 2 {
		t.Fatalf(".Handoff = %+v", hc)
	}
	if got := hc.Items[0].Issue.Title; got != "first" {
		t.Errorf("item 0 title = %q, want the claim-time title", got)
	}
	if it := hc.Items[1]; it.Branch != "vincent/second" || it.HeadSHA != "sha-second" || it.TaskID == 0 ||
		it.Delivery.Kind != string(store.DeliveryChange) || !it.Delivery.Ready {
		t.Errorf("item 1 = %+v", it)
	}

	var file struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(d.file, &file); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(hc.ItemLines, "\n")
	if len(lines) != len(file.Items) {
		t.Fatalf("ItemLines has %d lines, the file %d items", len(lines), len(file.Items))
	}
	items, _, err := st.ListHandoffItems(ctx, hc.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range lines {
		var el struct {
			Issue json.RawMessage `json:"issue"`
		}
		if err := json.Unmarshal([]byte(line), &el); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		compact, _ := json.Marshal(file.Items[i])
		if line != string(compact) {
			t.Errorf("line %d = %s, want the file's element %s", i, line, compact)
		}
		f, _, _ := issueFileOf(&store.Task{Issue: items[i].Issue})
		want, _ := json.Marshal(f)
		if string(el.Issue) != string(want) {
			t.Errorf("item %d issue = %s, want $VINCENT_ISSUE_FILE's %s", i, el.Issue, want)
		}
	}

	// The file lands in the worktree's git dir, and a change in the rows is
	// rewritten on the next attempt.
	env, err := writeHandoffFile(task, d, ok)
	if err != nil {
		t.Fatal(err)
	}
	if env.File != filepath.Join(task.WorktreePath, ".git", handoffFileName) {
		t.Errorf("File = %q", env.File)
	}
	got, err := os.ReadFile(env.File)
	if err != nil || string(got) != string(d.file) {
		t.Fatalf("file = %q, %v", got, err)
	}
	if err := os.WriteFile(env.File, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeHandoffFile(task, d, ok); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(env.File); string(got) != string(d.file) {
		t.Errorf("retry left %q, want the file rewritten", got)
	}
}

// Every task that is not a handoff task renders a zero `.Handoff` and gets
// no file.
func TestOrdinaryTaskHasNoHandoff(t *testing.T) {
	st, task, _ := handoffFixture(t)
	other := &store.Task{
		ProjectID: task.ProjectID, Title: "plain", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/plain", State: store.TaskQueued,
	}
	if err := st.CreateTask(t.Context(), other, nil); err != nil {
		t.Fatal(err)
	}
	d, ok, err := loadHandoff(t.Context(), st, other)
	if err != nil || ok || d.ctx.ID != 0 {
		t.Fatalf("loadHandoff(ordinary) = %+v, %v, %v", d.ctx, ok, err)
	}
	env, err := writeHandoffFile(other, d, ok)
	if err != nil || env.File != "" {
		t.Errorf("writeHandoffFile(ordinary) = %+v, %v", env, err)
	}
}
