package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// Task 142.7: the sync status is folded per provider, and a project with no
// row gets a derived reason rather than "pending" when it resolves to none.

func repoWithOrigin(t *testing.T, url string) string {
	t.Helper()
	repo := testrepo.Init(t, "main")
	if url != "" {
		testrepo.Run(t, repo, "remote", "add", "origin", url)
	}
	return repo
}

func jiraSource() map[string]any {
	return map[string]any{"provider": "jira", "jira": map[string]any{
		"site": "https://acme.atlassian.net", "project_key": "ENG",
	}}
}

func (h *projectHarness) syncStatus(t *testing.T, method string, id int64, want int) issueSyncStatusBody {
	t.Helper()
	resp, out := h.doJSON(t, method, "/v1/projects/"+itoa(id)+"/issues/sync", nil)
	if resp.StatusCode != want {
		t.Fatalf("%s sync = %d, want %d: %s", method, resp.StatusCode, want, out)
	}
	var got issueSyncStatusBody
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	return got
}

var projectSeq atomic.Int64

// createID creates a project under a unique name: testrepo's directories
// share a basename, which the API would otherwise take as the name.
func (h *projectHarness) createID(t *testing.T, body map[string]any) int64 {
	t.Helper()
	body["name"] = "p" + itoa(projectSeq.Add(1))
	return int64(h.mustCreate(t, body)["id"].(float64))
}

func TestIssueSyncRowlessReasons(t *testing.T) {
	h := newProjectHarness(t)
	for _, tc := range []struct {
		name     string
		origin   string
		source   map[string]any
		provider string
		reason   string
		repo     string
	}{
		{"unbound github origin", "https://github.com/o/r.git", nil, "github", syncReasonPending, "o/r"},
		{"unbound other origin", "https://example.com/o/r.git", nil, "none", syncReasonNotGitHub, ""},
		{"unbound no origin", "", nil, "none", syncReasonNotGitHub, ""},
		{"explicit none", "https://github.com/o/r.git", map[string]any{"provider": "none"}, "none", syncReasonSourceNotGitHub, ""},
		{"explicit github, other origin", "https://example.com/o/r.git", map[string]any{"provider": "github"}, "github", syncReasonNotGitHub, ""},
		{"jira", "https://example.com/o/r.git", jiraSource(), "jira", syncReasonPending, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"path": repoWithOrigin(t, tc.origin)}
			if tc.source != nil {
				body["issue_source"] = tc.source
			}
			id := h.createID(t, body)
			got := h.syncStatus(t, http.MethodGet, id, http.StatusOK)
			if got.Provider != tc.provider || got.Reason != tc.reason || got.OK || got.Repo != tc.repo {
				t.Errorf("status = %+v, want provider %s, reason %s, repo %q", got, tc.provider, tc.reason, tc.repo)
			}
			if got.Enabled != (tc.provider != "none") {
				t.Errorf("enabled = %v for provider %s", got.Enabled, tc.provider)
			}
		})
	}
}

// A project that resolves to none has no pass to wake: "sync now" records
// nothing and answers with the derived status. Neither has github pinned on
// an origin off github.com (review F2): the daemon's GitHub pass stands down
// on it, so a recorded request would read pending for ever.
func TestIssueSyncNowOnNoneCreatesNoRow(t *testing.T) {
	h := newProjectHarness(t)
	woke := make(chan int64, 3)
	h.store.OnIssueSyncRequested(func(id int64) { woke <- id })
	for _, tc := range []struct {
		name     string
		body     map[string]any
		provider string
		reason   string
	}{
		{"other origin", map[string]any{"path": repoWithOrigin(t, "https://example.com/o/r.git")}, "none", syncReasonNotGitHub},
		{"explicit none", map[string]any{
			"path": repoWithOrigin(t, "https://github.com/o/r.git"), "issue_source": map[string]any{"provider": "none"},
		}, "none", syncReasonSourceNotGitHub},
		{"explicit github, other origin", map[string]any{
			"path": repoWithOrigin(t, "https://example.com/o/r.git"), "issue_source": map[string]any{"provider": "github"},
		}, "github", syncReasonNotGitHub},
	} {
		id := h.createID(t, tc.body)
		got := h.syncStatus(t, http.MethodPost, id, http.StatusAccepted)
		if got.Reason != tc.reason || got.Provider != tc.provider {
			t.Errorf("%s: sync now = %+v, want %s/%s", tc.name, got, tc.provider, tc.reason)
		}
		if _, err := h.store.GetIssueSyncState(t.Context(), id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: sync now created a row (err %v)", tc.name, err)
		}
		if got := h.syncStatus(t, http.MethodGet, id, http.StatusOK); got.Reason != tc.reason {
			t.Errorf("%s: status after sync now = %+v, want %s", tc.name, got, tc.reason)
		}
	}
	select {
	case id := <-woke:
		t.Errorf("sync now on a none project woke project %d", id)
	default:
	}
}

// "Sync now" stamps the resolved provider on the requested row, which is how
// the daemon picks the provider's pass.
func TestIssueSyncNowStampsProvider(t *testing.T) {
	h := newProjectHarness(t)
	for _, tc := range []struct {
		name, want string
		body       map[string]any
	}{
		{"github", "github", map[string]any{"path": repoWithOrigin(t, "https://github.com/o/r.git")}},
		{"jira", "jira", map[string]any{"path": repoWithOrigin(t, "https://github.com/o/r.git"), "issue_source": jiraSource()}},
	} {
		id := h.createID(t, tc.body)
		got := h.syncStatus(t, http.MethodPost, id, http.StatusAccepted)
		if got.Provider != tc.want || got.Reason != syncReasonPending {
			t.Errorf("%s: sync now = %+v", tc.name, got)
		}
		st, err := h.store.GetIssueSyncState(t.Context(), id)
		if err != nil || st.Provider != tc.want || st.RequestedAt == nil {
			t.Errorf("%s: requested row = %+v, %v; want provider %s", tc.name, st, err, tc.want)
		}
	}
}

// A Jira project is folded with jira's switches, never github's.
func TestIssueSyncJiraFoldsJiraConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mut     func(*config.Config)
		enabled bool
		reason  string
	}{
		{"github off", func(c *config.Config) { c.GitHub.Enabled = false }, true, ""},
		{"github poll off", func(c *config.Config) { c.GitHub.PollInterval = 0 }, true, ""},
		{"jira off", func(c *config.Config) { c.Jira.Enabled = false }, false, issuesource.ReasonDisabled},
		{"jira poll off", func(c *config.Config) { c.Jira.PollInterval = 0 }, false, syncReasonPollDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			tc.mut(&cfg)
			h := newProjectHarnessWithConfig(t, func() config.Config { return cfg })
			id := h.createID(t, map[string]any{"path": repoWithOrigin(t, "https://github.com/o/r.git"), "issue_source": jiraSource()})

			// No row: the switch, or pending.
			want := tc.reason
			if want == "" {
				want = syncReasonPending
			}
			got := h.syncStatus(t, http.MethodGet, id, http.StatusOK)
			if got.Provider != "jira" || got.Enabled != tc.enabled || got.Reason != want {
				t.Errorf("no row = %+v, want enabled %v, reason %s", got, tc.enabled, want)
			}
			if got.Site != "https://acme.atlassian.net" {
				t.Errorf("site = %q", got.Site)
			}

			// An ok Jira row.
			now := time.Now().UTC()
			if err := h.store.PutIssueSyncState(t.Context(), store.IssueSyncState{
				ProjectID: id, Provider: "jira", LastAttemptAt: &now, LastOKAt: &now, OK: true,
			}); err != nil {
				t.Fatal(err)
			}
			got = h.syncStatus(t, http.MethodGet, id, http.StatusOK)
			if got.Provider != "jira" || got.Reason != tc.reason || got.OK != (tc.reason == "") {
				t.Errorf("jira row = %+v, want reason %q", got, tc.reason)
			}
			if got.Reason == syncReasonDisabled {
				t.Error("a jira row was reported github_disabled")
			}
		})
	}
}

// A stored row is folded by its own provider.
func TestRenderIssueSyncFoldsRowProvider(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cfg := config.Default()
	cfg.GitHub.Enabled = false
	res := issuesource.Resolution{Provider: store.IssueProviderGitHub, OriginGitHub: true}
	jira := &store.IssueSyncState{Provider: "jira", OK: true, LastAttemptAt: &now}
	if got := renderIssueSync(cfg, res, jira); got.Provider != "jira" || !got.OK || got.Reason != "" || !got.Enabled {
		t.Errorf("jira row with github off = %+v", got)
	}
	gh := &store.IssueSyncState{Provider: "github", OK: true, LastAttemptAt: &now}
	if got := renderIssueSync(cfg, res, gh); got.Reason != syncReasonDisabled || got.OK {
		t.Errorf("github row with github off = %+v", got)
	}
	// A row predating the provider column is a GitHub import's.
	legacy := &store.IssueSyncState{OK: true, LastAttemptAt: &now}
	if got := renderIssueSync(cfg, res, legacy); got.Provider != "github" || got.Reason != syncReasonDisabled {
		t.Errorf("legacy row = %+v", got)
	}
}

// Project stats carry the same derived reason as the status route.
func TestProjectStatsIssueSyncRowless(t *testing.T) {
	h := newProjectHarness(t)
	id := h.createID(t, map[string]any{"path": repoWithOrigin(t, "https://example.com/o/r.git")})
	resp, out := h.doJSON(t, http.MethodGet, "/v1/projects/"+itoa(id)+"?stats=true", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: %d %s", resp.StatusCode, out)
	}
	var got struct {
		Stats struct {
			IssueSync projectSyncStats `json:"issue_sync"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if got.Stats.IssueSync.Reason != syncReasonNotGitHub || got.Stats.IssueSync.Enabled {
		t.Errorf("stats issue_sync = %+v, want not_github", got.Stats.IssueSync)
	}
}
