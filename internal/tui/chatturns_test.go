package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// finishedChat is a chat with n finished turns, newest last, each with result
// text so a turn whose transcript never arrives still renders (§17).
func finishedChat(t *testing.T, n int) *chatView {
	t.Helper()
	v := chatViewFixture()
	v.client = offlineClient()
	for i := 1; i <= n; i++ {
		v.turns = append(v.turns, apiclient.ChatTurn{
			ID: int64(i), Seq: i, State: "done",
			Prompt: "ask " + strings.Repeat("x", i), ResultText: "answer " + strings.Repeat("y", i),
		})
	}
	return v
}

// TestChatFetchesTheNewestFinishedTurnsEagerly is decision 6's first half:
// §15 view 9 has always specified that finished turns render from their
// transcript records, and the client only ever fetched the running turn.
func TestChatFetchesTheNewestFinishedTurnsEagerly(t *testing.T) {
	v := finishedChat(t, 9)
	v.fetchTranscripts()
	if len(v.fetched) != eagerTurns {
		t.Fatalf("opening the chat asked for %d transcripts, want the %d newest",
			len(v.fetched), eagerTurns)
	}
	for seq := 5; seq <= 9; seq++ {
		if !v.fetched[seq] {
			t.Errorf("turn %d is among the newest %d and was not fetched", seq, eagerTurns)
		}
	}
	for seq := 1; seq <= 4; seq++ {
		if v.fetched[seq] {
			t.Errorf("turn %d was fetched eagerly; older turns wait to be scrolled to", seq)
		}
	}
}

// TestChatFetchesOlderTurnsWhenScrolledTo is the lazy half: reading back
// through a long conversation fills it in as it is read, and asking twice for
// the same turn does not happen.
func TestChatFetchesOlderTurnsWhenScrolledTo(t *testing.T) {
	v := finishedChat(t, 9)
	v.fetchTranscripts()
	v.bodyDirty = true
	v.render(60, 14)
	// Paged to the top, not paged a fixed number of times: how many pages a
	// conversation is depends on how many rows the body has, and the body is
	// whatever the header and the framed composer left it.
	for range 20 {
		before := v.vp.YOffset()
		v.updateKey(registryKey(t, "pgup"))
		if v.vp.YOffset() == before {
			break
		}
	}
	if !v.fetched[1] {
		t.Fatalf("scrolling to the top fetched %v, never turn 1", v.fetched)
	}
	before := len(v.fetched)
	if cmd := v.fetchVisibleTranscripts(); cmd != nil {
		t.Fatal("a second look at the same turns asked for them again")
	}
	if len(v.fetched) != before {
		t.Fatalf("the fetch set grew from %d to %d with no new turns on screen", before, len(v.fetched))
	}
}

// TestChatFallsBackToResultText holds §17: a transcript that has gone to
// retention leaves the turn's answer, and it is shown with no banner.
func TestChatFallsBackToResultText(t *testing.T) {
	v := finishedChat(t, 2)
	// The fetch came back empty, which is what a pruned transcript looks like.
	v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: 2, records: nil})
	body := strings.Join(plainLines(v.bodyLines(60)), "\n")
	if !strings.Contains(body, "answer yy") {
		t.Fatalf("a turn with no transcript lost its answer:\n%s", body)
	}
	for _, banner := range []string{"unavailable", "pruned", "missing"} {
		if strings.Contains(body, banner) {
			t.Errorf("a pruned transcript grew a %q banner:\n%s", banner, body)
		}
	}
}

// TestChatRecordCapEmitsTheTruncationMarker bounds a long conversation the way
// the output pane bounds a step (§18): the oldest records go, and the body
// says so once.
func TestChatRecordCapEmitsTheTruncationMarker(t *testing.T) {
	v := finishedChat(t, 2)
	big := make([]apiclient.TranscriptRecord, maxRecords)
	for i := range big {
		big[i] = apiclient.TranscriptRecord{Type: "agent.output", Text: "line"}
	}
	v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: 1, records: big})
	v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: 2, records: big[:10]})
	total := 0
	for _, recs := range v.turnRecords {
		total += len(recs)
	}
	if total > maxRecords {
		t.Fatalf("the conversation holds %d records, past the %d cap", total, maxRecords)
	}
	if !v.truncated {
		t.Fatal("records were dropped and the view does not know it")
	}
	if body := strings.Join(plainLines(v.bodyLines(60)), "\n"); !strings.Contains(body, "earlier output truncated") {
		t.Fatalf("the body did not say output was dropped:\n%s", body[:min(len(body), 300)])
	}
}
