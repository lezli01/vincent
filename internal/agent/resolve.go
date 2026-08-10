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

// Source names the §8.6 precedence level a resolved field came from. It is
// what lets a client report *why* a step runs what it runs without
// re-implementing the precedence (T4.7 decision).
type Source string

// The four §8.6 precedence levels, in order.
const (
	SourceStep     Source = "step"     // §8.6 level 1
	SourceTask     Source = "task"     // §8.6 level 2
	SourceWorkflow Source = "workflow" // §8.6 level 3
	SourceAdapter  Source = "adapter"  // §8.6 level 4
)

// Sources reports the level each field of a Selection came from. A field no
// level set is SourceAdapter — for Agent that means DefaultAgent, for model
// and effort it means the adapter's own default (empty here; the catalog is
// what can name it).
type Sources struct {
	Agent  Source
	Model  Source
	Effort Source
}

// Resolve applies the §8.6 precedence — explicit step field, then the
// task-level override, then workflow defaults, then the adapter default
// (empty, meaning the CLI decides).
//
// Model and effort are agent-scoped: they only inherit from a level whose
// agent matches the step's resolved agent, so a claude alias never reaches
// codex.
func Resolve(step, override, defaults Level) Selection {
	sel, _ := ResolveWithSources(step, override, defaults)
	return sel
}

// ResolveWithSources is Resolve plus the provenance of each field. The engine
// only needs the triple; the API's resolution endpoint (§13.2) needs to say
// which level won, and both must come from one implementation or the two
// answers drift.
func ResolveWithSources(step, override, defaults Level) (Selection, Sources) {
	// Level 4 of §8.6 is the adapter default; when no level names an agent at
	// all, the daemon's default adapter runs the step.
	resolved := firstNonEmpty(step.Agent, override.Agent, defaults.Agent, DefaultAgent)

	src := Sources{Agent: SourceAdapter, Model: SourceAdapter, Effort: SourceAdapter}
	switch {
	case step.Agent != "":
		src.Agent = SourceStep
	case override.Agent != "":
		src.Agent = SourceTask
	case defaults.Agent != "":
		src.Agent = SourceWorkflow
	}

	// Each level carries the agent it was written for; a level whose agent
	// differs from the resolved one contributes nothing but its own agent
	// field. The task override inherits the workflow's agent when it does
	// not name one, since it replaces the workflow defaults.
	overrideAgent := firstNonEmpty(override.Agent, defaults.Agent)
	inScope := func(levelAgent string) bool { return levelAgent == "" || levelAgent == resolved }

	sel := Selection{Agent: resolved, Model: step.Model, Effort: step.Effort}
	if sel.Model != "" {
		src.Model = SourceStep
	}
	if sel.Effort != "" {
		src.Effort = SourceStep
	}
	if sel.Model == "" && inScope(overrideAgent) && override.Model != "" {
		sel.Model, src.Model = override.Model, SourceTask
	}
	if sel.Model == "" && inScope(defaults.Agent) && defaults.Model != "" {
		sel.Model, src.Model = defaults.Model, SourceWorkflow
	}
	if sel.Effort == "" && inScope(overrideAgent) && override.Effort != "" {
		sel.Effort, src.Effort = override.Effort, SourceTask
	}
	if sel.Effort == "" && inScope(defaults.Agent) && defaults.Effort != "" {
		sel.Effort, src.Effort = defaults.Effort, SourceWorkflow
	}
	return sel, src
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
