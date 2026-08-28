package workflow

// The `permission_mode: restricted` gate (spec §9.4, §8.2 — task 041). A step
// that asks to run restricted runs only on an adapter that can restrict on
// this host; this file owns the resolution and the mismatch clause every
// enforcement point embeds, the way input.go owns the §7.4 one and
// platform.go the §8.1.1 one.
//
// It is the only security-sensitive capability gap vincent has: every other
// missing capability degrades visibly, while running a step full-auto because
// restricting was unavailable inverts the choice the step made (§9.7). That is
// why this one moved forward from the engine to task creation, and why the
// engine's restricted_unsupported backstop stays where it is.

import (
	"fmt"

	"github.com/lezli01/vincent/internal/agent"
)

// PermissionMode resolves a step's `permission_mode` (§9.4): the step's own
// field, then workflow defaults, then full-auto. It is exported because the
// API's gate and the engine must read the same value the run executes under —
// the reason InputPolicy is exported.
func (w *Workflow) PermissionMode(step Step) string {
	if w == nil {
		return firstNonEmptyString(step.PermissionMode, PermissionFullAuto)
	}
	return firstNonEmptyString(step.PermissionMode, w.Defaults.PermissionMode, PermissionFullAuto)
}

// StepRequiresRestricted reports whether the step resolves to `restricted`.
//
// Only agent steps count: `permission_mode` is what an agent CLI is launched
// with, and a command step's shell has never consulted it.
func (w *Workflow) StepRequiresRestricted(step Step) bool {
	return step.Type == StepAgent && w.PermissionMode(step) == PermissionRestricted
}

// RestrictedMismatch explains why this workflow cannot run under the given
// task overrides, in one clause the API's 400 embeds. It is empty when every
// restricted step resolves to an adapter that can restrict here.
//
// incapable answers "this adapter is known not to restrict on this host" — a
// *positive* no, and one that needs no installed binary, because the answer
// depends on adapter identity and GOOS rather than on the build (task 041).
func (w *Workflow) RestrictedMismatch(override agent.Level, incapable func(string) bool) string {
	if w == nil || incapable == nil {
		return ""
	}
	defaults := agent.Level{Agent: w.Defaults.Agent, Model: w.Defaults.Model, Effort: w.Defaults.Effort}
	for _, step := range w.Steps {
		if !w.StepRequiresRestricted(step) {
			continue
		}
		sel := agent.Resolve(
			agent.Level{Agent: step.Agent, Model: step.Model, Effort: step.Effort},
			override, defaults,
		)
		if incapable(sel.Agent) {
			return fmt.Sprintf(
				"step %q runs restricted and agent %q cannot restrict on this host",
				step.DisplayName(), sel.Agent)
		}
	}
	return ""
}
