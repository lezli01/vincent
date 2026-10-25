package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// The `vincent handoff` tree (task 137.5) against the real API handlers:
// start with ids and with select flags, ls with its filters, and show.
func TestHandoffCommands(t *testing.T) {
	h := newLiveHarness(t, func(o *liveOptions) { o.worktrees = true })
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
		out, errOut, code := runCLI(t, append(append([]string{"handoff", "start", "--project", project, "--workflow", "adhoc", "--paused"}, args...), "--json")...)
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

	_, errOut, code := runCLI(t, "handoff", "start", "--project", project, "--workflow", "adhoc", "--kind", "bug")
	if code != 1 || !strings.Contains(errOut, "no issue eligible") {
		t.Errorf("empty selection: exit %d (%s)", code, errOut)
	}
	for _, n := range []string{"0", "-1"} {
		_, errOut, code = runCLI(t, "handoff", "start", "--project", project, "--workflow", "adhoc", "--max", n)
		if code == 0 || !strings.Contains(errOut, "--max must be at least 1") {
			t.Errorf("--max %s: exit %d (%s)", n, code, errOut)
		}
	}
	_, errOut, code = runCLI(t, "handoff", "start", "--project", project, "--workflow", "adhoc", feature, "--kind", "bug")
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
}
