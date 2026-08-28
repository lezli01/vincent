package taskrun

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// TestResolveSelection proves the wrapper feeds each §8.6 level's fields to
// the shared resolver; the precedence ladder itself is table-tested where
// the resolver lives, in internal/agent (T2.11).
func TestResolveSelection(t *testing.T) {
	got := resolveSelection(
		workflow.Step{Effort: "low"},
		workflow.Defaults{Agent: "claude", Model: "sonnet"},
		&store.Task{ModelOverride: "opus"},
	)
	want := agent.Selection{Agent: "claude", Model: "opus", Effort: "low"}
	if got != want {
		t.Errorf("resolveSelection = %+v, want %+v (step effort, override model, defaults agent)", got, want)
	}
}

func TestResolveStepSettings(t *testing.T) {
	two, zero := 2, 0
	fiveMin := config.Duration(5 * time.Minute)
	oneHour := config.Duration(time.Hour)
	cfg := config.Default()

	// max_retries: step, then defaults, then §7.2's default of 1.
	if got := resolveMaxRetries(workflow.Step{MaxRetries: &two}, workflow.Defaults{MaxRetries: &zero}); got != 2 {
		t.Errorf("step max_retries = %d, want 2", got)
	}
	if got := resolveMaxRetries(workflow.Step{}, workflow.Defaults{MaxRetries: &zero}); got != 0 {
		t.Errorf("defaults max_retries = %d, want 0", got)
	}
	if got := resolveMaxRetries(workflow.Step{}, workflow.Defaults{}); got != defaultMaxRetries {
		t.Errorf("fallback max_retries = %d, want %d", got, defaultMaxRetries)
	}

	// retry_backoff: step, then defaults, then zero — which is an immediate
	// retry, i.e. every workflow written before task 028.
	thirtySec := config.Duration(30 * time.Second)
	twoMin := config.Duration(2 * time.Minute)
	if got := resolveRetryBackoff(
		workflow.Step{RetryBackoff: &thirtySec}, workflow.Defaults{RetryBackoff: &twoMin},
	); got != 30*time.Second {
		t.Errorf("step retry_backoff = %s, want 30s", got)
	}
	if got := resolveRetryBackoff(workflow.Step{}, workflow.Defaults{RetryBackoff: &twoMin}); got != 2*time.Minute {
		t.Errorf("defaults retry_backoff = %s, want 2m", got)
	}
	if got := resolveRetryBackoff(workflow.Step{}, workflow.Defaults{}); got != 0 {
		t.Errorf("fallback retry_backoff = %s, want 0", got)
	}
	// An explicit zero on the step beats a workflow-wide default: that is how
	// one step asks for an immediate second shot at its own compile error.
	zeroBackoff := config.Duration(0)
	if got := resolveRetryBackoff(
		workflow.Step{RetryBackoff: &zeroBackoff}, workflow.Defaults{RetryBackoff: &twoMin},
	); got != 0 {
		t.Errorf("explicit zero retry_backoff = %s, want 0", got)
	}

	// timeout: step, then defaults, then the per-type daemon default.
	agentStep := workflow.Step{Type: workflow.StepAgent}
	commandStep := workflow.Step{Type: workflow.StepCommand}
	if got := resolveTimeout(workflow.Step{Timeout: &fiveMin}, workflow.Defaults{Timeout: &oneHour}, cfg); got != 5*time.Minute {
		t.Errorf("step timeout = %s, want 5m", got)
	}
	if got := resolveTimeout(agentStep, workflow.Defaults{Timeout: &oneHour}, cfg); got != time.Hour {
		t.Errorf("defaults timeout = %s, want 1h", got)
	}
	if got := resolveTimeout(agentStep, workflow.Defaults{}, cfg); got != cfg.Defaults.AgentTimeout.Std() {
		t.Errorf("agent fallback timeout = %s, want %s", got, cfg.Defaults.AgentTimeout)
	}
	if got := resolveTimeout(commandStep, workflow.Defaults{}, cfg); got != cfg.Defaults.CommandTimeout.Std() {
		t.Errorf("command fallback timeout = %s, want %s", got, cfg.Defaults.CommandTimeout)
	}
	// A check inherits the command timeout, never the (much longer) agent one.
	if got := resolveCheckTimeout(agentStep, cfg); got != cfg.Defaults.CommandTimeout.Std() {
		t.Errorf("check timeout = %s, want the command default %s", got, cfg.Defaults.CommandTimeout)
	}
	if got := resolveCheckTimeout(workflow.Step{CheckTimeout: &fiveMin}, cfg); got != 5*time.Minute {
		t.Errorf("explicit check timeout = %s, want 5m", got)
	}
}

func TestResolvePermissionAndInputPolicy(t *testing.T) {
	cases := []struct {
		step     workflow.Step
		defaults workflow.Defaults
		wantPerm agent.PermissionMode
		wantIn   agent.InputPolicy
	}{
		{wantPerm: agent.FullAuto, wantIn: agent.InputWait},
		{
			defaults: workflow.Defaults{PermissionMode: workflow.PermissionRestricted, OnInput: workflow.InputDeny},
			wantPerm: agent.Restricted, wantIn: agent.InputDeny,
		},
		{
			step:     workflow.Step{PermissionMode: workflow.PermissionFullAuto, OnInput: workflow.InputWait},
			defaults: workflow.Defaults{PermissionMode: workflow.PermissionRestricted, OnInput: workflow.InputDeny},
			wantPerm: agent.FullAuto, wantIn: agent.InputWait,
		},
	}
	for _, tc := range cases {
		wf := &workflow.Workflow{Defaults: tc.defaults}
		if got := resolvePermission(wf, tc.step); got != tc.wantPerm {
			t.Errorf("resolvePermission(%+v, %+v) = %s, want %s", tc.step, tc.defaults, got, tc.wantPerm)
		}
		if got := resolveInputPolicy(tc.step, tc.defaults); got != tc.wantIn {
			t.Errorf("resolveInputPolicy(%+v, %+v) = %s, want %s", tc.step, tc.defaults, got, tc.wantIn)
		}
	}
}
