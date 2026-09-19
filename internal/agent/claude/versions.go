package claude

import "github.com/lezli01/vincent/internal/agent"

// Version compatibility for the Claude Code CLI (spec §9.2, task 041).

// testedVersions are the builds vincent's parsers were captured against:
// help_2.1.224.txt is what the option probe is parsed from, the
// stream_*_2.1.226.jsonl fixtures and stream_skill_permission_2.1.277.jsonl
// pin the §7.4 control protocol (the latter for a `Skill` request, task 124),
// and the auth_status_*_2.1.268.json fixtures pin the §9.5 auth probe. A build
// outside this list is `untested`, which is the normal state for a user on a
// current CLI and changes nothing about how a step runs.
var testedVersions = []string{"2.1.224", "2.1.226", "2.1.268", "2.1.277"}

// incompatibleVersions are builds vincent knows break. It ships empty: no
// claude release has been observed to break these parsers. Tests inject one
// through SetIncompatibleVersions, so the verdict is exercised rather than
// left to be wrong on the day it is first needed.
var incompatibleVersions []string

// SetIncompatibleVersions overrides the known-bad list and returns a restore
// func, the seam SetSandboxAvailable establishes for cursor's platform fact.
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
