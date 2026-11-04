package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// Task 142.14: the sync status renders the bound source and the provider's
// display name, so no client builds either from a provider's parts.

func TestIssueSyncStatusRendersSource(t *testing.T) {
	cfg := config.Default()
	cfg.Jira.Enabled = false
	h := newProjectHarnessWithConfig(t, func() config.Config { return cfg })
	for _, tc := range []struct {
		name                         string
		origin                       string
		source                       map[string]any
		provider, reason, src, pname string
	}{
		{"jira, switched off", "https://github.com/o/r.git", jiraSource(), "jira", syncReasonJiraDisabled, "acme.atlassian.net/ENG", "Jira"},
		{"github", "https://github.com/o/r.git", nil, "github", syncReasonPending, "o/r", "GitHub"},
		{"explicit none", "https://github.com/o/r.git", map[string]any{"provider": "none"}, "none", syncReasonSourceNotGitHub, "", ""},
		{"derived other origin", "https://example.com/o/r.git", nil, "none", syncReasonNotGitHub, "", ""},
		{"github pinned on other origin", "https://example.com/o/r.git", map[string]any{"provider": "github"}, "github", syncReasonNotGitHub, "", "GitHub"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"path": repoWithOrigin(t, tc.origin)}
			if tc.source != nil {
				body["issue_source"] = tc.source
			}
			id := h.createID(t, body)
			got := h.syncStatus(t, http.MethodGet, id, http.StatusOK)
			if got.Provider != tc.provider || got.Reason != tc.reason || got.Source != tc.src || got.ProviderName != tc.pname {
				t.Errorf("status = %+v, want provider %s, reason %s, source %q, provider_name %q",
					got, tc.provider, tc.reason, tc.src, tc.pname)
			}
			if tc.provider == "jira" && got.Site != "https://acme.atlassian.net" {
				t.Errorf("site = %q, want the bound site kept", got.Site)
			}
		})
	}
}

// A GitHub project's status is v0.12.0's, key for key, plus the two
// display fields — row-less (the origin fallback) and with a stored row.
func TestIssueSyncStatusGitHubCompat(t *testing.T) {
	h := newProjectHarness(t)
	id := h.createID(t, map[string]any{"path": repoWithOrigin(t, "https://github.com/o/r.git")})
	v012 := []string{
		"enabled", "provider", "repo", "ok", "reason", "import_complete",
		"writes_pending", "writes_failed", "writes_conflict",
	}
	check := func(label string, extra ...string) {
		t.Helper()
		resp, out := h.doJSON(t, http.MethodGet, "/v1/projects/"+itoa(id)+"/issues/sync", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: get = %d: %s", label, resp.StatusCode, out)
		}
		var raw map[string]any
		if err := json.Unmarshal(out, &raw); err != nil {
			t.Fatalf("%s: decode: %v", label, err)
		}
		want := append(append(slices.Clone(v012), extra...), "source", "provider_name")
		slices.Sort(want)
		got := slices.Sorted(maps.Keys(raw))
		if !slices.Equal(got, want) {
			t.Errorf("%s: keys = %v, want %v", label, got, want)
		}
		if raw["source"] != raw["repo"] || raw["repo"] != "o/r" || raw["provider_name"] != "GitHub" || raw["provider"] != "github" {
			t.Errorf("%s: body = %s", label, out)
		}
	}
	check("row-less")

	now := time.Now().UTC()
	if err := h.store.PutIssueSyncState(t.Context(), store.IssueSyncState{
		ProjectID: id, Provider: "github", Repo: "o/r",
		LastAttemptAt: &now, LastOKAt: &now, OK: true, ImportComplete: true,
	}); err != nil {
		t.Fatal(err)
	}
	// An ok row omits reason, and now has a last sync.
	resp, out := h.doJSON(t, http.MethodGet, "/v1/projects/"+itoa(id)+"/issues/sync", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["reason"]; ok {
		t.Fatalf("an ok row carries a reason: %s", out)
	}
	v012 = slices.DeleteFunc(v012, func(k string) bool { return k == "reason" })
	check("stored row", "last_synced_at")
}
