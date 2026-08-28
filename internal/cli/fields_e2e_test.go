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

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// fieldConsumerWorkflow is a project-scoped workflow whose one command step
// renders a task field into its body. `git config -f` is used rather than
// `touch`/`echo` because a step body runs under the *daemon's* shell — /bin/sh
// on POSIX, pwsh on Windows (§8.3) — so it has to be spelled in the
// intersection of the two (CLAUDE.md), and git is in it.
const fieldConsumerWorkflow = `name: field-consumer
steps:
  - id: record
    type: command
    max_retries: 0
    run: git config -f fields.ini task.ticket {{ index .Task.Fields "ticket" }}
`

// TestTaskFieldsReachATemplate is task 045's done-when: fields supplied on the
// command line reach a step template through the real binary and a real
// daemon, and the effect is visible on disk in the task's worktree.
//
// TestCommandsAgainstLiveDaemon cannot prove this. It points every adapter at
// /nonexistent/claude and never runs a step, so it shows only that the daemon
// stored what was sent. What is asserted here is the whole path: --fields-file
// on stdin, a --field overriding one of its keys, the daemon's validation, the
// snapshot, and `{{ index .Task.Fields … }}` rendering into a command step.
func TestTaskFieldsReachATemplate(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(hermeticEnv(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})

	// No step here runs an agent, and a machine that happens to have one
	// installed must not change the outcome.
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(
		"agents:\n  claude:\n    path: \"/nonexistent/claude\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	repo := testrepo.Init(t, "main")
	testrepo.WriteFile(t, repo,
		filepath.Join(workflow.ProjectDirName, "field-consumer.yaml"), fieldConsumerWorkflow)
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "add the field-consumer workflow")

	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}
	out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo, "--json")
	if code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	var project struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &project); err != nil {
		t.Fatalf("project add --json is not JSON: %v (%q)", err, out)
	}

	// The file supplies three fields; --field overrides one of them. That is
	// the documented precedence (task 045 decision 2) and it is what the step
	// body ends up rendering.
	const doc = `{"ticket":"OPS-000","owner":"ana","note":"two words"}`
	out, code = runVincentStdin(t, dataDir, cfgDir, doc,
		"task", "add", "--project", strconv.FormatInt(project.ID, 10),
		"--workflow", "field-consumer", "--title", "fields reach the template",
		"--fields-file", "-", "--field", "ticket=OPS-42")
	if code != 0 {
		t.Fatalf("task add: code %d, out %q", code, out)
	}

	// The human confirmation line: names sorted, a count, and no value — not
	// even the one that is a plain word (task 045 decision 4).
	if !strings.Contains(out, "fields: note, owner, ticket (3)") {
		t.Errorf("task add does not confirm the field names:\n%s", out)
	}
	for _, value := range []string{"OPS-42", "OPS-000", "ana", "two words"} {
		if strings.Contains(out, value) {
			t.Errorf("task add echoes the field value %q:\n%s", value, out)
		}
	}

	taskID := taskIDFromCreated(t, out)

	// The task runs to completion: one command step, no agent, no gate.
	detail := waitForTaskState(t, dataDir, cfgDir, taskID, "done")
	if detail.WorktreePath == "" {
		t.Fatalf("task %s has no worktree path", taskID)
	}
	if detail.Fields["ticket"] != "OPS-42" {
		t.Errorf("stored ticket = %q, want the --field value OPS-42 (it beats the file's)",
			detail.Fields["ticket"])
	}
	if detail.Fields["owner"] != "ana" || detail.Fields["note"] != "two words" {
		t.Errorf("stored fields = %v, want the file's owner and note alongside", detail.Fields)
	}

	// The proof: the step body rendered the field and the run left it on disk.
	written, err := os.ReadFile(filepath.Join(detail.WorktreePath, "fields.ini"))
	if err != nil {
		t.Fatalf("read the file the step wrote: %v", err)
	}
	if !strings.Contains(string(written), "OPS-42") {
		t.Errorf("the step wrote %q, want the rendered field value OPS-42", written)
	}
}

// runVincentStdin is runVincent with a body on standard input, which is the
// only way to exercise `--fields-file -` and the `-` forms of --prompt-file,
// --run-file and --body.
func runVincentStdin(t *testing.T, dataDir, cfgDir, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(vincentBin, args...)
	cmd.Env = append(hermeticEnv(),
		config.EnvDataDir+"="+dataDir,
		config.EnvConfigDir+"="+cfgDir,
	)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	code := cmd.ProcessState.ExitCode()
	if err != nil && code < 0 {
		t.Fatalf("vincent %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), code
}

// taskIDFromCreated reads the id out of `task N created: …`, which is the
// human line the --json-free form prints.
func taskIDFromCreated(t *testing.T, out string) string {
	t.Helper()
	_, rest, ok := strings.Cut(out, "task ")
	if !ok {
		t.Fatalf("no `task N created` line in:\n%s", out)
	}
	id, _, ok := strings.Cut(rest, " ")
	if !ok {
		t.Fatalf("no `task N created` line in:\n%s", out)
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		t.Fatalf("task id %q is not a number, from:\n%s", id, out)
	}
	return id
}

// taskDetail is the slice of `vincent task show --json` this test reads.
type taskDetail struct {
	State        string            `json:"state"`
	BlockReason  *string           `json:"block_reason"`
	WorktreePath string            `json:"worktree_path"`
	Fields       map[string]string `json:"fields"`
}

// waitForTaskState polls `task show --json` until the task reaches want, and
// fails fast on a terminal state that is not it.
func waitForTaskState(t *testing.T, dataDir, cfgDir, id, want string) taskDetail {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		out, code := runVincent(t, dataDir, cfgDir, "task", "show", id, "--json")
		if code != 0 {
			t.Fatalf("task show: code %d, out %q", code, out)
		}
		var got taskDetail
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("task show --json is not JSON: %v (%q)", err, out)
		}
		if got.State == want {
			return got
		}
		if got.State == "blocked" || got.State == "aborted" {
			t.Fatalf("task %s ended %s (%s), want %s", id, got.State, deref(got.BlockReason), want)
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s stuck in %s, want %s", id, got.State, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
