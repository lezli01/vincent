package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestCommandsAgainstLiveDaemon is the T4.2 done-when: the data subcommands
// driven through the real binary against a real daemon, in both output modes.
//
// It is one test rather than several because they share an expensive fixture
// (a detached daemon plus a git repo) and because the interesting assertions
// are sequential: a task can only be listed after it is created, and only
// cancelled while it exists.
func TestCommandsAgainstLiveDaemon(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(os.Environ(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})

	// Point every adapter at nothing: this test never runs a step, and a
	// machine that happens to have claude installed must not change the
	// outcome.
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(
		"agents:\n  claude:\n    path: \"/nonexistent/claude\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}

	repo := testrepo.Init(t, "main")

	t.Run("project add and ls", func(t *testing.T) {
		out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo, "--json")
		if code != 0 {
			t.Fatalf("project add: code %d, out %q", code, out)
		}
		var p struct {
			ID            int64  `json:"id"`
			Path          string `json:"path"`
			DefaultBranch string `json:"default_branch"`
		}
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			t.Fatalf("project add --json is not JSON: %v (%q)", err, out)
		}
		if p.ID == 0 || p.DefaultBranch != "main" {
			t.Errorf("project = %+v, want an id and default branch main", p)
		}

		out, code = runVincent(t, dataDir, cfgDir, "project", "ls")
		if code != 0 {
			t.Fatalf("project ls: code %d, out %q", code, out)
		}
		// The table carries a header even with rows, and names the project.
		if !strings.Contains(out, "ID") || !strings.Contains(out, "BRANCH") {
			t.Errorf("project ls has no header:\n%s", out)
		}
		if !strings.Contains(out, filepath.Base(repo)) {
			t.Errorf("project ls does not list the project:\n%s", out)
		}
	})

	var taskID string
	t.Run("task add ls show cancel", func(t *testing.T) {
		out, code := runVincent(t, dataDir, cfgDir,
			"task", "add", "--project", "1", "--title", "cli e2e task",
			"--description", "created by the CLI test", "--json")
		if code != 0 {
			t.Fatalf("task add: code %d, out %q", code, out)
		}
		var created struct {
			ID         int64  `json:"id"`
			Title      string `json:"title"`
			BranchName string `json:"branch_name"`
			State      string `json:"state"`
		}
		if err := json.Unmarshal([]byte(out), &created); err != nil {
			t.Fatalf("task add --json is not JSON: %v (%q)", err, out)
		}
		if created.ID == 0 || created.Title != "cli e2e task" || created.BranchName == "" {
			t.Fatalf("created task = %+v, want id, title and branch", created)
		}
		taskID = strconv.FormatInt(created.ID, 10)

		out, code = runVincent(t, dataDir, cfgDir, "task", "ls")
		if code != 0 {
			t.Fatalf("task ls: code %d, out %q", code, out)
		}
		if !strings.Contains(out, "cli e2e task") || !strings.Contains(out, "STATE") {
			t.Errorf("task ls does not show the task:\n%s", out)
		}

		// A filter that matches nothing still prints the header — an empty
		// result must look like an empty table, not a broken command.
		out, code = runVincent(t, dataDir, cfgDir, "task", "ls", "--state", "done")
		if code != 0 || !strings.Contains(out, "STATE") {
			t.Errorf("empty task ls: code %d, out %q", code, out)
		}
		out, code = runVincent(t, dataDir, cfgDir, "task", "ls", "--state", "done", "--json")
		if code != 0 || strings.TrimSpace(out) != "[]" {
			t.Errorf("empty task ls --json = %q (code %d), want []", out, code)
		}

		out, code = runVincent(t, dataDir, cfgDir, "task", "show", taskID)
		if code != 0 {
			t.Fatalf("task show: code %d, out %q", code, out)
		}
		for _, want := range []string{"cli e2e task", "created by the CLI test", created.BranchName} {
			if !strings.Contains(out, want) {
				t.Errorf("task show is missing %q:\n%s", want, out)
			}
		}

		out, code = runVincent(t, dataDir, cfgDir, "task", "cancel", taskID)
		if code != 0 {
			t.Fatalf("task cancel: code %d, out %q", code, out)
		}
		if !strings.Contains(out, "aborted") && !strings.Contains(out, "cancel") {
			t.Errorf("task cancel says nothing useful: %q", out)
		}
	})

	t.Run("a rejected request exits 1", func(t *testing.T) {
		// The daemon answered and said no. That is exit 1 — distinct from
		// exit 2, which means nothing answered at all.
		out, code := runVincent(t, dataDir, cfgDir, "task", "show", "999999")
		if code != 1 {
			t.Errorf("task show for a missing id: code %d, want 1 (out %q)", code, out)
		}
		out, code = runVincent(t, dataDir, cfgDir,
			"task", "add", "--project", "999999", "--title", "nope")
		if code != 1 {
			t.Errorf("task add into a missing project: code %d, want 1 (out %q)", code, out)
		}
	})

	t.Run("workflow ls and validate", func(t *testing.T) {
		out, code := runVincent(t, dataDir, cfgDir, "workflow", "ls", "--json")
		if code != 0 {
			t.Fatalf("workflow ls: code %d, out %q", code, out)
		}
		var entries []struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		}
		if err := json.Unmarshal([]byte(out), &entries); err != nil {
			t.Fatalf("workflow ls --json is not JSON: %v (%q)", err, out)
		}
		if len(entries) == 0 {
			t.Error("workflow ls returned nothing; the built-in adhoc workflow should be there")
		}
	})

	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "stop"); code != 0 {
		t.Fatalf("daemon stop: code %d, out %q", code, out)
	}

	// Everything below runs with no daemon at all.
	t.Run("no daemon exits 2", func(t *testing.T) {
		for _, args := range [][]string{
			{"project", "ls"},
			{"task", "ls"},
			{"workflow", "ls"},
		} {
			out, code := runVincent(t, dataDir, cfgDir, args...)
			if code != 2 {
				t.Errorf("%v with no daemon: code %d, want 2 (out %q)", args, code, out)
			}
			// The message has to name the fix; exit 2 alone is a riddle.
			if !strings.Contains(out, "vincent daemon start") {
				t.Errorf("%v does not point at `vincent daemon start`: %q", args, out)
			}
		}
	})

	t.Run("validate needs no daemon", func(t *testing.T) {
		// This is the whole point of keeping validate local (PR U decision):
		// it must work in CI and in a pre-commit hook, where no daemon runs.
		good := filepath.Join(t.TempDir(), "good.yaml")
		if err := os.WriteFile(good, []byte(
			"name: ok-flow\nsteps:\n  - id: s1\n    type: command\n    run: echo hi\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, code := runVincent(t, dataDir, cfgDir, "workflow", "validate", good)
		if code != 0 {
			t.Fatalf("validate a good file: code %d, out %q", code, out)
		}
		if !strings.Contains(out, "ok") || !strings.Contains(out, "ok-flow") {
			t.Errorf("validate output does not report the workflow: %q", out)
		}

		bad := filepath.Join(t.TempDir(), "bad.yaml")
		if err := os.WriteFile(bad, []byte(
			"name: broken\nsteps:\n  - id: s1\n    type: nonsense\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, code = runVincent(t, dataDir, cfgDir, "workflow", "validate", bad)
		if code != 1 {
			t.Errorf("validate an invalid file: code %d, want 1 (out %q)", code, out)
		}
		if !strings.Contains(out, "invalid") {
			t.Errorf("validate does not say the file is invalid: %q", out)
		}

		out, code = runVincent(t, dataDir, cfgDir, "workflow", "validate", bad, "--json")
		if code != 1 {
			t.Errorf("validate --json exit code = %d, want 1", code)
		}
		var res struct {
			Valid  bool `json:"valid"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		// --json must stay parseable on the failure path: stdout carries the
		// document, findings go to stderr, and runVincent merges them — so
		// parse only the JSON object.
		if err := json.Unmarshal([]byte(jsonObject(out)), &res); err != nil {
			t.Fatalf("validate --json is not JSON: %v (%q)", err, out)
		}
		if res.Valid || len(res.Errors) == 0 {
			t.Errorf("validate --json = %+v, want invalid with errors", res)
		}
	})

	t.Run("the shipped examples validate", func(t *testing.T) {
		// T5.6's done-when, met through the actual CLI rather than a unit
		// test standing in for it: this is exactly what CI now runs.
		matches, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.yaml"))
		if err != nil || len(matches) == 0 {
			t.Fatalf("no examples found: %v", err)
		}
		for _, m := range matches {
			out, code := runVincent(t, dataDir, cfgDir, "workflow", "validate", m)
			if code != 0 {
				t.Errorf("%s does not validate through the CLI: code %d\n%s", m, code, out)
			}
		}
	})
}

// jsonObject extracts the first {...} block, so a merged stdout/stderr stream
// stays parseable.
func jsonObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return s
	}
	return s[start : end+1]
}
