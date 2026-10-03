package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// The discussion thread's routes (task 130 decision 24, 130.16).

func decodeComment(t *testing.T, resp *http.Response, out []byte, want int) issueCommentBody {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, want, out)
	}
	var c issueCommentBody
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatalf("decode comment: %v: %s", err, out)
	}
	return c
}

func (h *issueHarness) comments(t *testing.T, id int64) []issueCommentBody {
	t.Helper()
	resp, out := h.do(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d/comments", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list comments = %d: %s", resp.StatusCode, out)
	}
	var env struct {
		Comments []issueCommentBody `json:"comments"`
	}
	if err := json.Unmarshal(out, &env); err != nil || env.Comments == nil {
		t.Fatalf("decode comments: %v: %s", err, out)
	}
	return env.Comments
}

func TestIssueCommentsOnALocalIssue(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	iss := h.create(t, map[string]any{"title": "local"})
	if !iss.Commentable {
		t.Error("a local issue says it is not commentable")
	}
	path := fmt.Sprintf("/v1/issues/%d/comments", iss.ID)
	if got := h.comments(t, iss.ID); len(got) != 0 {
		t.Fatalf("a new issue's thread = %+v", got)
	}

	mark := h.maxEvent(t)
	var added []issueCommentBody
	for _, body := range []string{" first \n", "second"} {
		resp, out := h.do(t, http.MethodPost, path, map[string]any{"body": body})
		added = append(added, decodeComment(t, resp, out, http.StatusCreated))
	}
	first := added[0]
	if first.Body != "first" || first.Author != osUsername() || first.IssueID != iss.ID || first.Remote ||
		first.RemoteKey != "" || first.CreatedAt.IsZero() {
		t.Errorf("added = %+v", first)
	}
	got := h.comments(t, iss.ID)
	if len(got) != 2 || got[0].ID != added[0].ID || got[1].ID != added[1].ID {
		t.Errorf("thread = %+v, want both, oldest first", got)
	}
	evs := h.issueEvents(t, mark)
	if len(evs) != 2 || evs[0].Type != store.EventIssueCommentAdded ||
		!strings.Contains(string(evs[0].Payload), `"by":"human"`) ||
		strings.Contains(string(evs[0].Payload), "first") {
		t.Errorf("events = %v, want two comment_added by human, no text", evs)
	}

	for name, body := range map[string]any{
		"empty":      map[string]any{"body": ""},
		"whitespace": map[string]any{"body": " \n\t"},
		"too long":   map[string]any{"body": strings.Repeat("x", maxDescriptionBytes+1)},
		"author":     map[string]any{"body": "hi", "author": "someone else"},
	} {
		resp, out := h.do(t, http.MethodPost, path, body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(out), CodeValidationFailed) {
			t.Errorf("%s = %d: %s", name, resp.StatusCode, out)
		}
	}
	if n := len(h.comments(t, iss.ID)); n != 2 {
		t.Errorf("a refused comment was written: %d comments", n)
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		resp, out := h.do(t, method, "/v1/issues/9999/comments", map[string]any{"body": "hi"})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s unknown issue = %d: %s", method, resp.StatusCode, out)
		}
	}
}

// TestIssueCommentOnAMirroredIssueIsRefused: a live remote's thread is
// GitHub's, so a comment is the 409 a title edit gets, for a human and a
// marked agent alike, writing nothing and queueing nothing for GitHub. A
// moved or missing remote gives the thread back (decision 24.3).
func TestIssueCommentOnAMirroredIssueIsRefused(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	iss := h.imported(t, "I_1", 1)
	path := fmt.Sprintf("/v1/issues/%d/comments", iss.ID)
	if got := h.must(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/v1/issues/%d", iss.ID), nil); got.Commentable {
		t.Error("a live remote's issue says it is commentable")
	}
	mark := h.maxEvent(t)
	for _, hdr := range [][]string{nil, {headerTaskMarker, "12"}} {
		resp, out := h.do(t, http.MethodPost, path, map[string]any{"body": "hi"}, hdr...)
		if d := conflict(t, resp, out); detailString(t, d, "reason") != issueReasonMirrored {
			t.Errorf("comment with %v = %s", hdr, out)
		}
	}
	if evs := h.issueEvents(t, mark); len(evs) != 0 {
		t.Errorf("a refused comment emitted %v", eventTypes(evs))
	}
	if n := h.pendingWrites(t); n != 0 {
		t.Errorf("a refused comment queued %d GitHub writes", n)
	}
	if got := h.comments(t, iss.ID); len(got) != 0 {
		t.Errorf("thread = %+v", got)
	}

	for i, status := range []string{store.RemoteStatusMoved, store.RemoteStatusMissing} {
		key := fmt.Sprintf("I_%d", i+2)
		gone := h.imported(t, key, i+2)
		if err := h.st.SetIssueRemoteStatus(t.Context(), h.pid, "github", key, status, ""); err != nil {
			t.Fatal(err)
		}
		if got := h.must(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/v1/issues/%d", gone.ID), nil); !got.Commentable || slices.Contains(got.Editable, "body") {
			t.Errorf("a %s remote's issue: commentable %v, editable %v; want a local thread and a mirrored body", status, got.Commentable, got.Editable)
		}
		resp, out := h.do(t, http.MethodPost, fmt.Sprintf("/v1/issues/%d/comments", gone.ID), map[string]any{"body": "still here"})
		decodeComment(t, resp, out, http.StatusCreated)
	}
	if n := h.pendingWrites(t); n != 0 {
		t.Errorf("a local comment queued %d GitHub writes", n)
	}
}
