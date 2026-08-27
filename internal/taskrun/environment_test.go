package taskrun

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// Command bodies that report what a step actually inherited.
const (
	envCmdPosix = `echo "pinned=[$VINCENT_T423_PINNED] ambient=[$VINCENT_T423_AMBIENT] task=[$VINCENT_TASK_ID]" > env.txt`
	//nolint:lll // one PowerShell line; splitting it would change what runs
	envCmdWindows = `"pinned=[$env:VINCENT_T423_PINNED] ambient=[$env:VINCENT_T423_AMBIENT] task=[$env:VINCENT_TASK_ID]" | Out-File -Encoding ascii env.txt`
)

func envSnapshot() string {
	return "name: env\nsteps:\n" + commandStep("report", script(envCmdPosix, envCmdWindows))
}

// TestEngineAppliesTheEnvironmentPolicy is T4.23's done-when at the process
// boundary: what a step runs under is decided by config, not by whatever
// started the daemon.
//
// The ambient variable is set on *this* process, which is the daemon's
// position — so it is inherited by accident exactly the way MSYSTEM was, and
// `unset` is what makes it stop being.
func TestEngineAppliesTheEnvironmentPolicy(t *testing.T) {
	t.Setenv("VINCENT_T423_AMBIENT", "inherited-by-accident")

	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.Environment = config.Environment{
			Inherit: config.InheritAll(),
			Unset:   []string{"VINCENT_T423_AMBIENT"},
			Set:     map[string]string{"VINCENT_T423_PINNED": "from-config"},
		}
	})
	task := h.createTask(t, envSnapshot())
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}

	raw, err := os.ReadFile(filepath.Join(done.WorktreePath, "env.txt"))
	if err != nil {
		t.Fatalf("read env.txt: %v", err)
	}
	got := strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", ""))

	if !strings.Contains(got, "pinned=[from-config]") {
		t.Errorf("environment.set did not reach the step: %q", got)
	}
	if !strings.Contains(got, "ambient=[]") {
		t.Errorf("environment.unset did not drop the inherited variable: %q", got)
	}
	// unset and set act on inherited state only. The §8.5 variables are facts
	// about the run, layered on afterwards, and must survive any policy.
	if !strings.Contains(got, "task=[") || strings.Contains(got, "task=[]") {
		t.Errorf("the VINCENT_* variables did not survive the policy: %q", got)
	}
}

// The default has to be indistinguishable from the behavior before the policy
// existed, or this is a breaking change wearing a config key.
func TestEngineDefaultEnvironmentStillInherits(t *testing.T) {
	t.Setenv("VINCENT_T423_AMBIENT", "still-here")

	h := newEngineHarnessWith(t, nil) // config.Default()
	task := h.createTask(t, envSnapshot())
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	raw, err := os.ReadFile(filepath.Join(done.WorktreePath, "env.txt"))
	if err != nil {
		t.Fatalf("read env.txt: %v", err)
	}
	if got := string(raw); !strings.Contains(got, "ambient=[still-here]") {
		t.Errorf("the default policy dropped an inherited variable: %q", got)
	}
}

// TestAgentStepGetsTheResolvedEnvironment is the other half of the boundary:
// RunSpec.Env was defined and documented from the start and never populated,
// so every adapter's `if spec.Env != nil` branch was dead and every agent ran
// under whatever started the daemon.
//
// The fake agent reports the variables it was actually given. That the
// scenario fires at all is itself part of the assertion — FAKEAGENT_SCENARIO
// reaches the child through the very policy under test.
func TestAgentStepGetsTheResolvedEnvironment(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "report-env")
	t.Setenv("FAKEAGENT_REPORT_ENV", "VINCENT_T423_PINNED,VINCENT_T423_AMBIENT")
	t.Setenv("VINCENT_T423_AMBIENT", "inherited-by-accident")

	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.Environment = config.Environment{
			Inherit: config.InheritAll(),
			Unset:   []string{"VINCENT_T423_AMBIENT"},
			Set:     map[string]string{"VINCENT_T423_PINNED": "from-config"},
		}
	})
	task := h.createTask(t, "name: env\nsteps:\n  - id: implement\n    type: agent\n    prompt: report\n")
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1", len(runs))
	}
	got := runs[0].ResultSummary
	if !strings.Contains(got, "VINCENT_T423_PINNED=[from-config]") {
		t.Errorf("environment.set did not reach the agent: %q", got)
	}
	if !strings.Contains(got, "VINCENT_T423_AMBIENT=[]") {
		t.Errorf("environment.unset did not drop the inherited variable: %q", got)
	}
}

// TestAgentStepGetsTheVincentVariables is task 036's prerequisite: an agent
// step saw the resolved base environment and none of the §8.5 run facts, so
// an agent had no way to name the step it was running — which is exactly what
// `vincent status` needs in order to address itself.
//
// VINCENT_STEP_ID and VINCENT_TASK_ID are the two the command uses; the rest
// of the block rides along, since it is one layering either way.
func TestAgentStepGetsTheVincentVariables(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "report-env")
	t.Setenv("FAKEAGENT_REPORT_ENV", "VINCENT_TASK_ID,VINCENT_STEP_ID,VINCENT_WORKTREE,VINCENT_BRANCH")

	h := newEngineHarness(t)
	task := h.createTask(t, "name: env\nsteps:\n  - id: implement\n    type: agent\n    prompt: report\n")
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1", len(runs))
	}
	got := runs[0].ResultSummary
	for _, want := range []string{
		"VINCENT_STEP_ID=[implement]",
		fmt.Sprintf("VINCENT_TASK_ID=[%d]", task.ID),
		"VINCENT_BRANCH=[" + done.BranchName + "]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("agent step environment misses %s: %q", want, got)
		}
	}
	if strings.Contains(got, "VINCENT_WORKTREE=[]") {
		t.Errorf("VINCENT_WORKTREE did not reach the agent: %q", got)
	}
}

// commandEnv's layering is the precedence, and Go's exec keeps the last of any
// duplicate name. Asserting the order here keeps a later refactor from
// reversing it, which would silently let a daemon-wide `set` overrule the
// step-level `env:` a workflow author wrote.
func TestCommandEnvLayering(t *testing.T) {
	base := []string{"FROM_POLICY=policy", "SHARED=policy"}
	rc := workflow.RenderContext{
		Task: workflow.TaskContext{ID: 7, Title: "t", BranchName: "b", BaseBranch: "main"},
	}
	got := commandEnv(base, rc, map[string]string{"SHARED": "step", "FROM_STEP": "step"})

	last := map[string]string{}
	for _, kv := range got {
		if k, v, ok := strings.Cut(kv, "="); ok {
			last[k] = v
		}
	}
	if last["FROM_POLICY"] != "policy" {
		t.Errorf("the resolved base did not reach the step: %q", last["FROM_POLICY"])
	}
	if last["FROM_STEP"] != "step" {
		t.Errorf("the step's own env: did not reach it: %q", last["FROM_STEP"])
	}
	if last["SHARED"] != "step" {
		t.Errorf("SHARED = %q, want the step's env: to win over the daemon policy", last["SHARED"])
	}
	if last["VINCENT_TASK_ID"] != "7" {
		t.Errorf("the §8.5 variables are missing: %q", last["VINCENT_TASK_ID"])
	}
}
