package taskrun

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// $VINCENT_ISSUE_FILE (§8.5, 130.14): the task's snapshot as `gh issue view
// --json` spells it, written from the row and never from the network.

// TestIssueFileMapping is the table the brief asks for: local, imported,
// tombstoned-remote and legacy snapshots all produce gh's keys, with
// vincent's extras beside them.
func TestIssueFileMapping(t *testing.T) {
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	closed := localSnapshot()
	closed.State, closed.CloseReason, closed.CreatedAt = "closed", "not_planned", created
	tomb := importedSnapshot()
	tomb.Remote, tomb.URL = nil, ""

	for _, tc := range []struct {
		name string
		task *store.Task
		want string
		env  workflow.IssueEnv
	}{
		{
			"local", &store.Task{Issue: localSnapshot()},
			`{"number":null,"title":"Daemon leaks the lock file","body":"Seen twice.","url":"",` +
				`"createdAt":null,"state":"OPEN","stateReason":null,"labels":[{"name":"bug"}],` +
				`"author":{"login":"lezli01"},"comments":[],"id":12,"kind":"bug","priority":2,"source":null}`,
			workflow.IssueEnv{ID: 12},
		},
		{
			"closed local", &store.Task{Issue: closed},
			`{"number":null,"title":"Daemon leaks the lock file","body":"Seen twice.","url":"",` +
				`"createdAt":"2025-01-02T03:04:05Z","state":"CLOSED","stateReason":"NOT_PLANNED",` +
				`"labels":[{"name":"bug"}],"author":{"login":"lezli01"},"comments":[],"id":12,` +
				`"kind":"bug","priority":2,"source":null}`,
			workflow.IssueEnv{ID: 12},
		},
		{
			"imported", &store.Task{Issue: importedSnapshot()},
			`{"number":200,"title":"Select a GitHub issue","body":"",` +
				`"url":"https://github.com/lezli01/vincent/issues/200","createdAt":null,"state":"OPEN",` +
				`"stateReason":null,"labels":[{"name":"enhancement"}],"author":{"login":"lezli01"},` +
				`"comments":[],"id":13,"kind":"","priority":0,"source":{"provider":"github",` +
				`"repo":"lezli01/vincent","number":200,"url":"https://github.com/lezli01/vincent/issues/200"}}`,
			workflow.IssueEnv{ID: 13, Number: 200, URL: "https://github.com/lezli01/vincent/issues/200"},
		},
		{
			"tombstoned remote", &store.Task{Issue: tomb},
			`{"number":null,"title":"Select a GitHub issue","body":"","url":"","createdAt":null,` +
				`"state":"OPEN","stateReason":null,"labels":[{"name":"enhancement"}],` +
				`"author":{"login":"lezli01"},"comments":[],"id":13,"kind":"","priority":0,"source":null}`,
			workflow.IssueEnv{ID: 13},
		},
		{
			"legacy", legacyTask(storedIssue()),
			`{"number":200,"title":"Select a GitHub issue when creating a task",` +
				`"body":"The body as it was when the task was created.",` +
				`"url":"https://github.com/lezli01/vincent/issues/200","createdAt":null,"state":"OPEN",` +
				`"stateReason":null,"labels":[{"name":"enhancement"},{"name":"area/api"}],` +
				`"author":{"login":"lezli01"},"comments":[],"id":null,"kind":"","priority":0,` +
				`"source":{"provider":"github","repo":"lezli01/vincent","number":200,` +
				`"url":"https://github.com/lezli01/vincent/issues/200"}}`,
			workflow.IssueEnv{Number: 200, URL: "https://github.com/lezli01/vincent/issues/200"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, env, ok := issueFileOf(tc.task)
			if !ok {
				t.Fatal("a linked task produced no file")
			}
			b, err := json.Marshal(f)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Errorf("file =\n %s\nwant\n %s", b, tc.want)
			}
			if env != tc.env {
				t.Errorf("env = %+v, want %+v", env, tc.env)
			}
		})
	}

	if _, _, ok := issueFileOf(&store.Task{ID: 7}); ok {
		t.Error("an unlinked task produced a file")
	}
}

// TestUnlinkedTaskWritesNoIssueFile: no snapshot, no file, no variables —
// and no need for a worktree to resolve.
func TestUnlinkedTaskWritesNoIssueFile(t *testing.T) {
	ie, err := writeIssueFile(&store.Task{ID: 7})
	if err != nil || ie != (workflow.IssueEnv{}) {
		t.Errorf("writeIssueFile(unlinked) = %+v, %v; want the zero value", ie, err)
	}
}

// TestLaneGetsTheParentsIssueFile: a fan-out lane's file is written from the
// snapshot it inherited, so a lane reads the issue without fetching it.
func TestLaneGetsTheParentsIssueFile(t *testing.T) {
	r := &Runner{}
	parent := &store.Task{ID: 7, ProjectID: 1, Title: "root", BranchName: "b", Issue: importedSnapshot()}
	env := &stepEnv{
		task: parent,
		wf:   &workflow.Workflow{Name: "root"},
		step: workflow.Step{ID: "build", Type: workflow.StepFanOut},
	}
	lane, err := r.laneTask(env, workflow.Lane{
		ID:    "api",
		Steps: []workflow.Step{{ID: "work", Type: workflow.StepCommand, Run: "exit 0"}},
	}, 0)
	if err != nil {
		t.Fatalf("laneTask: %v", err)
	}
	gitDir := filepath.Join(t.TempDir(), "repo", ".git", "worktrees", "lane")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lane.WorktreePath = filepath.Join(t.TempDir(), "lane")
	if err := os.MkdirAll(lane.WorktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	// Git writes forward slashes on every platform.
	gitFile := "gitdir: " + filepath.ToSlash(gitDir) + "\n"
	if err := os.WriteFile(filepath.Join(lane.WorktreePath, ".git"), []byte(gitFile), 0o644); err != nil {
		t.Fatal(err)
	}
	ie, err := writeIssueFile(lane)
	if err != nil {
		t.Fatalf("writeIssueFile: %v", err)
	}
	if want := filepath.Join(gitDir, issueFileName); ie.File != want {
		t.Errorf("File = %q, want %q", ie.File, want)
	}
	if ie.ID != 13 || ie.Number != 200 {
		t.Errorf("env = %+v, want the parent's issue", ie)
	}
	raw, err := os.ReadFile(ie.File)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Number != 200 || got.Title != "Select a GitHub issue" {
		t.Errorf("lane file = %s (%v)", raw, err)
	}
}

// TestCommandStepReadsTheIssueFile is the end-to-end half: a command step on
// a linked task finds the file through $VINCENT_ISSUE_FILE, reads the
// snapshot from it, and the file never shows up in the worktree's status.
func TestCommandStepReadsTheIssueFile(t *testing.T) {
	h := newEngineHarnessWith(t, nil)
	snap := &store.IssueSnapshot{
		ID: 21, Title: "Expose the issue snapshot", Body: "Read it from a file.",
		State: "open", Kind: "feature", Labels: []string{"taskrun", "workflow"}, Author: "lezli01",
	}
	wf := "name: issuefile\nsteps:\n" + commandStep("read", script(
		`cat "$VINCENT_ISSUE_FILE" > issue.txt && echo "$VINCENT_ISSUE_FILE|$VINCENT_ISSUE_ID|$VINCENT_ISSUE_NUMBER" > vars.txt`,
		//nolint:lll // two PowerShell statements; splitting changes nothing but readability
		"Get-Content -Raw $env:VINCENT_ISSUE_FILE | Out-File -Encoding ascii issue.txt; \"$env:VINCENT_ISSUE_FILE|$env:VINCENT_ISSUE_ID|$env:VINCENT_ISSUE_NUMBER\" | Out-File -Encoding ascii vars.txt",
	))
	task := h.createTaskWith(t, wf, func(task *store.Task) { task.Issue = snap })
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}

	raw, err := os.ReadFile(filepath.Join(done.WorktreePath, "issue.txt"))
	if err != nil {
		t.Fatalf("read issue.txt: %v", err)
	}
	var got struct {
		Number *int                `json:"number"`
		Title  string              `json:"title"`
		Body   string              `json:"body"`
		State  string              `json:"state"`
		Labels []map[string]string `json:"labels"`
		ID     int64               `json:"id"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the step's copy is not JSON: %v\n%s", err, raw)
	}
	if got.Title != snap.Title || got.Body != snap.Body || got.State != "OPEN" || got.ID != 21 || got.Number != nil {
		t.Errorf("file as the step read it = %+v", got)
	}
	if !reflect.DeepEqual(got.Labels, []map[string]string{{"name": "taskrun"}, {"name": "workflow"}}) {
		t.Errorf("labels = %v", got.Labels)
	}

	rawVars, err := os.ReadFile(filepath.Join(done.WorktreePath, "vars.txt"))
	if err != nil {
		t.Fatalf("read vars.txt: %v", err)
	}
	vars := strings.Split(strings.TrimSpace(string(rawVars)), "|")
	if len(vars) != 3 || vars[1] != "21" || vars[2] != "" {
		t.Fatalf("variables = %q, want FILE|21| (no NUMBER for a local issue)", vars)
	}
	if !filepath.IsAbs(vars[0]) || filepath.Base(vars[0]) != issueFileName {
		t.Errorf("VINCENT_ISSUE_FILE = %q, want an absolute path to %s", vars[0], issueFileName)
	}

	// Outside the working tree: `git add -A` stages the step's two outputs
	// and nothing else.
	gitIn(t, done.WorktreePath, "add", "-A")
	staged := strings.Fields(gitIn(t, done.WorktreePath, "diff", "--cached", "--name-only"))
	slices.Sort(staged)
	if !slices.Equal(staged, []string{"issue.txt", "vars.txt"}) {
		t.Errorf("git add -A staged %v, want only the step's outputs", staged)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
