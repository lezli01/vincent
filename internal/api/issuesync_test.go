package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

func (h *issueHarness) syncStatus(t *testing.T, method string, id int64, want int) issueSyncStatusBody {
	t.Helper()
	resp, out := h.do(t, method, fmt.Sprintf("/v1/projects/%d/issues/sync", id), nil)
	if resp.StatusCode != want {
		t.Fatalf("%s sync = %d, want %d: %s", method, resp.StatusCode, want, out)
	}
	var got issueSyncStatusBody
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	return got
}

func TestIssueSyncStatusRoutes(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	ctx := t.Context()

	// No row while enabled: pending, never ok.
	got := h.syncStatus(t, http.MethodGet, h.pid, http.StatusOK)
	if !got.Enabled || got.OK || got.Reason != syncReasonPending || got.Provider != "github" {
		t.Errorf("no row = %+v, want enabled, pending", got)
	}

	// Sync now records the request and answers 202; with no attempt yet the
	// project is still pending, though the row now exists.
	requested := make(chan int64, 1)
	h.st.OnIssueSyncRequested(func(id int64) { requested <- id })
	got = h.syncStatus(t, http.MethodPost, h.pid, http.StatusAccepted)
	if got.OK || got.Reason != syncReasonPending {
		t.Errorf("after sync now = %+v, want pending", got)
	}
	select {
	case id := <-requested:
		if id != h.pid {
			t.Errorf("woke project %d, want %d", id, h.pid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sync now never woke the importer")
	}
	st, err := h.st.GetIssueSyncState(ctx, h.pid)
	if err != nil || st.RequestedAt == nil {
		t.Fatalf("sync state after request = %+v, %v", st, err)
	}

	// What the importer recorded is reported as it stands.
	now := time.Now().UTC().Truncate(time.Second)
	limited := now.Add(time.Hour)
	if err := h.st.PutIssueSyncState(ctx, store.IssueSyncState{
		ProjectID: h.pid, Provider: "github", Repo: "o/r",
		LastAttemptAt: &now, LastOKAt: &now, OK: true, ImportComplete: true,
	}); err != nil {
		t.Fatalf("PutIssueSyncState: %v", err)
	}
	got = h.syncStatus(t, http.MethodGet, h.pid, http.StatusOK)
	if !got.OK || got.Reason != "" || got.Repo != "o/r" || !got.ImportComplete ||
		got.LastSyncedAt == nil || !got.LastSyncedAt.Equal(now) {
		t.Errorf("ok row = %+v", got)
	}
	if err := h.st.PutIssueSyncState(ctx, store.IssueSyncState{
		ProjectID: h.pid, Provider: "github", Repo: "o/r", LastAttemptAt: &now, LastOKAt: &now,
		Reason: "rate_limited", RateLimitedUntil: &limited,
	}); err != nil {
		t.Fatalf("PutIssueSyncState: %v", err)
	}
	got = h.syncStatus(t, http.MethodGet, h.pid, http.StatusOK)
	if got.OK || got.Reason != "rate_limited" || got.RateLimitedUntil == nil {
		t.Errorf("rate-limited row = %+v", got)
	}

	// Unknown project: 404 on both.
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		resp, out := h.do(t, m, "/v1/projects/9999/issues/sync", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s unknown project = %d: %s", m, resp.StatusCode, out)
		}
	}
}

func TestRenderIssueSyncSwitchesWin(t *testing.T) {
	t.Parallel()
	now := time.Now()
	row := &store.IssueSyncState{Provider: "github", Repo: "o/r", OK: true, LastAttemptAt: &now, LastOKAt: &now}
	on := config.Default().GitHub
	off, noPoll := on, on
	off.Enabled = false
	noPoll.PollInterval = 0
	for _, tc := range []struct {
		name    string
		gh      config.GitHub
		enabled bool
		reason  string
	}{
		{"disabled", off, false, syncReasonDisabled},
		{"poll off", noPoll, false, syncReasonPollDisabled},
		{"on", on, true, ""},
	} {
		got := renderIssueSync(tc.gh, row)
		if got.Enabled != tc.enabled || got.Reason != tc.reason || got.OK != (tc.reason == "") {
			t.Errorf("%s: %+v", tc.name, got)
		}
		if got.LastSyncedAt == nil {
			t.Errorf("%s: a switch hides the last sync time", tc.name)
		}
	}
}

func TestIssueSourceCarriesSyncFields(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	ctx := t.Context()
	imported, _, err := h.st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: h.pid, Provider: "github", RemoteKey: "I_9", Repo: "o/r", Number: 9,
		URL: "https://github.com/o/r/issues/9", Title: "t", State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	path := fmt.Sprintf("/v1/issues/%d", imported.ID)
	got := h.must(t, http.StatusOK, http.MethodGet, path, nil)
	if got.Source == nil || got.Source.LastSyncedAt == nil || got.Source.Status != "" {
		t.Fatalf("live source = %+v", got.Source)
	}
	if err := h.st.SetIssueRemoteStatus(ctx, h.pid, "github", "I_9", store.RemoteStatusMissing, ""); err != nil {
		t.Fatalf("SetIssueRemoteStatus: %v", err)
	}
	got = h.must(t, http.StatusOK, http.MethodGet, path, nil)
	if got.Source == nil || got.Source.Status != store.RemoteStatusMissing {
		t.Errorf("missing source = %+v", got.Source)
	}
}

func TestDoctorReportsIssueSync(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	now := time.Now().UTC()
	if err := h.st.PutIssueSyncState(t.Context(), store.IssueSyncState{
		ProjectID: h.pid, Provider: "github", Repo: "o/r", LastAttemptAt: &now, Reason: "not_found",
	}); err != nil {
		t.Fatalf("PutIssueSyncState: %v", err)
	}
	rep := h.srv.doctorReport(t.Context(), false)
	if len(rep.GitHub.Sync) != 1 {
		t.Fatalf("sync rows = %+v", rep.GitHub.Sync)
	}
	row := rep.GitHub.Sync[0]
	if row.ProjectID != h.pid || row.Project != "p" || row.OK || row.Reason != "not_found" {
		t.Errorf("sync row = %+v", row)
	}
}
