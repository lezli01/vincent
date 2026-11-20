package store

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// draftEvents counts the chat.draft_changed events the store's hook is handed
// from now on, which is what a subscriber would see.
func draftEvents(s *Store) func() int {
	var mu sync.Mutex
	n := 0
	s.SetEventHook(func(e *Event) {
		if e.Type == EventChatDraft {
			mu.Lock()
			n++
			mu.Unlock()
		}
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// hunkMarks is a chat's marks as "path#hash", in the listing's order.
func hunkMarks(t *testing.T, s *Store, chatID int64) []string {
	t.Helper()
	rs, err := s.ListChatHunkReviews(t.Context(), chatID)
	if err != nil {
		t.Fatalf("ListChatHunkReviews: %v", err)
	}
	out := make([]string, len(rs))
	for i, r := range rs {
		if r.ChatID != chatID || r.AcceptedAt.IsZero() {
			t.Fatalf("mark %d = %+v", i, r)
		}
		out[i] = r.Path + "#" + r.HunkHash
	}
	return out
}

// TestChatHunkReviewRoundTrip: accept, list in (path, hash) order, unaccept;
// both writes idempotent, a second accept keeping the first accepted_at.
func TestChatHunkReviewRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	c := testChat(t, s, testProject(t, s, "p1").ID)
	for _, m := range [][2]string{{"b/z.go", "h1"}, {"a.go", "h2"}, {"a.go", "h1"}} {
		if err := s.AcceptChatHunk(ctx, c.ID, m[0], m[1]); err != nil {
			t.Fatalf("AcceptChatHunk(%v): %v", m, err)
		}
	}
	want := "a.go#h1 a.go#h2 b/z.go#h1"
	if got := strings.Join(hunkMarks(t, s, c.ID), " "); got != want {
		t.Fatalf("marks = %q, want %q", got, want)
	}
	first, err := s.ListChatHunkReviews(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptChatHunk(ctx, c.ID, "a.go", "h1"); err != nil {
		t.Fatalf("second accept: %v", err)
	}
	again, err := s.ListChatHunkReviews(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 3 || !again[0].AcceptedAt.Equal(first[0].AcceptedAt) {
		t.Fatalf("second accept: %d marks, accepted_at %v then %v", len(again), first[0].AcceptedAt, again[0].AcceptedAt)
	}
	for range 2 { // the second removes a mark that is not there
		if err := s.UnacceptChatHunk(ctx, c.ID, "a.go", "h1"); err != nil {
			t.Fatalf("UnacceptChatHunk: %v", err)
		}
	}
	if got := strings.Join(hunkMarks(t, s, c.ID), " "); got != "a.go#h2 b/z.go#h1" {
		t.Fatalf("marks after unaccept = %q", got)
	}
}

// TestChatHunkReviewUnknownChat: a mark on no chat is ErrNotFound, as is a
// reject; removing and listing one are not errors.
func TestChatHunkReviewUnknownChat(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	if err := s.AcceptChatHunk(ctx, 99, "a.go", "h"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AcceptChatHunk = %v, want ErrNotFound", err)
	}
	called := false
	_, added, err := s.RecordChatHunkReject(ctx, 99, "a.go", "h", func(string) (string, bool) {
		called = true
		return "note", true
	})
	if !errors.Is(err, ErrNotFound) || added || called {
		t.Fatalf("RecordChatHunkReject = added %v, edit called %v, %v; want ErrNotFound", added, called, err)
	}
	if err := s.UnacceptChatHunk(ctx, 99, "a.go", "h"); err != nil {
		t.Fatalf("UnacceptChatHunk: %v", err)
	}
	if got := hunkMarks(t, s, 99); len(got) != 0 {
		t.Fatalf("marks = %v", got)
	}
}

// TestChatHunkReviewsSurviveReopen: the marks are the daemon's, not a
// client's (task 168 decision 2), so a restart keeps them.
func TestChatHunkReviewsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "with space", "vincent.db")
	seedTemplate(t, path)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := testChat(t, s, testProject(t, s, "p1").ID)
	if err := s.AcceptChatHunk(t.Context(), c.ID, "a.go", "h1"); err != nil {
		t.Fatalf("AcceptChatHunk: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s, err = Open(path); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := strings.Join(hunkMarks(t, s, c.ID), " "); got != "a.go#h1" {
		t.Fatalf("marks after reopen = %q", got)
	}
}

// TestChatHunkReviewsArePerChatAndGoWithIt: two chats never see each other's
// marks, and deleting a chat's row removes its marks and only its (task 168
// decision 9).
func TestChatHunkReviewsArePerChatAndGoWithIt(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	a, b := testChat(t, s, p.ID), testChat(t, s, p.ID)
	for _, id := range []int64{a.ID, b.ID} {
		if err := s.AcceptChatHunk(ctx, id, "shared.go", "same"); err != nil {
			t.Fatalf("AcceptChatHunk: %v", err)
		}
	}
	if err := s.AcceptChatHunk(ctx, a.ID, "only-a.go", "h"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnacceptChatHunk(ctx, b.ID, "only-a.go", "h"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(hunkMarks(t, s, a.ID), " "); got != "only-a.go#h shared.go#same" {
		t.Fatalf("a's marks = %q", got)
	}
	if got := strings.Join(hunkMarks(t, s, b.ID), " "); got != "shared.go#same" {
		t.Fatalf("b's marks = %q", got)
	}

	if _, err := s.SetChatState(ctx, a.ID, chatstate.Archived); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
	if err := s.DeleteChatCascade(ctx, a.ID); err != nil {
		t.Fatalf("DeleteChatCascade: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_hunk_reviews WHERE chat_id = ?`, a.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d marks left on the deleted chat", n)
	}
	if got := strings.Join(hunkMarks(t, s, b.ID), " "); got != "shared.go#same" {
		t.Fatalf("b's marks after a's delete = %q", got)
	}
}

// TestRecordChatHunkReject: the mark goes and the note lands in the draft
// together, announced by exactly one chat.draft_changed, with the stash and
// the hunk's neighbours untouched.
func TestRecordChatHunkReject(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	c := testChat(t, s, testProject(t, s, "p1").ID)
	if _, err := s.UpdateChatDraft(ctx, c.ID, ptr("typed so far"), ptr("set aside")); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"h1", "h2"} {
		if err := s.AcceptChatHunk(ctx, c.ID, "a.go", h); err != nil {
			t.Fatal(err)
		}
	}
	count := draftEvents(s)
	before, err := s.ListEvents(ctx, EventFilter{Types: []string{EventChatDraft}})
	if err != nil {
		t.Fatal(err)
	}

	got, added, err := s.RecordChatHunkReject(ctx, c.ID, "a.go", "h1", func(draft string) (string, bool) {
		return "rejected a.go\n\n" + draft, true
	})
	if err != nil || !added {
		t.Fatalf("RecordChatHunkReject = added %v, %v", added, err)
	}
	const want = "rejected a.go\n\ntyped so far"
	if got.Draft != want || got.Stash != "set aside" || got.DraftUpdatedAt == nil {
		t.Fatalf("returned chat = draft %q stash %q at %v", got.Draft, got.Stash, got.DraftUpdatedAt)
	}
	read, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Draft != want || read.Stash != "set aside" || !read.DraftUpdatedAt.Equal(*got.DraftUpdatedAt) {
		t.Fatalf("stored chat = draft %q stash %q at %v", read.Draft, read.Stash, read.DraftUpdatedAt)
	}
	if marks := strings.Join(hunkMarks(t, s, c.ID), " "); marks != "a.go#h2" {
		t.Fatalf("marks after reject = %q", marks)
	}
	if n := count(); n != 1 {
		t.Fatalf("%d chat.draft_changed published, want 1", n)
	}
	after, err := s.ListEvents(ctx, EventFilter{Types: []string{EventChatDraft}})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("%d chat.draft_changed recorded, want %d", len(after), len(before)+1)
	}
	if p := string(after[len(after)-1].Payload); strings.Contains(p, "rejected") || !strings.Contains(p, `"has_draft":true`) {
		t.Fatalf("event payload = %s", p)
	}

	// A hunk that was never accepted is rejected the same way.
	if _, added, err = s.RecordChatHunkReject(ctx, c.ID, "b.go", "nope", func(d string) (string, bool) {
		return "second\n" + d, true
	}); err != nil || !added {
		t.Fatalf("reject of an unmarked hunk = added %v, %v", added, err)
	}
	if n := count(); n != 2 {
		t.Fatalf("%d chat.draft_changed published, want 2", n)
	}
}

// TestRecordChatHunkRejectLeavesTheDraftAlone: a reject is never refused for
// the note's sake (task 168 decision 8). Whenever the note cannot be written
// the mark still goes, the draft and its stamp are byte-identical, nothing is
// published, and the call succeeds with noteAdded false.
func TestRecordChatHunkRejectLeavesTheDraftAlone(t *testing.T) {
	cases := []struct {
		name     string
		terminal bool
		edit     func(string) (string, bool)
	}{
		{"edit declines", false, func(d string) (string, bool) { return "ignored " + d, false }},
		{"over the cap", false, func(d string) (string, bool) {
			return strings.Repeat("x", MaxChatDraftBytes+1-len(d)) + d, true
		}},
		{"unchanged", false, func(d string) (string, bool) { return d, true }},
		{"terminal chat", true, func(d string) (string, bool) { return "late " + d, true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			ctx := t.Context()
			c := testChat(t, s, testProject(t, s, "p1").ID)
			if _, err := s.SetChatDraft(ctx, c.ID, "typed so far"); err != nil {
				t.Fatal(err)
			}
			if tc.terminal {
				// Archiving clears the draft; what must hold is that the
				// reject does not write one back.
				if _, err := s.SetChatState(ctx, c.ID, chatstate.Archived); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.AcceptChatHunk(ctx, c.ID, "a.go", "h1"); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetChat(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			evs, err := s.ListEvents(ctx, EventFilter{Types: []string{EventChatDraft}})
			if err != nil {
				t.Fatal(err)
			}
			count := draftEvents(s)

			got, added, err := s.RecordChatHunkReject(ctx, c.ID, "a.go", "h1", tc.edit)
			if err != nil || added {
				t.Fatalf("RecordChatHunkReject = added %v, %v; want false, nil", added, err)
			}
			after, err := s.GetChat(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, ch := range []*Chat{got, after} {
				if ch.Draft != before.Draft || ch.Stash != before.Stash {
					t.Fatalf("draft %q stash %q, want %q %q", ch.Draft, ch.Stash, before.Draft, before.Stash)
				}
				if (ch.DraftUpdatedAt == nil) != (before.DraftUpdatedAt == nil) ||
					(ch.DraftUpdatedAt != nil && !ch.DraftUpdatedAt.Equal(*before.DraftUpdatedAt)) {
					t.Fatalf("draft_updated_at moved from %v to %v", before.DraftUpdatedAt, ch.DraftUpdatedAt)
				}
			}
			if marks := hunkMarks(t, s, c.ID); len(marks) != 0 {
				t.Fatalf("the mark survived the reject: %v", marks)
			}
			if n := count(); n != 0 {
				t.Fatalf("%d chat.draft_changed published, want 0", n)
			}
			if now, err := s.ListEvents(ctx, EventFilter{Types: []string{EventChatDraft}}); err != nil || len(now) != len(evs) {
				t.Fatalf("%d chat.draft_changed recorded, want %d (%v)", len(now), len(evs), err)
			}
		})
	}
}
