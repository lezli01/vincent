package apiclient_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// TestPendingRequestDecodes reads a persisted §7.4 request back through the
// real handler: the form renders from this, so a field rename here is a form
// that silently loses its options.
func TestPendingRequestDecodes(t *testing.T) {
	h := newHarness(t)
	pending := `{"kind":"question","questions":[
		{"text":"Which color?","header":"Color","options":["Blue","Red"]},
		{"text":"Which files?","options":["a.go","b.go"],"multi_select":true}]}`
	h.awaitInput(t, h.taskID, pending)

	task, err := h.client().GetTask(context.Background(), h.taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	req, ok, err := task.PendingRequest()
	if err != nil || !ok {
		t.Fatalf("PendingRequest: ok = %v, err = %v", ok, err)
	}
	if req.Kind != apiclient.InputKindQuestion || len(req.Questions) != 2 {
		t.Fatalf("request = %+v, want a two-question request", req)
	}
	if got := req.Questions[0]; got.Header != "Color" || len(got.Options) != 2 || got.MultiSelect {
		t.Errorf("question 0 = %+v, want a single-select with two options", got)
	}
	if !req.Questions[1].MultiSelect {
		t.Error("question 1 lost multi_select; the form would accept one answer only")
	}
}

// TestPendingRequestAbsent: every state but awaiting_input has no request,
// and that is not an error the form should report.
func TestPendingRequestAbsent(t *testing.T) {
	h := newHarness(t)
	task, err := h.client().GetTask(context.Background(), h.taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if _, ok, err := task.PendingRequest(); ok || err != nil {
		t.Errorf("PendingRequest on a queued task: ok = %v, err = %v", ok, err)
	}
}

// TestAnswerRoundTrip: a validated answer reaches the daemon's own validation
// and returns the task to running. There is no live agent behind this one, so
// what is proven is the wire contract — the resume itself is covered
// end-to-end in internal/tui.
func TestAnswerRoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.awaitInput(t, h.taskID, `{"kind":"question","questions":[{"text":"Which color?","options":["Blue"]}]}`)

	resp := apiclient.InputResponse{Answers: map[string][]string{"Which color?": {"Blue"}}}
	got, err := h.client().Answer(ctx, h.taskID, resp)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got.State != string(store.TaskRunning) {
		t.Errorf("state = %q, want running", got.State)
	}
}

// TestValidateMatchesTheDaemonsRules keeps the form's submit gate honest: the
// daemon rejects the same bodies with 400, and a form that disagreed would
// either block a legal answer or promise one the daemon refuses.
func TestValidateMatchesTheDaemonsRules(t *testing.T) {
	question := apiclient.InputRequest{
		Kind: apiclient.InputKindQuestion,
		Questions: []apiclient.InputQuestion{
			{Text: "one", Options: []string{"a", "b"}},
			{Text: "many", Options: []string{"x", "y"}, MultiSelect: true},
		},
	}
	permission := apiclient.InputRequest{
		Kind:       apiclient.InputKindPermission,
		Permission: &apiclient.InputPermission{Tool: "Bash", Summary: "rm -rf"},
	}
	allow := true

	for _, tc := range []struct {
		name string
		req  apiclient.InputRequest
		resp apiclient.InputResponse
		ok   bool
	}{
		{"every question answered", question, apiclient.InputResponse{Answers: map[string][]string{
			"one": {"a"}, "many": {"x", "y"},
		}}, true},
		{"free text instead of an option", question, apiclient.InputResponse{Answers: map[string][]string{
			"one": {"something else"}, "many": {"x"},
		}}, true},
		{"a question left unanswered", question, apiclient.InputResponse{Answers: map[string][]string{
			"one": {"a"},
		}}, false},
		{"two values for a single-select", question, apiclient.InputResponse{Answers: map[string][]string{
			"one": {"a", "b"}, "many": {"x"},
		}}, false},
		{"empty value", question, apiclient.InputResponse{Answers: map[string][]string{
			"one": {""}, "many": {"x"},
		}}, false},
		{"an answer to a question not asked", question, apiclient.InputResponse{Answers: map[string][]string{
			"one": {"a"}, "many": {"x"}, "ghost": {"z"},
		}}, false},
		{"allow on a question", question, apiclient.InputResponse{
			Answers: map[string][]string{"one": {"a"}, "many": {"x"}}, Allow: &allow,
		}, false},
		{"permission allowed", permission, apiclient.InputResponse{Allow: &allow}, true},
		{"permission without a decision", permission, apiclient.InputResponse{}, false},
		{"permission with answers", permission, apiclient.InputResponse{
			Allow: &allow, Answers: map[string][]string{"one": {"a"}},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate(tc.resp)
			if tc.ok && err != nil {
				t.Errorf("Validate = %v, want accepted", err)
			}
			if !tc.ok && err == nil {
				t.Error("Validate accepted a body the daemon answers 400 to")
			}
		})
	}
}

// TestAnswerRejectedByDaemonSurfaces: the client's own check is a courtesy,
// not the authority — a body that slips past it still fails at the daemon.
func TestAnswerRejectedByDaemonSurfaces(t *testing.T) {
	h := newHarness(t)
	h.awaitInput(t, h.taskID, `{"kind":"question","questions":[{"text":"Which color?"}]}`)

	_, err := h.client().Answer(context.Background(), h.taskID,
		apiclient.InputResponse{Answers: map[string][]string{"Which color?": {"Blue", "Red"}}})
	if err == nil {
		t.Fatal("two answers to a single-select were accepted")
	}
}

// awaitInput parks the task on an input request the way the engine does:
// the pending request rides the transition into awaiting_input, which is the
// invariant the store enforces (§7.4).
func (h *harness) awaitInput(t *testing.T, id int64, pending string) {
	t.Helper()
	if !json.Valid([]byte(pending)) {
		t.Fatalf("test pending_input is not JSON: %s", pending)
	}
	h.setState(t, id, store.TaskRunning)
	if _, _, err := h.st.TransitionTask(t.Context(), id,
		store.TaskRunning, store.TaskAwaitingInput,
		store.TaskChange{PendingInput: &pending}); err != nil {
		t.Fatalf("park on input: %v", err)
	}
}
