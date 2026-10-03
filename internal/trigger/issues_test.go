package trigger

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// issuesDoc is an armed `type: issues` trigger on project 1 that links the
// task it creates to the issue.
func issuesDoc(id, extra string) string {
	return `id: ` + id + `
enabled: true
source:
  type: issues
  project: 1
action:
  type: create_task
  title: '{{ .Event.action }} {{ .Event.issue_id }} by {{ .Event.by }}'
  issue: '{{ .Event.issue_id }}'
` + extra
}

func (h *harness) localIssue(title string, by issuestate.Actor) *store.Issue {
	h.t.Helper()
	iss, err := h.st.CreateIssue(h.t.Context(), store.NewIssue{ProjectID: 1, Title: title, Author: "me"}, by)
	if err != nil {
		h.t.Fatalf("CreateIssue: %v", err)
	}
	return iss
}

func (h *harness) label(id int64, by issuestate.Actor, names ...string) {
	h.t.Helper()
	if _, err := h.st.SetIssueLabels(h.t.Context(), id, names, by); err != nil {
		h.t.Fatalf("SetIssueLabels: %v", err)
	}
}

func (h *harness) remoteIssue(key, author string, number int, labels []string) *store.Issue {
	h.t.Helper()
	iss, _, err := h.st.UpsertRemoteIssue(h.t.Context(), store.RemoteIssue{
		ProjectID: 1, Provider: "github", RemoteKey: key, Repo: "o/r", Number: number,
		Title: "imported " + key, Author: author, State: issuestate.Open, Labels: labels,
	}, issuestate.Sync)
	if err != nil {
		h.t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	return iss
}

// TestIssuesLabelFiresOnceOnALocalProject: on a project with no GitHub
// remote, arming seeds past history, and a label added afterwards fires once
// and links the created task to the issue through issue_id.
func TestIssuesLabelFiresOnceOnALocalProject(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	old := h.localIssue("before arming", issuestate.Human)
	h.label(old.ID, issuestate.Human, "x")

	h.write("lbl", issuesDoc("lbl", "match:\n  action: labeled\n  labels: x\n"))
	h.m.pollIssues(ctx)
	c := h.cursor("lbl")
	if c == nil || c.Cursor == nil || !c.LastPollOK {
		t.Fatalf("cursor after the seed = %+v", c)
	}
	if rows := h.ledger("lbl"); len(rows) != 0 {
		t.Fatalf("the seed fired history: %+v", rows)
	}

	iss := h.localIssue("after arming", issuestate.Human)
	h.label(iss.ID, issuestate.Human, "x", "y")
	h.m.pollIssues(ctx)
	h.m.pollIssues(ctx)

	rows := h.ledger("lbl")
	fired := 0
	for _, r := range rows {
		if r.Outcome == store.DeliveryFired {
			fired++
			if !strings.HasPrefix(r.EventID, "issue:"+itoa(iss.ID)+":labeled:") {
				t.Errorf("fired event id = %q", r.EventID)
			}
		}
	}
	if fired != 1 {
		t.Fatalf("ledger = %+v, want exactly one fired", rows)
	}
	reqs := h.api.requests()
	if len(reqs) != 1 || reqs[0].body.IssueID == nil || *reqs[0].body.IssueID != iss.ID ||
		reqs[0].body.Title != "labeled "+itoa(iss.ID)+" by human" {
		t.Errorf("replays = %+v", reqs)
	}
}

// TestIssuesEventPayload: the event carries the actor, the delta and the
// issue in the `.Issue` shape, read at judge time.
func TestIssuesEventPayload(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	iss := h.localIssue("payload", issuestate.Human)
	h.label(iss.ID, issuestate.Agent, "a", "b")
	if _, err := h.st.TransitionIssue(ctx, iss.ID, issuestate.Close, issuestate.Completed, nil, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	evs, err := h.st.ListEvents(ctx, store.EventFilter{Types: issueEventTypes})
	if err != nil || len(evs) != 3 {
		t.Fatalf("events = %v, %v", evs, err)
	}
	var got []Event
	for i := range evs {
		mapped, err := issueEvents(ctx, h.st, &evs[i])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, mapped...)
	}
	if len(got) != 3 {
		t.Fatalf("mapped = %+v", got)
	}
	if got[0]["action"] != "opened" || got[0]["by"] != "human" || got[0]["author"] != "me" ||
		got[0]["issue_id"] != iss.ID || got[0]["project_id"] != int64(1) {
		t.Errorf("opened = %+v", got[0])
	}
	if got[1]["action"] != "labeled" || got[1]["by"] != "agent" ||
		!reflect.DeepEqual(got[1]["labels"], []any{"a", "b"}) {
		t.Errorf("labeled = %+v", got[1])
	}
	if got[2]["action"] != "closed" || got[2]["from"] != "open" || got[2]["to"] != "closed" ||
		got[2]["reason"] != "completed" || got[2]["state"] != "closed" {
		t.Errorf("closed = %+v", got[2])
	}
	issue, _ := got[0]["Issue"].(map[string]any)
	if issue["Number"] != iss.ID || issue["Title"] != "payload" || issue["State"] != "closed" {
		t.Errorf(".Issue = %+v", issue)
	}
}

// TestIssuesAgentEchoFilteredByBy: an agent's label change carries `by:
// agent`, which `match: {by: human}` drops — the echo-loop guard.
func TestIssuesAgentEchoFilteredByBy(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("h", issuesDoc("h", "match:\n  action: labeled\n  by: human\n"))
	h.m.pollIssues(ctx)
	iss := h.localIssue("echo", issuestate.Human)
	h.label(iss.ID, issuestate.Agent, "triaged")
	h.m.pollIssues(ctx)
	rows := h.ledger("h")
	if len(rows) != 2 || count(rows, store.DeliveryFired) != 0 {
		t.Errorf("ledger = %+v, want the opened and the agent's labeled both filtered", rows)
	}
	if n := len(h.api.requests()); n != 0 {
		t.Errorf("replays = %d, want none", n)
	}
}

// TestIssuesSyncTrust: a sync `opened` is untrusted — refused for an author
// allowed_actors does not name, fired for one it does — while a human's
// `opened` needs no allowlist at all.
func TestIssuesSyncTrust(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("open", issuesDoc("open", "match:\n  action: opened\nallowed_actors: [alice]\n"))
	h.m.pollIssues(ctx)

	h.remoteIssue("NODE_BOB", "bob", 1, nil)
	alice := h.remoteIssue("NODE_ALICE", "Alice", 2, nil)
	mine := h.localIssue("local", issuestate.Human)
	h.m.pollIssues(ctx)

	outcomes := map[int64]string{}
	for _, r := range h.ledger("open") {
		id, _ := parseIssueEventID(r.EventID)
		outcomes[id] = r.Outcome
	}
	if len(outcomes) != 3 || outcomes[alice.ID] != store.DeliveryFired || outcomes[mine.ID] != store.DeliveryFired {
		t.Errorf("outcomes = %v, want bob filtered and Alice and the local issue fired", outcomes)
	}
	for id, o := range outcomes {
		if id != alice.ID && id != mine.ID && o != store.DeliveryFiltered {
			t.Errorf("bob's sync opened = %s, want filtered", o)
		}
	}
	j, err := h.m.Test(ctx, "open", Event{"id": "x", "action": "opened", "by": "sync", "author": "bob"})
	if err != nil || j.MatchMiss != "allowed_actors" {
		t.Errorf("Test(sync bob) = %+v, %v", j, err)
	}
	j, err = h.m.Test(ctx, "open", Event{"id": "y", "action": "opened", "by": "human", "author": "bob"})
	if err != nil || !j.Matched {
		t.Errorf("Test(human bob) = %+v, %v", j, err)
	}
}

// TestIssuesSyncLabelFiresOnce: an imported issue's label change arrives by
// sync and fires once, with the delta the refresh recorded.
func TestIssuesSyncLabelFiresOnce(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	iss := h.remoteIssue("NODE_1", "outsider", 7, []string{"a"})
	h.write("lbl", issuesDoc("lbl", "match:\n  action: labeled\n"))
	h.m.pollIssues(ctx)
	h.remoteIssue("NODE_1", "outsider", 7, []string{"a", "ready"})
	h.m.pollIssues(ctx)
	rows := h.ledger("lbl")
	if len(rows) != 1 || rows[0].Outcome != store.DeliveryFired {
		t.Fatalf("ledger = %+v", rows)
	}
	j, err := h.m.Test(ctx, "lbl", Event{"id": "z", "action": "labeled", "by": "sync", "labels": []any{"ready"}})
	if err != nil || !j.Matched {
		t.Errorf("Test = %+v, %v", j, err)
	}
	if reqs := h.api.requests(); len(reqs) != 1 || reqs[0].body.Title != "labeled "+itoa(iss.ID)+" by sync" {
		t.Errorf("replays = %+v", reqs)
	}
}

// TestIssuesRestartResumes: an event committed while the manager was down
// is delivered after the restart, and the restart re-fires nothing it had
// already delivered.
func TestIssuesRestartResumes(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("lbl", issuesDoc("lbl", "match:\n  action: labeled\n"))
	h.m.pollIssues(ctx)
	first := h.localIssue("one", issuestate.Human)
	h.label(first.ID, issuestate.Human, "x")
	h.m.pollIssues(ctx)

	h.m.Stop()
	second := h.localIssue("two", issuestate.Human)
	h.label(second.ID, issuestate.Human, "x")
	m := h.restart()
	// Rewind the cursor, as a crash between the fire and the cursor write
	// would leave it: the ledger's dedupe is what keeps the redelivery quiet.
	c := h.cursor("lbl")
	zero := "0"
	c.Cursor = &zero
	if err := h.st.PutTriggerCursor(ctx, c); err != nil {
		t.Fatal(err)
	}
	m.pollIssues(ctx)
	if n := count(h.ledger("lbl"), store.DeliveryFired); n != 2 {
		t.Errorf("fired = %d, want 2: %+v", n, h.ledger("lbl"))
	}
	if n := len(h.api.requests()); n != 2 {
		t.Errorf("replays = %d, want 2", n)
	}
}

// TestIssuesDisarmReseeds: disarming drops the cursor, and re-arming seeds
// afresh, so an off period fires nothing.
func TestIssuesDisarmReseeds(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("lbl", issuesDoc("lbl", "match:\n  action: labeled\n"))
	h.m.pollIssues(ctx)
	h.enabled.Store(false)
	h.m.reconcile(ctx)
	if c := h.cursor("lbl"); c != nil {
		t.Fatalf("cursor survived the disarm: %+v", c)
	}
	iss := h.localIssue("while off", issuestate.Human)
	h.label(iss.ID, issuestate.Human, "x")
	h.enabled.Store(true)
	h.m.pollIssues(ctx)
	h.m.pollIssues(ctx)
	if rows := h.ledger("lbl"); len(rows) != 0 {
		t.Errorf("the off period fired: %+v", rows)
	}
}

// TestIssuesPollDry: the live-poll dry run judges what the next pass would
// and moves nothing.
func TestIssuesPollDry(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("lbl", issuesDoc("lbl", "match:\n  action: labeled\n"))
	dry, err := h.m.PollDry(ctx, "lbl")
	if err != nil || !dry.Seed {
		t.Fatalf("unseeded PollDry = %+v, %v", dry, err)
	}
	h.m.pollIssues(ctx)
	iss := h.localIssue("dry", issuestate.Human)
	h.label(iss.ID, issuestate.Human, "x")
	before := h.cursor("lbl")
	dry, err = h.m.PollDry(ctx, "lbl")
	if err != nil || dry.Seed || len(dry.Events) != 2 || dry.Events[1].Outcome != store.DeliveryFired {
		t.Fatalf("PollDry = %+v, %v", dry, err)
	}
	if after := h.cursor("lbl"); !reflect.DeepEqual(after, before) {
		t.Errorf("the dry run moved the cursor: %+v", after)
	}
	if rows := h.ledger("lbl"); len(rows) != 0 {
		t.Errorf("the dry run wrote the ledger: %+v", rows)
	}
}

// TestIssuesIssueRender: `issue:` renders to a positive id or to nothing,
// and anything else is the delivery's error, as github_issue's is.
func TestIssuesIssueRender(t *testing.T) {
	d := &Definition{ID: "t", Source: Source{Type: SourceIssues, Project: 1},
		Action: Action{Type: ActionCreateTask, Title: "t", Issue: "{{ .Event.issue_id }}"}}
	for _, tc := range []struct {
		in   any
		want int64
		err  bool
	}{{int64(4), 4, false}, {"", 0, false}, {"0", 0, true}, {"-2", 0, true}, {"x", 0, true}} {
		rp, err := renderAction(d, renderData{Event: Event{"issue_id": tc.in}})
		if (err != nil) != tc.err {
			t.Errorf("%v: err = %v", tc.in, err)
			continue
		}
		if err != nil {
			continue
		}
		body, _ := rp.Body.(*CreateBody)
		switch {
		case tc.want == 0 && body.IssueID != nil:
			t.Errorf("%v: issue_id = %d, want none", tc.in, *body.IssueID)
		case tc.want != 0 && (body.IssueID == nil || *body.IssueID != tc.want):
			t.Errorf("%v: issue_id = %v, want %d", tc.in, body.IssueID, tc.want)
		}
	}
}

// TestIssuesMapper: the action vocabulary each stored event yields.
func TestIssuesMapper(t *testing.T) {
	actions := func(typ string, p issuePayload) []string {
		var out []string
		for _, c := range mapIssueEvent(typ, &p) {
			out = append(out, c.action)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		typ  string
		p    issuePayload
		want []string
	}{
		{"created", store.EventIssueCreated, issuePayload{}, []string{"opened"}},
		{"closed", store.EventIssueStateChanged, issuePayload{From: "open", To: "closed"}, []string{"closed"}},
		{"reopened", store.EventIssueStateChanged, issuePayload{From: "closed", To: "open"}, []string{"reopened"}},
		{"labels", store.EventIssueLabelsChanged, issuePayload{LabelsAdded: []string{"a"}, LabelsRemoved: []string{"b"}},
			[]string{"labeled", "unlabeled"}},
		{"labels before enrichment", store.EventIssueLabelsChanged, issuePayload{}, nil},
		{"sync refresh", store.EventIssueUpdated, issuePayload{From: "open", To: "closed", LabelsAdded: []string{"a"}},
			[]string{"closed", "labeled"}},
		{"edit", store.EventIssueUpdated, issuePayload{}, nil},
		{"comment", store.EventIssueCommentAdded, issuePayload{}, nil},
		{"deleted", store.EventIssueDeleted, issuePayload{}, nil},
	} {
		if got := actions(tc.typ, tc.p); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// parseIssueEventID reads the issue id out of `issue:{id}:{action}:{event}`.
func parseIssueEventID(id string) (int64, bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 4 || parts[0] != "issue" {
		return 0, false
	}
	n, ok := issueCursor(&store.TriggerCursor{Cursor: &parts[1]})
	return n, ok
}
