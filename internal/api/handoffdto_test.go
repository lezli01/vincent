package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestHandoffTUIFields: what task 137.11's TUI reads without deriving it —
// handoff_id on the task's detail and list row, the issue detail's
// handoffs with this issue's outcome, and the marker's paused fact.
func TestHandoffTUIFields(t *testing.T) {
	h := newActionHarness(t)
	returned := handOffIssue(t, h, store.NewIssue{Title: "returned"})
	claimed := handOffIssue(t, h, store.NewIssue{Title: "claimed"})
	ho := startHandoff(t, h, returned.ID, claimed.ID)
	wantStatus(t, http.StatusOK)(h.postMarked(t, itemVerbPath(ho.ID, strconv.FormatInt(returned.ID, 10), "return"),
		map[string]any{}))

	var task taskResponse
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", ho.TaskID), nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &task) != nil ||
		task.HandoffID == nil || *task.HandoffID != ho.ID {
		t.Fatalf("task detail handoff_id: %d %s", resp.StatusCode, body)
	}
	var rows []taskResponse
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks?project_id=%d", h.projectID), nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &rows) != nil {
		t.Fatalf("task list: %d %s", resp.StatusCode, body)
	}
	for _, r := range rows {
		switch {
		case r.ID == ho.TaskID && (r.HandoffID == nil || *r.HandoffID != ho.ID):
			t.Errorf("handoff task row handoff_id = %v, want %d", r.HandoffID, ho.ID)
		case r.ID != ho.TaskID && r.HandoffID != nil:
			t.Errorf("task %d row handoff_id = %d, want none", r.ID, *r.HandoffID)
		}
	}

	issue := func(id int64) issueBody {
		t.Helper()
		var out issueBody
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d", id), nil)
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("issue %d: %d %s", id, resp.StatusCode, body)
		}
		return out
	}
	got := issue(returned.ID).Handoffs
	if len(got) != 1 || got[0].HandoffID != ho.ID || got[0].TaskID != ho.TaskID ||
		got[0].Outcome != store.HandoffReturned || got[0].OutcomeAt == nil || got[0].OutcomeBy == "" {
		t.Errorf("returned issue handoffs = %+v", got)
	}
	c := issue(claimed.ID)
	if len(c.Handoffs) != 1 || c.Handoffs[0].Outcome != "" || c.Handoffs[0].OutcomeAt != nil {
		t.Errorf("claimed issue handoffs = %+v, want one unresolved", c.Handoffs)
	}
	// startHandoff starts the task paused, so nothing runs it here.
	if c.Handoff == nil || !c.Handoff.Paused {
		t.Errorf("claimed marker under a paused task = %+v, want paused", c.Handoff)
	}
	resp, body = h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/resume", ho.TaskID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume the handoff task: %d %s", resp.StatusCode, body)
	}
	if c := issue(claimed.ID); c.Handoff == nil || c.Handoff.Paused {
		t.Errorf("claimed marker once resumed = %+v, want not paused", c.Handoff)
	}
	if b := issue(h.issue(t).ID).Handoffs; b == nil || len(b) != 0 {
		t.Errorf("an issue no handoff took: handoffs = %#v, want []", b)
	}
}
