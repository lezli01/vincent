package codex

import (
	"context"

	"github.com/lezli01/vincent/internal/agent"
)

// Curated catalog (spec §9.6). The codex CLI enumerates nothing (`--help`
// documents only `-c key=value`), so this is the whole catalog — source
// `curated`, no probe.
//
// Efforts include xhigh: observed working in a real codex config (PR E grill
// session, 2026-08-08) even though older docs stop at high. Models are
// deliberately empty — codex model availability is account-dependent (the
// same id is accepted on one plan and rejected with a 400 on another), so
// the catalog never advertises a model that might be rejected; pickers offer
// free text and the CLI default only (spec §9.3).
var curatedEfforts = []string{"minimal", "low", "medium", "high", "xhigh"}

// Options implements agent.Adapter: the curated catalog, verbatim. No
// subprocess runs and no error is possible — a missing binary is reported by
// Detect, not here. Defaults stay empty: the CLI decides.
func (a *Adapter) Options(context.Context) (agent.Options, error) {
	return a.Curated(), nil
}

// Curated implements agent.Adapter; for codex it is the whole catalog.
func (a *Adapter) Curated() agent.Options {
	efforts := make([]agent.Option, 0, len(curatedEfforts))
	for _, v := range curatedEfforts {
		efforts = append(efforts, agent.Option{Value: v, Source: agent.SourceCurated})
	}
	return agent.Options{
		Models:  []agent.Option{},
		Efforts: efforts,
		// `codex exec` is strictly non-interactive (§9.3): no version of it
		// can stop to ask, so a workflow step requiring input is refused
		// against this adapter at load time (task 013).
		InputSupport: agent.InputNever,
		// codex restricts through its own sandbox flags, which it has on
		// every platform it runs on (§9.3) — task 041.
		RestrictedSupport: agent.RestrictedAlways,
	}
}
