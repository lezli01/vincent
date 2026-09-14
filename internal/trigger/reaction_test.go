package trigger

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/internal/store"
)

// reactionDef is docFor's command trigger on project 1 with the given action,
// its branch rendered from `.Event.ref`.
func reactionDef(t *testing.T, action string, edit func(doc map[string]any)) *Definition {
	t.Helper()
	doc := docFor(SourceCommand, action)
	if edit != nil {
		edit(doc)
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	d, errs := Parse(b, "t1")
	if len(errs) > 0 {
		t.Fatalf("Parse: %v\n%s", errs, b)
	}
	return d
}

func taskOnBranch(t *testing.T, st *store.Store, branch string, archived bool) int64 {
	t.Helper()
	task := &store.Task{
		ProjectID: 1, Title: branch, WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: branch, State: store.TaskDone,
	}
	if archived {
		at := time.Now().UTC()
		task.ArchivedAt = &at
	}
	if err := st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task.ID
}

// TestReactionTargetResolution: `target: branch` finds the newest unarchived
// task on the branch; none is `refused` naming the branch; the route's own 409
// is `refused` against the task (decision 31C).
func TestReactionTargetResolution(t *testing.T) {
	now := time.Now()

	t.Run("no task on the branch", func(t *testing.T) {
		st := openStore(t)
		api := newFakeAPI(t, st, http.StatusOK, `{}`)
		del, err := fire(t.Context(), st, api, reactionDef(t, ActionFollowUp, nil), Event{"id": "e1", "ref": "feat/gone"}, now)
		if err != nil || del.Outcome != store.DeliveryRefused || !strings.Contains(del.Detail, `"feat/gone"`) {
			t.Fatalf("fire = %+v, %v", del, err)
		}
		if n := len(api.requests()); n != 0 {
			t.Errorf("%d replays with no target", n)
		}
		rows, _ := st.ListTriggerDeliveries(t.Context(), "t1", 10)
		if len(rows) != 1 || rows[0].Outcome != store.DeliveryRefused || rows[0].TaskID != nil ||
			!strings.Contains(rows[0].Detail, "feat/gone") {
			t.Errorf("ledger = %+v", rows)
		}
	})

	t.Run("an archived task is not a target", func(t *testing.T) {
		st := openStore(t)
		api := newFakeAPI(t, st, http.StatusOK, `{}`)
		taskOnBranch(t, st, "feat/old", true)
		del, err := fire(t.Context(), st, api, reactionDef(t, ActionRetry, nil), Event{"id": "e1", "ref": "feat/old"}, now)
		if err != nil || del.Outcome != store.DeliveryRefused || len(api.requests()) != 0 {
			t.Errorf("fire = %+v, %v (%d replays)", del, err, len(api.requests()))
		}
	})

	// The store's branch claim (task 001) keeps two unarchived tasks in one
	// project off the same branch, so "several" means archived namesakes
	// beside the one live task; newest-first only breaks a tie the claim
	// already rules out.
	t.Run("the live task among archived namesakes", func(t *testing.T) {
		st := openStore(t)
		api := newFakeAPI(t, st, http.StatusOK, `{}`)
		taskOnBranch(t, st, "feat/dup", true)
		newest := taskOnBranch(t, st, "feat/dup", false)
		del, err := fire(t.Context(), st, api, reactionDef(t, ActionFollowUp, nil), Event{"id": "e1", "ref": "feat/dup"}, now)
		if err != nil || del.Outcome != store.DeliveryFired || del.TaskID == nil || *del.TaskID != newest {
			t.Fatalf("fire = %+v, %v; want task %d", del, err, newest)
		}
		if reqs := api.requests(); len(reqs) != 1 || reqs[0].path != fmt.Sprintf("/v1/tasks/%d/follow_up", newest) {
			t.Errorf("replays = %+v", reqs)
		}
		rows, _ := st.ListTriggerDeliveries(t.Context(), "t1", 10)
		if len(rows) != 1 || rows[0].TaskID == nil || *rows[0].TaskID != newest {
			t.Errorf("ledger = %+v", rows)
		}
		evs, _ := st.ListEvents(t.Context(), store.EventFilter{Types: []string{store.EventTriggerFired}})
		if len(evs) != 1 || evs[0].TaskID == nil || *evs[0].TaskID != newest {
			t.Errorf("trigger.fired = %+v", evs)
		}
	})

	t.Run("an FSM-invalid target is refused against it", func(t *testing.T) {
		st := openStore(t)
		envelope := `{"error":{"code":"invalid_state","message":"cannot retry a done task"}}`
		api := newFakeAPI(t, st, http.StatusConflict, envelope)
		id := taskOnBranch(t, st, "feat/done", false)
		del, err := fire(t.Context(), st, api, reactionDef(t, ActionRetry, nil), Event{"id": "e1", "ref": "feat/done"}, now)
		if err != nil || del.Outcome != store.DeliveryRefused {
			t.Fatalf("fire = %+v, %v", del, err)
		}
		rows, _ := st.ListTriggerDeliveries(t.Context(), "t1", 10)
		if len(rows) != 1 || rows[0].TaskID == nil || *rows[0].TaskID != id || rows[0].Detail != envelope {
			t.Errorf("ledger = %+v, want refused against task %d with the envelope", rows, id)
		}
		if evs, _ := st.ListEvents(t.Context(), store.EventFilter{Types: []string{store.EventTriggerFired}}); len(evs) != 0 {
			t.Errorf("a refusal published trigger.fired: %+v", evs)
		}
	})
}

// TestReactionReplays: each reaction replays its route with the body a client
// would send — paused under propose, none for cancel — and no Idempotency-Key,
// which only the create route honours.
func TestReactionReplays(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		edit   func(doc map[string]any)
		verb   string
		body   string
	}{
		{"follow_up proposes paused", ActionFollowUp, nil, "follow_up", `{"prompt":"fix it","paused":true}`},
		{
			"follow_up with on_fire create", ActionFollowUp,
			func(d map[string]any) { d["on_fire"] = OnFireCreate }, "follow_up", `{"prompt":"fix it"}`,
		},
		{
			"retry proposes paused with its override", ActionRetry,
			func(d map[string]any) { setPath(d, "action.prompt", "again: {{ .Event.id }}", false) },
			"retry", `{"prompt_override":"again: e1","paused":true}`,
		},
		{"retry without a prompt", ActionRetry, nil, "retry", `{"paused":true}`},
		{"cancel sends no body", ActionCancel, nil, "cancel", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openStore(t)
			api := newFakeAPI(t, st, http.StatusOK, `{}`)
			id := taskOnBranch(t, st, "feat/x", false)
			del, err := fire(t.Context(), st, api, reactionDef(t, tc.action, tc.edit), Event{"id": "e1", "ref": "feat/x"}, time.Now())
			if err != nil || del.Outcome != store.DeliveryFired {
				t.Fatalf("fire = %+v, %v", del, err)
			}
			reqs := api.requests()
			if len(reqs) != 1 {
				t.Fatalf("%d replays", len(reqs))
			}
			r := reqs[0]
			if r.method != http.MethodPost || r.path != fmt.Sprintf("/v1/tasks/%d/%s", id, tc.verb) {
				t.Errorf("replayed %s %s", r.method, r.path)
			}
			if string(r.raw) != tc.body {
				t.Errorf("body = %q, want %q", r.raw, tc.body)
			}
			if wantCT := tc.body != ""; (r.contentType == "application/json") != wantCT {
				t.Errorf("Content-Type = %q with body %q", r.contentType, r.raw)
			}
			if r.key != "" {
				t.Errorf("a reaction sent Idempotency-Key %q", r.key)
			}
		})
	}

	t.Run("create_task carries the key", func(t *testing.T) {
		st := openStore(t)
		api := newFakeAPI(t, st, 0, "")
		if _, err := fire(t.Context(), st, api, reactionDef(t, ActionCreateTask, nil), Event{"id": "e1"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if reqs := api.requests(); len(reqs) != 1 || reqs[0].path != createPath || reqs[0].key != IdempotencyKey("t1", "e1") {
			t.Errorf("replays = %+v", reqs)
		}
	})
}
