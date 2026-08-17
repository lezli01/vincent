package workflow

// The `on_input: require` gate (spec §7.4, §8.2 — task 013). A step that
// requires mid-run input runs only on an adapter that can stop and wait for a
// human answer; this file owns the resolution and the mismatch clause every
// enforcement point embeds, the way platform.go owns the §8.1.1 one.

import (
	"fmt"

	"github.com/lezli01/vincent/internal/agent"
)

// InputPolicy resolves a step's `on_input` (§7.4): the step's own field, then
// workflow defaults, then `wait`. It is exported because the API's gate and
// the engine's pre-flight must read the same value the engine runs under.
func (w *Workflow) InputPolicy(step Step) string {
	if w == nil {
		return firstNonEmptyString(step.OnInput, InputWait)
	}
	return firstNonEmptyString(step.OnInput, w.Defaults.OnInput, InputWait)
}

// StepRequiresInput reports whether the step resolves to `require`.
//
// A step's own `on_input` wins over `defaults:` like every other field, so
// `defaults.on_input: require` with a step's `on_input: deny` leaves that one
// step unattended — deliberate, and the only way to say it (task 013
// decision 7).
func (w *Workflow) StepRequiresInput(step Step) bool {
	return step.Type == StepAgent && w.InputPolicy(step) == InputRequire
}

// RequiresInput reports whether the *task-level* agent choice is constrained
// by this workflow: some step requires mid-run input and does not pin its own
// `agent:`, so the agent it runs on is the one the task picks (§8.6 level 2).
//
// A workflow whose requiring steps all pin a capable agent constrains nothing
// — restricting the picker on account of a step that ignores the picker would
// refuse a task that runs perfectly (task 013 decision 6).
func (w *Workflow) RequiresInput() bool {
	if w == nil {
		return false
	}
	for _, step := range w.Steps {
		if w.StepRequiresInput(step) && step.Agent == "" {
			return true
		}
	}
	return false
}

// InputMismatch explains why this workflow cannot run under the given task
// overrides, in one clause the API's 400 and the TUI's row error both embed.
// It is empty when every requiring step resolves to an agent that can ask.
//
// incapable answers "this adapter is known not to support mid-run input" —
// a *positive* no. An unknown answer must be false: a probe that could not
// speak is not evidence (task 013 decision 5).
func (w *Workflow) InputMismatch(override agent.Level, incapable func(string) bool) string {
	if w == nil || incapable == nil {
		return ""
	}
	defaults := agent.Level{Agent: w.Defaults.Agent, Model: w.Defaults.Model, Effort: w.Defaults.Effort}
	for _, step := range w.Steps {
		if !w.StepRequiresInput(step) {
			continue
		}
		sel := agent.Resolve(
			agent.Level{Agent: step.Agent, Model: step.Model, Effort: step.Effort},
			override, defaults,
		)
		if incapable(sel.Agent) {
			return fmt.Sprintf(
				"step %q requires mid-run input and agent %q cannot provide it",
				step.DisplayName(), sel.Agent)
		}
	}
	return ""
}

// firstNonEmptyString is the local copy of the resolver's helper; workflow
// resolves two of its own fields and does not otherwise depend on taskrun.
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
