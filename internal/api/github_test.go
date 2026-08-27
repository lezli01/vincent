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
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// The §13.2 GitHub endpoints and `github_issue` on POST /v1/tasks (task 035),
// against the real handlers over httptest.
//
// The daemon's GitHub client is pointed at cmd/fakegh, so these run on
// Windows, macOS and Linux and none of them touches the network. The fake
// records its argv, which is how "with the integration disabled, no GitHub
// call is made" is *asserted* rather than assumed.

// issueWorkflowYAML declares the three names the prefill maps plus two it
// must not touch: `notes` is undeclared-adjacent (declared, but not one of
// the three) and `ticket` carries a pattern no issue value satisfies.
const issueWorkflowYAML = `name: fix-issue
description: Fix a reported issue.
fields:
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

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
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
	var out []githubIssueResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("list body: %v (%s)", err, body)
	}
	if len(out) != 2 {
		t.Fatalf("listed %d issues, want 2", len(out))
	}
	if out[0].Number != 200 {
		t.Errorf("first issue = #%d, want the newest (#200)", out[0].Number)
	}
	if out[0].Prefill != nil {
		t.Error("a listing that named no workflow carried a prefill")
	}
	// The state and limit the daemon actually asked gh for.
	if calls := h.ghCalls(t); !strings.Contains(calls, "--state open") {
		t.Errorf("gh was not asked for open issues:\n%s", calls)
	}
}

// TestGitHubIssuesPrefill is decision 7, end to end: exact-name matches only,
// type-and-pattern-valid values only, and the link line appended as its own
// trailing block.
func TestGitHubIssuesPrefill(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	writeWorkflowFile(t, h.globalDir, "fix-issue", issueWorkflowYAML)
	h.reg.ReloadGlobal()

	resp, body := h.issues(t, "workflow=fix-issue")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	var out []githubIssueResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("list body: %v (%s)", err, body)
	}
	prefill := out[0].Prefill
	if prefill == nil {
		t.Fatal("no prefill on a listing that named a workflow")
	}
	if prefill.Title != out[0].Title {
		t.Errorf("prefill title = %q, want the issue title %q", prefill.Title, out[0].Title)
	}
	wantTail := "\n\nGitHub issue #200: https://github.com/octo/repo/issues/200"
	if !strings.HasSuffix(prefill.Description, wantTail) {
		t.Errorf("description does not end in the link block:\n%q", prefill.Description)
	}
	if !strings.HasPrefix(prefill.Description, out[0].Body) {
		t.Errorf("description does not start with the issue body:\n%q", prefill.Description)
	}
	if got := prefill.Fields["labels"]; got != "enhancement, area/api" {
		t.Errorf("labels = %q, want the comma-joined list", got)
	}
	if got := prefill.Fields["assignee"]; got != "hubot" {
		t.Errorf("assignee = %q, want hubot", got)
	}
	// Declared `integer`, so the milestone *number*, not its title.
	if got := prefill.Fields["milestone"]; got != "4" {
		t.Errorf("milestone = %q, want the number 4 for an integer field", got)
	}
	// `notes` is declared but is not one of the three names, and `ticket`
	// declares a pattern nothing an issue offers satisfies. Neither is
	// invented, and neither is filled with a value the create call would 400
	// on.
	for _, name := range []string{"notes", "ticket"} {
		if value, ok := prefill.Fields[name]; ok {
			t.Errorf("prefill invented %s = %q", name, value)
		}
	}

	// An issue with no metadata leaves every declared field empty rather than
	// filling it with blanks.
	bare := out[1].Prefill
	if bare == nil {
		t.Fatal("the second issue carried no prefill")
	}
	if len(bare.Fields) != 0 {
		t.Errorf("an issue with no labels/assignee/milestone prefilled %v", bare.Fields)
	}
	if bare.Description != "GitHub issue #41: https://github.com/octo/repo/issues/41" {
		t.Errorf("an empty body produced %q, want the link line alone", bare.Description)
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

func TestGitHubIssuesUnknownWorkflow(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.issues(t, "workflow=no-such-workflow")
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
}

// TestCreateTaskFromAnIssue is the create path: the daemon fetches, prefills
// what the request left unset, and persists the snapshot on the row.
func TestCreateTaskFromAnIssue(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	writeWorkflowFile(t, h.globalDir, "fix-issue", issueWorkflowYAML)
	h.reg.ReloadGlobal()

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "workflow": "fix-issue", "github_issue": 200,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var tr taskResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatalf("task body: %v (%s)", err, body)
	}
	if tr.Title != "GitHub integration: select a GitHub issue when creating a task" {
		t.Errorf("title = %q, want the issue's", tr.Title)
	}
	if !strings.HasSuffix(tr.Description,
		"GitHub issue #200: https://github.com/octo/repo/issues/200") {
		t.Errorf("description does not end in the link line:\n%q", tr.Description)
	}
	if tr.Fields["labels"] != "enhancement, area/api" || tr.Fields["milestone"] != "4" {
		t.Errorf("fields = %v, want the mapped prefill", tr.Fields)
	}
	if tr.GitHubIssue == nil {
		t.Fatal("the created task carries no issue snapshot")
	}
	if tr.GitHubIssue.Number != 200 || tr.GitHubIssue.Repo != "octo/repo" {
		t.Errorf("snapshot = %+v, want #200 of octo/repo", tr.GitHubIssue)
	}
	if tr.GitHubIssue.FetchedAt.IsZero() {
		t.Error("the snapshot carries no fetched_at; a task cannot say how old it is")
	}
	// And it is on the row, not merely in the response.
	stored, err := h.store.GetTask(t.Context(), tr.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.GitHubIssue == nil || stored.GitHubIssue.Number != 200 {
		t.Errorf("stored issue = %+v", stored.GitHubIssue)
	}
}

// TestCreateTaskExplicitValuesWinOverTheIssue is decision 2's precedence rule
// — the one that makes the CLI flag and the TUI's previewed prefill produce
// the same stored task, and that keeps "nothing is locked" true.
func TestCreateTaskExplicitValuesWinOverTheIssue(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	writeWorkflowFile(t, h.globalDir, "fix-issue", issueWorkflowYAML)
	h.reg.ReloadGlobal()

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id":   h.projectID,
		"workflow":     "fix-issue",
		"github_issue": 200,
		"title":        "My own title",
		"description":  "My own description",
		// Present with an empty value: a row the human cleared on purpose.
		// Presence wins, so the daemon must not put the prefill back.
		"fields": map[string]string{"labels": "", "assignee": "someone-else"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var tr taskResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatalf("task body: %v (%s)", err, body)
	}
	if tr.Title != "My own title" || tr.Description != "My own description" {
		t.Errorf("explicit title/description lost: %q / %q", tr.Title, tr.Description)
	}
	if tr.Fields["labels"] != "" {
		t.Errorf("labels = %q, want the cleared value to stand", tr.Fields["labels"])
	}
	if tr.Fields["assignee"] != "someone-else" {
		t.Errorf("assignee = %q, want the explicit value", tr.Fields["assignee"])
	}
	// A field the request said nothing about is still prefilled.
	if tr.Fields["milestone"] != "4" {
		t.Errorf("milestone = %q, want the prefill for an unmentioned field", tr.Fields["milestone"])
	}
	// The snapshot is stored either way: it is what `.Issue` renders from.
	if tr.GitHubIssue == nil || tr.GitHubIssue.Number != 200 {
		t.Errorf("snapshot = %+v", tr.GitHubIssue)
	}
}

// TestCreateTaskWithAnUnknownIssue: the reason travels, `gh`'s text does not.
func TestCreateTaskWithAnUnknownIssue(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "github_issue": 9999,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", resp.StatusCode, body)
	}
	var e errorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body: %v (%s)", err, body)
	}
	if e.Error.Details["reason"] != github.ReasonNotFound {
		t.Errorf("details = %v, want reason %q", e.Error.Details, github.ReasonNotFound)
	}
}

func TestCreateTaskRejectsANonPositiveIssueNumber(t *testing.T) {
	h := newGitHubHarness(t, nil, ghOrigin)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "github_issue": 0, "title": "t",
	})
	// 0 is JSON's way of saying "absent" only for a value type; the field is
	// a pointer, so an explicit 0 is a request to link issue zero.
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
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

// TestCreateTaskFromAnIssueRefusedWhenDisabled: the toggle is the whole gate,
// and it is enforced on the create path too, not only in the picker.
func TestCreateTaskFromAnIssueRefusedWhenDisabled(t *testing.T) {
	off := func() config.Config {
		c := config.Default()
		c.GitHub.Enabled = false
		return c
	}
	h := newGitHubHarness(t, off, ghOrigin)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID, "github_issue": 200,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", resp.StatusCode, body)
	}
	if calls := h.ghCalls(t); calls != "" {
		t.Errorf("a disabled integration still invoked gh:\n%s", calls)
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
