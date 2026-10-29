package copilot

import (
	"context"

	"github.com/lezli01/vincent/internal/agent"
)

// Options implements agent.Adapter. It probes nothing: copilot ships no
// model enumeration worth trusting — `help config` lists models the account
// cannot use, and every explicit `--model` tried at capture was refused (task
// 138 decision 9) — and its effort catalog is task 138.6's. So the catalog is
// the curated floor, with no network call and no auth probe behind it.
func (a *Adapter) Options(context.Context) (agent.Options, error) { return a.Curated(), nil }

// Curated implements agent.Adapter: the compiled-in floor, no probe.
//
// Models is empty and DefaultModel is "": with no `--model`, the CLI resolves
// `auto` by itself (decision 9), and buildArgs passes `--model` only when §8.6
// resolved one. Naming `auto` here would claim vincent passes a value it
// never does.
func (a *Adapter) Curated() agent.Options {
	return agent.Options{
		Models:       []agent.Option{},
		Efforts:      []agent.Option{},
		DefaultModel: "",
		// `copilot --output-format json` has no input channel vincent
		// drives: the prompt is the whole of stdin (§9.9).
		InputSupport: agent.InputNever,
		// Restricted mode is refused until task 138.10 (#862) proves the
		// `--available-tools` set per OS (decision 8).
		RestrictedSupport: agent.RestrictedNever,
	}
}
