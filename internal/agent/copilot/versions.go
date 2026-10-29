package copilot

import "github.com/lezli01/vincent/internal/agent"

// Version compatibility for the copilot CLI (spec §9.9, task 041).

// testedVersions are the builds vincent's parsers were captured against, in
// the form Detect records them: `GitHub Copilot CLI 1.0.105.` reduced to its
// semver (task 138 decision 15).
//
// The comparison is exact string equality, the same as every adapter's
// (task 041), and never a range. 1.0.84 is the *documented* minimum — below
// it `--help` is not clap-shaped and the effort flag is spelled differently
// (task 138's version findings) — but nothing enforces it: a build between
// 1.0.84 and 1.0.105 judges `untested`, which is advisory, exactly as one
// below it does (§9.9, task 138 open question 2).
var testedVersions = []string{"1.0.105"}

// incompatibleVersions are builds vincent knows break. It ships empty: no
// copilot release has been observed to break these parsers. Tests inject one
// through SetIncompatibleVersions.
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
