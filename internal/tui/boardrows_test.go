package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestBoardStateShowsChildrenRollup is what pays for hiding lanes from the
// board (task 014 decision 13): a blocked lane is invisible in the list, so
// the parent's row has to say it is there.
func TestBoardStateShowsChildrenRollup(t *testing.T) {
	parent := apiclient.Task{ID: 1, State: stateAwaitingChildren}
	if got := renderBoardState(parent); !strings.Contains(got, stateAwaitingChildren) {
		t.Errorf("state cell = %q, want it to name the state", got)
	}

	parent.Children = &apiclient.ChildrenRollup{Total: 3, Settled: 1, Blocked: []int64{7, 8}}
	if got := renderBoardState(parent); !strings.Contains(got, "2 blocked") {
		t.Errorf("state cell = %q, want the blocked lanes surfaced", got)
	}

	// With nothing waiting on a human, progress is the useful thing to say.
	parent.Children = &apiclient.ChildrenRollup{Total: 4, Settled: 3}
	if got := renderBoardState(parent); !strings.Contains(got, "3/4 done") {
		t.Errorf("state cell = %q, want progress", got)
	}
}

// TestAwaitingChildrenBandsWithRunningWork: a parked parent's subtree is
// working, so sorting it among terminal tasks would hide an active fan-out at
// the bottom of the board. It is also not an attention state — nobody is
// being asked for anything by the parent itself.
func TestAwaitingChildrenBandsWithRunningWork(t *testing.T) {
	if band(stateAwaitingChildren) != bandRunning {
		t.Errorf("awaiting_children bands as %d, want bandRunning", band(stateAwaitingChildren))
	}
	if needsAttention(stateAwaitingChildren) {
		t.Error("awaiting_children reads as needing attention; the parent asks for nothing")
	}
}
