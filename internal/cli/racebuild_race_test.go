//go:build race

package cli

// raceEnabled reports whether this test binary is race-instrumented. It comes
// from the build rather than from an environment guess, because there is no
// environment variable that says so: `go test -race` sets the `race` build tag
// and nothing else.
const raceEnabled = true
