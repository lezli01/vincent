package issues

import (
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestWritesBackAsksTheRegistry: a live remote writes back for every provider
// internal/issueprovider declares as writing back, not only GitHub (task
// 142.5); a moved or missing remote and an unknown provider stay local.
func TestWritesBackAsksTheRegistry(t *testing.T) {
	id := int64(1)
	for _, tc := range []struct {
		provider, status string
		want             bool
	}{
		{"github", store.RemoteStatusLive, true},
		{"jira", store.RemoteStatusLive, true},
		{"jira", store.RemoteStatusMoved, false},
		{"jira", store.RemoteStatusMissing, false},
		{"gitlab", store.RemoteStatusLive, false},
	} {
		iss := &store.Issue{ID: id, Remote: &store.IssueRemote{IssueID: &id, Provider: tc.provider, Status: tc.status}}
		if got := WritesBack(iss); got != tc.want {
			t.Errorf("WritesBack(%s, %q) = %v, want %v", tc.provider, tc.status, got, tc.want)
		}
	}
	if WritesBack(&store.Issue{ID: id}) {
		t.Error("a local issue writes back")
	}
}

// TestJiraIssueStateIsGuarded: an ungranted agent may neither close nor
// reopen a live Jira import; a human may, and the close enqueues set_state.
func TestJiraIssueStateIsGuarded(t *testing.T) {
	f := newGuardFixtureFrom(t, "jira")
	ctx := t.Context()

	if _, err := f.svc.Close(ctx, issuestate.Agent, f.other.ID, "", nil); !errors.Is(err, ErrForgeWriteNeedsHuman) {
		t.Fatalf("agent close = %v, want ErrForgeWriteNeedsHuman", err)
	}
	if _, err := f.svc.Close(ctx, issuestate.Human, f.other.ID, "", nil); err != nil {
		t.Fatalf("human close: %v", err)
	}
	writes, err := f.st.IssueWrites(ctx, f.other.ID)
	if err != nil || len(writes) != 1 || writes[0].Op != store.OutboxOpSetState ||
		writes[0].Desired != store.RemoteStateOf(issuestate.Closed, issuestate.Completed) {
		t.Fatalf("writes after the human close = %+v, %v; want one closed set_state", writes, err)
	}
	if _, err := f.svc.Reopen(ctx, issuestate.Agent, f.other.ID); !errors.Is(err, ErrForgeWriteNeedsHuman) {
		t.Errorf("agent reopen = %v, want ErrForgeWriteNeedsHuman", err)
	}
}

// TestGrantedJiraIssueWrites: a granted handoff's close and comment on a
// Jira import enqueue set_state and add_comment exactly as for GitHub.
func TestGrantedJiraIssueWrites(t *testing.T) {
	f := newGuardFixtureFrom(t, "jira", "issue.close", "issue.comment")
	ctx := t.Context()
	agent := Caller{By: issuestate.Agent, TaskID: ptr(f.handoff.TaskID)}

	by, wctx, err := f.svc.IssueWriteAs(ctx, agent, store.CapIssueClose, f.claimed.ID)
	if err != nil || by != issuestate.Handoff {
		t.Fatalf("IssueWriteAs close = %s, %v; want handoff", by, err)
	}
	if _, err := f.svc.Close(wctx, by, f.claimed.ID, "", nil); err != nil {
		t.Fatalf("granted close: %v", err)
	}
	by, wctx, err = f.svc.IssueWriteAs(ctx, agent, store.CapIssueComment, f.claimed.ID)
	if err != nil || by != issuestate.Handoff {
		t.Fatalf("IssueWriteAs comment = %s, %v; want handoff", by, err)
	}
	if _, err := f.svc.Comment(wctx, by, f.claimed.ID, "task 1", "landed"); err != nil {
		t.Fatalf("granted comment: %v", err)
	}
	writes, err := f.st.IssueWrites(ctx, f.claimed.ID)
	if err != nil || len(writes) != 2 || writes[0].Op != store.OutboxOpSetState || writes[1].Op != store.OutboxOpAddComment {
		t.Fatalf("writes = %+v, %v; want set_state then add_comment", writes, err)
	}
	if _, _, err := f.svc.IssueWriteAs(ctx, Caller{By: issuestate.Agent}, store.CapIssueClose, f.claimed.ID); !errors.Is(err, ErrForgeWriteNeedsHuman) {
		t.Errorf("ungranted close = %v, want ErrForgeWriteNeedsHuman", err)
	}
}
