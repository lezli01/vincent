package taskrun

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// TestResolveSelection walks the §8.6 precedence ladder and the
// agent-scoped inheritance rule that keeps a claude alias away from codex.
func TestResolveSelection(t *testing.T) {
	tests := []struct {
		name     string
		step     workflow.Step
		defaults workflow.Defaults
		task     store.Task
		want     selection
	}{
		{
			name: "nothing declared falls back to the daemon default agent",
			want: selection{Agent: DefaultAgent},
		},
		{
			name:     "workflow defaults apply",
			defaults: workflow.Defaults{Agent: "claude", Model: "sonnet", Effort: "low"},
			want:     selection{Agent: "claude", Model: "sonnet", Effort: "low"},
		},
		{
			name:     "task override replaces workflow defaults",
			defaults: workflow.Defaults{Agent: "claude", Model: "sonnet", Effort: "low"},
			task:     store.Task{AgentOverride: "claude", ModelOverride: "opus", EffortOverride: "max"},
			want:     selection{Agent: "claude", Model: "opus", Effort: "max"},
		},
		{
			name:     "explicit step fields beat the task override",
			step:     workflow.Step{Agent: "claude", Model: "haiku", Effort: "low"},
			defaults: workflow.Defaults{Agent: "claude", Model: "sonnet"},
			task:     store.Task{AgentOverride: "claude", ModelOverride: "opus", EffortOverride: "max"},
			want:     selection{Agent: "claude", Model: "haiku", Effort: "low"},
		},
		{
			name:     "a step pinning another agent does not inherit its model",
			step:     workflow.Step{Agent: "codex"},
			defaults: workflow.Defaults{Agent: "claude", Model: "sonnet", Effort: "max"},
			want:     selection{Agent: "codex"},
		},
		{
			name:     "a step pinning another agent keeps its own model and effort",
			step:     workflow.Step{Agent: "codex", Effort: "high"},
			defaults: workflow.Defaults{Agent: "claude", Model: "sonnet", Effort: "max"},
			want:     selection{Agent: "codex", Effort: "high"},
		},
		{
			name:     "a task override switching agent drops the workflow model",
			defaults: workflow.Defaults{Agent: "claude", Model: "sonnet", Effort: "max"},
			task:     store.Task{AgentOverride: "codex"},
			want:     selection{Agent: "codex"},
		},
		{
			name:     "a task override switching agent carries its own model",
			defaults: workflow.Defaults{Agent: "claude", Model: "sonnet"},
			task:     store.Task{AgentOverride: "codex", ModelOverride: "gpt-5.2"},
			want:     selection{Agent: "codex", Model: "gpt-5.2"},
		},
		{
			name:     "a model-only task override rides the workflow agent",
			defaults: workflow.Defaults{Agent: "claude", Model: "sonnet"},
			task:     store.Task{ModelOverride: "opus"},
			want:     selection{Agent: "claude", Model: "opus"},
		},
		{
			name:     "a model-only task override does not reach a step that switched agent",
			step:     workflow.Step{Agent: "codex"},
			defaults: workflow.Defaults{Agent: "claude"},
			task:     store.Task{ModelOverride: "opus"},
			want:     selection{Agent: "codex"},
		},
		{
			name:     "workflow defaults fill what the task override leaves unset",
			defaults: workflow.Defaults{Agent: "claude", Model: "sonnet", Effort: "low"},
			task:     store.Task{EffortOverride: "max"},
			want:     selection{Agent: "claude", Model: "sonnet", Effort: "max"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSelection(tt.step, tt.defaults, &tt.task)
			if got != tt.want {
				t.Errorf("resolveSelection = %+v, want %+v", got, tt.want)
			}
		})
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
		if got := resolvePermission(tc.step, tc.defaults); got != tc.wantPerm {
			t.Errorf("resolvePermission(%+v, %+v) = %s, want %s", tc.step, tc.defaults, got, tc.wantPerm)
		}
		if got := resolveInputPolicy(tc.step, tc.defaults); got != tc.wantIn {
			t.Errorf("resolveInputPolicy(%+v, %+v) = %s, want %s", tc.step, tc.defaults, got, tc.wantIn)
		}
	}
}
