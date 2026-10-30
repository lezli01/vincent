package copilot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// helpTimeout bounds the --help probe subprocess, the same cold-logon bound
// as versionTimeout.
const helpTimeout = 20 * time.Second

// curatedEfforts is the floor the --help probe merges into: the
// `--reasoning-effort` values copilot 1.0.105 lists (help_1.0.105.txt).
var curatedEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// effortValuesRe matches the clap-style line that closes the
// `--reasoning-effort <level>` block: `Set the reasoning effort level
// [possible values: none, minimal, …]`. The description sits on the line
// after the flag, so the flag and its values are matched across that break.
var effortValuesRe = regexp.MustCompile(`--reasoning-effort\b[^\n]*\n[^\[\n]*\[possible values:\s*([^\]]*)\]`)

// Options implements agent.Adapter: a `copilot --no-auto-update --help`
// probe, through the same launcher seam as Detect so the build probed is the
// build that runs (task 138 decision 5). The parsed `--reasoning-effort`
// values carry source "cli" and are merged over the curated floor. When the
// binary is missing, the probe fails, or the help has no clap-style
// `--reasoning-effort` line — what a commander-style build below 1.0.84
// prints — the curated catalog is returned alongside the error: degrade,
// never block (spec §9.6), but visibly.
//
// Models are never probed (see Curated).
func (a *Adapter) Options(ctx context.Context) (agent.Options, error) {
	return a.optionsWith(ctx, nil)
}

// optionsWith is Options through a launcher, as detectWith is Detect.
func (a *Adapter) optionsWith(ctx context.Context, l agent.Launcher) (agent.Options, error) {
	path, err := a.resolvePathWith(l)
	if err != nil {
		return a.Curated(), err
	}
	out, _, err := agent.ProbeWith(ctx, l, helpTimeout, path, noAutoUpdate, "--help")
	if err != nil {
		return a.Curated(), fmt.Errorf("copilot --help failed: %w", err)
	}
	efforts, err := parseHelp(string(out))
	if err != nil {
		return a.Curated(), err
	}
	return mergeOptions(efforts), nil
}

// errNoEffortLine is parseHelp's verdict on help text that does not list
// `--reasoning-effort` values in clap's shape.
var errNoEffortLine = errors.New("copilot --help has no clap-style `--reasoning-effort … [possible values: …]` line")

// parseHelp extracts the `--reasoning-effort` values from clap-style help.
// CRLF is folded first: a Windows build's help must parse the same.
func parseHelp(help string) ([]string, error) {
	m := effortValuesRe.FindStringSubmatch(strings.ReplaceAll(help, "\r\n", "\n"))
	if m == nil {
		return nil, errNoEffortLine
	}
	var efforts []string
	for v := range strings.SplitSeq(m[1], ",") {
		if v = strings.TrimSpace(v); v != "" {
			efforts = append(efforts, v)
		}
	}
	if len(efforts) == 0 {
		return nil, errNoEffortLine
	}
	return efforts, nil
}

// Curated implements agent.Adapter: the compiled-in floor, no probe.
//
// Models is empty and DefaultModel is "" (task 138 decision 9): copilot lists
// models only through the interactive `/model` command
// (cli-programmatic-reference), `help config`'s static list is not what the
// account may use, and an unavailable id is refused before any request is
// spent (unavailable_model_1.0.105.txt) — so ids are accepted free-form and
// the CLI's refusal is the check. With no `--model`, the CLI resolves `auto`
// by itself, and buildArgs passes `--model` only when §8.6 resolved one;
// naming `auto` here would claim vincent passes a value it never does.
func (a *Adapter) Curated() agent.Options { return mergeOptions(nil) }

// mergeOptions builds the catalog: probed efforts first (source cli), then
// curated ones not already present (source curated).
func mergeOptions(cliEfforts []string) agent.Options {
	return agent.Options{
		Models:       []agent.Option{},
		Efforts:      mergeValues(cliEfforts, curatedEfforts),
		DefaultModel: "",
		// `copilot --output-format json` has no input channel vincent
		// drives: the prompt is the whole of stdin (§9.9).
		InputSupport: agent.InputNever,
		// Restricted is the --available-tools set on every OS (task
		// 138.10; 138.3 proved it on macOS, Linux and Windows).
		RestrictedSupport: agent.RestrictedAlways,
	}
}

func mergeValues(cli, curated []string) []agent.Option {
	seen := make(map[string]bool, len(cli)+len(curated))
	out := make([]agent.Option, 0, len(cli)+len(curated))
	for _, v := range cli {
		if !seen[v] {
			seen[v] = true
			out = append(out, agent.Option{Value: v, Source: agent.SourceCLI})
		}
	}
	for _, v := range curated {
		if !seen[v] {
			seen[v] = true
			out = append(out, agent.Option{Value: v, Source: agent.SourceCurated})
		}
	}
	return out
}
