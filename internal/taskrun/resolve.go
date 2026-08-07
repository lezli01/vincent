package taskrun

import (
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// defaultMaxRetries is the §7.2 default: one retry, i.e. up to two attempts.
const defaultMaxRetries = 1

// selection is the resolved (agent, model, effort) triple for one agent step
// (spec §8.6). It is recorded on the StepRun and passed to the adapter.
type selection struct {
	Agent  string
	Model  string
	Effort string
}

// resolveSelection applies the §8.6 precedence — explicit step field, then
// the task-level override, then workflow defaults, then the adapter default
// (empty, meaning the CLI decides).
//
// Model and effort are agent-scoped: they only inherit from a level whose
// agent matches the step's resolved agent, so a claude alias never reaches
// codex. T2.11 moves this into internal/agent and adds catalog validation.
func resolveSelection(step workflow.Step, defaults workflow.Defaults, task *store.Task) selection {
	// Level 4 of §8.6 is the adapter default; when no level names an agent at
	// all, the daemon's default adapter runs the step.
	resolved := firstNonEmpty(step.Agent, task.AgentOverride, defaults.Agent, DefaultAgent)

	// Each level carries the agent it was written for; a level whose agent
	// differs from the resolved one contributes nothing but its own agent
	// field. The task override inherits the workflow's agent when it does
	// not name one, since it replaces the workflow defaults.
	taskAgent := firstNonEmpty(task.AgentOverride, defaults.Agent)
	inScope := func(levelAgent string) bool { return levelAgent == "" || levelAgent == resolved }

	sel := selection{Agent: resolved, Model: step.Model, Effort: step.Effort}
	if sel.Model == "" && inScope(taskAgent) {
		sel.Model = task.ModelOverride
	}
	if sel.Model == "" && inScope(defaults.Agent) {
		sel.Model = defaults.Model
	}
	if sel.Effort == "" && inScope(taskAgent) {
		sel.Effort = task.EffortOverride
	}
	if sel.Effort == "" && inScope(defaults.Agent) {
		sel.Effort = defaults.Effort
	}
	return sel
}

// resolvePermission resolves the step's permission mode (§9.4): step field,
// then workflow defaults, then full-auto.
func resolvePermission(step workflow.Step, defaults workflow.Defaults) agent.PermissionMode {
	switch firstNonEmpty(step.PermissionMode, defaults.PermissionMode) {
	case workflow.PermissionRestricted:
		return agent.Restricted
	default:
		return agent.FullAuto
	}
}

// resolveInputPolicy resolves the step's reaction to an input request
// (§7.4): step field, then workflow defaults, then wait.
func resolveInputPolicy(step workflow.Step, defaults workflow.Defaults) agent.InputPolicy {
	switch firstNonEmpty(step.OnInput, defaults.OnInput) {
	case workflow.InputDeny:
		return agent.InputDeny
	default:
		return agent.InputWait
	}
}

// resolveMaxRetries resolves the step's retry budget (§7.2): step field,
// then workflow defaults, then 1.
func resolveMaxRetries(step workflow.Step, defaults workflow.Defaults) int {
	if step.MaxRetries != nil {
		return *step.MaxRetries
	}
	if defaults.MaxRetries != nil {
		return *defaults.MaxRetries
	}
	return defaultMaxRetries
}

// resolveTimeout resolves a step's timeout (§7.2): step field, then workflow
// defaults, then the daemon default for the step type.
func resolveTimeout(step workflow.Step, defaults workflow.Defaults, cfg config.Config) time.Duration {
	if step.Timeout != nil {
		return step.Timeout.Std()
	}
	if defaults.Timeout != nil {
		return defaults.Timeout.Std()
	}
	if step.Type == workflow.StepAgent {
		return cfg.Defaults.AgentTimeout.Std()
	}
	return cfg.Defaults.CommandTimeout.Std()
}

// resolveCheckTimeout resolves the timeout of a step's check command, which
// defaults to the daemon's command timeout rather than the step's own.
func resolveCheckTimeout(step workflow.Step, cfg config.Config) time.Duration {
	if step.CheckTimeout != nil {
		return step.CheckTimeout.Std()
	}
	return cfg.Defaults.CommandTimeout.Std()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
