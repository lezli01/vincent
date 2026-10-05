package tui

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/store"
)

// The issue thread on the detail (task 130 decision 24, 130.16) against the
// **real** API handlers over httptest: the thread renders oldest first, `W`
// writes a local comment through a stubbed $EDITOR, an empty buffer posts
// nothing, a mirrored issue withholds the key, a mirrored edit re-reads on
// issue.comment_updated, and a 409 that races the key surfaces as an error.
func TestIssueThreadAgainstTheRealAPI(t *testing.T) {
	h := newNewTaskLiveHarness(t)
	ctx := context.Background()

	local, err := h.st.CreateIssue(ctx, store.NewIssue{
		ProjectID: h.projectID, Title: "Crash on start", Body: "It crashes.", Author: "human",
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if _, err := h.st.AddIssueComment(ctx, local.ID, "alice", "first **take**", "", issuestate.Human); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	imported, _, err := h.st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: h.projectID, Provider: "github", RemoteKey: "I_41", Repo: "octo/web", Number: 41,
		URL: "https://github.com/octo/web/issues/41", RemoteJSON: `{"state":"open"}`,
		Title: "Dark mode", Author: "octocat", State: issuestate.Open,
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	posted := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	remote := store.RemoteIssueComment{RemoteKey: "9001", Author: "octocat", Body: "from github", CreatedAt: posted, UpdatedAt: posted}
	if _, err := h.st.UpsertRemoteIssueComment(ctx, imported.ID, remote); err != nil {
		t.Fatalf("UpsertRemoteIssueComment: %v", err)
	}

	detail := issueDetailView(t, h)
	h.send(openIssueMsg{id: local.ID, projectID: local.ProjectID})
	h.p.until(10*time.Second, "the local issue's thread", func() bool {
		return h.m.active == viewIssue && detail.loaded && detail.id == local.ID && len(detail.comments) == 1
	})
	out := ansi.Strip(detail.render(120, 60))
	for _, want := range []string{"Comments", "1 comment", "alice", "first take"} {
		if !strings.Contains(out, want) {
			t.Errorf("the thread is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "· github") {
		t.Errorf("a local comment is marked as GitHub's:\n%s", out)
	}
	if !hasCommentRow(detail.liveBindings(bindingsFor(ctxIssue))) {
		t.Fatal("W is withheld on a local issue")
	}

	// An empty buffer is the way out: nothing is posted.
	detail.w.exec = fakeExec(t, func(path string) error { return os.WriteFile(path, []byte("  \n"), 0o600) }, nil)
	h.sendKey(keyPress("W"))
	h.p.until(5*time.Second, "the empty draft's note", func() bool { return strings.Contains(detail.note, "nothing posted") })
	if list, err := h.st.ListIssueComments(ctx, local.ID); err != nil || len(list) != 1 {
		t.Fatalf("an empty draft wrote a comment: %d comments, %v", len(list), err)
	}

	// A saved draft is posted and the thread re-reads, oldest first.
	detail.w.exec = fakeExec(t, func(path string) error { return os.WriteFile(path, []byte("looks **good**\n"), 0o600) }, nil)
	h.sendKey(keyPress("W"))
	h.p.until(10*time.Second, "the new comment in the thread", func() bool {
		return len(detail.comments) == 2 && detail.comments[1].Body == "looks **good**"
	})
	if detail.noteBad || !strings.Contains(detail.note, "commented on issue") {
		t.Errorf("note = %q (bad %v), want the comment confirmed", detail.note, detail.noteBad)
	}
	if c := detail.comments[1]; c.Remote || c.Author == "" {
		t.Errorf("the posted comment is %+v, want a local one with a daemon-derived author", c)
	}
	out = ansi.Strip(detail.render(120, 60))
	if first, second := strings.Index(out, "first take"), strings.Index(out, "looks good"); first < 0 || second < first {
		t.Errorf("the thread is not oldest first:\n%s", out)
	}

	// The imported issue's remote is live: its comment is marked, and W is
	// withheld and refused.
	h.send(openIssueMsg{id: imported.ID, projectID: imported.ProjectID})
	h.p.until(10*time.Second, "the imported issue's thread", func() bool {
		return detail.loaded && detail.id == imported.ID && len(detail.comments) == 1
	})
	out = ansi.Strip(detail.render(120, 60))
	for _, want := range []string{"octocat", "from github", "· github"} {
		if !strings.Contains(out, want) {
			t.Errorf("the mirrored thread is missing %q:\n%s", want, out)
		}
	}
	if hasCommentRow(detail.liveBindings(bindingsFor(ctxIssue))) {
		t.Error("W is offered on an issue mirrored from GitHub")
	}
	if strings.Contains(out, opKey(keymap.Comment)+" writes one") {
		t.Errorf("the mirrored thread advertises W:\n%s", out)
	}
	detail.w.exec = fakeExec(t, func(string) error {
		t.Error("W opened $EDITOR on a mirrored issue")
		return nil
	}, nil)
	h.sendKey(keyPress("W"))
	if !detail.noteBad || !strings.Contains(detail.note, "mirrored") {
		t.Errorf("note = %q, want the mirrored refusal", detail.note)
	}

	// A mirrored edit updates in place and re-reads on issue.comment_updated.
	remote.Body, remote.UpdatedAt = "edited on github", posted.Add(time.Hour)
	if _, err := h.st.UpsertRemoteIssueComment(ctx, imported.ID, remote); err != nil {
		t.Fatalf("UpsertRemoteIssueComment: %v", err)
	}
	h.p.until(10*time.Second, "the mirrored edit", func() bool {
		return len(detail.comments) == 1 && detail.comments[0].Body == "edited on github"
	})
	if out = ansi.Strip(detail.render(120, 60)); !strings.Contains(out, "· edited") {
		t.Errorf("the edited comment is not marked:\n%s", out)
	}

	// A 409 that races the key — the view still thinking the thread local
	// — surfaces as an error line, and nothing is written.
	detail.issue.Commentable = true
	detail.w.exec = fakeExec(t, func(path string) error { return os.WriteFile(path, []byte("too late"), 0o600) }, nil)
	h.sendKey(keyPress("W"))
	h.p.until(10*time.Second, "the refused comment's error", func() bool {
		return !detail.posting && detail.noteBad && strings.Contains(detail.note, "comment on issue")
	})
	if list, err := h.st.ListIssueComments(ctx, imported.ID); err != nil || len(list) != 1 {
		t.Fatalf("a refused comment was written: %d comments, %v", len(list), err)
	}

	// A moved remote keeps the body sync's but gives the thread back
	// (decision 24.3): W is offered, advertised, and the comment posts.
	moved, _, err := h.st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: h.projectID, Provider: "github", RemoteKey: "I_42", Repo: "octo/web", Number: 42,
		URL: "https://github.com/octo/web/issues/42", RemoteJSON: `{"state":"open"}`,
		Title: "Transferred", Author: "octocat", State: issuestate.Open,
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	if err := h.st.SetIssueRemoteStatus(ctx, h.projectID, "github", "I_42", store.RemoteStatusMoved, "octo/elsewhere#1"); err != nil {
		t.Fatalf("SetIssueRemoteStatus: %v", err)
	}
	h.send(openIssueMsg{id: moved.ID, projectID: moved.ProjectID})
	h.p.until(10*time.Second, "the moved issue's detail", func() bool {
		return detail.loaded && detail.id == moved.ID && detail.issue.Source != nil
	})
	if slices.Contains(detail.issue.Editable, "body") {
		t.Fatalf("a moved issue's body is editable: %v", detail.issue.Editable)
	}
	if !hasCommentRow(detail.liveBindings(bindingsFor(ctxIssue))) {
		t.Fatal("W is withheld on an issue whose remote moved")
	}
	if out = ansi.Strip(detail.render(120, 60)); !strings.Contains(out, opKey(keymap.Comment)+" writes one") {
		t.Errorf("the moved issue's empty thread does not offer W:\n%s", out)
	}
	detail.w.exec = fakeExec(t, func(path string) error { return os.WriteFile(path, []byte("still ours"), 0o600) }, nil)
	h.sendKey(keyPress("W"))
	h.p.until(10*time.Second, "the moved issue's comment", func() bool {
		return len(detail.comments) == 1 && detail.comments[0].Body == "still ours"
	})
	if detail.noteBad {
		t.Errorf("note = %q, want the comment confirmed", detail.note)
	}
}

func hasCommentRow(rows []binding) bool {
	for _, b := range rows {
		if b.context == ctxIssue && b.op == keymap.Comment {
			return true
		}
	}
	return false
}
