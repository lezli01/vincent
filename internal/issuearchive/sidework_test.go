package issuearchive

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// TestIsIssueMain: the main role makes a main task, except on a merge-back,
// which is side work for ordering.
func TestIsIssueMain(t *testing.T) {
	for _, tc := range []struct {
		role, wf string
		want     bool
	}{
		{store.IssueWorktreeMain, "adhoc", true},
		{store.IssueWorktreeMain, workflow.MergeBackName, false},
		{store.IssueWorktreeSide, "adhoc", false},
		{"", "adhoc", false},
	} {
		if got := IsIssueMain(tc.role, tc.wf); got != tc.want {
			t.Errorf("IsIssueMain(%q, %q) = %v, want %v", tc.role, tc.wf, got, tc.want)
		}
	}
}

// TestSideWorkHold is review F3 as one rule: a refused side task holds main
// tasks; a refused main task holds nothing; side tasks are never held.
func TestSideWorkHold(t *testing.T) {
	var h SideWorkHold
	if h.Holds(true) || h.Holds(false) {
		t.Fatal("the zero hold holds")
	}
	h.Refuse(true)
	if h.Holds(true) {
		t.Error("a refused main task held another main task")
	}
	h.Refuse(false)
	if !h.Holds(true) {
		t.Error("a refused side task did not hold the main tasks")
	}
	if h.Holds(false) {
		t.Error("a side task was held for side work")
	}
}
