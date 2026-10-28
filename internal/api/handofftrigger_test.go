package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/trigger"
)

// Task 137.9: a handoff a trigger's in-process replay creates is attributed
// to that trigger, which only the request context can say — the same body
// over plain HTTP is a person's.

func TestHandoffCreatedByTrigger(t *testing.T) {
	h := newActionHarness(t)
	a := handOffIssue(t, h, store.NewIssue{Title: "a"})
	b := handOffIssue(t, h, store.NewIssue{Title: "b"})
	body := func(id int64) []byte {
		enc, err := json.Marshal(map[string]any{
			"project_id": h.projectID, "workflow": handoffWorkflowName, "issue_ids": []int64{id},
			"grant": []string{"issue.close"}, "paused": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return enc
	}

	// The trigger's replay: the context names it, and its grant passes.
	req := httptest.NewRequest(http.MethodPost, h.ts.URL+"/v1/handoffs", bytes.NewReader(body(a.ID)))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(trigger.ContextWithTrigger(req.Context(), "nightly-train"))
	rec := httptest.NewRecorder()
	h.ts.Config.Handler.ServeHTTP(rec, req)
	got := decodeHandoff(t, rec.Result(), rec.Body.Bytes(), http.StatusCreated)
	if got.CreatedBy != string(store.HandoffByTrigger) || got.TriggerID != "nightly-train" {
		t.Errorf("trigger handoff: created_by %q trigger_id %q", got.CreatedBy, got.TriggerID)
	}

	// The same body over the wire, whatever it claims, is a person's.
	resp, raw := h.postMarked(t, "/v1/handoffs", json.RawMessage(body(b.ID)))
	got = decodeHandoff(t, resp, raw, http.StatusCreated)
	if got.CreatedBy != string(store.HandoffByHuman) || got.TriggerID != "" {
		t.Errorf("HTTP handoff: created_by %q trigger_id %q", got.CreatedBy, got.TriggerID)
	}
}

// TestTriggerDryRunShowsHandoff: `vincent trigger test` on a start_handoff
// shows the POST /v1/handoffs it would send, with its issue ids rendered.
func TestTriggerDryRunShowsHandoff(t *testing.T) {
	h := newTriggerHarness(t)
	h.writeTrigger(t, "train", `id: train
source:
  type: command
  project: `+strconv.FormatInt(h.projectID, 10)+`
  poll_interval: 5m
  command: [never-run]
action:
  type: start_handoff
  workflow: train
  issue_ids: ['{{ .Event.issue }}', '{{ .Event.second }}']
  grant: [issue.close]
`)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/triggers/train/test", map[string]any{"event": map[string]any{"id": "e", "issue": "#12", "second": ""}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test: status %d: %s", resp.StatusCode, body)
	}
	var j trigger.Judgement
	decodeInto(t, body, &j)
	if j.Action == nil || j.Action.Type != trigger.ActionStartHandoff || j.Action.Method != http.MethodPost || j.Action.Path != "/v1/handoffs" {
		t.Fatalf("judgement = %+v", j)
	}
	b, _ := j.Action.Body.(map[string]any)
	if ids, _ := b["issue_ids"].([]any); len(ids) != 1 || ids[0] != float64(12) || b["paused"] != true || b["restricted"] != true {
		t.Errorf("rendered body = %v", b)
	}
}
