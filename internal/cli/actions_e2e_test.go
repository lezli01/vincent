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
)

// TestHumanActionCommands is task 048's done-when, and issue #89's: every §6
// human action driven through the real binary against a real daemon, with no
// TTY and no curl — a blocked task taken back to `running`, a gate approved
// and rejected, an archive refused and then forced, and a project removed.
//
// It is one test because the interesting assertions are sequential and share
// one expensive fixture: a task can only be retried after it has blocked, and
// only blocked after the scheduler admitted it.
//
// No agent runs here. Every workflow step is a `command`, so the assertions
// depend on the daemon and the shell rather than on which agent CLI the host
// happens to have — and the step bodies stay inside the sh ∩ pwsh
// intersection the gate scripts are held to (`exit N`, `sleep N`, `git ...`).
func TestHumanActionCommands(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(hermeticEnv(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})

	// One slot globally, so a task that has not been admitted yet stays
	// `queued` for as long as the test needs it to: pause is valid from
	// `queued`, and racing the scheduler for that window would be flaky.
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(
		"max_parallel_tasks: 1\nagents:\n  claude:\n    path: \"/nonexistent/claude\"\n"),
		0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	repo := testrepo.Init(t, "main")
	writeActionWorkflow(t, repo, "blocky", "steps:\n  - id: fail\n    type: command\n    run: exit 1\n")
	writeActionWorkflow(t, repo, "slow", "steps:\n  - id: wait\n    type: command\n    run: sleep 30\n")
	writeActionWorkflow(t, repo, "gated", "steps:\n"+
		"  - id: gate\n    type: manual\n    instructions: approve me\n"+
		"  - id: after\n    type: command\n    run: git --version\n")
	// `git config -f` is how a step writes a file to disk in both the POSIX
	// and the pwsh spelling of §8.3's shell; the untracked file it leaves is
	// what makes the worktree dirty.
	writeActionWorkflow(t, repo, "messy", "steps:\n"+
		"  - id: mess\n    type: command\n    run: git config -f leftover.txt dirt.left yes\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "workflows")

	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}
	if out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo, "--json"); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}

	// The slot holder. Everything created after this stays queued until it is
	// cancelled, which is what makes the `queued` assertions deterministic.
	hog := addActionTask(t, dataDir, cfgDir, "slow", "--workflow", "slow")
	waitForState(t, dataDir, cfgDir, hog, "running")

	t.Run("pause and resume a queued task", func(t *testing.T) {
		id := addActionTask(t, dataDir, cfgDir, "pausable", "--workflow", "blocky")
		out, code := runVincent(t, dataDir, cfgDir, "task", "pause", id)
		if code != 0 || !strings.Contains(out, "paused") {
			t.Fatalf("task pause: code %d, out %q; want 0 and the new state", code, out)
		}
		// The state printed is the daemon's, after the fact — so a second
		// pause is refused by the FSM with the state it actually found.
		out, code = runVincent(t, dataDir, cfgDir, "task", "pause", id)
		if code != 1 {
			t.Errorf("pause an already paused task: code %d, want 1 (out %q)", code, out)
		}

		out, code = runVincent(t, dataDir, cfgDir, "task", "resume", id, "--json")
		if code != 0 {
			t.Fatalf("task resume: code %d, out %q", code, out)
		}
		var resumed struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		}
		if err := json.Unmarshal([]byte(out), &resumed); err != nil {
			t.Fatalf("task resume --json is not JSON: %v (%q)", err, out)
		}
		if resumed.State != "queued" {
			t.Errorf("resumed task = %+v, want state queued", resumed)
		}
		if _, code := runVincent(t, dataDir, cfgDir, "task", "cancel", id); code != 0 {
			t.Errorf("cancel the paused task: code %d", code)
		}
	})

	t.Run("approve, reject and skip a manual gate", func(t *testing.T) {
		approved := addActionTask(t, dataDir, cfgDir, "approve me", "--workflow", "gated")
		rejected := addActionTask(t, dataDir, cfgDir, "reject me", "--workflow", "gated")
		skipped := addActionTask(t, dataDir, cfgDir, "skip me", "--workflow", "gated")
		// Free the slot: all three need admitting to reach the gate.
		if _, code := runVincent(t, dataDir, cfgDir, "task", "cancel", hog); code != 0 {
			t.Fatalf("cancel the slot holder: code %d", code)
		}
		for _, id := range []string{approved, rejected, skipped} {
			waitForState(t, dataDir, cfgDir, id, "awaiting_gate")
		}

		if out, code := runVincent(t, dataDir, cfgDir, "task", "approve", approved); code != 0 {
			t.Fatalf("task approve: code %d, out %q", code, out)
		}
		waitForState(t, dataDir, cfgDir, approved, "done")

		out, code := runVincent(t, dataDir, cfgDir, "task", "reject", rejected)
		if code != 0 || !strings.Contains(out, "blocked") {
			t.Fatalf("task reject: code %d, out %q; want 0 and blocked", code, out)
		}
		if out, code := runVincent(t, dataDir, cfgDir, "task", "skip", skipped); code != 0 {
			t.Fatalf("task skip: code %d, out %q", code, out)
		}
		waitForState(t, dataDir, cfgDir, skipped, "done")

		// An action the FSM refuses for the state the task is in: exit 1
		// with the daemon's own wording, not a broken command.
		out, code = runVincent(t, dataDir, cfgDir, "task", "approve", approved)
		if code != 1 {
			t.Errorf("approve a done task: code %d, want 1 (out %q)", code, out)
		}
		if !strings.Contains(out, "done") {
			t.Errorf("the refusal does not name the state it found: %q", out)
		}
	})

	// The issue's headline: a blocked task rescued from a script. `--run` is
	// edit+retry, so the step that failed is replaced in this task's snapshot
	// only — which is also what makes `running` observable rather than a race
	// against a command that exits immediately.
	t.Run("retry takes a blocked task back to running", func(t *testing.T) {
		id := addActionTask(t, dataDir, cfgDir, "retry me", "--workflow", "blocky")
		waitForState(t, dataDir, cfgDir, id, "blocked")

		// Two spellings of one value is a usage error, refused locally
		// before any request is sent.
		out, code := runVincent(t, dataDir, cfgDir,
			"task", "retry", id, "--prompt", "a", "--prompt-file", "b")
		if code != 1 {
			t.Errorf("--prompt with --prompt-file: code %d, want 1 (out %q)", code, out)
		}
		if state := taskState(t, dataDir, cfgDir, id); state != "blocked" {
			t.Errorf("the refused retry moved the task to %s; it must send no request", state)
		}

		out, code = runVincent(t, dataDir, cfgDir, "task", "retry", id, "--run", "sleep 30")
		if code != 0 || !strings.Contains(out, "queued") {
			t.Fatalf("task retry --run: code %d, out %q; want 0 and queued", code, out)
		}
		waitForState(t, dataDir, cfgDir, id, "running")
		if _, code := runVincent(t, dataDir, cfgDir, "task", "cancel", id); code != 0 {
			t.Errorf("cancel the retried task: code %d", code)
		}
	})

	t.Run("retry --branch clears a branch_exists block", func(t *testing.T) {
		// The block this recovers from is a *race*, not a typo: creating the
		// task validates the name against the repository, and the collision
		// appears afterwards — someone else pushed that branch, or an earlier
		// run left it behind. So the task is parked behind a full slot while
		// the branch is created under it, which is the situation task 001
		// gave `branch_override` for.
		slot := addActionTask(t, dataDir, cfgDir, "slot", "--workflow", "slow")
		waitForState(t, dataDir, cfgDir, slot, "running")
		id := addActionTask(t, dataDir, cfgDir, "collide", "--branch", "contested")
		testrepo.Run(t, repo, "branch", "contested")
		if _, code := runVincent(t, dataDir, cfgDir, "task", "cancel", slot); code != 0 {
			t.Fatalf("cancel the slot holder: code %d", code)
		}
		waitForState(t, dataDir, cfgDir, id, "blocked")
		before := taskJSON(t, dataDir, cfgDir, id)
		if before.BlockReason != "branch_exists" {
			t.Fatalf("block_reason = %q, want branch_exists", before.BlockReason)
		}

		if out, code := runVincent(t, dataDir, cfgDir,
			"task", "retry", id, "--branch", "rescued"); code != 0 {
			t.Fatalf("task retry --branch: code %d, out %q", code, out)
		}
		// The task is the same task: same id, its own history intact, on the
		// branch the flag named. That is the whole point of the recovery —
		// the alternative was deleting the task and creating it again.
		after := taskJSON(t, dataDir, cfgDir, id)
		if after.ID != before.ID || after.BranchName != "rescued" {
			t.Errorf("after retry --branch: id %d branch %q; want id %d on rescued",
				after.ID, after.BranchName, before.ID)
		}
		if after.State == "blocked" && after.BlockReason == "branch_exists" {
			t.Errorf("the task is still blocked on branch_exists: %+v", after)
		}
		if _, code := runVincent(t, dataDir, cfgDir, "task", "cancel", id); code != 0 {
			t.Logf("cancel after branch rescue: already settled")
		}
	})

	t.Run("repair reads its prompt from stdin", func(t *testing.T) {
		id := addActionTask(t, dataDir, cfgDir, "repair me", "--workflow", "blocky")
		waitForState(t, dataDir, cfgDir, id, "blocked")

		// A repair with no instructions never leaves the process.
		if out, code := runVincent(t, dataDir, cfgDir, "task", "repair", id); code != 1 {
			t.Errorf("task repair with no prompt: code %d, want 1 (out %q)", code, out)
		}

		out, code := runVincentStdin(t, dataDir, cfgDir,
			"the check failed; fix the config\nand try again\n",
			"task", "repair", id, "--prompt-file", "-")
		if code != 0 || !strings.Contains(out, "queued") {
			t.Fatalf("task repair --prompt-file -: code %d, out %q; want 0 and queued", code, out)
		}
		if _, code := runVincent(t, dataDir, cfgDir, "task", "cancel", id); code != 0 {
			t.Logf("cancel after repair: already settled")
		}
	})

	t.Run("archive refuses a dirty worktree until --force", func(t *testing.T) {
		id := addActionTask(t, dataDir, cfgDir, "messy", "--workflow", "messy")
		waitForState(t, dataDir, cfgDir, id, "done")

		out, code := runVincent(t, dataDir, cfgDir, "task", "archive", id)
		if code != 1 {
			t.Fatalf("archive a dirty worktree: code %d, want 1 (out %q)", code, out)
		}
		// The reason is a discriminator in details, not prose in the message,
		// and the way out has to be named or exit 1 is a riddle.
		if !strings.Contains(out, "--force") {
			t.Errorf("the refusal does not offer --force: %q", out)
		}

		if out, code := runVincent(t, dataDir, cfgDir,
			"task", "archive", id, "--force"); code != 0 {
			t.Fatalf("task archive --force: code %d, out %q", code, out)
		}
		if state := taskState(t, dataDir, cfgDir, id); state != "archived" {
			t.Errorf("state after archive --force = %s, want archived", state)
		}
	})

	t.Run("answer refuses a task that is not waiting for input", func(t *testing.T) {
		id := addActionTask(t, dataDir, cfgDir, "not asking", "--workflow", "blocky")
		waitForState(t, dataDir, cfgDir, id, "blocked")
		out, code := runVincent(t, dataDir, cfgDir, "task", "answer", id, "--allow")
		if code != 1 {
			t.Errorf("answer a blocked task: code %d, want 1 (out %q)", code, out)
		}
		if !strings.Contains(out, "not waiting for input") {
			t.Errorf("the refusal does not say why: %q", out)
		}
		if _, code := runVincent(t, dataDir, cfgDir, "task", "cancel", id); code != 0 {
			t.Errorf("cancel: code %d", code)
		}
	})

	t.Run("project rm", func(t *testing.T) {
		// A project still holding tasks is refused, and the count reaches the
		// user intact — it is the thing that tells them what --force will do.
		out, code := runVincent(t, dataDir, cfgDir, "project", "rm", "1")
		if code != 1 {
			t.Fatalf("project rm with live tasks: code %d, want 1 (out %q)", code, out)
		}
		if !strings.Contains(out, "non-archived task") {
			t.Errorf("the refusal does not carry the daemon's count: %q", out)
		}

		// A *running* task is refused whether or not --force is set: force
		// cannot help, and the message names the task to cancel.
		running := addActionTask(t, dataDir, cfgDir, "still running", "--workflow", "slow")
		waitForState(t, dataDir, cfgDir, running, "running")
		out, code = runVincent(t, dataDir, cfgDir, "project", "rm", "1", "--force")
		if code != 1 {
			t.Fatalf("project rm --force with a running task: code %d, want 1 (out %q)", code, out)
		}
		if !strings.Contains(out, "is running") || !strings.Contains(out, running) {
			t.Errorf("the refusal does not name the running task %s: %q", running, out)
		}
		if _, code := runVincent(t, dataDir, cfgDir, "task", "cancel", running); code != 0 {
			t.Errorf("cancel the running task: code %d", code)
		}

		// An empty project is removed, and --json says so rather than
		// leaving a parser looking at an empty stdout for a 204.
		second := testrepo.Init(t, "main")
		out, code = runVincent(t, dataDir, cfgDir, "project", "add", second, "--json")
		if code != 0 {
			t.Fatalf("project add: code %d, out %q", code, out)
		}
		var added struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &added); err != nil {
			t.Fatalf("project add --json is not JSON: %v (%q)", err, out)
		}
		id := strconv.FormatInt(added.ID, 10)
		out, code = runVincent(t, dataDir, cfgDir, "project", "rm", id, "--json")
		if code != 0 {
			t.Fatalf("project rm: code %d, out %q", code, out)
		}
		var removed struct {
			ID      int64 `json:"id"`
			Removed bool  `json:"removed"`
		}
		if err := json.Unmarshal([]byte(out), &removed); err != nil {
			t.Fatalf("project rm --json is not JSON: %v (%q)", err, out)
		}
		if removed.ID != added.ID || !removed.Removed {
			t.Errorf("project rm --json = %+v, want the id and removed", removed)
		}
		if out, code := runVincent(t, dataDir, cfgDir, "project", "rm", id); code != 1 {
			t.Errorf("project rm twice: code %d, want 1 (out %q)", code, out)
		}
	})

	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "stop"); code != 0 {
		t.Fatalf("daemon stop: code %d, out %q", code, out)
	}

	// Every action is a thin client, and no thin client starts a daemon: a
	// `task retry` inside a shell loop that silently spawned one would be a
	// surprise in a CI job (PR U decision).
	t.Run("no daemon exits 2 and starts nothing", func(t *testing.T) {
		for _, args := range [][]string{
			{"task", "retry", "1"},
			{"task", "approve", "1"},
			{"task", "answer", "1", "--allow"},
			{"task", "archive", "1"},
			{"project", "rm", "1"},
		} {
			out, code := runVincent(t, dataDir, cfgDir, args...)
			if code != 2 {
				t.Errorf("%v with no daemon: code %d, want 2 (out %q)", args, code, out)
			}
			if !strings.Contains(out, "vincent daemon start") {
				t.Errorf("%v does not point at `vincent daemon start`: %q", args, out)
			}
		}
		if out, code := runVincent(t, dataDir, cfgDir, "daemon", "status"); code != 1 {
			t.Errorf("daemon status after the actions: code %d, want 1 (out %q)", code, out)
		}
	})
}

// writeActionWorkflow puts a project-scope workflow in the repository, named after
// the file so the CLI can select it with --workflow.
func writeActionWorkflow(t *testing.T, repo, name, body string) {
	t.Helper()
	dir := filepath.Join(repo, ".vincent", "workflows")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"),
		[]byte("name: "+name+"\n"+body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// addActionTask creates a task in project 1 and returns its id as the string every
// subcommand takes.
func addActionTask(t *testing.T, dataDir, cfgDir, title string, extra ...string) string {
	t.Helper()
	args := append([]string{"task", "add", "--project", "1", "--title", title, "--json"}, extra...)
	out, code := runVincent(t, dataDir, cfgDir, args...)
	if code != 0 {
		t.Fatalf("task add %q: code %d, out %q", title, code, out)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("task add --json is not JSON: %v (%q)", err, out)
	}
	return strconv.FormatInt(created.ID, 10)
}

// taskView is the part of `task show --json` these assertions read.
type taskView struct {
	ID               int64    `json:"id"`
	State            string   `json:"state"`
	BranchName       string   `json:"branch_name"`
	BlockReason      string   `json:"block_reason"`
	AvailableActions []string `json:"available_actions"`
}

func taskJSON(t *testing.T, dataDir, cfgDir, id string) taskView {
	t.Helper()
	out, code := runVincent(t, dataDir, cfgDir, "task", "show", id, "--json")
	if code != 0 {
		t.Fatalf("task show %s --json: code %d, out %q", id, code, out)
	}
	var v taskView
	if err := json.Unmarshal([]byte(jsonObject(out)), &v); err != nil {
		t.Fatalf("task show --json is not JSON: %v (%q)", err, out)
	}
	return v
}

func taskState(t *testing.T, dataDir, cfgDir, id string) string {
	t.Helper()
	return taskJSON(t, dataDir, cfgDir, id).State
}

// waitForState polls until the task reaches want. The daemon owns the
// transition — admission, the step, the block — so the test waits for it
// rather than predicting when it happens.
func waitForState(t *testing.T, dataDir, cfgDir, id, want string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = taskState(t, dataDir, cfgDir, id)
		if last == want {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("task %s is %s after 90s, want %s", id, last, want)
}
