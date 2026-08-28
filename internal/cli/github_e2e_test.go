package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github/githubtest"
	"github.com/lezli01/vincent/internal/testrepo"
)

// `--github-issue`, `vincent github`, and the `vincent doctor` row, through
// the real binary against a real detached daemon (task 035).
//
// The daemon finds cmd/fakegh as `gh` because the test prepends its directory
// to PATH — the daemon inherits the environment its parent had, which is §2's
// posture and the reason this is testable at all. GITHUB_TOKEN and GH_TOKEN
// are stripped so a developer's own token cannot change which leg answers.

// runVincentGH is runVincent with the fake `gh` on PATH and the two token
// variables removed.
func runVincentGH(t *testing.T, dataDir, cfgDir, ghDir, scenario string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(vincentBin, args...)
	cmd.Env = append(ghEnv(ghDir, scenario),
		config.EnvDataDir+"="+dataDir,
		config.EnvConfigDir+"="+cfgDir,
	)
	out, err := cmd.CombinedOutput()
	code := cmd.ProcessState.ExitCode()
	if err != nil && code < 0 {
		t.Fatalf("vincent %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), code
}

func ghEnv(ghDir, scenario string) []string {
	out := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case name == "GITHUB_TOKEN" || name == "GH_TOKEN":
			continue
		case strings.EqualFold(name, "PATH"):
			out = append(out, name+"="+ghDir+string(os.PathListSeparator)+value)
		default:
			out = append(out, kv)
		}
	}
	return append(out, "FAKEGH_SCENARIO="+scenario)
}

func gitRemote(t *testing.T, repo, remote string) {
	t.Helper()
	cmd := exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
}

// storedTask is `vincent task show --json` reduced to the fields a create
// decides. The id, branch and timestamps are dropped because two tasks
// necessarily differ in them — everything left is what the two paths are
// claimed to agree on.
type storedTask struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Fields      map[string]string `json:"fields"`
	Workflow    string            `json:"workflow"`
	GitHubIssue json.RawMessage   `json:"github_issue"`
}

// snapshotSansFetchTime drops `fetched_at`, which is the one field two
// creates of the same issue must differ in: it records when *this* task's
// snapshot was taken.
func snapshotSansFetchTime(t *testing.T, task storedTask) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(task.GitHubIssue, &out); err != nil {
		t.Fatalf("issue snapshot is not JSON: %v (%s)", err, task.GitHubIssue)
	}
	delete(out, "fetched_at")
	return out
}

func showTask(t *testing.T, dataDir, cfgDir, ghDir, id string) storedTask {
	t.Helper()
	out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success", "task", "show", id, "--json")
	if code != 0 {
		t.Fatalf("task show %s: code %d, out %q", id, code, out)
	}
	var task storedTask
	if err := json.Unmarshal([]byte(out), &task); err != nil {
		t.Fatalf("task show --json is not JSON: %v (%q)", err, out)
	}
	return task
}

func TestGitHubIssueCommandsAgainstLiveDaemon(t *testing.T) {
	ghDir := filepath.Dir(githubtest.BuildFakeGH(t))
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(os.Environ(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})
	// No adapter: this test never runs a step. All three are pointed at
	// nothing, not just claude, because the doctor subtest below probes every
	// adapter — and cursor's probe is an authenticated network call (§9.7), so
	// a machine with cursor-agent installed would make the request that the
	// report is composed from take longer than the client's 10s budget.
	pointAgentsAtNothing(t, cfgDir)
	if out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success", "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}

	repo := testrepo.Init(t, "main")
	gitRemote(t, repo, "https://github.com/octo/repo.git")
	if out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
		"project", "add", repo, "--json"); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}

	t.Run("github status and issues", func(t *testing.T) {
		out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"github", "status", "--project", "1")
		if code != 0 {
			t.Fatalf("github status: code %d, out %q", code, out)
		}
		if !strings.Contains(out, "octo/repo") || !strings.Contains(out, "readable via gh") {
			t.Errorf("github status does not report a readable repo:\n%s", out)
		}

		out, code = runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"github", "issues", "--project", "1")
		if code != 0 {
			t.Fatalf("github issues: code %d, out %q", code, out)
		}
		for _, want := range []string{"ISSUE", "#200", "#41", "enhancement"} {
			if !strings.Contains(out, want) {
				t.Errorf("github issues does not show %q:\n%s", want, out)
			}
		}

		out, code = runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"github", "issues", "--project", "1", "--json")
		if code != 0 {
			t.Fatalf("github issues --json: code %d, out %q", code, out)
		}
		var listed []struct {
			Number int      `json:"number"`
			Labels []string `json:"labels"`
		}
		if err := json.Unmarshal([]byte(out), &listed); err != nil {
			t.Fatalf("github issues --json is not JSON: %v (%q)", err, out)
		}
		if len(listed) != 2 || listed[0].Number != 200 {
			t.Fatalf("listed = %+v, want #200 first", listed)
		}
		if len(listed[0].Labels) != 2 {
			t.Errorf("labels = %v, want a real list", listed[0].Labels)
		}
	})

	// The claim decision 2 exists to make testable: the flag path and the
	// TUI's previewed-prefill path go through one implementation, so a create
	// that names only the issue and a create that spells out everything the
	// preview showed produce the same stored task.
	t.Run("the flag and a TUI-shaped request agree", func(t *testing.T) {
		out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"task", "add", "--project", "1", "--github-issue", "200", "--json")
		if code != 0 {
			t.Fatalf("task add --github-issue: code %d, out %q", code, out)
		}
		var flagPath struct {
			ID          int64  `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal([]byte(out), &flagPath); err != nil {
			t.Fatalf("task add --json is not JSON: %v (%q)", err, out)
		}

		// What the TUI sends: the same issue, plus every prefilled value it
		// previewed and the human left alone.
		out, code = runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"task", "add", "--project", "1", "--github-issue", "200",
			"--title", flagPath.Title, "--description", flagPath.Description, "--json")
		if code != 0 {
			t.Fatalf("task add (TUI-shaped): code %d, out %q", code, out)
		}
		var formPath struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &formPath); err != nil {
			t.Fatalf("task add --json is not JSON: %v (%q)", err, out)
		}

		a := showTask(t, dataDir, cfgDir, ghDir, strconv.FormatInt(flagPath.ID, 10))
		b := showTask(t, dataDir, cfgDir, ghDir, strconv.FormatInt(formPath.ID, 10))
		if a.Title != b.Title || a.Description != b.Description || a.Workflow != b.Workflow {
			t.Errorf("the two paths stored different tasks:\n %+v\n %+v", a, b)
		}
		if x, y := snapshotSansFetchTime(t, a), snapshotSansFetchTime(t, b); !reflect.DeepEqual(x, y) {
			t.Errorf("the two paths stored different issue snapshots:\n %v\n %v", x, y)
		}
		if !strings.Contains(a.Description, "GitHub issue #200:") {
			t.Errorf("the flag path stored no link line:\n%q", a.Description)
		}
	})

	t.Run("explicit flags override the issue", func(t *testing.T) {
		out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"task", "add", "--project", "1", "--github-issue", "200",
			"--title", "My own framing", "--json")
		if code != 0 {
			t.Fatalf("task add: code %d, out %q", code, out)
		}
		var created struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			GitHubIssue *struct {
				Number int `json:"number"`
			} `json:"github_issue"`
		}
		if err := json.Unmarshal([]byte(out), &created); err != nil {
			t.Fatalf("task add --json is not JSON: %v (%q)", err, out)
		}
		if created.Title != "My own framing" {
			t.Errorf("title = %q, want the explicit flag to win", created.Title)
		}
		// The description was not given, so it is still the issue's.
		if !strings.Contains(created.Description, "GitHub issue #200:") {
			t.Errorf("description = %q, want the issue-derived one", created.Description)
		}
		if created.GitHubIssue == nil || created.GitHubIssue.Number != 200 {
			t.Errorf("snapshot = %+v, want #200 linked either way", created.GitHubIssue)
		}
	})

	t.Run("neither title nor issue is refused", func(t *testing.T) {
		out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"task", "add", "--project", "1")
		if code == 0 {
			t.Fatalf("task add with no title and no issue succeeded: %q", out)
		}
		if !strings.Contains(out, "title") || !strings.Contains(out, "github-issue") {
			t.Errorf("the refusal does not name the two ways to supply a title:\n%s", out)
		}
	})

	t.Run("doctor reports the integration", func(t *testing.T) {
		out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success", "doctor")
		if code != 0 && code != 1 {
			t.Fatalf("doctor: code %d, out %q", code, out)
		}
		if !strings.Contains(out, "GITHUB") {
			t.Errorf("doctor has no GITHUB section:\n%s", out)
		}
		if !strings.Contains(out, "gh cli") || !strings.Contains(out, "readable via gh") {
			t.Errorf("doctor does not report a usable gh:\n%s", out)
		}
	})
}

// TestGitHubUnusableLeavesTaskCreationAlone is the acceptance criterion for a
// machine that cannot read GitHub. The `gh` here is present but logged out and
// no token is set — the same "no credential" a missing `gh` produces, staged
// this way because a test cannot hide a `gh` that is genuinely installed on
// the developer's PATH. The gh-*absent* rendering is covered by
// TestDoctorGitHubRowWithoutGH and by internal/github's own detection test.
func TestGitHubUnusableLeavesTaskCreationAlone(t *testing.T) {
	ghDir := filepath.Dir(githubtest.BuildFakeGH(t))
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(os.Environ(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})
	// Every adapter, for the reason given in the test above: this one reads
	// doctor too.
	pointAgentsAtNothing(t, cfgDir)
	if out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "logged-out",
		"daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}
	repo := testrepo.Init(t, "main")
	gitRemote(t, repo, "https://github.com/octo/repo.git")
	if out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "logged-out",
		"project", "add", repo, "--json"); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}

	// The diagnosis lives beside the other environment checks, and says what
	// is still possible.
	out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "logged-out", "doctor")
	if code != 0 && code != 1 {
		t.Fatalf("doctor: code %d, out %q", code, out)
	}
	if !strings.Contains(out, "unavailable: no GitHub credential") {
		t.Errorf("doctor does not name the reason:\n%s", out)
	}
	if !strings.Contains(out, "tasks can still be created without an issue") {
		t.Errorf("doctor does not say what still works:\n%s", out)
	}

	// And that is true: an ordinary task is unaffected.
	out, code = runVincentGH(t, dataDir, cfgDir, ghDir, "logged-out",
		"task", "add", "--project", "1", "--title", "an ordinary task", "--json")
	if code != 0 {
		t.Fatalf("task add without an issue: code %d, out %q", code, out)
	}

	// Naming an issue is refused with the reason, not with a stack trace.
	out, code = runVincentGH(t, dataDir, cfgDir, ghDir, "logged-out",
		"task", "add", "--project", "1", "--github-issue", "200")
	if code != 1 {
		t.Fatalf("task add --github-issue: code %d, want 1 (out %q)", code, out)
	}
	if !strings.Contains(out, "GitHub is not available") {
		t.Errorf("the refusal does not explain itself:\n%s", out)
	}
}
