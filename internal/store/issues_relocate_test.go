package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
)

// TestRelocateIssueRemote (task 142.12): a moved Jira issue's row follows
// it — repo, number, url and key rewritten, marked moved with moved_to — in
// one write announcing one issue.updated; a repeat announces nothing; a
// later listing at the binding refreshes it back to live.
func TestRelocateIssueRemote(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	in := jiraRemoteIn(p.ID, "10001", "acme.atlassian.net/ENG", 7)
	iss := mustUpsertRemote(t, s, in)
	updates := func() []Event { return issueEvents(t, s, EventIssueUpdated) }

	to := in
	to.Repo, to.Number, to.URL = "acme.atlassian.net/OPS", 3, "https://acme.atlassian.net/browse/OPS-3"
	if err := s.RelocateIssueRemote(ctx, p.ID, "jira", "nope", to, to.URL); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key = %v, want ErrNotFound", err)
	}
	if err := s.RelocateIssueRemote(ctx, p.ID, "jira", "10001", to, to.URL); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIssue(ctx, iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := got.Remote
	if r.Status != RemoteStatusMoved || r.Repo != to.Repo || r.Number != 3 || r.URL != to.URL || r.RemoteKey != "10001" {
		t.Errorf("relocated remote = %+v", r)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(r.RemoteJSON), &raw); err != nil || raw["moved_to"] != to.URL || raw["n"] != float64(1) {
		t.Errorf("remote_json = %s (%v)", r.RemoteJSON, err)
	}
	if got.State != issuestate.Open || got.Title != in.Title {
		t.Errorf("relocation touched the issue: %+v", got)
	}
	evs := updates()
	if len(evs) != 1 {
		t.Fatalf("issue.updated = %d, want 1", len(evs))
	}
	if m := payloadOf(t, evs[0]); !reflect.DeepEqual(m["changed"], []any{"remote", "remote_status"}) || m["by"] != "sync" {
		t.Errorf("payload = %v", m)
	}
	if err := s.RelocateIssueRemote(ctx, p.ID, "jira", "10001", to, to.URL); err != nil || len(updates()) != 1 {
		t.Errorf("repeat = %v, %d events", err, len(updates()))
	}

	// A new key is taken when free, and refused when another row holds it.
	mustUpsertRemote(t, s, jiraRemoteIn(p.ID, "10002", "acme.atlassian.net/ENG", 8))
	rekey := to
	rekey.RemoteKey = "10002"
	if err := s.RelocateIssueRemote(ctx, p.ID, "jira", "10001", rekey, to.URL); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetIssue(ctx, iss.ID); got.Remote.RemoteKey != "10001" {
		t.Errorf("relocation took a held key: %+v", got.Remote)
	}
	rekey.RemoteKey = "20001"
	if err := s.RelocateIssueRemote(ctx, p.ID, "jira", "10001", rekey, to.URL); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetIssue(ctx, iss.ID); got.Remote.RemoteKey != "20001" {
		t.Errorf("relocation kept the old key: %+v", got.Remote)
	}

	// Back at the binding: a refresh under the key makes it live again.
	back := in
	back.RemoteKey = "20001"
	again := mustUpsertRemote(t, s, back)
	if again.Remote.Status != RemoteStatusLive || again.Remote.Repo != in.Repo || again.Remote.Number != 7 {
		t.Errorf("after return = %+v", again.Remote)
	}

	// A tombstone is never touched.
	if err := s.DeleteIssue(ctx, iss.ID, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	before := len(issueEvents(t, s, allIssueEventTypes()...))
	if err := s.RelocateIssueRemote(ctx, p.ID, "jira", "20001", to, to.URL); err != nil {
		t.Errorf("tombstone = %v", err)
	}
	if after := len(issueEvents(t, s, allIssueEventTypes()...)); after != before {
		t.Errorf("tombstone write appended %d events", after-before)
	}
}
