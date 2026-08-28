package claude

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// Curated catalog (spec §9.6), shipped with vincent as the floor the --help
// probe merges into. Values as of claude 2.1.x.
var (
	curatedModels  = []string{"sonnet", "opus", "haiku"}
	curatedEfforts = []string{"low", "medium", "high", "xhigh", "max"}
)

// Options implements agent.Adapter: an ad-hoc `claude --help` probe. Parsed
// values carry source "cli" and are merged with the curated catalog; when
// the binary is missing or its help output fails, the curated catalog is
// returned alongside the error — degrade, never block (spec §9.6). Caching
// by binary identity belongs to the /v1/agents endpoint (T2.11), not here.
func (a *Adapter) Options(ctx context.Context) (agent.Options, error) {
	path, err := a.resolvePath()
	if err != nil {
		return mergeOptions(nil, nil), err
	}
	out, _, err := agent.Probe(ctx, helpTimeout, path, "--help")
	if err != nil {
		return mergeOptions(nil, nil), fmt.Errorf("claude --help failed: %w", err)
	}
	models, efforts := parseHelp(string(out))
	return mergeOptions(models, efforts), nil
}

// Curated implements agent.Adapter: the compiled-in catalog, no probe.
func (a *Adapter) Curated() agent.Options { return mergeOptions(nil, nil) }

// mergeOptions builds the catalog: probed values first (source cli), then
// curated values not already present (source curated). Defaults stay empty —
// the CLI decides.
func mergeOptions(cliModels, cliEfforts []string) agent.Options {
	return agent.Options{
		Models:  mergeValues(cliModels, curatedModels),
		Efforts: mergeValues(cliEfforts, curatedEfforts),
		// Whether *this* claude can take mid-run input is a version question
		// Detect answers (§9.3); the catalog only says the answer is worth
		// asking, so §8.2 leaves a requiring step alone (task 013).
		InputSupport: agent.InputDetected,
		// `--allowedTools` is a flag every claude build has (§9.2), so
		// restricted mode is available on every platform (task 040).
		RestrictedSupport: agent.RestrictedAlways,
	}
}

func mergeValues(cli, curated []string) []Option {
	seen := make(map[string]bool, len(cli)+len(curated))
	out := make([]Option, 0, len(cli)+len(curated))
	for _, v := range cli {
		if !seen[v] {
			seen[v] = true
			out = append(out, Option{Value: v, Source: agent.SourceCLI})
		}
	}
	for _, v := range curated {
		if !seen[v] {
			seen[v] = true
			out = append(out, Option{Value: v, Source: agent.SourceCurated})
		}
	}
	return out
}

// Option aliases the shared option type for brevity inside this package.
type Option = agent.Option

var (
	// optionStartRe matches the first line of an option block in commander
	// help output, e.g. `  --model <model>  Model for the session...`.
	optionStartRe = regexp.MustCompile(`^\s+(?:-\w,\s+)?--([\w-]+)`)
	// choicesRe extracts double-quoted values from `(choices: "low", ...)`.
	choicesRe = regexp.MustCompile(`\(choices:\s*([^)]*)\)`)
	quotedRe  = regexp.MustCompile(`"([^"]+)"`)
	// aliasRe extracts single-quoted pure-alpha words — model aliases like
	// 'sonnet', deliberately not dated full names like
	// 'claude-sonnet-4-5-20250929' (examples of format, not stable options).
	aliasRe = regexp.MustCompile(`'([a-z]+)'`)
)

// parseHelp extracts the --effort enum and the documented --model aliases
// from claude's --help text (verified against 2.1.224). Descriptions wrap
// across indented lines, so options are parsed as blocks. An unparseable
// help yields empty slices — the caller falls back to curated-only.
func parseHelp(help string) (models, efforts []string) {
	blocks := splitOptionBlocks(help)
	if text, ok := blocks["model"]; ok {
		for _, m := range aliasRe.FindAllStringSubmatch(text, -1) {
			models = append(models, m[1])
		}
	}
	if text, ok := blocks["effort"]; ok {
		if m := choicesRe.FindStringSubmatch(text); m != nil {
			for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
				efforts = append(efforts, q[1])
			}
		}
	}
	return models, efforts
}

// splitOptionBlocks groups help lines into per-flag blocks: a line matching
// `--flag` starts a block; following lines that don't start one continue it.
func splitOptionBlocks(help string) map[string]string {
	blocks := make(map[string]string)
	var name string
	var b strings.Builder
	flush := func() {
		if name != "" {
			blocks[name] = b.String()
		}
		b.Reset()
	}
	for _, line := range strings.Split(help, "\n") {
		if m := optionStartRe.FindStringSubmatch(line); m != nil {
			flush()
			name = m[1]
		}
		if name != "" {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	flush()
	return blocks
}
