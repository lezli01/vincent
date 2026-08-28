package codex

import "github.com/lezli01/vincent/internal/agent"

// Version compatibility for the codex CLI (spec §9.3, task 040).

// testedVersions are the builds vincent's parsers were captured against:
// 0.142.5 is the invocation pinned in §9.3, and reasoning_0.147.0.jsonl pins
// the reasoning dialect. A build outside this list is `untested`, which is
// the normal state for a user on a current CLI and changes nothing about how
// a step runs.
var testedVersions = []string{"0.142.5", "0.147.0"}

// incompatibleVersions are builds vincent knows break. It ships empty: no
// codex release has been observed to break these parsers. Tests inject one
// through SetIncompatibleVersions, so the verdict is exercised rather than
// left to be wrong on the day it is first needed.
var incompatibleVersions []string

// SetIncompatibleVersions overrides the known-bad list and returns a restore
// func, the seam cursor.SetSandboxAvailable establishes for a platform fact.
// Production code never calls it.
func SetIncompatibleVersions(v []string) (restore func()) {
	prev := incompatibleVersions
	incompatibleVersions = v
	return func() { incompatibleVersions = prev }
}

// versionVerdict judges a probed version against the tables above.
func versionVerdict(version string) agent.VersionVerdict {
	return agent.JudgeVersion(version, testedVersions, incompatibleVersions)
}
