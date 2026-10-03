package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The `vincent issue` tree (task 130.6) against the real API handlers.

// runCLIStdin is runCLI with stdin, for `--body-file -`.
func runCLIStdin(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	if err != nil {
		var ee exitError
		if !errors.As(err, &ee) {
			errOut.WriteString("Error: " + err.Error() + "\n")
		}
	}
	return out.String(), errOut.String(), asExitCode(err)
}

// issueJSON runs an issue command with --json and decodes the issue it
// answers with.
func issueJSON(t *testing.T, args ...string) apiclient.Issue {
	t.Helper()
	out, errOut, code := runCLI(t, append(args, "--json")...)
	if code != 0 {
		t.Fatalf("%v: exit %d (%s)", args, code, errOut)
	}
	var iss apiclient.Issue
	if err := json.Unmarshal([]byte(out), &iss); err != nil {
		t.Fatalf("%v --json = %q: %v", args, out, err)
	}
	return iss
}

func issueListJSON(t *testing.T, args ...string) []apiclient.Issue {
	t.Helper()
	out, errOut, code := runCLI(t, append(append([]string{"issue", "ls"}, args...), "--json")...)
	if code != 0 {
		t.Fatalf("issue ls %v: exit %d (%s)", args, code, errOut)
	}
	var list []apiclient.Issue
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("issue ls --json = %q: %v", out, err)
	}
	return list
}

func (h *liveHarness) project() string { return strconv.FormatInt(h.projectID, 10) }

// One issue through its whole life: filed, read, edited, closed and reopened,
// then deleted.
func TestIssueLifecycle(t *testing.T) {
	h := newLiveHarness(t)
	iss := issueJSON(t, "issue", "add", "--project", h.project(), "--title", "the parser drops tabs",
		"--body", "steps to reproduce", "--label", "bug", "--label", "parser", "--kind", "bug", "--priority", "2")
	id := strconv.FormatInt(iss.ID, 10)
	if iss.State != "open" || iss.Kind != "bug" || iss.Priority != 2 || !slices.Equal(iss.Labels, []string{"bug", "parser"}) {
		t.Fatalf("add = %+v", iss)
	}

	out, errOut, code := runCLI(t, "issue", "show", id)
	if code != 0 {
		t.Fatalf("show: exit %d (%s)", code, errOut)
	}
	for _, want := range []string{"the parser drops tabs", "state     open", "kind      bug", "priority  2", "bug, parser", "tasks     0", "steps to reproduce"} {
		if !strings.Contains(out, want) {
			t.Errorf("show does not carry %q:\n%s", want, out)
		}
	}

	iss = issueJSON(t, "issue", "edit", id, "--title", "the parser drops tabs and spaces",
		"--add-label", "urgent", "--remove-label", "parser", "--priority", "0", "--kind", "")
	if iss.Title != "the parser drops tabs and spaces" || iss.Priority != 0 || iss.Kind != "" ||
		!slices.Equal(iss.Labels, []string{"bug", "urgent"}) || iss.Body != "steps to reproduce" {
		t.Errorf("edit = %+v; want the title, a cleared priority and kind, the label delta, the body untouched", iss)
	}

	out, _, code = runCLI(t, "issue", "close", id, "--reason", "not_planned")
	if code != 0 || !strings.Contains(out, "closed (not_planned)") {
		t.Errorf("close: exit %d, %q", code, out)
	}
	if iss = issueJSON(t, "issue", "reopen", id); iss.State != "open" {
		t.Errorf("reopen = %+v", iss)
	}
	if iss = issueJSON(t, "issue", "close", id); iss.State != "closed" || iss.CloseReason != "completed" {
		t.Errorf("close with no reason = %+v; want completed", iss)
	}
	issueJSON(t, "issue", "reopen", id)

	other := issueJSON(t, "issue", "add", "--project", h.project(), "--title", "the original")
	iss = issueJSON(t, "issue", "close", id, "--reason", "duplicate", "--duplicate-of", strconv.FormatInt(other.ID, 10))
	if iss.CloseReason != "duplicate" || iss.DuplicateOf == nil || *iss.DuplicateOf != other.ID {
		t.Errorf("close as duplicate = %+v", iss)
	}
	out, _, _ = runCLI(t, "issue", "show", id)
	if want := "duplicate of #" + strconv.FormatInt(other.ID, 10); !strings.Contains(out, want) {
		t.Errorf("show does not say %q:\n%s", want, out)
	}
	issueJSON(t, "issue", "reopen", id)
	if iss = issueJSON(t, "issue", "close", id, "--reason", "duplicate"); iss.CloseReason != "duplicate" || iss.DuplicateOf != nil {
		t.Errorf("close as duplicate of nothing = %+v", iss)
	}
	// The API is what holds --duplicate-of to reason duplicate.
	issueJSON(t, "issue", "reopen", id)
	if _, errOut, code = runCLI(t, "issue", "close", id, "--duplicate-of", strconv.FormatInt(other.ID, 10)); code != 1 || !strings.HasPrefix(errOut, "Error: ") {
		t.Errorf("--duplicate-of without --reason duplicate: exit %d, %q", code, errOut)
	}

	if _, errOut, code = runCLI(t, "issue", "delete", id); code != 1 || !strings.Contains(errOut, "pass --force") {
		t.Errorf("delete without --force: exit %d, %q", code, errOut)
	}
	if _, _, code = runCLI(t, "issue", "show", id); code != 0 {
		t.Fatal("delete without --force removed the issue")
	}
	if out, _, code = runCLI(t, "issue", "rm", id, "--force"); code != 0 || !strings.Contains(out, "issue "+id+" deleted") {
		t.Errorf("rm --force: exit %d, %q", code, out)
	}
	if _, _, code = runCLI(t, "issue", "show", id); code != 1 {
		t.Errorf("show after delete: exit %d, want 1", code)
	}
	out, _, code = runCLI(t, "issue", "delete", strconv.FormatInt(other.ID, 10), "--force", "--json")
	var reports []deleteReport
	if code != 0 || json.Unmarshal([]byte(out), &reports) != nil || len(reports) != 1 || !reports[0].Deleted {
		t.Errorf("delete --json: exit %d, %q", code, out)
	}
}

// ls without --project spans every project and names it; with it, it
// filters. Every filter reaches the query.
func TestIssueLs(t *testing.T) {
	h := newLiveHarness(t)
	if out, _, code := runCLI(t, "issue", "ls", "--project", h.project(), "--json"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("empty ls --json: exit %d, %q; want []", code, out)
	}
	p := &store.Project{Name: "elsewhere", Path: "/elsewhere", DefaultBranch: "main"}
	if err := h.st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	second := strconv.FormatInt(p.ID, 10)
	a := issueJSON(t, "issue", "add", "--project", h.project(), "--title", "alpha needle", "--label", "bug", "--kind", "bug")
	b := issueJSON(t, "issue", "add", "--project", second, "--title", "beta", "--body", "a needle in the body", "--label", "bug", "--label", "ui")
	c := issueJSON(t, "issue", "add", "--project", second, "--title", "gamma")
	issueJSON(t, "issue", "close", strconv.FormatInt(c.ID, 10))

	out, errOut, code := runCLI(t, "issue", "ls")
	if code != 0 {
		t.Fatalf("ls: exit %d (%s)", code, errOut)
	}
	if !strings.Contains(out, "PROJECT") || !strings.Contains(out, "vincent") || !strings.Contains(out, "elsewhere") ||
		!strings.Contains(out, "closed (completed)") {
		t.Errorf("ls across projects:\n%s", out)
	}
	if out, _, _ = runCLI(t, "issue", "ls", "--project", second); strings.Contains(out, "PROJECT") || strings.Contains(out, "alpha") {
		t.Errorf("ls --project shows the column or another project's issue:\n%s", out)
	}

	ids := func(list []apiclient.Issue) []int64 {
		out := make([]int64, 0, len(list))
		for i := range list {
			out = append(out, list[i].ID)
		}
		slices.Sort(out)
		return out
	}
	for _, tc := range []struct {
		args []string
		want []int64
	}{
		{nil, []int64{a.ID, b.ID, c.ID}},
		{[]string{"--project", second}, []int64{b.ID, c.ID}},
		{[]string{"--state", "closed"}, []int64{c.ID}},
		{[]string{"--state", "open", "--state", "closed"}, []int64{a.ID, b.ID, c.ID}},
		{[]string{"--label", "bug", "--label", "ui"}, []int64{b.ID}},
		{[]string{"--kind", "bug"}, []int64{a.ID}},
		{[]string{"--search", "needle"}, []int64{a.ID, b.ID}},
		{[]string{"--source", "github"}, []int64{}},
		{[]string{"--source", "local", "--limit", "1"}, nil},
	} {
		got := ids(issueListJSON(t, tc.args...))
		if tc.want == nil {
			if len(got) != 1 {
				t.Errorf("ls %v = %v, want one row", tc.args, got)
			}
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("ls %v = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// TestIssueLsByGitHubNumber: `--github N` is the lookup that replaced
// `task add --github-issue` (task 130.11, decision 22.4). It needs --project,
// answers only that project's issue imported from #N, and the id it answers
// with is what `task add --issue` takes.
func TestIssueLsByGitHubNumber(t *testing.T) {
	h := newLiveHarness(t)
	p := &store.Project{Name: "elsewhere", Path: "/elsewhere", DefaultBranch: "main"}
	if err := h.st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	imp := func(pid int64, key string, number int) *store.Issue {
		t.Helper()
		iss, _, err := h.st.UpsertRemoteIssue(t.Context(), store.RemoteIssue{
			ProjectID: pid, Provider: "github", RemoteKey: key, Repo: "o/r", Number: number,
			URL: "https://github.com/o/r/issues/" + strconv.Itoa(number), RemoteJSON: `{"state":"OPEN"}`,
			Title: "imported " + key, State: issuestate.Open,
		}, issuestate.Sync)
		if err != nil {
			t.Fatalf("UpsertRemoteIssue: %v", err)
		}
		return iss
	}
	want := imp(h.projectID, "I_12", 12)
	imp(h.projectID, "I_13", 13)
	imp(p.ID, "I_12_elsewhere", 12)

	got := issueListJSON(t, "--project", h.project(), "--github", "12")
	if len(got) != 1 || got[0].ID != want.ID {
		t.Errorf("ls --github 12 = %+v, want only issue %d", got, want.ID)
	}
	if got := issueListJSON(t, "--project", h.project(), "--github", "99"); len(got) != 0 {
		t.Errorf("ls --github 99 = %+v, want nothing", got)
	}
	for _, args := range [][]string{
		{"issue", "ls", "--github", "12"},
		{"issue", "ls", "--project", h.project(), "--github", "0"},
	} {
		_, errOut, code := runCLI(t, args...)
		if code == 0 || !strings.Contains(errOut, "--github") {
			t.Errorf("%v: exit %d, stderr %q; want a refusal naming --github", args, code, errOut)
		}
	}
}

// --body-file reads a file or stdin byte for byte, refuses more than 4 MiB,
// and cannot be combined with --body.
func TestIssueAddBodyFile(t *testing.T) {
	h := newLiveHarness(t)
	out, errOut, code := runCLIStdin(t, "line one\n\tline two\n",
		"issue", "add", "--project", h.project(), "--title", "from stdin", "--body-file", "-", "--json")
	var iss apiclient.Issue
	if code != 0 || json.Unmarshal([]byte(out), &iss) != nil || iss.Body != "line one\n\tline two\n" {
		t.Fatalf("--body-file -: exit %d, %q (%s)", code, out, errOut)
	}
	_, errOut, code = runCLIStdin(t, strings.Repeat("x", maxInputFileBytes+1),
		"issue", "add", "--project", h.project(), "--title", "too big", "--body-file", "-")
	if code != 1 || !strings.Contains(errOut, "must be at most") {
		t.Errorf("oversize body: exit %d, %q", code, errOut)
	}
	_, errOut, code = runCLI(t, "issue", "add", "--project", h.project(), "--title", "both", "--body", "x", "--body-file", "-")
	if code != 1 || !strings.Contains(errOut, "body") {
		t.Errorf("--body with --body-file: exit %d, %q", code, errOut)
	}
	if list := issueListJSON(t); len(list) != 1 {
		t.Errorf("refused adds left issues behind: %d issues", len(list))
	}
}

// The same --idempotency-key returns the first run's issue; without one,
// two runs file two issues.
func TestIssueAddIdempotencyKey(t *testing.T) {
	h := newLiveHarness(t)
	args := []string{"issue", "add", "--project", h.project(), "--title", "once", "--idempotency-key", "k-1"}
	first, second := issueJSON(t, args...), issueJSON(t, args...)
	if first.ID != second.ID {
		t.Errorf("same key filed %d and %d", first.ID, second.ID)
	}
	plain := []string{"issue", "add", "--project", h.project(), "--title", "twice"}
	if a, b := issueJSON(t, plain...), issueJSON(t, plain...); a.ID == b.ID {
		t.Errorf("no key, yet both runs returned issue %d", a.ID)
	}
	if list := issueListJSON(t); len(list) != 3 {
		t.Errorf("got %d issues, want 3", len(list))
	}
}

// edit sends the version it read: a change in between is refused with a
// line saying to re-run, and nothing is retried. An edit naming no field
// never reaches the daemon.
func TestIssueEditStaleVersion(t *testing.T) {
	h := newLiveHarness(t)
	iss := issueJSON(t, "issue", "add", "--project", h.project(), "--title", "original")
	id := strconv.FormatInt(iss.ID, 10)

	if _, errOut, code := runCLI(t, "issue", "edit", id); code != 1 || !strings.Contains(errOut, "nothing to change") {
		t.Errorf("edit with no flags: exit %d, %q", code, errOut)
	}

	t.Cleanup(func() { beforeIssuePatch = func() {} })
	beforeIssuePatch = func() {
		beforeIssuePatch = func() {}
		if _, errOut, code := runCLI(t, "issue", "edit", id, "--title", "someone else's"); code != 0 {
			t.Errorf("concurrent edit: exit %d (%s)", code, errOut)
		}
	}
	_, errOut, code := runCLI(t, "issue", "edit", id, "--title", "mine")
	if code != 1 || !strings.Contains(errOut, "changed since it was read; re-run") {
		t.Errorf("stale edit: exit %d, %q", code, errOut)
	}
	if got := issueJSON(t, "issue", "show", id); got.Title != "someone else's" {
		t.Errorf("title = %q; the stale edit was applied or retried", got.Title)
	}
}

// labels lists the project's catalogue with counts.
func TestIssueLabels(t *testing.T) {
	h := newLiveHarness(t)
	issueJSON(t, "issue", "add", "--project", h.project(), "--title", "a", "--label", "bug")
	issueJSON(t, "issue", "add", "--project", h.project(), "--title", "b", "--label", "bug", "--label", "ui")

	out, errOut, code := runCLI(t, "issue", "labels", "--project", h.project())
	if code != 0 || !strings.Contains(out, "NAME") || !strings.Contains(out, "ISSUES") {
		t.Fatalf("labels: exit %d, %q (%s)", code, out, errOut)
	}
	out, _, _ = runCLI(t, "issue", "labels", "--project", h.project(), "--json")
	var labels []apiclient.IssueLabel
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		t.Fatalf("labels --json = %q: %v", out, err)
	}
	counts := map[string]int{}
	for _, l := range labels {
		counts[l.Name] = l.IssueCount
	}
	if counts["bug"] != 2 || counts["ui"] != 1 {
		t.Errorf("label counts = %v", counts)
	}
}
