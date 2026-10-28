package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// The `vincent handoff` tree (task 137.5) against the real API handlers:
// start with ids and with select flags, ls with its filters, and show.
func TestHandoffCommands(t *testing.T) {
	// POST /v1/handoffs runs only a `role: handoff` workflow (task 137.8).
	globalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(globalDir, "integrate.yaml"), []byte(
		"name: integrate\nrole: handoff\nsteps:\n  - {id: gate, type: manual, instructions: go}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := workflow.NewRegistry(globalDir, workflow.Options{}, nil)
	reg.Reload()
	h := newLiveHarness(t, func(o *liveOptions) { o.worktrees, o.workflows = true, reg })
	ctx := t.Context()
	repo := testrepo.Init(t, "main")
	p := &store.Project{Name: "shipping", Path: repo, DefaultBranch: "main"}
	if err := h.st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project := strconv.FormatInt(p.ID, 10)
	handOff := func(title, kind string, priority int) string {
		t.Helper()
		iss, err := h.st.CreateIssue(ctx, store.NewIssue{ProjectID: p.ID, Title: title, Kind: kind, Priority: priority}, issuestate.Human)
		if err != nil {
			t.Fatal(err)
		}
		task := &store.Task{
			ProjectID: p.ID, Title: title, WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
			BaseBranch: "main", BranchName: "work/" + title, State: store.TaskRunning,
			IssueID: &iss.ID, IssueWorktree: store.IssueWorktreeMain,
		}
		if err := h.st.CreateTask(ctx, task, nil); err != nil {
			t.Fatal(err)
		}
		d := &store.Delivery{Kind: store.DeliveryChange, Title: title, HeadSHA: "abc", RecordedAt: time.Now().UTC()}
		if _, _, err := h.st.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: d}); err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(iss.ID, 10)
	}
	named := handOff("named", "bug", 2)
	urgent := handOff("urgent", "bug", 1)
	feature := handOff("feature", "feature", 1)

	start := func(args ...string) apiclient.Handoff {
		t.Helper()
		out, errOut, code := runCLI(t, append(append([]string{"handoff", "start", "--project", project, "--workflow", "integrate", "--paused"}, args...), "--json")...)
		if code != 0 {
			t.Fatalf("handoff start %v: exit %d (%s)", args, code, errOut)
		}
		var got apiclient.Handoff
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("handoff start --json = %q: %v", out, err)
		}
		return got
	}
	first := start(named, "--title", "Ship the named one")
	if len(first.Items) != 1 || strconv.FormatInt(first.Items[0].IssueID, 10) != named || first.Task == nil || first.Task.Title != "Ship the named one" {
		t.Errorf("start with ids = %+v", first)
	}
	second := start("--kind", "bug", "--delivery", "change", "--max", "5")
	if len(second.Items) != 1 || strconv.FormatInt(second.Items[0].IssueID, 10) != urgent {
		t.Errorf("start with a selection = %+v", second.Items)
	}

	_, errOut, code := runCLI(t, "handoff", "start", "--project", project, "--workflow", "integrate", "--kind", "bug")
	if code != 1 || !strings.Contains(errOut, "no issue eligible") {
		t.Errorf("empty selection: exit %d (%s)", code, errOut)
	}
	for _, n := range []string{"0", "-1"} {
		_, errOut, code = runCLI(t, "handoff", "start", "--project", project, "--workflow", "integrate", "--max", n)
		if code == 0 || !strings.Contains(errOut, "--max must be at least 1") {
			t.Errorf("--max %s: exit %d (%s)", n, code, errOut)
		}
	}
	_, errOut, code = runCLI(t, "handoff", "start", "--project", project, "--workflow", "integrate", feature, "--kind", "bug")
	if code == 0 || !strings.Contains(errOut, "cannot be combined") {
		t.Errorf("ids with select flags: exit %d (%s)", code, errOut)
	}

	out, errOut, code := runCLI(t, "handoff", "ls", "--project", project, "--state", "live")
	if code != 0 {
		t.Fatalf("ls: exit %d (%s)", code, errOut)
	}
	if !strings.Contains(out, "CREATED_BY") || strings.Count(out, "live") != 2 {
		t.Errorf("ls:\n%s", out)
	}
	out, _, code = runCLI(t, "handoff", "ls", "--issue", urgent, "--json")
	var rows []apiclient.Handoff
	if code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].ID != second.ID {
		t.Errorf("ls --issue: exit %d %s", code, out)
	}

	out, errOut, code = runCLI(t, "handoff", "show", strconv.FormatInt(first.ID, 10))
	if code != 0 {
		t.Fatalf("show: exit %d (%s)", code, errOut)
	}
	for _, want := range []string{"state    live", "task     " + strconv.FormatInt(first.TaskID, 10), "ISSUE", named, "change"} {
		if !strings.Contains(out, want) {
			t.Errorf("show does not carry %q:\n%s", want, out)
		}
	}

	// The item verbs (task 137.6).
	hid := strconv.FormatInt(first.ID, 10)
	note := filepath.Join(t.TempDir(), "comment.md")
	if err := os.WriteFile(note, []byte("Merged into main."), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = runCLI(t, "handoff", "deliver", hid, named, "--note", "landed", "--comment-file", note)
	if code != 0 {
		t.Fatalf("deliver: exit %d (%s)", code, errOut)
	}
	if !strings.Contains(out, "delivered") || !strings.Contains(out, "landed") {
		t.Errorf("deliver:\n%s", out)
	}
	namedID, _ := strconv.ParseInt(named, 10, 64)
	if iss, _ := h.st.GetIssue(ctx, namedID); iss.State != issuestate.Closed {
		t.Errorf("deliver left the issue %s", iss.State)
	}
	if comments, _ := h.st.ListIssueComments(ctx, namedID); len(comments) != 1 || comments[0].Body != "Merged into main." {
		t.Errorf("comments = %+v", comments)
	}
	_, errOut, code = runCLI(t, "handoff", "release", hid, named)
	if code == 0 || !strings.Contains(errOut, "already delivered") {
		t.Errorf("release of a delivered item: exit %d (%s)", code, errOut)
	}
	_, errOut, code = runCLI(t, "handoff", "wait", hid)
	if code == 0 || !strings.Contains(errOut, "--all") {
		t.Errorf("wait with neither ids nor --all: exit %d (%s)", code, errOut)
	}
	out, errOut, code = runCLI(t, "handoff", "release", strconv.FormatInt(second.ID, 10), "--all", "--json")
	var released apiclient.Handoff
	if code != 0 || json.Unmarshal([]byte(out), &released) != nil || released.Items[0].Outcome != "released" {
		t.Errorf("release --all: exit %d %s (%s)", code, out, errOut)
	}
}
