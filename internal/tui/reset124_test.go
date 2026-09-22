package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// resetRecord is the mark claude's `/clear` leaves (task 124.20): a record
// whose whole content is its type.
var resetRecord = apiclient.TranscriptRecord{
	Type: recConversationReset,
	Raw:  json.RawMessage(`{"type":"agent.conversation_reset"}`),
}

// TestConversationResetShowsAtEveryLevel is task 124.20 decision 4. Quiet
// included: quiet is the level most likely to leave a reader believing the
// history on screen is the history the agent has, and `/clear` is a human act
// — which is what quiet acknowledges (task 124 decision 37).
func TestConversationResetShowsAtEveryLevel(t *testing.T) {
	for _, level := range []outputLevel{levelQuiet, levelCompact, levelNormal, levelVerbose} {
		lines := plainLines(outputLines(
			[]apiclient.TranscriptRecord{resetRecord}, level, 80, lineOpts{expandKey: "v"}))
		got := strings.Join(lines, "\n")
		if !strings.Contains(got, "conversation reset") {
			t.Errorf("level %d = %q, want the reset marked", level, got)
		}
		// Words, not a glyph or a colour, so the line reads under NO_COLOR —
		// the rule skillSegs follows.
		if !strings.Contains(got, "no longer sees the turns above") {
			t.Errorf("level %d = %q, want the mark to say what it means", level, got)
		}
		// One line, like a skill load, and never a full-width rule
		// (decision 2).
		if len(lines) != 1 {
			t.Errorf("level %d drew %d lines, want one: %q", level, len(lines), got)
		}
	}
}

// TestConversationResetIsNotUnrecognized: the record normalizes, so it is
// never the agent.raw a pane dims into its "unrecognized line(s)" count.
func TestConversationResetIsNotUnrecognized(t *testing.T) {
	d := newTestDetail(t)
	d.width = 80
	d.records = []apiclient.TranscriptRecord{resetRecord}
	d.level.set(levelNormal)

	got := strings.Join(plainLines(d.outputLines()), "\n")
	if strings.Contains(got, "unrecognized") {
		t.Errorf("task output pane = %q, want the reset counted as a record", got)
	}
	if !strings.Contains(got, "conversation reset") {
		t.Errorf("task output pane = %q, want the reset marked", got)
	}
}
