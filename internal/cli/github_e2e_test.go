package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github/githubtest"
	"github.com/lezli01/vincent/internal/testrepo"
)

// `vincent github`, `vincent issue ls --github`, and the `vincent doctor`
// row, through the real binary against a real detached daemon (task 035,
// task 130.11).
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
		if !strings.Contains(out, "octo/repo") || !strings.Contains(out, "available via gh") {
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

	// Task 130 decision 7: `--github-issue` is gone, so naming it is cobra's
	// unknown-flag refusal, and the daemon is never asked.
	t.Run("task add --github-issue is an unknown flag", func(t *testing.T) {
		out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"task", "add", "--project", "1", "--github-issue", "200")
		if code == 0 {
			t.Fatalf("task add --github-issue succeeded: %q", out)
		}
		if !strings.Contains(out, "unknown flag: --github-issue") {
			t.Errorf("the refusal is not an unknown flag:\n%s", out)
		}
	})

	// The lookup that replaced it (decision 21.4): import, look the number
	// up, and create the task from the issue id it answers with.
	t.Run("issue ls --github finds the imported issue", func(t *testing.T) {
		if out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"issue", "ls", "--github", "200"); code == 0 || !strings.Contains(out, "--project") {
			t.Errorf("issue ls --github without --project: code %d, out %q", code, out)
		}
		if out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"issue", "sync", "--project", "1"); code != 0 {
			t.Fatalf("issue sync: code %d, out %q", code, out)
		}
		var found []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
				"issue", "ls", "--project", "1", "--github", "200", "--json")
			if code != 0 {
				t.Fatalf("issue ls --github: code %d, out %q", code, out)
			}
			if err := json.Unmarshal([]byte(out), &found); err != nil {
				t.Fatalf("issue ls --json is not JSON: %v (%q)", err, out)
			}
			if len(found) > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if len(found) != 1 {
			t.Fatalf("issue ls --github 200 = %+v, want the one imported issue", found)
		}
		// Another number lists nothing, rather than everything.
		out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"issue", "ls", "--project", "1", "--github", "9999", "--json")
		if code != 0 || strings.TrimSpace(out) != "[]" {
			t.Errorf("issue ls --github 9999: code %d, out %q, want []", code, out)
		}
		out, code = runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"task", "add", "--project", "1", "--issue", strconv.FormatInt(found[0].ID, 10), "--json")
		if code != 0 {
			t.Fatalf("task add --issue: code %d, out %q", code, out)
		}
		var created struct {
			Title       string `json:"title"`
			GitHubIssue *struct {
				Number int `json:"number"`
			} `json:"github_issue"`
		}
		if err := json.Unmarshal([]byte(out), &created); err != nil {
			t.Fatalf("task add --json is not JSON: %v (%q)", err, out)
		}
		if !strings.HasPrefix(created.Title, "#200 ") {
			t.Errorf("title = %q, want the imported issue's numbered title", created.Title)
		}
		if created.GitHubIssue == nil || created.GitHubIssue.Number != 200 {
			t.Errorf("derived github_issue = %+v, want #200", created.GitHubIssue)
		}
	})

	// `--issue` (task 130.7): a vincent issue, resolved daemon-side from the
	// id alone, linked, and confirmed by name.
	t.Run("task add --issue links and prefills", func(t *testing.T) {
		c, err := apiclient.Discover(dataDir)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		iss, err := c.CreateIssue(t.Context(), apiclient.CreateIssueRequest{
			ProjectID: 1, Title: "Lock file leaks", Body: "Seen on start.",
		}, "")
		if err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		id := strconv.FormatInt(iss.ID, 10)
		out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"task", "add", "--project", "1", "--issue", id, "--json")
		if code != 0 {
			t.Fatalf("task add --issue: code %d, out %q", code, out)
		}
		var created struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Issue       *struct {
				ID int64 `json:"id"`
			} `json:"issue"`
		}
		if err := json.Unmarshal([]byte(out), &created); err != nil {
			t.Fatalf("task add --json is not JSON: %v (%q)", err, out)
		}
		if created.Title != iss.Title || created.Description != iss.Body {
			t.Errorf("created %q / %q, want the issue's", created.Title, created.Description)
		}
		if created.Issue == nil || created.Issue.ID != iss.ID {
			t.Errorf("issue = %+v, want %d linked", created.Issue, iss.ID)
		}
		out, code = runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"task", "add", "--project", "1", "--issue", id, "--title", "My own framing")
		if code != 0 {
			t.Fatalf("task add --issue --title: code %d, out %q", code, out)
		}
		if !strings.Contains(out, "My own framing") || !strings.Contains(out, "from issue "+id+": Lock file leaks") {
			t.Errorf("task add --issue output does not confirm the title and the issue:\n%s", out)
		}
	})

	t.Run("--issue is refused beside the pull-request prefill", func(t *testing.T) {
		for _, other := range []string{"--github-pull"} {
			out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
				"task", "add", "--project", "1", "--issue", "1", other, "200")
			if code == 0 {
				t.Fatalf("--issue with %s succeeded: %q", other, out)
			}
			if !strings.Contains(out, "issue") || !strings.Contains(out, strings.TrimPrefix(other, "--")) {
				t.Errorf("--issue with %s: the refusal does not name both flags:\n%s", other, out)
			}
		}
	})

	t.Run("neither title nor issue is refused", func(t *testing.T) {
		out, code := runVincentGH(t, dataDir, cfgDir, ghDir, "success",
			"task", "add", "--project", "1")
		if code == 0 {
			t.Fatalf("task add with no title and no issue succeeded: %q", out)
		}
		if !strings.Contains(out, "title") || !strings.Contains(out, "issue") || strings.Contains(out, "github-issue") {
			t.Errorf("the refusal does not name the ways to supply a title:\n%s", out)
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
		if !strings.Contains(out, "gh cli") || !strings.Contains(out, "available via gh") {
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
	if !strings.Contains(out, "tasks and local issues are unaffected") {
		t.Errorf("doctor does not say what still works:\n%s", out)
	}

	// And that is true: an ordinary task is unaffected.
	out, code = runVincentGH(t, dataDir, cfgDir, ghDir, "logged-out",
		"task", "add", "--project", "1", "--title", "an ordinary task", "--json")
	if code != 0 {
		t.Fatalf("task add without an issue: code %d, out %q", code, out)
	}

	// Naming a pull request is refused with the reason, not with a stack
	// trace.
	out, code = runVincentGH(t, dataDir, cfgDir, ghDir, "logged-out",
		"task", "add", "--project", "1", "--github-pull", "412")
	if code != 1 {
		t.Fatalf("task add --github-pull: code %d, want 1 (out %q)", code, out)
	}
	if !strings.Contains(out, "GitHub is not available") {
		t.Errorf("the refusal does not explain itself:\n%s", out)
	}
}
