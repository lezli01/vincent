package tui

import (
	"testing"

	"github.com/lezli01/vincent/internal/taskstate"
)

// TestNeedsAttentionIsTaskstateNeedsHuman holds the board's `!` set to the
// daemon's per-project `attention` count (task 132.1): a state one pins and
// the other does not count would make the picker and the board disagree.
func TestNeedsAttentionIsTaskstateNeedsHuman(t *testing.T) {
	for _, s := range taskstate.All {
		if got, want := needsAttention(string(s)), taskstate.NeedsHuman(s); got != want {
			t.Errorf("needsAttention(%s) = %v, taskstate.NeedsHuman = %v", s, got, want)
		}
	}
}
