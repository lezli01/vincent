//go:build !race

package cli

// raceEnabled reports whether this test binary is race-instrumented. See the
// `race` half of this pair.
const raceEnabled = false
