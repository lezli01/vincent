package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/github/githubtest"
	"github.com/lezli01/vincent/internal/testrepo"
)

// `vincent github pr link`, `unlink`, `show` and `checks` (task 102), through
// the real binary against a real detached daemon with cmd/fakegh as `gh`.

// runVincentGHSplit is runVincentGH with stdout kept apart from stderr, for
// the assertions that `--json` puts the body on stdout even when it exits 1.
func runVincentGHSplit(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(vincentBin, args...)
	cmd.Env = env
	var outBuf, errBuf strings.Builder
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err := cmd.Run()
	code = cmd.ProcessState.ExitCode()
	if err != nil && code < 0 {
		t.Fatalf("vincent %s: %v\n%s%s", strings.Join(args, " "), err, outBuf.String(), errBuf.String())
	}
	return outBuf.String(), errBuf.String(), code
}

// storedPull is `task show --json`'s `github_pull`, kept raw so a no-op can be
// proved byte-identical, `linked_at` included.
func storedPull(t *testing.T, run func(args ...string) (string, int), id string) (json.RawMessage, apiclient.TaskDetail) {
	t.Helper()
	out, code := run("task", "show", id, "--json")
	if code != 0 {
		t.Fatalf("task show %s: code %d, out %q", id, code, out)
	}
	var raw struct {
		GitHubPull json.RawMessage `json:"github_pull"`
	}
	var detail apiclient.TaskDetail
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("task show --json is not JSON: %v (%q)", err, out)
	}
	if err := json.Unmarshal([]byte(out), &detail); err != nil {
		t.Fatalf("task show --json is not a TaskDetail: %v (%q)", err, out)
	}
	return raw.GitHubPull, detail
}

func TestGitHubPRLinkCommandsAgainstLiveDaemon(t *testing.T) {
	ghDir := filepath.Dir(githubtest.BuildFakeGH(t))
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	argvFile := filepath.Join(t.TempDir(), "gh-argv")
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(os.Environ(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})
	// No step here needs an agent to succeed; nothing should find a real one.
	pointAgentsAtNothing(t, cfgDir)
	env := append(ghEnv(ghDir, "success"),
		config.EnvDataDir+"="+dataDir,
		config.EnvConfigDir+"="+cfgDir,
		"FAKEGH_ARGV_FILE="+argvFile,
	)
	run := func(args ...string) (string, int) {
		t.Helper()
		stdout, stderr, code := runVincentGHSplit(t, env, args...)
		return stdout + stderr, code
	}
	if out, code := run("daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}

	repo := testrepo.Init(t, "main")
	gitRemote(t, repo, "https://github.com/octo/repo.git")
	if out, code := run("project", "add", repo, "--json"); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	other := testrepo.Init(t, "main")
	gitRemote(t, other, "https://gitlab.com/octo/repo.git")
	if out, code := run("project", "add", other, "--json"); code != 0 {
		t.Fatalf("project add (gitlab): code %d, out %q", code, out)
	}
	// Titles that slug to nothing like fakegh's head branches, so the
	// reconciler cannot link either task on its own and muddy what a human
	// did.
	for _, spec := range []struct{ project, title string }{
		{"1", "linked by hand"},
		{"1", "never linked"},
		{"2", "on gitlab"},
	} {
		if out, code := run("task", "add", "--project", spec.project, "--title", spec.title, "--json"); code != 0 {
			t.Fatalf("task add %q: code %d, out %q", spec.title, code, out)
		}
	}

	t.Run("argument errors are refused locally", func(t *testing.T) {
		before, _ := os.ReadFile(argvFile)
		out, code := run("github", "pr", "link", "abc", "--task", "1")
		if code != 1 || !strings.Contains(out, "positive integer") {
			t.Errorf("pr link abc: code %d, want 1 naming the number (out %q)", code, out)
		}
		for _, sub := range []string{"link 412", "unlink", "show", "checks"} {
			args := append([]string{"github", "pr"}, strings.Fields(sub)...)
			out, code := run(args...)
			if code != 1 || !strings.Contains(out, "task") {
				t.Errorf("pr %s without --task: code %d, want 1 naming the flag (out %q)", sub, code, out)
			}
		}
		if _, detail := storedPull(t, run, "1"); detail.GitHubPull != nil {
			t.Errorf("a refused link wrote %+v", detail.GitHubPull)
		}
		after, _ := os.ReadFile(argvFile)
		if string(after) != string(before) {
			t.Errorf("a refused command reached gh:\n%s", after[len(before):])
		}
	})

	t.Run("link names the pull request and makes no gh call", func(t *testing.T) {
		before, _ := os.ReadFile(argvFile)
		out, code := run("github", "pr", "link", "412", "--task", "1")
		if code != 0 {
			t.Fatalf("pr link: code %d, out %q", code, out)
		}
		if !strings.Contains(out, "Linked task 1 to octo/repo#412.") {
			t.Errorf("pr link confirmation = %q", out)
		}
		after, _ := os.ReadFile(argvFile)
		if string(after) != string(before) {
			t.Errorf("pr link called gh (task 052 decision 5):\n%s", after[len(before):])
		}
		_, detail := storedPull(t, run, "1")
		link := detail.GitHubPull
		if link == nil || link.Repo != "octo/repo" || link.Number != 412 ||
			link.Source != "human" || link.Suppressed {
			t.Errorf("stored link = %+v, want a live human link to octo/repo#412", link)
		}
	})

	t.Run("show reads the live pull request", func(t *testing.T) {
		out, code := run("github", "pr", "show", "--task", "1")
		if code != 0 {
			t.Fatalf("pr show: code %d, out %q", code, out)
		}
		for _, want := range []string{
			"octo/repo#412", "open", "Add a thing", "human",
			"https://github.com/octo/repo/pull/412",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("pr show does not show %q:\n%s", want, out)
			}
		}
		stdout, _, code := runVincentGHSplit(t, env, "github", "pr", "show", "--task", "1", "--json")
		if code != 0 {
			t.Fatalf("pr show --json: code %d, out %q", code, stdout)
		}
		var pull apiclient.GitHubTaskPull
		if err := json.Unmarshal([]byte(stdout), &pull); err != nil {
			t.Fatalf("pr show --json is not a GitHubTaskPull: %v (%q)", err, stdout)
		}
		if !pull.Linked || pull.Pull == nil || pull.Pull.Number != 412 {
			t.Errorf("pr show --json = %+v, want linked with a pull", pull)
		}
	})

	t.Run("checks reads the rollup and exits 0 on a failing one", func(t *testing.T) {
		out, code := run("github", "pr", "checks", "--task", "1")
		if code != 0 {
			t.Fatalf("pr checks: code %d, want 0 whatever CI concluded (out %q)", code, out)
		}
		if !strings.Contains(out, "octo/repo#412: failure on d3adb33fd3ad") {
			t.Errorf("pr checks head line missing:\n%s", out)
		}
		want := map[string]string{
			"build":             "5150",
			"test":              "5150",
			"license/cla":       "-",
			"ci/legacy-builder": "-",
		}
		for _, line := range strings.Split(out, "\n") {
			cols := strings.Fields(line)
			if len(cols) < 3 {
				continue
			}
			if runID, ok := want[cols[0]]; ok {
				if cols[2] != runID {
					t.Errorf("%s RUN = %q, want %q (line %q)", cols[0], cols[2], runID, line)
				}
				delete(want, cols[0])
			}
		}
		if len(want) > 0 {
			t.Errorf("pr checks is missing rows %v:\n%s", want, out)
		}

		stdout, _, code := runVincentGHSplit(t, env, "github", "pr", "checks", "--task", "1", "--json")
		if code != 0 {
			t.Fatalf("pr checks --json: code %d, out %q", code, stdout)
		}
		var checks apiclient.GitHubTaskChecks
		if err := json.Unmarshal([]byte(stdout), &checks); err != nil {
			t.Fatalf("pr checks --json is not a GitHubTaskChecks: %v (%q)", err, stdout)
		}
		if checks.State != "failure" || checks.Ref == "" || len(checks.Runs) != 4 {
			t.Fatalf("pr checks --json = %+v, want failure, a ref and four runs", checks)
		}
		for _, r := range checks.Runs {
			actions := r.Name == "build" || r.Name == "test"
			if (r.RunID > 0) != actions {
				t.Errorf("run %q run_id = %d, want it set only on Actions rows", r.Name, r.RunID)
			}
		}
	})

	t.Run("unlink removes the link and says it is sticky", func(t *testing.T) {
		out, code := run("github", "pr", "unlink", "--task", "1")
		if code != 0 {
			t.Fatalf("pr unlink: code %d, out %q", code, out)
		}
		if !strings.Contains(out, "Unlinked octo/repo#412 from task 1.") ||
			!strings.Contains(out, "will not link it again") {
			t.Errorf("pr unlink confirmation = %q", out)
		}
		_, detail := storedPull(t, run, "1")
		link := detail.GitHubPull
		if link == nil || !link.Suppressed || link.Repo != "octo/repo" || link.Number != 412 {
			t.Errorf("stored link = %+v, want suppressed with repo and number kept", link)
		}

		for _, sub := range []string{"show", "checks"} {
			out, code := run("github", "pr", sub, "--task", "1")
			if code != 1 || !strings.Contains(out, "no linked pull request") {
				t.Errorf("pr %s after unlink: code %d, want 1 saying not linked (out %q)", sub, code, out)
			}
			stdout, _, code := runVincentGHSplit(t, env, "github", "pr", sub, "--task", "1", "--json")
			var body struct {
				Linked *bool `json:"linked"`
			}
			if err := json.Unmarshal([]byte(stdout), &body); err != nil {
				t.Fatalf("pr %s --json after unlink is not JSON: %v (%q)", sub, err, stdout)
			}
			if code != 1 || body.Linked == nil || *body.Linked {
				t.Errorf("pr %s --json after unlink: code %d, linked %v, want 1 and false", sub, code, body.Linked)
			}
		}
	})

	t.Run("an unlink with nothing linked changes nothing", func(t *testing.T) {
		before, _ := storedPull(t, run, "1")
		out, code := run("github", "pr", "unlink", "--task", "1")
		if code != 1 || !strings.Contains(out, "nothing to unlink") {
			t.Errorf("second pr unlink: code %d, want 1 saying nothing to unlink (out %q)", code, out)
		}
		if after, _ := storedPull(t, run, "1"); string(after) != string(before) {
			t.Errorf("second unlink changed the stored link:\n before %s\n after  %s", before, after)
		}

		// The case the refusal exists for: DELETE here would write a
		// suppressed number-0 link the reconciler then honours for good.
		out, code = run("github", "pr", "unlink", "--task", "2")
		if code != 1 || !strings.Contains(out, "nothing to unlink") {
			t.Errorf("pr unlink on a never-linked task: code %d, want 1 (out %q)", code, out)
		}
		if raw, _ := storedPull(t, run, "2"); len(raw) != 0 && string(raw) != "null" {
			t.Errorf("a never-linked task now carries github_pull %s", raw)
		}
	})

	t.Run("link fails on a project that is not on GitHub", func(t *testing.T) {
		out, code := run("github", "pr", "link", "412", "--task", "3")
		if code != 1 || !strings.Contains(out, "not a github.com repository") {
			t.Errorf("pr link on a gitlab project: code %d, want 1 with not_github (out %q)", code, out)
		}
	})

	// Last, because it switches the integration off for the daemon.
	t.Run("a named reason fails show and checks", func(t *testing.T) {
		if out, code := run("github", "pr", "link", "412", "--task", "1"); code != 0 {
			t.Fatalf("relink: code %d, out %q", code, out)
		}
		if out, code := run("config", "set", "github.enabled", "false"); code != 0 {
			t.Fatalf("config set github.enabled false: code %d, out %q", code, out)
		}
		for _, sub := range []string{"show", "checks"} {
			out, code := run("github", "pr", sub, "--task", "1")
			if code != 1 || !strings.Contains(out, github.Message(github.ReasonDisabled)) {
				t.Errorf("pr %s disabled: code %d, want 1 with the disabled message (out %q)", sub, code, out)
			}
			stdout, _, code := runVincentGHSplit(t, env, "github", "pr", sub, "--task", "1", "--json")
			var body struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal([]byte(stdout), &body); err != nil {
				t.Fatalf("pr %s --json disabled is not JSON: %v (%q)", sub, err, stdout)
			}
			if code != 1 || body.Reason != github.ReasonDisabled {
				t.Errorf("pr %s --json disabled: code %d, reason %q, want 1 and disabled", sub, code, body.Reason)
			}
		}
	})
}
