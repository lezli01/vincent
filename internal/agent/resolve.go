package agent

// DefaultAgent is the adapter used when no §8.6 level names an agent.
const DefaultAgent = "claude"

// Selection is the resolved (agent, model, effort) triple for one agent step
// (spec §8.6). It is recorded on the StepRun and passed to the adapter.
type Selection struct {
	Agent  string
	Model  string
	Effort string
}

// Level is the agent/model/effort one §8.6 precedence level declares. Levels
// are plain values so this package depends on neither the workflow nor the
// store types that carry them (T2.11 decision).
type Level struct {
	Agent  string
	Model  string
	Effort string
}

// Resolve applies the §8.6 precedence — explicit step field, then the
// task-level override, then workflow defaults, then the adapter default
// (empty, meaning the CLI decides).
//
// Model and effort are agent-scoped: they only inherit from a level whose
// agent matches the step's resolved agent, so a claude alias never reaches
// codex.
func Resolve(step, override, defaults Level) Selection {
	// Level 4 of §8.6 is the adapter default; when no level names an agent at
	// all, the daemon's default adapter runs the step.
	resolved := firstNonEmpty(step.Agent, override.Agent, defaults.Agent, DefaultAgent)

	// Each level carries the agent it was written for; a level whose agent
	// differs from the resolved one contributes nothing but its own agent
	// field. The task override inherits the workflow's agent when it does
	// not name one, since it replaces the workflow defaults.
	overrideAgent := firstNonEmpty(override.Agent, defaults.Agent)
	inScope := func(levelAgent string) bool { return levelAgent == "" || levelAgent == resolved }

	sel := Selection{Agent: resolved, Model: step.Model, Effort: step.Effort}
	if sel.Model == "" && inScope(overrideAgent) {
		sel.Model = override.Model
	}
	if sel.Model == "" && inScope(defaults.Agent) {
		sel.Model = defaults.Model
	}
	if sel.Effort == "" && inScope(overrideAgent) {
		sel.Effort = override.Effort
	}
	if sel.Effort == "" && inScope(defaults.Agent) {
		sel.Effort = defaults.Effort
	}
	return sel
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
