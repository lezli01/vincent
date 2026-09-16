package chatrun

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// TurnWritesNoTranscript is pinned to exactly the two failures runTurn raises
// before transcript.Open has made a file (task 103 decision 4). Every other
// reason is an ending with a file behind it, so a client that took one of those
// for "never had a transcript" would report a pruned file as nothing to see.
func TestTurnWritesNoTranscriptIsExactlyTheTwoEarlyFailures(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   bool
	}{
		{ReasonAgentUnavailable, true},
		{ReasonTranscriptIOError, true},
		{ReasonSessionLost, false},
		{ReasonAgentError, false},
		{ReasonCanceled, false},
		{ReasonTimeout, false},
		{ReasonInputTimeout, false},
		{ReasonTranscriptLimit, false},
		{"", false},
	} {
		if got := TurnWritesNoTranscript(tc.reason); got != tc.want {
			t.Errorf("TurnWritesNoTranscript(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}

// The predicate describes runTurn's ordering, so this holds it to what runTurn
// actually leaves on disk: a turn on an adapter nobody registered fails before
// the file is opened, and a turn that ran has one.
func TestTurnWritesNoTranscriptMatchesRunTurn(t *testing.T) {
	h := newHarness(t)

	gone := h.chatOn(t, "no-such-agent")
	turn := h.sendAndWait(t, gone.ID, "hello?")
	if turn.State != chatstate.TurnFailed || turn.FailReason != ReasonAgentUnavailable {
		t.Fatalf("turn = %s/%s, want failed/%s", turn.State, turn.FailReason, ReasonAgentUnavailable)
	}
	path := filepath.Join(h.runner.transcriptDir(gone.ID), fmt.Sprintf("%d.jsonl", turn.Seq))
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an %s turn left %s behind (stat: %v)", ReasonAgentUnavailable, path, err)
	}
	if !TurnWritesNoTranscript(turn.FailReason) {
		t.Errorf("TurnWritesNoTranscript(%q) = false for a turn with no file", turn.FailReason)
	}

	ran := h.chat(t)
	turn = h.sendAndWait(t, ran.ID, "hello")
	if turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s (%s: %s)", turn.State, turn.FailReason, turn.ErrorMessage)
	}
	_ = h.transcript(t, ran.ID, turn.Seq) // fails the test when the file is missing
	if TurnWritesNoTranscript(turn.FailReason) {
		t.Errorf("TurnWritesNoTranscript(%q) = true for a turn that wrote one", turn.FailReason)
	}
}
