package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/github/githubtest"
	"github.com/lezli01/vincent/internal/testrepo"
)

// `vincent github pr merge|close|reopen|comment|rerun` (task 068.4), through
// the real binary against a real detached daemon whose `gh` is cmd/fakegh.

func TestGitHubPRWriteCommandsAgainstLiveDaemon(t *testing.T) {
	ghDir := filepath.Dir(githubtest.BuildFakeGH(t))
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	// The daemon inherits these at start, and hands them to every gh it runs.
	argvLog := filepath.Join(t.TempDir(), "gh-argv.txt")
	stdinLog := filepath.Join(t.TempDir(), "gh-stdin.txt")
	t.Setenv("FAKEGH_ARGV_FILE", argvLog)
	t.Setenv("FAKEGH_STDIN_FILE", stdinLog)
	t.Setenv("FAKEGH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(os.Environ(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})
	pointAgentsAtNothing(t, cfgDir)
	run := func(args ...string) (string, int) {
		t.Helper()
		return runVincentGH(t, dataDir, cfgDir, ghDir, "success", args...)
	}
	if out, code := run("daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}
	repo := testrepo.Init(t, "main")
	gitRemote(t, repo, "https://github.com/octo/repo.git")
	if out, code := run("project", "add", repo, "--json"); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	out, code := run("task", "add", "--project", "1", "--title", "a task", "--json")
	if code != 0 {
		t.Fatalf("task add: code %d, out %q", code, out)
	}
	var task struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &task); err != nil {
		t.Fatalf("task add --json: %v (%q)", err, out)
	}
	id := strconv.FormatInt(task.ID, 10)
	linkPullOverAPI(t, dataDir, task.ID, 412)

	t.Run("comment from stdin", func(t *testing.T) {
		cmd := exec.Command(vincentBin, "github", "pr", "comment", "--task", id, "--body-file", "-")
		cmd.Env = append(ghEnv(ghDir, "success"),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		cmd.Stdin = strings.NewReader("Looks good.\n\nShip it.")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("pr comment: %v\n%s", err, b)
		}
		if !strings.Contains(string(b), "#issuecomment-1") {
			t.Errorf("pr comment did not print the comment URL:\n%s", b)
		}
		if got, _ := os.ReadFile(stdinLog); string(got) != "Looks good.\n\nShip it." {
			t.Errorf("gh received the body %q", got)
		}
	})

	t.Run("rerun", func(t *testing.T) {
		out, code := run("github", "pr", "rerun", "--task", id, "--run-id", "5150", "--json")
		if code != 0 || !strings.Contains(out, `"run_id": 5150`) {
			t.Fatalf("pr rerun: code %d, out %q", code, out)
		}
		out, code = run("github", "pr", "rerun", "--task", id, "--run-id", "9999")
		if code != 1 || !strings.Contains(out, "refused these values") {
			t.Fatalf("pr rerun of an unknown run: code %d, out %q", code, out)
		}
	})

	t.Run("close and reopen", func(t *testing.T) {
		if out, code := run("github", "pr", "close", "--task", id); code != 0 || !strings.Contains(out, "Closed octo/repo#412 (closed)") {
			t.Fatalf("pr close: code %d, out %q", code, out)
		}
		if out, code := run("github", "pr", "reopen", "--task", id); code != 0 || !strings.Contains(out, "Reopened octo/repo#412 (open)") {
			t.Fatalf("pr reopen: code %d, out %q", code, out)
		}
	})

	t.Run("merge names what is sent", func(t *testing.T) {
		// Without --head-sha there is nothing to pin the merge to, and cobra
		// refuses before the daemon is asked.
		if out, code := run("github", "pr", "merge", "--task", id, "--method", "merge"); code == 0 || !strings.Contains(out, "head-sha") {
			t.Fatalf("pr merge without --head-sha: code %d, out %q", code, out)
		}
		head := "d3adb33fd3adb33fd3adb33fd3adb33fd3adb33f"
		out, code := run("github", "pr", "merge", "--task", id, "--method", "squash", "--head-sha", head)
		if code != 0 || !strings.Contains(out, "Merged octo/repo#412 (merged)") {
			t.Fatalf("pr merge: code %d, out %q", code, out)
		}
		out, code = run("github", "pr", "merge", "--task", id, "--method", "squash", "--head-sha", head)
		if code != 1 || !strings.Contains(out, "will not merge") {
			t.Fatalf("a second pr merge: code %d, out %q", code, out)
		}
	})

	b, _ := os.ReadFile(argvLog)
	for _, want := range []string{
		"pr comment 412 -R octo/repo --body-file -",
		"run rerun 5150 --failed -R octo/repo",
		"pr close 412 -R octo/repo",
		"pr reopen 412 -R octo/repo",
		"pr merge 412 -R octo/repo --squash --match-head-commit d3adb33f",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("gh was not asked for %q:\n%s", want, b)
		}
	}
}

// linkPullOverAPI is the human link, which has no subcommand of its own: it
// is POST /v1/tasks/{id}/github/pull against the running daemon.
func linkPullOverAPI(t *testing.T, dataDir string, taskID int64, number int) {
	t.Helper()
	ri, err := daemon.ReadRuntimeInfo(dataDir)
	if err != nil {
		t.Fatalf("daemon.json: %v", err)
	}
	token, err := daemon.ReadToken(dataDir)
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	body, _ := json.Marshal(map[string]int{"number": number})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/tasks/%d/github/pull", ri.Port, taskID), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link answered %d", resp.StatusCode)
	}
}
