package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestIssueHandoffMarker: the handoff marker (task 137.10) on GET
// /v1/issues rows and the detail, and the repeatable ?handoff= filter.
func TestIssueHandoffMarker(t *testing.T) {
	h := newActionHarness(t)
	pending := handOffIssue(t, h, store.NewIssue{Title: "pending"})
	claimed := handOffIssue(t, h, store.NewIssue{Title: "claimed"})
	delivered := handOffIssue(t, h, store.NewIssue{Title: "delivered"})
	ho := startHandoff(t, h, claimed.ID, delivered.ID)
	wantStatus(t, http.StatusOK)(h.postMarked(t, itemVerbPath(ho.ID, strconv.FormatInt(delivered.ID, 10), "deliver"),
		map[string]any{"close": "none"}))
	open, err := h.store.CreateIssue(t.Context(), store.NewIssue{ProjectID: h.projectID, Title: "open"}, "human")
	if err != nil {
		t.Fatal(err)
	}

	list := func(query string) []issueRowBody {
		t.Helper()
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues?project_id=%d&%s", h.projectID, query), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET ?%s: %d %s", query, resp.StatusCode, body)
		}
		var out []issueRowBody
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	byID := map[int64]*issueHandoffBody{}
	for _, row := range list("") {
		byID[row.ID] = row.Handoff
	}
	want := func(id int64, state store.HandoffMarker, named bool) {
		t.Helper()
		got := byID[id]
		if got == nil || got.State != state {
			t.Fatalf("issue %d handoff = %+v, want %s", id, got, state)
		}
		if named && (got.HandoffID == nil || *got.HandoffID != ho.ID || got.TaskID == nil || *got.TaskID != ho.TaskID) {
			t.Errorf("issue %d %s names %v/%v, want handoff %d task %d", id, state, got.HandoffID, got.TaskID, ho.ID, ho.TaskID)
		}
		if !named && (got.HandoffID != nil || got.TaskID != nil) {
			t.Errorf("issue %d %s names %v/%v, want none", id, state, got.HandoffID, got.TaskID)
		}
	}
	want(pending.ID, store.HandoffMarkerPending, false)
	want(claimed.ID, store.HandoffMarkerClaimed, true)
	want(delivered.ID, store.HandoffMarkerDelivered, true)
	if got := byID[open.ID]; got != nil {
		t.Errorf("open issue handoff = %+v, want null", got)
	}

	// The wire spells "no marker" as null, and the detail carries it too.
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d", open.ID), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"handoff":null`) {
		t.Errorf("detail of an open issue: %d %s", resp.StatusCode, body)
	}
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d", claimed.ID), nil)
	var detail issueBody
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &detail) != nil ||
		detail.Handoff == nil || detail.Handoff.State != store.HandoffMarkerClaimed {
		t.Errorf("detail of a claimed issue: %d %s", resp.StatusCode, body)
	}

	ids := func(rows []issueRowBody) []int64 {
		out := make([]int64, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		slices.Sort(out)
		return out
	}
	if got := ids(list("handoff=claimed&handoff=delivered")); !slices.Equal(got, []int64{claimed.ID, delivered.ID}) {
		t.Errorf("?handoff=claimed&handoff=delivered = %v", got)
	}
	if got := ids(list("handoff=pending&lane=hand_off")); !slices.Equal(got, []int64{pending.ID}) {
		t.Errorf("?handoff=pending&lane=hand_off = %v", got)
	}

	resp, body = h.doJSON(t, http.MethodGet, "/v1/issues?handoff=bogus", nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), CodeValidationFailed) ||
		!strings.Contains(string(body), "pending, claimed, waiting, delivered, idle") {
		t.Errorf("?handoff=bogus: %d %s", resp.StatusCode, body)
	}
}
