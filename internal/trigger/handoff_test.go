package trigger

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// handoffSrc is an armed start_handoff trigger on project 1; action is the
// lines under `action:` after its type, and extra what follows the action.
func handoffSrc(action, extra string) string {
	return `id: train
enabled: true
source:
  type: command
  project: 1
  poll_interval: 1s
  command: [poll]
action:
  type: start_handoff
  workflow: handoff-train
` + action + extra
}

func handoffDef(t *testing.T, action, extra string) *Definition {
	t.Helper()
	d, errs := Parse([]byte(handoffSrc(action, extra)), "train")
	if len(errs) > 0 {
		t.Fatalf("Parse: %v", errs)
	}
	return d
}

// fakeHandoffAPI answers POST /v1/handoffs as the real route does: a 201
// whose body carries the handoff's own id beside its task's, so a test can
// tell which of the two reached the ledger. Its task is a real row, which the
// ledger's task_id references. A non-zero status answers that instead.
type fakeHandoffAPI struct {
	mu      sync.Mutex
	st      *store.Store
	status  int
	body    string
	reqs    []recorded
	via     []string
	tasks   []int64
	handoff int64
}

func newFakeHandoffAPI(t *testing.T, st *store.Store) *fakeHandoffAPI {
	t.Helper()
	p := &store.Project{Name: "p", Path: "/p", DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return &fakeHandoffAPI{st: st, handoff: 900}
}

func (f *fakeHandoffAPI) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *fakeHandoffAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	f.reqs = append(f.reqs, recorded{
		method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"),
		key: r.Header.Get("Idempotency-Key"), raw: b,
	})
	f.via = append(f.via, ViaTrigger(r.Context()))
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
		return
	}
	task := &store.Task{
		ProjectID: 1, Title: "Hand off", WorkflowName: "handoff-train", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: fmt.Sprintf("vincent/h%d", len(f.reqs)), State: store.TaskPaused,
	}
	if err := f.st.CreateTask(r.Context(), task, nil); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.tasks = append(f.tasks, task.ID)
	f.handoff++
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]int64{"id": f.handoff, "task_id": task.ID})
}

func TestStartHandoffValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		extra  string
		want   string // the path refused, "" for valid
	}{
		{"issue_ids", "  issue_ids: ['{{ .Event.issue }}', '#7']\n", "", ""},
		{"select", "  select:\n    lane: hand_off\n    delivery: change\n    kind: [bug]\n    labels: [ship]\n    max: 5\n    order: priority\n", "", ""},
		{"select with a delivery list", "  select:\n    delivery: [change, reply]\n", "", ""},
		{"grant", "  select: {}\n  grant: [issue.close, pull.merge]\n", "", ""},
		{"neither", "", "", "action.issue_ids"},
		{"both", "  issue_ids: ['1']\n  select: {}\n", "", "action.issue_ids"},
		{"an empty issue_ids entry", "  issue_ids: ['']\n", "", "action.issue_ids[0]"},
		{"another lane", "  select:\n    lane: backlog\n", "", "action.select.lane"},
		{"an unknown delivery", "  select:\n    delivery: [change, patch]\n", "", "action.select.delivery"},
		{"max zero", "  select:\n    max: 0\n", "", "action.select.max"},
		{"another order", "  select:\n    order: age\n", "", "action.select.order"},
		{"an unknown capability", "  select: {}\n  grant: [issue.close, repo.delete]\n", "", "action.grant"},
		{"issue", "  select: {}\n  issue: '1'\n", "", "action.issue"},
		{"github_pull", "  select: {}\n  github_pull: '1'\n", "", "action.github_pull"},
		{"merge_back", "  select: {}\n  merge_back: {}\n", "", "action.merge_back"},
		{"target", "  select: {}\n  target: branch\n", "", "action.target"},
		{"branch", "  select: {}\n  branch: x\n", "", "action.branch"},
		{"prompt", "  select: {}\n  prompt: x\n", "", "action.prompt"},
		{"on_fire and limits", "  select: {}\n", "on_fire: create\npermission: workflow\nlimits:\n  max_per_hour: 2\n  max_task_cost_usd: 3\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := Parse([]byte(handoffSrc(tc.action, tc.extra)), "train")
			if tc.want == "" {
				if len(errs) > 0 {
					t.Fatalf("refused: %v", errs)
				}
				return
			}
			if !hasPath(errs, tc.want) {
				t.Fatalf("errors %v, want one at %s", errs, tc.want)
			}
		})
	}

	// A handoff needs its workflow: there is no default.
	src := strings.Replace(handoffSrc("  select: {}\n", ""), "  workflow: handoff-train\n", "", 1)
	if _, errs := Parse([]byte(src), "train"); !hasPath(errs, "action.workflow") {
		t.Errorf("no workflow: %v, want action.workflow", errs)
	}

	// The handoff's own fields are refused on every other action.
	for _, action := range []string{ActionCreateTask, ActionFollowUp, ActionRetry, ActionCancel} {
		for _, key := range []string{"issue_ids", "select", "grant"} {
			doc := docFor(SourceCommand, action)
			val := map[string]any{"issue_ids": []any{"1"}, "select": map[string]any{"max": 1}, "grant": []any{"issue.close"}}[key]
			setPath(doc, "action."+key, val, false)
			if errs := parseDoc(t, doc); !hasPath(errs, "action."+key) {
				t.Errorf("%s with %s: %v, want it refused", action, key, errs)
			}
		}
	}
}

// TestStartHandoffReplay: the rendered POST /v1/handoffs carries the
// trigger's on_fire, permission and cost cap as a create does, renders each
// issue_ids entry and drops the empty ones, and sends the Idempotency-Key.
// The ledger's task is the handoff's task, never the handoff's id, and the
// replay's context names the trigger.
func TestStartHandoffReplay(t *testing.T) {
	st := openStore(t)
	api := newFakeHandoffAPI(t, st)
	d := handoffDef(t, "  title: 'train {{ .Event.n }}'\n  issue_ids: ['{{ .Event.a }}', '{{ .Event.missing }}', '#{{ .Event.b }}']\n  grant: [issue.close]\n",
		"limits:\n  max_task_cost_usd: 4.5\n")
	del, err := fire(t.Context(), st, api, d, Event{"id": "e1", "n": "1", "a": "3", "missing": "", "b": "8"}, ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if del.Outcome != store.DeliveryFired {
		t.Fatalf("outcome = %s (%s)", del.Outcome, del.Detail)
	}
	reqs := api.requests()
	if len(reqs) != 1 || reqs[0].path != "/v1/handoffs" || reqs[0].method != http.MethodPost {
		t.Fatalf("replays = %+v", reqs)
	}
	if reqs[0].key != IdempotencyKey("train", "e1") {
		t.Errorf("Idempotency-Key = %q", reqs[0].key)
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[0].raw, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"project_id": 1.0, "workflow": "handoff-train", "title": "train 1",
		"issue_ids": []any{3.0, 8.0}, "grant": []any{"issue.close"},
		"paused": true, "restricted": true, "max_task_cost_usd": 4.5,
	}
	if fmt.Sprint(body) != fmt.Sprint(want) {
		t.Errorf("body = %v\nwant   %v", body, want)
	}
	if del.TaskID == nil || *del.TaskID != api.tasks[0] {
		t.Errorf("ledger task = %v, want the handoff's task %d", del.TaskID, api.tasks[0])
	}
	if api.via[0] != "train" {
		t.Errorf("replay context names trigger %q, want train", api.via[0])
	}

	// on_fire: create and permission: workflow drop paused and restricted.
	d = handoffDef(t, "  select:\n    delivery: change\n    max: 2\n", "on_fire: create\npermission: workflow\n")
	if _, err := fire(t.Context(), st, api, d, Event{"id": "e2"}, ghT0); err != nil {
		t.Fatal(err)
	}
	body = nil
	if err := json.Unmarshal(api.requests()[1].raw, &body); err != nil {
		t.Fatal(err)
	}
	want = map[string]any{
		"project_id": 1.0, "workflow": "handoff-train",
		"select": map[string]any{"delivery": []any{"change"}, "max": 2.0},
	}
	if fmt.Sprint(body) != fmt.Sprint(want) {
		t.Errorf("create body = %v\nwant          %v", body, want)
	}
}

// TestStartHandoffAllEmptyIsRefused: every issue_ids entry rendering empty
// still sends `[]` rather than falling back to a selection, and the route's
// 400 lands the delivery refused.
func TestStartHandoffAllEmptyIsRefused(t *testing.T) {
	st := openStore(t)
	api := newFakeHandoffAPI(t, st)
	api.status, api.body = http.StatusBadRequest, `{"error":{"code":"validation_failed"}}`
	d := handoffDef(t, "  issue_ids: ['{{ .Event.a }}']\n", "")
	del, err := fire(t.Context(), st, api, d, Event{"id": "e1", "a": ""}, ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if del.Outcome != store.DeliveryRefused {
		t.Errorf("outcome = %s, want refused", del.Outcome)
	}
	if raw := string(api.requests()[0].raw); !strings.Contains(raw, `"issue_ids":[]`) || strings.Contains(raw, "select") {
		t.Errorf("body = %s, want issue_ids: [] and no select", raw)
	}
}

// TestStartHandoffNothingToHandOffKeepsTheKey: an empty selection's 409 is
// recorded refused, which the dedupe lookup ignores, so the next event with
// the same key still fires.
func TestStartHandoffNothingToHandOffKeepsTheKey(t *testing.T) {
	st := openStore(t)
	api := newFakeHandoffAPI(t, st)
	api.status = http.StatusConflict
	api.body = `{"error":{"code":"invalid_state","details":{"reason":"nothing_to_hand_off"}}}`
	d := handoffDef(t, "  select: {}\n", "dedupe_key: nightly\n")
	del, err := fire(t.Context(), st, api, d, Event{"id": "e1"}, ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if del.Outcome != store.DeliveryRefused || !strings.Contains(del.Detail, "nothing_to_hand_off") {
		t.Fatalf("empty selection = %s (%s), want refused naming the reason", del.Outcome, del.Detail)
	}
	api.status = 0
	del, err = fire(t.Context(), st, api, d, Event{"id": "e2"}, ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if del.Outcome != store.DeliveryFired {
		t.Errorf("second event with the same key = %s, want fired", del.Outcome)
	}
}

// TestStartHandoffQueueCoalesce is decision 8's event-driven batch: events
// arriving while the handoff task is unsettled are held, and when it settles
// the newest fires once, selecting everything that accumulated.
func TestStartHandoffQueueCoalesce(t *testing.T) {
	h := newHarness(t)
	api := &fakeHandoffAPI{st: h.st, handoff: 900}
	h.m.deps.Handler = api
	h.write("train", handoffSrc("  select: {}\n", "on_fire: create\noverrun: queue_coalesce\n"))
	e, ok := h.reg.Get("train")
	if !ok || !e.Valid() {
		t.Fatalf("trigger train not loaded: %v", e.Errors)
	}
	d := e.Def

	first, err := fire(t.Context(), h.st, api, d, Event{"id": "e1"}, ghT0)
	if err != nil {
		t.Fatal(err)
	}
	if first.TaskID == nil {
		t.Fatalf("first = %+v", first)
	}
	setState(t, h.st, *first.TaskID, store.TaskRunning)
	for _, id := range []string{"e2", "e3", "e4"} {
		del, err := fire(t.Context(), h.st, api, d, Event{"id": id}, ghT0)
		if err != nil {
			t.Fatal(err)
		}
		if del.Outcome != store.DeliveryQueued {
			t.Errorf("%s while the handoff runs = %s, want queued", id, del.Outcome)
		}
	}
	h.m.drain(t.Context())
	if got := len(api.requests()); got != 1 {
		t.Fatalf("replays while busy = %d, want 1", got)
	}
	setState(t, h.st, *first.TaskID, store.TaskDone)
	h.m.drain(t.Context())
	if got := len(api.requests()); got != 2 {
		t.Fatalf("replays after settling = %d, want 2", got)
	}
	if raw := string(api.requests()[1].raw); !strings.Contains(raw, `"select":{}`) {
		t.Errorf("drained body = %s, want the selection", raw)
	}
	var superseded int
	for _, r := range h.ledger("train") {
		if r.Outcome == store.DeliverySuperseded {
			superseded++
		}
	}
	if superseded != 2 {
		t.Errorf("superseded = %d, want e2 and e3", superseded)
	}
}

// handoffItemEvent is a handoff.item_changed for issue, as the store writes
// it, on handoff task taskID.
func handoffItemEvent(id, issue, taskID int64, from, to, by string) *store.Event {
	payload, _ := json.Marshal(map[string]any{"handoff_id": 5, "issue_id": issue, "from": from, "to": to, "by": by})
	pid := int64(1)
	return &store.Event{ID: id, Type: store.EventHandoffItemChanged, ProjectID: &pid, TaskID: &taskID, Payload: payload}
}

func issuesHandoffDef(t *testing.T, match string) *Definition {
	t.Helper()
	d, errs := Parse([]byte(issuesDoc("h", match)), "h")
	if len(errs) > 0 {
		t.Fatalf("Parse: %v", errs)
	}
	return d
}

// TestIssuesHandoffChanged: the source maps handoff.item_changed to
// handoff_changed with the outcome moved to and from, the handoff and its
// task, and the item's actor — and only for a trigger whose match.action
// names it.
func TestIssuesHandoffChanged(t *testing.T) {
	st := openStore(t)
	if err := st.CreateProject(t.Context(), &store.Project{Name: "p", Path: "/p", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	iss, err := st.CreateIssue(t.Context(), store.NewIssue{ProjectID: 1, Title: "x", Author: "me"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	e := handoffItemEvent(40, iss.ID, 11, "waiting", "delivered", "handoff")

	named := issuesHandoffDef(t, "match:\n  action: handoff_changed\nallowed_actors: [me]\n")
	evs, err := issueEventsFor(t.Context(), st, named, e)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events = %v, %v", evs, err)
	}
	ev := evs[0]
	for k, want := range map[string]any{
		"action": ActionHandoffChanged, "issue_id": iss.ID, "project_id": int64(1), "handoff_id": int64(5),
		"outcome": "delivered", "from_outcome": "waiting", "task_id": int64(11), "by": "handoff",
		"author": "me", "state": "open",
	} {
		if fmt.Sprint(ev[k]) != fmt.Sprint(want) {
			t.Errorf("%s = %v, want %v", k, ev[k], want)
		}
	}
	if _, ok := ev["from"]; ok {
		t.Errorf("from is set on handoff_changed: %v", ev)
	}

	// An action-less trigger never sees it, nor one naming other actions.
	for _, match := range []string{"allowed_actors: [me]\n", "match:\n  action: [opened, lane_changed]\nallowed_actors: [me]\n"} {
		evs, err := issueEventsFor(t.Context(), st, issuesHandoffDef(t, match), e)
		if err != nil || len(evs) != 0 {
			t.Errorf("match %q saw %v, %v", match, evs, err)
		}
	}
	// A repeated outcome is no change.
	if evs, _ := issueEventsFor(t.Context(), st, named, handoffItemEvent(41, iss.ID, 11, "waiting", "waiting", "human")); len(evs) != 0 {
		t.Errorf("an unchanged outcome yielded %v", evs)
	}
}

// TestIssuesHandoffChangedTrust: a sync-delivered handoff_changed is
// untrusted, as closed is; a trigger that leaves sync out of match.by needs
// no allowed_actors.
func TestIssuesHandoffChangedTrust(t *testing.T) {
	if _, errs := Parse([]byte(issuesDoc("h", "match:\n  action: handoff_changed\n")), "h"); !hasPath(errs, "allowed_actors") {
		t.Errorf("handoff_changed with sync possible: %v, want allowed_actors required", errs)
	}
	for _, extra := range []string{
		"match:\n  action: handoff_changed\n  by: [handoff, human, agent, task, settle]\n",
		"match:\n  action: handoff_changed\nallowed_actors: [me]\n",
	} {
		if _, errs := Parse([]byte(issuesDoc("h", extra)), "h"); len(errs) > 0 {
			t.Errorf("%q refused: %v", extra, errs)
		}
	}
	// by: handoff on an ordinary issue event — a granted write — is never
	// held to allowed_actors: only sync is.
	d := issuesHandoffDef(t, "match:\n  action: labeled\nallowed_actors: [someone-else]\n")
	if actorChecked(d, Event{"by": "handoff"}) {
		t.Error("allowed_actors applies to by: handoff")
	}
	if !actorChecked(d, Event{"by": "sync"}) {
		t.Error("allowed_actors does not apply to by: sync")
	}
}

// grantDoc is a start_handoff trigger for project 7 with a grant.
func grantDoc(enabled bool, grant string) string {
	b := "id: t\nenabled: " + fmt.Sprint(enabled) + "\nsource:\n  type: command\n  project: 7\n  poll_interval: 5m\n  command: [\"never-run\"]\n" +
		"action:\n  type: start_handoff\n  workflow: wf\n  select: {}\n"
	if grant != "" {
		b += "  grant: " + grant + "\n"
	}
	return b
}

func TestArmingChangesGrant(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after []string
		want          string
	}{
		{"none", nil, nil, ""},
		{"new grant", nil, []string{"issue.close"}, "grant"},
		{"widened", []string{"issue.close"}, []string{"issue.close", "pull.merge"}, "grant"},
		{"kept, reordered", []string{"issue.close", "pull.merge"}, []string{"pull.merge", "issue.close"}, ""},
		{"narrowed", []string{"issue.close", "pull.merge"}, []string{"issue.close"}, ""},
		{"removed", []string{"issue.close"}, nil, ""},
		{"swapped", []string{"issue.close"}, []string{"pull.merge"}, "grant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(ArmingChanges(Switches{Grant: tc.before}, Switches{Grant: tc.after}), ",")
			if got != tc.want {
				t.Errorf("ArmingChanges = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestApplyProposalGrant: vincent trigger apply refuses a staged file that
// adds or widens a grant, new or existing, armed or not, and lets one that
// narrows it through.
func TestApplyProposalGrant(t *testing.T) {
	const project = 7
	for _, tc := range []struct {
		name       string
		existing   string
		staged     string
		wantRefuse string
	}{
		{"new without a grant", "", grantDoc(false, ""), ""},
		{"new with a grant", "", grantDoc(false, "[issue.close]"), "grant"},
		{"armed file gains a grant", grantDoc(true, ""), grantDoc(true, "[issue.close]"), "grant"},
		{"widened", grantDoc(false, "[issue.close]"), grantDoc(false, "[issue.close, pull.merge]"), "grant"},
		{"narrowed", grantDoc(true, "[issue.close, pull.merge]"), grantDoc(true, "[issue.close]"), ""},
		{"removed", grantDoc(false, "[issue.close]"), grantDoc(false, ""), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProposalFixture(t)
			version := Absent
			if tc.existing != "" {
				version = f.onDisk(t, tc.existing)
			}
			f.stage(t, map[string]string{"t": tc.staged}, map[string]string{"t": version})
			_, refusals, err := ApplyProposal(f.staging, f.w, project)
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for _, r := range refusals {
				keys = append(keys, r.Key)
				if r.Key == "grant" && !strings.Contains(r.Message, "write to GitHub") {
					t.Errorf("refusal message = %q", r.Message)
				}
			}
			if got := strings.Join(keys, ","); got != tc.wantRefuse {
				t.Errorf("refused = %q, want %q (%v)", got, tc.wantRefuse, refusals)
			}
		})
	}
}
