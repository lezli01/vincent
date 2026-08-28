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

// resolvePermission resolves the step's permission mode (§9.4) and maps it
// onto the adapter vocabulary. The resolution itself lives in
// workflow.PermissionMode: the API refuses a restricted step on an adapter
// that cannot restrict here (task 040), and a gate that resolved the field
// differently from the engine would refuse the wrong tasks.
func resolvePermission(wf *workflow.Workflow, step workflow.Step) agent.PermissionMode {
	if wf.PermissionMode(step) == workflow.PermissionRestricted {
		return agent.Restricted
	}
	return agent.FullAuto
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

// resolveRetryBackoff resolves the wait between a step's attempts (§7.2,
// task 028): step field, then workflow defaults, then zero.
//
// Two levels, not three: there is no `config.yaml` key, exactly as there is
// none for `max_retries`. Retry policy is a workflow's business, and
// `config.Defaults` is timeouts (PR V decision). Zero is the default and
// means today's behaviour — an immediate retry.
func resolveRetryBackoff(step workflow.Step, defaults workflow.Defaults) time.Duration {
	if step.RetryBackoff != nil {
		return step.RetryBackoff.Std()
	}
	if defaults.RetryBackoff != nil {
		return defaults.RetryBackoff.Std()
	}
	return 0
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
