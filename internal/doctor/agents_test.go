package doctor

import "testing"

// TestAgentHealthIsNeverAProblem pins task 005 decision 7 against the four
// verdicts task 040 added: an adapter can be absent, logged out, running an
// untested build and running a known-bad one at once, and `vincent doctor`
// still exits 0.
//
// The decision is that the unhealthy set is *closed*: it holds defects a user
// can act on with a repair, and "you are on a newer CLI than vincent's
// fixtures" is the normal state of a healthy machine. Widening it here would
// make the exit code fire on almost every installation.
func TestAgentHealthIsNeverAProblem(t *testing.T) {
	no := false
	r := &Report{Agents: []Agent{
		{Name: "missing", Available: false, Error: "not found on PATH"},
		{Name: "loggedout", Available: true, LoggedIn: &no, VersionVerdict: "untested"},
		{Name: "incompatible", Available: true, VersionVerdict: "incompatible"},
		{Name: "norestrict", Available: true, RestrictedVerdict: "unsupported"},
	}}
	r.Evaluate()
	if len(r.Problems) != 0 {
		t.Errorf("Problems = %+v, want none: adapter health is a row, never an exit code", r.Problems)
	}
	if !r.Healthy() {
		t.Error("Healthy() = false for a report whose only findings are adapter rows")
	}
}
