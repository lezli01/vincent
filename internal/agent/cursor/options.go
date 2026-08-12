package cursor

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// modelsTimeout bounds the `cursor-agent models` probe. It is generous
// because, uniquely among the adapters, this probe is an authenticated
// network call rather than a read of the binary's own help text (spec §9.6,
// §9.7) — and it is bounded for exactly the same reason.
const modelsTimeout = 20 * time.Second

// curatedModels is the floor the probe merges into: `auto` alone. Cursor's
// enumeration is account-scoped *and* over-broad — a listed id can still be
// rejected at run time — so the only id worth shipping as curated is the one
// that means "let the server choose" (spec §9.7).
var curatedModels = []string{defaultModel}

// modelLineRe matches one entry of `cursor-agent models` output:
// `gpt-5.3-codex-low - Codex 5.3 Low`. The heading and the trailing `Tip:`
// paragraph do not match, which is what keeps them out of the catalog.
var modelLineRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._\-\[\]=,]*) - \S`)

// Options implements agent.Adapter: an ad-hoc `cursor-agent models` probe.
// Parsed values carry source "cli" and are merged with the curated floor;
// when the binary is missing, unauthenticated, or offline, the curated
// catalog is returned alongside the error — degrade, never block (spec §9.6).
func (a *Adapter) Options(ctx context.Context) (agent.Options, error) {
	path, err := a.resolvePath()
	if err != nil {
		return a.Curated(), err
	}
	out, _, err := agent.Probe(ctx, modelsTimeout, path, "models")
	if err != nil {
		return a.Curated(), fmt.Errorf("cursor-agent models failed: %w", err)
	}
	return mergeOptions(parseModels(string(out))), nil
}

// Curated implements agent.Adapter: the compiled-in floor, no probe.
func (a *Adapter) Curated() agent.Options { return mergeOptions(nil) }

// mergeOptions builds the catalog: probed values first (source cli), then
// curated values not already present (source curated).
//
// Efforts is empty and stays empty: cursor has no effort flag, and reasoning
// depth is selected through the model id (spec §9.7). DefaultModel is the one
// non-empty adapter default in vincent — the value buildArgs actually passes
// when §8.6 resolves nothing — so `/v1/resolve` can name it at level 4.
func mergeOptions(cliModels []string) agent.Options {
	seen := make(map[string]bool, len(cliModels)+len(curatedModels))
	models := make([]agent.Option, 0, len(cliModels)+len(curatedModels))
	for _, v := range cliModels {
		if !seen[v] {
			seen[v] = true
			models = append(models, agent.Option{Value: v, Source: agent.SourceCLI})
		}
	}
	for _, v := range curatedModels {
		if !seen[v] {
			seen[v] = true
			models = append(models, agent.Option{Value: v, Source: agent.SourceCurated})
		}
	}
	return agent.Options{
		Models:       models,
		Efforts:      []agent.Option{},
		DefaultModel: defaultModel,
	}
}

// parseModels extracts the model ids from `cursor-agent models` output
// (verified against 2026.08.04-aaa8809). Unparseable output yields nothing —
// the caller falls back to curated-only.
func parseModels(out string) []string {
	var models []string
	for _, line := range strings.Split(out, "\n") {
		if m := modelLineRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			models = append(models, m[1])
		}
	}
	return models
}
