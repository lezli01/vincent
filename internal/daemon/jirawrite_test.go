package daemon

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issues"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/jira/fakejira"
	"github.com/lezli01/vincent/internal/store"
)

// The Jira write-back end to end (task 142.13, #909): the outbox's drain,
// through issuesource.Jira and the real jira.Client, against the fake Jira
// over the same TLS leg jirasync_test.go imports through.

// outbox is a drain wired with the Jira source the way daemon.go wires it,
// on the fixture's clock and with no pacing wait.
func (f *jiraE2E) outbox() *IssueOutbox {
	cfg := func() config.Config { return f.cfg }
	o := NewIssueOutbox(f.store, cfg, nil, slog.New(slog.NewTextHandler(f.logs, nil))).
		WithJiraSource(newJiraSource(cfg, f.environ, f.ts.Client()))
	// The store stamps a row due at the wall clock; the fixture's clock
	// may be behind it, or advanced past it.
	o.now = func() time.Time {
		if now := time.Now(); now.After(f.now()) {
			return now
		}
		return f.now()
	}
	o.sleep = func(context.Context, time.Duration) bool { return true }
	return o
}

// jiraIssue is the fake's copy of an issue.
func (f *jiraE2E) jiraIssue(id string) fakejira.Issue {
	f.t.Helper()
	c, err := f.fake.Load()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, is := range c.Issues {
		if is.ID == id {
			return is
		}
	}
	f.t.Fatalf("fake has no issue %s", id)
	return fakejira.Issue{}
}

func (f *jiraE2E) posts(id, what string) int {
	f.t.Helper()
	n, err := fakejira.CountRequests(f.fake.RequestLog, "POST", "rest/api/3/issue/"+id+"/"+what, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *jiraE2E) humanTransition(id string, action issuestate.Action, reason issuestate.Reason) {
	f.t.Helper()
	if _, err := f.store.TransitionIssue(f.t.Context(), f.issue(id).ID, action, reason, nil, issuestate.Human); err != nil {
		f.t.Fatal(err)
	}
}

func (f *jiraE2E) newestWrite(id string) *store.IssueOutbox {
	f.t.Helper()
	writes, err := f.store.IssueWrites(f.t.Context(), f.issue(id).ID)
	if err != nil || len(writes) == 0 {
		f.t.Fatalf("writes of %s = %+v, %v", id, writes, err)
	}
	return writes[len(writes)-1]
}

// withDuplicate offers Duplicate on VIN's Done screen, and adds OPS-1, an
// open issue in the team-managed OPS project (no resolution screen).
func withDuplicate(c *fakejira.Corpus) {
	for i := range c.Projects {
		p := &c.Projects[i]
		for j := range p.Transitions {
			tr := &p.Transitions[j]
			if p.Key == "VIN" && tr.ID == "31" {
				tr.Fields[0].AllowedValues = append(tr.Fields[0].AllowedValues, fakejira.Allowed{ID: "10002", Name: "Duplicate"})
			}
		}
	}
	c.Issues = append(c.Issues, fakejira.Issue{
		ID: "20001", Key: "OPS-1", Project: "OPS", Summary: "Rotate keys", IssueType: "Task",
		Status: "10000", Created: "2026-08-04T09:00:00.000+0000", Updated: "2026-08-04T09:00:00.000+0000",
	})
}

// TestJiraWriteBackReachesJiraOnce: close as completed, not_planned and
// duplicate, and reopen, each sent through the outbox exactly once, on a
// company-managed workflow (resolution on the screen) and a team-managed
// one (none) — where no fields key is sent.
func TestJiraWriteBackReachesJiraOnce(t *testing.T) {
	for _, tc := range []struct {
		name, project, id string
		reason            issuestate.Reason
		status, res       string
	}{
		{"completed", "VIN", "10001", issuestate.Completed, "10002", "Done"},
		{"not planned", "VIN", "10001", issuestate.NotPlanned, "10002", "Won't Do"},
		{"duplicate", "VIN", "10002", issuestate.Duplicate, "10002", "Duplicate"},
		{"team-managed completed", "OPS", "20001", issuestate.Completed, "10002", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJiraE2E(t, withDuplicate)
			f.bind(tc.project)
			f.tick()
			f.wantOK()
			out := f.outbox()
			f.humanTransition(tc.id, issuestate.Close, tc.reason)
			out.Drain(t.Context())
			out.Drain(t.Context())
			if w := f.newestWrite(tc.id); w.Status != store.OutboxDone {
				t.Fatalf("write = %+v, want done", w)
			}
			is := f.jiraIssue(tc.id)
			gotRes := ""
			if is.Resolution != nil {
				gotRes = is.Resolution.Name
			}
			if is.Status != tc.status || gotRes != tc.res {
				t.Errorf("Jira holds status %s resolution %q, want %s %q", is.Status, gotRes, tc.status, tc.res)
			}
			if n := f.posts(tc.id, "transitions"); n != 1 {
				t.Errorf("transition POSTs = %d, want 1", n)
			}
			// Reopen goes back through the same path.
			f.humanTransition(tc.id, issuestate.Reopen, "")
			out.Drain(t.Context())
			if w := f.newestWrite(tc.id); w.Status != store.OutboxDone {
				t.Fatalf("reopen write = %+v, want done", w)
			}
			if is := f.jiraIssue(tc.id); is.Status != "10000" || is.Resolution != nil {
				t.Errorf("after reopen Jira holds %s %+v", is.Status, is.Resolution)
			}
			if n := f.posts(tc.id, "transitions"); n != 2 {
				t.Errorf("transition POSTs = %d, want 2", n)
			}
			// The next import agrees and changes nothing.
			f.advance(time.Minute)
			f.tick()
			if iss := f.issue(tc.id); iss.State != issuestate.Open {
				t.Errorf("after the import the issue is %s", iss.State)
			}
		})
	}
}

// TestJiraWriteBackChooserFailures: a workflow the chooser cannot carry the
// close through fails the row with its reason, keeps the local state, and
// the issue's sync block says so; read-only fails no_write_scope.
func TestJiraWriteBackChooserFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func(p *fakejira.Project)
		scenario string
		want     string
	}{
		{"no direct done", func(p *fakejira.Project) {
			p.Transitions[2].From = []string{"10001"} // Done only from In Progress
		}, "", issuesource.ReasonNoTransition},
		{"two done", func(p *fakejira.Project) {
			p.Transitions = append(p.Transitions, fakejira.Transition{
				ID: "51", Name: "Resolve", From: []string{"10000"}, To: "10002",
				Fields: p.Transitions[2].Fields,
			})
		}, "", issuesource.ReasonAmbiguousTransition},
		{"needs a field", func(p *fakejira.Project) {
			p.Transitions[2].Fields = append(p.Transitions[2].Fields,
				fakejira.ScreenField{Key: "customfield_10050", Name: "Root cause", Required: true})
		}, "", issuesource.ReasonTransitionNeedsFields},
		{"read-only", nil, fakejira.ScenarioReadOnly, issuesource.ReasonNoWriteScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJiraE2E(t, func(c *fakejira.Corpus) {
				if tc.edit != nil {
					tc.edit(&c.Projects[0])
				}
			})
			f.tick()
			f.wantOK()
			if tc.scenario != "" {
				f.setScenario(tc.scenario)
			}
			f.humanTransition("10001", issuestate.Close, issuestate.Completed)
			f.outbox().Drain(t.Context())
			w := f.newestWrite("10001")
			if w.Status != store.OutboxFailed || w.LastReason != tc.want {
				t.Fatalf("write = %s/%s, want failed/%s", w.Status, w.LastReason, tc.want)
			}
			iss := f.issue("10001")
			if iss.State != issuestate.Closed || iss.Sync == nil || iss.Sync.LastReason != tc.want {
				t.Errorf("issue = %s, sync %+v; want closed with %s", iss.State, iss.Sync, tc.want)
			}
			if is := f.jiraIssue("10001"); is.Status != "10000" {
				t.Errorf("Jira moved to %s", is.Status)
			}
			// Terminal: another drain sends nothing.
			before := f.posts("10001", "transitions")
			f.outbox().Drain(t.Context())
			if n := f.posts("10001", "transitions"); n != before {
				t.Errorf("a failed row was retried")
			}
			// Nor does the next import revert the local close.
			f.advance(time.Minute)
			f.tick()
			if iss := f.issue("10001"); iss.State != issuestate.Closed {
				t.Errorf("the import reverted the local close to %s", iss.State)
			}
		})
	}
}

// TestJiraWriteBackConflict: Jira moved off the base first, so the drain
// adopts Jira's state as sync and the row ends conflict.
func TestJiraWriteBackConflict(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.tick()
	f.wantOK()
	f.humanTransition("10001", issuestate.Close, issuestate.Completed)
	if err := f.fake.Transition("10001", "31", map[string]any{"resolution": map[string]any{"name": "Won't Do"}}); err != nil {
		t.Fatal(err)
	}
	f.outbox().Drain(t.Context())
	w := f.newestWrite("10001")
	if w.Status != store.OutboxConflict || w.LastReason != outboxReasonRemoteChanged {
		t.Fatalf("write = %s/%s, want conflict/%s", w.Status, w.LastReason, outboxReasonRemoteChanged)
	}
	if iss := f.issue("10001"); iss.State != issuestate.Closed || iss.CloseReason != issuestate.NotPlanned {
		t.Errorf("issue = %s/%s, want Jira's closed/not_planned", iss.State, iss.CloseReason)
	}
	if n := f.posts("10001", "transitions"); n != 0 {
		t.Errorf("a conflict was written: %d POSTs", n)
	}
}

// touch stamps VIN-1 updated at the fixture's now, so it sits inside every
// incremental window that follows.
func (f *jiraE2E) touch() {
	f.t.Helper()
	if err := f.fake.SetUpdated("10001", f.now()); err != nil {
		f.t.Fatal(err)
	}
}

// TestJiraSearchLagDoesNotRevertAWrite is decision 1: with the search index
// still answering the pre-write status after a human's close was written,
// importer ticks do not revert it, and the confirming read by id is what
// decides.
func TestJiraSearchLagDoesNotRevertAWrite(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.touch()
	f.tick()
	f.wantOK()
	f.setScenario("search-lag 5")
	f.advance(time.Second)
	f.humanTransition("10001", issuestate.Close, issuestate.Completed)
	f.outbox().Drain(t.Context())
	if w := f.newestWrite("10001"); w.Status != store.OutboxDone {
		t.Fatalf("write = %+v, want done", w)
	}
	gets := func() int {
		n, err := fakejira.CountRequests(f.fake.RequestLog, "POST", "rest/api/3/issue/bulkfetch", 0)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := gets()
	for range 3 {
		f.advance(time.Minute)
		f.tick()
		f.wantOK()
		if iss := f.issue("10001"); iss.State != issuestate.Closed {
			t.Fatalf("a lagging search reverted the close: %s", iss.State)
		}
	}
	if gets() == before {
		t.Error("no confirming read was made: the lag was never seen")
	}
	if n := len(f.issueWrites("10001")); n != 1 {
		t.Errorf("writes = %d, want the one", n)
	}
}

// TestJiraWriteSettledMidPassIsNotReverted is review F1 on #945: the
// listing is read while the human's close is still pending, so the
// confirming read skips the issue; the drain then settles the write before
// the refresh's transaction opens. The stale listed open must not be
// adopted — no state change is announced at all — and the next passes,
// re-reading by id, keep it closed.
func TestJiraWriteSettledMidPassIsNotReverted(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.touch()
	f.tick()
	f.wantOK()
	f.setScenario("search-lag 5")
	f.advance(time.Second)
	f.humanTransition("10001", issuestate.Close, issuestate.Completed)
	// Listed again in the next incremental window, as open: the write has
	// not reached Jira yet.
	f.touch()
	ctx := t.Context()
	after, err := f.store.MaxEventID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := f.outbox()
	drained := false
	f.sync.afterConfirm = func() {
		if drained {
			return
		}
		drained = true
		out.Drain(ctx)
		if w := f.newestWrite("10001"); w.Status != store.OutboxDone {
			t.Errorf("write = %+v, want done before the refresh", w)
		}
	}
	for range 3 {
		f.advance(time.Minute)
		f.tick()
		f.wantOK()
		if iss := f.issue("10001"); iss.State != issuestate.Closed {
			t.Fatalf("a write settled mid-pass was reverted: %s", iss.State)
		}
	}
	if !drained {
		t.Fatal("the pass never reached the seam")
	}
	evs, err := f.store.ListEvents(ctx, store.EventFilter{
		AfterID: after, Types: []string{store.EventIssueStateChanged}, ProjectID: f.project.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Errorf("state changes announced after the close = %d, want none", len(evs))
	}
	if n := f.posts("10001", "transitions"); n != 1 {
		t.Errorf("transition POSTs = %d, want the one write", n)
	}
}

func (f *jiraE2E) issueWrites(id string) []*store.IssueOutbox {
	f.t.Helper()
	writes, err := f.store.IssueWrites(f.t.Context(), f.issue(id).ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return writes
}

// TestJiraPostFunctionDriftSettlesOnce is decision 2: a post-function that
// sets another resolution after the write is adopted once, as sync, and
// over three ticks nothing is written again and nothing flips back.
func TestJiraPostFunctionDriftSettlesOnce(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.touch()
	f.tick()
	f.wantOK()
	f.advance(time.Second)
	f.humanTransition("10001", issuestate.Close, issuestate.Completed)
	out := f.outbox()
	out.Drain(t.Context())
	// The post-function: Done → Done with Won't Do, through the fake's own
	// workflow (a reopen and a close), stamped now.
	f.advance(time.Second)
	if err := f.fake.Transition("10001", "41", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.fake.Transition("10001", "31", map[string]any{"resolution": map[string]any{"name": "Won't Do"}}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		f.advance(time.Minute)
		f.tick()
		out.Drain(t.Context())
		iss := f.issue("10001")
		if iss.State != issuestate.Closed || iss.CloseReason != issuestate.NotPlanned {
			t.Fatalf("tick %d: issue %s/%s, want Jira's closed/not_planned", i, iss.State, iss.CloseReason)
		}
	}
	if n := f.posts("10001", "transitions"); n != 1 {
		t.Errorf("transition POSTs = %d, want the one write", n)
	}
	if n := len(f.issueWrites("10001")); n != 1 {
		t.Errorf("writes = %d, want the one: sync enqueues none", n)
	}
}

// TestJiraHandoffCommentPostedAsADF: a granted handoff's comment is posted
// as ADF, keyed by Jira's comment id, and the next comment sync updates it
// in place instead of importing a duplicate.
func TestJiraHandoffCommentPostedAsADF(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.tick()
	f.wantOK()
	ctx := t.Context()
	id := f.issue("10002").ID
	c, err := issues.New(f.store).Comment(store.WithIssueHandoff(ctx, 1), issuestate.Handoff, id, "task 9", "landed in `main`")
	if err != nil {
		t.Fatalf("handoff comment: %v", err)
	}
	f.outbox().Drain(ctx)
	got, err := f.store.GetIssueComment(ctx, c.ID)
	if err != nil || got.RemoteKey == "" {
		t.Fatalf("comment = %+v, %v; want a remote key", got, err)
	}
	cs, err := f.fake.Load()
	if err != nil {
		t.Fatal(err)
	}
	var posted *fakejira.Comment
	for i := range cs.Comments {
		if cs.Comments[i].ID == got.RemoteKey {
			posted = &cs.Comments[i]
		}
	}
	if posted == nil || !strings.Contains(string(posted.Body), `"doc"`) || !strings.Contains(posted.BodyHTML, "landed") {
		t.Fatalf("Jira holds %+v for key %s", posted, got.RemoteKey)
	}
	f.advance(time.Minute)
	f.tick()
	var mine int
	for _, line := range f.comments("10002") {
		if strings.HasPrefix(line, got.RemoteKey+":") {
			mine++
		}
	}
	if mine != 1 {
		t.Errorf("thread = %q, want the one row keyed %s", f.comments("10002"), got.RemoteKey)
	}
	if n := f.posts("10002", "comment"); n != 1 {
		t.Errorf("comment POSTs = %d, want 1", n)
	}
}

// TestJiraAgentCloseStillRefused: an ungranted agent may not close an
// imported Jira issue, and nothing is enqueued.
func TestJiraAgentCloseStillRefused(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.tick()
	f.wantOK()
	id := f.issue("10001").ID
	_, err := issues.New(f.store).Transition(t.Context(), issuestate.Agent, id, issuestate.Close, issuestate.Completed)
	if !errors.Is(err, issues.ErrForgeWriteNeedsHuman) {
		t.Fatalf("agent close = %v, want forge_write_needs_human", err)
	}
	if n := len(f.issueWrites("10001")); n != 0 {
		t.Errorf("an agent's refused close enqueued %d writes", n)
	}
}

// TestJiraPendingWriteSurvivesRestart: a write enqueued and never drained
// survives the store being closed and reopened, and the restarted drain
// sends it once.
func TestJiraPendingWriteSurvivesRestart(t *testing.T) {
	f := newJiraE2E(t, nil)
	f.tick()
	f.wantOK()
	f.humanTransition("10001", issuestate.Close, issuestate.Completed)
	path := filepath.Join(f.project.Path, "vincent.db")
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f.store = st
	if w := f.newestWrite("10001"); w.Status != store.OutboxPending {
		t.Fatalf("after the restart the write is %s", w.Status)
	}
	out := f.outbox()
	out.Drain(t.Context())
	out.Drain(t.Context())
	if w := f.newestWrite("10001"); w.Status != store.OutboxDone {
		t.Fatalf("write = %+v, want done", w)
	}
	if n := f.posts("10001", "transitions"); n != 1 {
		t.Errorf("transition POSTs = %d, want 1", n)
	}
}
