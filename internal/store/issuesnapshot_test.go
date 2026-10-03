package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
)

// TestNewIssueSnapshotOfALocalIssue (task 130 decision 5): a local issue
// freezes its own fields and carries no Remote.
func TestNewIssueSnapshotOfALocalIssue(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	iss, err := s.CreateIssue(t.Context(), NewIssue{
		ProjectID: p.ID, Title: "Lock file leaks", Body: "Seen twice.", Kind: "bug",
		Author: "lezli01", Priority: 2, Labels: []string{"daemon", "bug"},
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	got := NewIssueSnapshot(iss, now)
	want := &IssueSnapshot{
		ID: iss.ID, Title: "Lock file leaks", Body: "Seen twice.", State: "open",
		Kind: "bug", Priority: 2, Labels: iss.Labels, Author: "lezli01",
		CreatedAt: iss.CreatedAt.UTC(), CapturedAt: now,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot =\n %+v\nwant\n %+v", got, want)
	}
	if NewIssueSnapshot(nil, now) != nil {
		t.Error("snapshot of nil is not nil")
	}
}

// TestNewIssueSnapshotOfAnImportedIssue: Remote comes from the issue_remotes
// row, enriched from remote_json where it carries GitHub's own spelling, and
// a tombstoned row contributes nothing.
func TestNewIssueSnapshotOfAnImportedIssue(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	iss, _, err := s.UpsertRemoteIssue(t.Context(), RemoteIssue{
		ProjectID: p.ID, Provider: "github", RemoteKey: "I_kw1", Repo: "o/r", Number: 7,
		URL: "https://github.com/o/r/issues/7",
		RemoteJSON: `{"repo":"o/r","number":7,"state":"open","assignee":"hubot",` +
			`"assignees":["hubot","octo"],"milestone":"v1","milestone_number":3,` +
			`"created_at":"2025-01-02T03:04:05Z"}`,
		Title: "#7 remote", Body: "rb", Author: "octo", State: issuestate.Open, Labels: []string{"bug"},
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	now := time.Now()
	got := NewIssueSnapshot(iss, now)
	wantRemote := &IssueSnapshotRemote{
		Provider: "github", Repo: "o/r", Number: 7, URL: "https://github.com/o/r/issues/7",
		State: "open", Assignees: []string{"hubot", "octo"}, Milestone: "v1", MilestoneNumber: 3,
	}
	if !reflect.DeepEqual(got.Remote, wantRemote) {
		t.Errorf("Remote = %+v, want %+v", got.Remote, wantRemote)
	}
	if got.ID != iss.ID || got.URL != wantRemote.URL || !got.CapturedAt.Equal(now.UTC()) {
		t.Errorf("snapshot = %+v", got)
	}
	// The remote's creation time wins over the vincent row's (130.14).
	if want := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC); !got.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want the remote's %v", got.CreatedAt, want)
	}

	// A record with no detail still yields the column reference.
	bare := *iss
	bareRemote := *iss.Remote
	bareRemote.RemoteJSON = "not json"
	bare.Remote = &bareRemote
	if r := NewIssueSnapshot(&bare, now).Remote; r == nil || r.Number != 7 || r.State != "open" || r.Assignees != nil {
		t.Errorf("Remote from columns alone = %+v", r)
	}
	if c := NewIssueSnapshot(&bare, now).CreatedAt; !c.Equal(iss.CreatedAt) {
		t.Errorf("CreatedAt without a remote time = %v, want the row's %v", c, iss.CreatedAt)
	}

	tomb := *iss
	tombRemote := *iss.Remote
	tombRemote.IssueID = nil
	tomb.Remote = &tombRemote
	if r := NewIssueSnapshot(&tomb, now).Remote; r != nil {
		t.Errorf("tombstone gave Remote %+v, want nil", r)
	}
}

// TestIssueSnapshotJSONIsBackwardCompatible: a row #660 wrote decodes into
// the widened type unchanged, and the widened type round-trips.
func TestIssueSnapshotJSONIsBackwardCompatible(t *testing.T) {
	old, err := unmarshalIssueSnapshot(`{"id":3,"title":"t","state":"open","labels":["a"]}`, true)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := (&IssueSnapshot{ID: 3, Title: "t", State: "open", Labels: []string{"a"}}); !reflect.DeepEqual(old, want) {
		t.Errorf("old row = %+v, want %+v", old, want)
	}
	wide := &IssueSnapshot{
		ID: 4, Title: "t", Body: "b", State: "closed", CloseReason: "completed", Author: "a",
		Remote:     &IssueSnapshotRemote{Provider: "github", Repo: "o/r", Number: 9, Assignees: []string{"x"}},
		CapturedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	}
	raw, err := marshalIssueSnapshot(wide)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := unmarshalIssueSnapshot(raw.(string), true)
	if err != nil || !reflect.DeepEqual(back, wide) {
		t.Errorf("round trip = %+v, %v; want %+v", back, err, wide)
	}
	var probe map[string]any
	_ = json.Unmarshal([]byte(raw.(string)), &probe)
	if _, ok := probe["remote"]; !ok {
		t.Errorf("encoded snapshot lacks remote: %s", raw)
	}
}

func TestIssueSnapshotCloneCopiesRemote(t *testing.T) {
	orig := &IssueSnapshot{ID: 1, Remote: &IssueSnapshotRemote{Provider: "github", Assignees: []string{"a"}}}
	c := orig.Clone()
	if c.Remote == orig.Remote || !reflect.DeepEqual(c, orig) {
		t.Fatalf("Clone = %+v, want an equal copy with its own Remote", c)
	}
	c.Remote.Assignees[0] = "mutated"
	if orig.Remote.Assignees[0] == "mutated" {
		t.Error("Clone shares the Assignees backing array")
	}
}
