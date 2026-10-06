package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/github/githubtest"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// The §13.2 GitHub issue endpoints (task 035), and the removal of the
// `github_issue` create field (task 130.11), against the real handlers over httptest.
//
// The daemon's GitHub client is pointed at cmd/fakegh, so these run on
// Windows, macOS and Linux and none of them touches the network. The fake
// records its argv, which is how "with the integration disabled, no GitHub
// call is made" is *asserted* rather than assumed.

// issueWorkflowYAML declares the four names the prefill maps plus two it
// must not touch: `notes` is undeclared-adjacent (declared, but not one of
// the four) and `ticket` carries a pattern no issue value satisfies.
const issueWorkflowYAML = `name: fix-issue
description: Fix a reported issue.
fields:
  - name: issue
    type: integer
  - name: labels
  - name: assignee
  - name: milestone
    type: integer
  - name: notes
  - name: ticket
    pattern: '^OPS-[0-9]+$'
steps:
  - {id: approve, type: manual, instructions: review}
`

type githubHarness struct {
	*projectHarness
	reg       *workflow.Registry
	globalDir string
	repo      string
	projectID int64
	argvLog   string
}

// newGitHubHarness builds a server whose project has a github.com origin and
// whose GitHub client is the fake `gh`. remote lets a test give the project a
// non-GitHub origin instead.
func newGitHubHarness(t *testing.T, cfg func() config.Config, remote string) *githubHarness {
	t.Helper()
	fake := githubtest.BuildFakeGH(t)
	argvLog := filepath.Join(t.TempDir(), "gh-argv.txt")
	t.Setenv("FAKEGH_ARGV_FILE", argvLog)
	if os.Getenv("FAKEGH_SCENARIO") == "" {
		t.Setenv("FAKEGH_SCENARIO", "success")
	}

	st, err := storetest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	wt := worktree.NewManager(git, t.TempDir())
	globalDir := filepath.Join(t.TempDir(), "workflows")
	reg := workflow.NewRegistry(globalDir, workflow.Options{}, nil)
	if cfg == nil {
		cfg = config.Default
	}
	s := New(Deps{
		Token:       testToken,
		Config:      cfg,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Git:         git,
		Worktrees:   wt,
		Workflows:   reg,
		GitHub: github.New(github.Options{
			GHPath: fake,
			// No token in the environment: the `gh` leg is the one under
			// test, and a stray GITHUB_TOKEN on a developer's machine must
			// not change which leg answers.
			Getenv: func(string) string { return "" },
		}),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	h := &githubHarness{
		projectHarness: &projectHarness{ts: ts, store: st, wt: wt},
		reg:            reg, globalDir: globalDir, argvLog: argvLog,
	}
	h.repo = testrepo.Init(t, "main")
	addRemote(t, h.repo, remote)
	project := h.mustCreate(t, map[string]any{"path": h.repo})
	h.projectID = int64(project["id"].(float64))
	return h
}

func addRemote(t *testing.T, repo, remote string) {
	t.Helper()
	if remote == "" {
		return
	}
	cmd := exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
}

func (h *githubHarness) ghCalls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.argvLog)
	if err != nil {
		return ""
	}
	return string(b)
}

func (h *githubHarness) status(t *testing.T) githubResponse {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet,
		"/v1/projects/"+strconv.FormatInt(h.projectID, 10)+"/github", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe: %d %s", resp.StatusCode, body)
	}
	var out githubResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("probe body: %v (%s)", err, body)
	}
	return out
}

const ghOrigin = "https://github.com/octo/repo.git"

// TestGitHubProbeAvailable is the happy answer: a github.com origin, the
// integration on, and a credential that works.
func TestGitHubProbeAvailable(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	got := h.status(t)
	if !got.Enabled || !got.Available {
		t.Fatalf("probe = %+v, want enabled and available", got)
	}
	if got.Repo != "octo/repo" {
		t.Errorf("repo = %q, want octo/repo derived from origin", got.Repo)
	}
	if got.Via != github.ViaGH {
		t.Errorf("via = %q, want gh", got.Via)
	}
}

// TestGitHubProbeDisabled: the toggle is off, so the answer is "disabled" —
// and, asserted rather than assumed, no `gh` process ran at all.
func TestGitHubProbeDisabled(t *testing.T) {
	off := func() config.Config {
		c := config.Default()
		c.GitHub.Enabled = false
		return c
	}
	h := newGitHubHarness(t, off, ghOrigin)
	got := h.status(t)
	if got.Enabled || got.Available {
		t.Fatalf("probe = %+v, want disabled and unavailable", got)
	}
	if got.Reason != github.ReasonDisabled {
		t.Errorf("reason = %q, want %q", got.Reason, github.ReasonDisabled)
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("a disabled integration still invoked gh:\n%s", calls)
	}
}

// TestGitHubProbeNotAGitHubProject: a GitLab origin is not a GitHub project,
// and it is never probed for a credential either — the gate stops at the
// first "no".
func TestGitHubProbeNotAGitHubProject(t *testing.T) {
	h := newGitHubHarness(t, nil, "https://gitlab.com/octo/repo.git")
	got := h.status(t)
	if !got.Enabled {
		t.Error("the integration reads as disabled; it is on, this project is simply not GitHub")
	}
	if got.Available || got.Reason != github.ReasonNotGitHub {
		t.Fatalf("probe = %+v, want unavailable with %q", got, github.ReasonNotGitHub)
	}
	if got.Repo != "" {
		t.Errorf("repo = %q, want empty", got.Repo)
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("a non-GitHub project still invoked gh:\n%s", calls)
	}
}

// TestGitHubProbeNoRemoteAtAll: a repository with no `origin` is the same
// answer. `git remote get-url origin` failing is not an error to report — it
// is what "not GitHub-based" looks like for most local repositories.
func TestGitHubProbeNoRemoteAtAll(t *testing.T) {
	h := newGitHubHarness(t, nil, "")
	if got := h.status(t); got.Available || got.Reason != github.ReasonNotGitHub {
		t.Fatalf("probe = %+v, want unavailable with %q", got, github.ReasonNotGitHub)
	}
}

// TestGitHubProbeUnavailableWithAReason: origin is GitHub and the integration
// is on, but nothing can authenticate. The reason is named, and it is the one
// `vincent doctor` explains.
func TestGitHubProbeUnavailableWithAReason(t *testing.T) {
	t.Setenv("FAKEGH_SCENARIO", "logged-out")
	h := newGitHubHarness(t, nil, ghOrigin)
	got := h.status(t)
	if got.Available {
		t.Fatalf("probe = %+v, want unavailable", got)
	}
	if got.Reason != github.ReasonNoCredential {
		t.Errorf("reason = %q, want %q", got.Reason, github.ReasonNoCredential)
	}
	if got.Message == "" {
		t.Error("an unavailable probe carries no message")
	}
	if strings.Contains(got.Message, "gh auth login") {
		t.Errorf("the probe leaked gh's own stderr: %q", got.Message)
	}
}

func (h *githubHarness) issues(t *testing.T, query string) (*http.Response, []byte) {
	t.Helper()
	path := "/v1/projects/" + strconv.FormatInt(h.projectID, 10) + "/github/issues"
	if query != "" {
		path += "?" + query
	}
	return h.doJSON(t, http.MethodGet, path, nil)
}

func TestGitHubIssuesList(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.issues(t, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var out []github.Issue
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("list body: %v (%s)", err, body)
	}
	if len(out) != 2 {
		t.Fatalf("listed %d issues, want 2", len(out))
	}
	if out[0].Number != 200 {
		t.Errorf("first issue = #%d, want the newest (#200)", out[0].Number)
	}
	// The state and limit the daemon actually asked gh for.
	if calls := h.ghCalls(t); !strings.Contains(calls, "--state open") {
		t.Errorf("gh was not asked for open issues:\n%s", calls)
	}
}

// TestGitHubIssuesListHasNoPrefill: the per-row `workflow` prefill went with
// the `github_issue` create field (task 130.11, decision 7). The raw listing
// stays, and a `workflow` parameter — known or not — changes nothing.
func TestGitHubIssuesListHasNoPrefill(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	writeWorkflowFile(t, h.globalDir, "fix-issue", issueWorkflowYAML)
	h.reg.ReloadGlobal()
	for _, query := range []string{"workflow=fix-issue", "workflow=no-such-workflow"} {
		resp, body := h.issues(t, query)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", query, resp.StatusCode, body)
		}
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("%s: list body: %v (%s)", query, err, body)
		}
		if len(rows) != 2 {
			t.Fatalf("%s: listed %d issues, want 2", query, len(rows))
		}
		for _, row := range rows {
			if _, ok := row["prefill"]; ok {
				t.Errorf("%s: a row still carries a prefill: %v", query, row)
			}
		}
	}
}

// TestGitHubIssuesRefusedWhenUnavailable: the §13.1 envelope, with the reason
// in `details` where a client can branch on it, and never `gh`'s own text.
func TestGitHubIssuesRefusedWhenUnavailable(t *testing.T) {
	t.Setenv("FAKEGH_SCENARIO", "logged-out")
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.issues(t, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", resp.StatusCode, body)
	}
	var e errorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body: %v (%s)", err, body)
	}
	if e.Error.Code != CodeInvalidState {
		t.Errorf("code = %q, want %q", e.Error.Code, CodeInvalidState)
	}
	if e.Error.Details["reason"] != github.ReasonNoCredential {
		t.Errorf("details = %v, want reason %q", e.Error.Details, github.ReasonNoCredential)
	}
}

// TestGitHubIssuesRejectsABadLimit: a client's own input, so a 400 rather
// than a call to GitHub with a nonsense bound.
func TestGitHubIssuesRejectsABadLimit(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.issues(t, "limit=0")
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	resp, body = h.issues(t, "limit=lots")
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
}

// TestCreateTaskRefusesGitHubIssue is task 130 decision 7's hard removal:
// `github_issue` is no longer a create field, so an old client's body is an
// unknown field — DisallowUnknownFields' 400, which decodeFailed reports as
// validation_failed naming the field (JSON that parses but does not fit, as
// opposed to invalid_json's unparseable body) — and nothing is fetched from
// GitHub on its behalf.
func TestCreateTaskRefusesGitHubIssue(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	writeWorkflowFile(t, h.globalDir, "fix-issue", issueWorkflowYAML)
	h.reg.ReloadGlobal()

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "workflow": "fix-issue", "github_issue": 200,
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(body), `unknown field \"github_issue\"`) {
		t.Errorf("the 400 does not name the unknown field: %s", body)
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("a refused github_issue create still invoked gh:\n%s", calls)
	}
	tasks, err := h.store.ListTasks(t.Context(), store.TaskFilter{})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("a refused create left %d task(s) behind", len(tasks))
	}
}

// TestCreateTaskWithoutAnIssueMakesNoGitHubCall: the ordinary path is
// untouched by this feature, asserted at the process level.
func TestCreateTaskWithoutAnIssueMakesNoGitHubCall(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "title": "an ordinary task",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("creating a task without an issue invoked gh:\n%s", calls)
	}
}

// TestGitHubEndpointsOnAMissingProject keeps the two new routes on §13.1's
// existing 404 shape rather than inventing one.
func TestGitHubEndpointsOnAMissingProject(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	for _, path := range []string{
		"/v1/projects/9999/github",
		"/v1/projects/9999/github/issues",
	} {
		resp, body := h.doJSON(t, http.MethodGet, path, nil)
		wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
	}
}
