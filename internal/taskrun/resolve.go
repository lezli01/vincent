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

// resolveSelection maps the engine's types onto the §8.6 resolver, which
// lives in internal/agent since T2.11 so catalog validation shares it.
func resolveSelection(step workflow.Step, defaults workflow.Defaults, task *store.Task) agent.Selection {
	return agent.Resolve(
		agent.Level{Agent: step.Agent, Model: step.Model, Effort: step.Effort},
		agent.Level{Agent: task.AgentOverride, Model: task.ModelOverride, Effort: task.EffortOverride},
		agent.Level{Agent: defaults.Agent, Model: defaults.Model, Effort: defaults.Effort},
	)
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
//
// `require` resolves to wait. It differs from wait only in what must be true
// before the step starts (task 013), and the pre-flight that enforces that has
// already run by the time this value reaches an adapter — so InputPolicy stays
// two-valued and no adapter learns a third.
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

// resolveInputTimeout resolves the bound on each awaiting_input wait (§7.4):
// step field, then workflow defaults, then the daemon default.
func resolveInputTimeout(step workflow.Step, defaults workflow.Defaults, cfg config.Config) time.Duration {
	if step.InputTimeout != nil {
		return step.InputTimeout.Std()
	}
	if defaults.InputTimeout != nil {
		return defaults.InputTimeout.Std()
	}
	return cfg.Defaults.InputTimeout.Std()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
