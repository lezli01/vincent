package cli

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// TestIssueLaneCell: the handoff marker (task 137.10) rides the LANE cell
// after the lane, with the handoff id when it names one, and before the
// attention mark.
func TestIssueLaneCell(t *testing.T) {
	h7, t9 := int64(7), int64(9)
	for _, tc := range []struct {
		lane      string
		handoff   *apiclient.IssueHandoff
		attention bool
		want      string
	}{
		{"open", nil, false, "open"},
		{"in_progress", nil, true, "in_progress !"},
		{"hand_off", &apiclient.IssueHandoff{State: "pending"}, false, "hand_off pending"},
		{"hand_off", &apiclient.IssueHandoff{State: "pending"}, true, "hand_off pending !"},
		{"hand_off", &apiclient.IssueHandoff{State: "claimed", HandoffID: &h7, TaskID: &t9}, false, "hand_off claimed H7"},
		{"in_progress", &apiclient.IssueHandoff{State: "waiting", HandoffID: &h7, TaskID: &t9}, true, "in_progress waiting H7 !"},
		{"hand_off", &apiclient.IssueHandoff{State: "delivered", HandoffID: &h7, TaskID: &t9}, false, "hand_off delivered H7"},
		{"in_progress", &apiclient.IssueHandoff{State: "idle"}, false, "in_progress idle"},
		{"in_progress", &apiclient.IssueHandoff{State: "idle"}, true, "in_progress idle !"},
	} {
		iss := &apiclient.Issue{Lane: tc.lane, Handoff: tc.handoff, Attention: tc.attention}
		if got := issueLane(iss); got != tc.want {
			t.Errorf("issueLane(%s, %+v, %v) = %q, want %q", tc.lane, tc.handoff, tc.attention, got, tc.want)
		}
	}
}

// TestIssueLsHandoff: the marker in `issue ls`, the repeatable --handoff
// whose values only the daemon validates, and the `handoff` line on show.
func TestIssueLsHandoff(t *testing.T) {
	h := newLiveHarness(t)
	ctx := t.Context()
	done := func(title, branch string) apiclient.Issue {
		t.Helper()
		iss := issueJSON(t, "issue", "add", "--project", h.project(), "--title", title)
		task := &store.Task{
			ProjectID: h.projectID, Title: title, WorkflowName: "adhoc", WorkflowSnapshot: "x",
			BaseBranch: "main", BranchName: branch, State: store.TaskDone, IssueID: &iss.ID,
		}
		if err := h.st.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return iss
	}
	pending := done("marker pending", "m-1")
	claimed := done("marker claimed", "m-2")
	idleID, _, finish := readinessIssue(t, h)
	finish()
	ho := &store.Handoff{CreatedBy: store.HandoffByHuman}
	hoTask := &store.Task{
		ProjectID: h.projectID, Title: "handoff", WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "m-handoff", State: store.TaskQueued,
	}
	if err := h.st.CreateHandoff(ctx, ho, []int64{claimed.ID}, hoTask, nil); err != nil {
		t.Fatalf("CreateHandoff: %v", err)
	}

	out, errOut, code := runCLI(t, "issue", "ls", "--project", h.project())
	if code != 0 {
		t.Fatalf("ls: exit %d (%s)", code, errOut)
	}
	hid := strconv.FormatInt(ho.ID, 10)
	for title, want := range map[string]string{
		"marker pending": "open hand_off pending -",
		"marker claimed": "open hand_off claimed H" + hid + " -",
		"explicit":       "open in_progress idle -",
	} {
		var row string
		for _, l := range strings.Split(out, "\n") {
			if strings.HasSuffix(l, title) {
				row = strings.Join(strings.Fields(l), " ")
			}
		}
		if !strings.Contains(row, want) {
			t.Errorf("row %q = %q, want %q", title, row, want)
		}
	}

	ids := func(args ...string) []int64 {
		t.Helper()
		list := issueListJSON(t, append([]string{"--project", h.project()}, args...)...)
		out := make([]int64, 0, len(list))
		for i := range list {
			out = append(out, list[i].ID)
		}
		slices.Sort(out)
		return out
	}
	if got, want := ids("--handoff", "pending", "--handoff", "idle"), []int64{pending.ID, idleID}; !slices.Equal(got, want) {
		t.Errorf("--handoff pending --handoff idle = %v, want %v", got, want)
	}
	if got, want := ids("--handoff", "claimed"), []int64{claimed.ID}; !slices.Equal(got, want) {
		t.Errorf("--handoff claimed = %v, want %v", got, want)
	}
	if _, errOut, code = runCLI(t, "issue", "ls", "--handoff", "bogus"); code != 1 || !strings.Contains(errOut, "unknown issue handoff") {
		t.Errorf("--handoff bogus: exit %d, %q; want 1 and the daemon's message", code, errOut)
	}

	out, _, _ = runCLI(t, "issue", "show", strconv.FormatInt(claimed.ID, 10))
	if want := "handoff   claimed (handoff " + hid + ", task " + strconv.FormatInt(ho.TaskID, 10) + ")"; !strings.Contains(out, want) {
		t.Errorf("show does not carry %q:\n%s", want, out)
	}
	out, _, _ = runCLI(t, "issue", "show", strconv.FormatInt(pending.ID, 10))
	if !strings.Contains(out, "handoff   pending\n") {
		t.Errorf("show of a pending issue:\n%s", out)
	}
}
