package agent

import "strings"

// Adapter version compatibility (spec §9.2, §9.3, §9.7 — task 041).
//
// A verdict is advisory everywhere: it is reported on /v1/agents, /v1/info,
// /v1/doctor and in both UIs, and it refuses nothing. The issue asked for a
// tested minimum/maximum per vincent release; that is not implementable for
// all three adapters — cursor's version is calver plus a commit sha
// (2026.08.04-aaa8809) and admits no range (§9.7) — so the comparison is
// exact string equality against the builds whose fixtures are in the tree,
// and the answer for a newer CLI is `untested`, which changes no behaviour.
//
// The one existing version gate — claude's input family [2.1.0, 3.0.0), PR F
// decision — is untouched: it gates one *capability* and degrades the
// invocation visibly, which is a different thing from judging the adapter.

// VersionVerdict is what vincent knows about the installed CLI build itself
// (task 041). It is the protocol-compatibility facet of adapter health that
// `input_verdict` does not cover: `input_verdict` answers "can this build
// hold a conversation", this answers "has this build ever been run against
// vincent's parsers".
type VersionVerdict string

// Version verdicts (task 041).
const (
	// VersionUnknown is "nobody can say": the binary is not installed, or
	// the version probe did not answer. It is the zero value, so an
	// Availability built by a test or a future adapter reports no judgement
	// rather than an accusation.
	VersionUnknown VersionVerdict = ""
	// VersionTested is a build vincent has fixtures for — its help output
	// and its stream dialect are pinned in testdata/.
	VersionTested VersionVerdict = "tested"
	// VersionUntested is any other build. This is the *normal* answer for a
	// user on a current CLI: vincent's fixtures lag the release train by
	// design, and an untested build is expected to work.
	VersionUntested VersionVerdict = "untested"
	// VersionIncompatible is a build vincent knows breaks. Every adapter's
	// list ships empty — vincent has observed no such build — but the
	// verdict is wired, rendered and tested, so the day one is found the
	// change is one string in one table rather than a new mechanism.
	VersionIncompatible VersionVerdict = "incompatible"
)

// JudgeVersion compares a probed version string against an adapter's tables.
//
// Comparison is exact string equality rather than a range, because one of the
// three adapters has no parseable version at all (§9.7) and a gate that works
// for two of three is a gate whose answer depends on which adapter you asked.
// An empty version — a probe that answered with nothing usable — is unknown,
// never untested: silence is not evidence, the same rule InputUnknown follows.
func JudgeVersion(version string, tested, incompatible []string) VersionVerdict {
	if strings.TrimSpace(version) == "" {
		return VersionUnknown
	}
	for _, v := range incompatible {
		if v == version {
			return VersionIncompatible
		}
	}
	for _, v := range tested {
		if v == version {
			return VersionTested
		}
	}
	return VersionUntested
}

// TestedVersionList renders an adapter's verified builds for a human: the
// list a user compares their own `--version` against when a row says
// `untested`. Empty for an adapter that pins nothing.
func TestedVersionList(tested []string) string { return strings.Join(tested, ", ") }

// RestrictedSupport is what an adapter can *ever* do about `permission_mode:
// restricted` on this host, known without probing (task 041). It is the
// permission-compatibility facet of adapter health, and it mirrors
// InputSupport exactly: it rides the catalog rather than an Adapter method so
// §8.2 validation and the creation-time gate can read it without spawning
// anything.
//
// It is a property of adapter identity and GOOS, not of the installed binary:
// cursor cannot restrict on Windows whether or not cursor-agent is installed
// (§9.7), which is what makes a refusal safe on a machine with no CLI at all.
type RestrictedSupport string

// Restricted support levels (task 041). The zero value is deliberately
// neither: an adapter that says nothing is unjudged, so a catalog built by a
// test or a future adapter never refuses a task by accident.
const (
	// RestrictedNever is a positive no — this adapter cannot restrict on
	// this host, on any version of the CLI. Only this level refuses
	// anything.
	RestrictedNever RestrictedSupport = "never"
	// RestrictedAlways is an adapter whose restricted mode is a flag it
	// always has: claude's --allowedTools, codex's sandbox (§9.2, §9.3).
	RestrictedAlways RestrictedSupport = "always"
)

// RestrictedEverPossible reports whether an adapter with these options could
// ever run a `restricted` step here. Only RestrictedNever answers false; an
// unset level is unjudged, which is what keeps validation from gating on
// silence — the rule InputEverPossible follows.
func (o Options) RestrictedEverPossible() bool { return o.RestrictedSupport != RestrictedNever }
