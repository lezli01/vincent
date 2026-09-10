package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// testArchivedBoard is the archived board with three archived rows loaded. It
// is the *same* model the live board is (decision 5), so every assertion here
// about grouping, folding and filtering is a fence around that sharing rather
// than a second copy of the live board's tests.
func testArchivedBoard() *board {
	b := newArchivedBoard()
	b.now = func() time.Time { return testNow }
	b.bell = func() {}
	b.loaded = true
	b.group, b.configGroup = nil, nil
	b.updateLoaded(boardLoadedMsg{tasks: []apiclient.Task{
		task(3, stateArchived), task(2, stateArchived), task(1, stateArchived),
	}})
	return b
}

func TestArchivedBoardAsksForArchivedRowsAndTheLiveBoardDoesNot(t *testing.T) {
	live := newBoard()
	if live.archived {
		t.Fatal("the live board is in archived mode")
	}
	arch := newArchivedBoard()
	arch.now = func() time.Time { return testNow }
	if !arch.archived {
		t.Fatal("newArchivedBoard did not set archived mode")
	}
	// The listing options are what the daemon actually sees, so they are what
	// this asserts: the live board's zero value excludes archived rows, and
	// the archived board's asks for exactly them, paged and windowed.
	opts := arch.listOptions()
	if opts.Archived != apiclient.ArchivedOnly {
		t.Fatalf("archived board asked for %q, want %q", opts.Archived, apiclient.ArchivedOnly)
	}
	if opts.Limit != archivedPageSize || opts.Offset != 0 {
		t.Fatalf("paging: limit %d offset %d, want %d and 0", opts.Limit, opts.Offset, archivedPageSize)
	}
	if got, want := opts.ArchivedSince, testNow.AddDate(0, 0, -7); !got.Equal(want) {
		t.Fatalf("opening window: archived_since %s, want %s", got, want)
	}
	if live.listOptions().Archived != apiclient.ArchivedExclude {
		t.Fatal("the live board asked for archived rows")
	}
}

func TestArchivedBoardDatePresetsTranslateToBounds(t *testing.T) {
	b := testArchivedBoard()
	want := []time.Time{
		testNow.AddDate(0, 0, -30), // after one press: 30 days
		{},                         // after two: all time, no bound at all
		testNow.AddDate(0, 0, -7),  // and back round to the opening window
	}
	for i, w := range want {
		b.updateKey(registryKey(t, "s"))
		got := b.listOptions().ArchivedSince
		if !got.Equal(w) {
			t.Fatalf("press %d: archived_since %v, want %v (window %q)", i+1, got, w, b.label())
		}
	}
}

func TestArchivedBoardPagesForwardOnlyWhileThePageWasFull(t *testing.T) {
	b := testArchivedBoard()
	// Three rows is a short page, which is the end of the listing: there is
	// no total to compare against, so a short page is the only signal.
	b.updateKey(registryKey(t, ">"))
	if b.page != 0 {
		t.Fatalf("a short page advanced to page %d", b.page+1)
	}
	full := make([]apiclient.Task, archivedPageSize)
	for i := range full {
		full[i] = task(int64(i+1), stateArchived)
	}
	b.updateLoaded(boardLoadedMsg{tasks: full})
	b.updateKey(registryKey(t, ">"))
	if b.page != 1 {
		t.Fatalf("a full page did not advance (page %d)", b.page+1)
	}
	if got, want := b.listOptions().Offset, archivedPageSize; got != want {
		t.Fatalf("offset %d, want %d", got, want)
	}
	b.updateKey(registryKey(t, "<"))
	if b.page != 0 {
		t.Fatalf("< did not go back (page %d)", b.page+1)
	}
	b.updateKey(registryKey(t, "<"))
	if b.page != 0 {
		t.Fatal("< walked off the front of the listing")
	}
}

func TestArchivedBoardCannotDeleteWithoutAnAnswer(t *testing.T) {
	b := testArchivedBoard()
	_, cmd := b.updateKey(registryKey(t, "D"))
	if b.delPrompt == nil {
		t.Fatal("D did not ask before deleting")
	}
	if cmd != nil {
		t.Fatal("D sent something before it was answered")
	}
	// A key that is not one of the three answers leaves the prompt up: a
	// permanent delete must not be cancelled *or* confirmed by a stray press.
	if _, cmd := b.updateKey(registryKey(t, "g")); cmd != nil {
		t.Fatal("a stray key confirmed the delete")
	}
	if b.delPrompt == nil {
		t.Fatal("a stray key dismissed the confirmation")
	}
	if _, cmd := b.updateKey(registryKey(t, "n")); cmd != nil {
		t.Fatal("n sent a delete")
	}
	if b.delPrompt != nil {
		t.Fatal("n left the confirmation up")
	}
}

func TestArchivedBoardConfirmationNamesTheConsequence(t *testing.T) {
	b := testArchivedBoard()
	b.updateKey(registryKey(t, "D"))
	q := b.delPrompt.question()
	for _, want := range []string{"permanently", "transcripts", "branch", "commits"} {
		if !strings.Contains(q, want) {
			t.Fatalf("the confirmation never says %q: %s", want, q)
		}
	}
}

func TestArchivedBoardBulkReportCountsRefusalsAndDeletions(t *testing.T) {
	b := testArchivedBoard()
	b.update(deleteResultMsg{
		total: 3,
		done:  []int64{1, 2},
		refused: []bulkFailure{{id: 3, err: &apiclient.Error{
			Status: 409, Code: "invalid_state", Message: "task 3 still has fan-out lanes (task 9)",
		}}},
		branches: 1,
	})
	if !strings.Contains(b.delNote, "2 of 3") {
		t.Fatalf("the report does not count the deletions: %s", b.delNote)
	}
	if !strings.Contains(b.delNote, "1 refused") {
		t.Fatalf("the report does not count the refusals: %s", b.delNote)
	}
	if !strings.Contains(b.delNote, "branch deleted") {
		t.Fatalf("the report does not say what happened to the branch: %s", b.delNote)
	}
	if !b.delNoteBad {
		t.Fatal("a report carrying a refusal is not marked as a problem")
	}
}

// TestArchivedBoardSharesTheLiveBoardsBehaviour is decision 5's fence: the
// archived board is the live board in a second mode, so grouping, folding and
// `/` must behave identically. A regression here means the mode has grown a
// branch it should not have.
func TestArchivedBoardSharesTheLiveBoardsBehaviour(t *testing.T) {
	b := testArchivedBoard()
	b.group, b.configGroup = defaultGrouping(), defaultGrouping()
	b.updateKey(registryKey(t, "g"))
	if b.group.equal(defaultGrouping()) {
		t.Fatal("g did not change the grouping on the archived board")
	}
	b.updateKey(registryKey(t, "/"))
	if !b.filtering {
		t.Fatal("/ did not open the filter on the archived board")
	}
	b.updateKey(registryKey(t, "esc"))
	b.updateKey(registryKey(t, "space"))
	if len(b.marks) != 1 {
		t.Fatalf("space marked %d rows, want 1", len(b.marks))
	}
}

func TestArchivedChatsBoardIsTerminalOnlyAndPaged(t *testing.T) {
	v := newArchivedChatsView()
	v.now = func() time.Time { return testNow }
	opts := v.listOptions()
	if opts.Archived != apiclient.ArchivedOnly {
		t.Fatalf("archived chats asked for %q, want %q", opts.Archived, apiclient.ArchivedOnly)
	}
	if opts.Limit != archivedPageSize {
		t.Fatalf("limit %d, want %d", opts.Limit, archivedPageSize)
	}
	if got, want := opts.ArchivedSince, testNow.AddDate(0, 0, -7); !got.Equal(want) {
		t.Fatalf("archived_since %s, want %s", got, want)
	}
	// The live chats board keeps its `s` cycle and its own default, untouched.
	live := newChatsView()
	if live.archived || live.listOptions().Archived != apiclient.ArchivedExclude {
		t.Fatal("the live chats board changed")
	}
}

func TestArchivedChatsBoardRefusesToAskAboutAHandedOffChat(t *testing.T) {
	v := newArchivedChatsView()
	v.now = func() time.Time { return testNow }
	v.applyLoaded(chatsLoadedMsg{
		chats: []apiclient.Chat{testChat(1, "handed_off", "given away")},
		names: map[int64]string{7: "repo"},
	})
	v.updateKey(registryKey(t, "D"))
	if v.delPrompt != nil {
		t.Fatal("the board asked about deleting a handed-off chat")
	}
	if !v.noteBad || !strings.Contains(v.note, "handed off") {
		t.Fatalf("the refusal does not name what is holding on: %q", v.note)
	}
}

// archivedChatsFixture is the chats board in archived mode with two archived
// rows loaded, and a client so `r` has something to reload with.
func archivedChatsFixture() *chatsView {
	v := newArchivedChatsView()
	v.now = func() time.Time { return testNow }
	v.client = &apiclient.Client{}
	v.applyLoaded(chatsLoadedMsg{
		chats: []apiclient.Chat{
			testChat(1, "archived", "first"),
			testChat(2, "archived", "second"),
		},
		names: map[int64]string{7: "repo"},
	})
	return v
}
