package cursor

import "github.com/lezli01/vincent/internal/agent"

// Version compatibility for the cursor-agent CLI (spec §9.7, task 041).

// testedVersions are the builds vincent's parsers were captured against, in
// the form cursor-agent prints them: calver plus a commit sha. That form is
// exactly why the comparison across every adapter is string equality rather
// than a range (§9.7) — there is no ordering to put 2026.08.04-aaa8809 into,
// and a gate that works for two adapters of three answers a different
// question depending on which one you ask.
var testedVersions = []string{"2026.08.04-aaa8809", "2026.08.11-e8db854"}

// incompatibleVersions are builds vincent knows break. It ships empty: no
// cursor-agent release has been observed to break these parsers. Tests inject
// one through SetIncompatibleVersions, the same seam SetSandboxAvailable is.
var incompatibleVersions []string

// SetIncompatibleVersions overrides the known-bad list and returns a restore
// func. Production code never calls it.
func SetIncompatibleVersions(v []string) (restore func()) {
	prev := incompatibleVersions
	incompatibleVersions = v
	return func() { incompatibleVersions = prev }
}

// versionVerdict judges a probed version against the tables above.
func versionVerdict(version string) agent.VersionVerdict {
	return agent.JudgeVersion(version, testedVersions, incompatibleVersions)
}
