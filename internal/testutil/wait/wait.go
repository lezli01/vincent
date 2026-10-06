package wait

import (
	"math"
	"os"
	"strconv"
	"testing"
	"time"
)

// EnvScale names the factor every budget here is multiplied by. It has the
// same name and the same parsing as apiclient.EnvTimeoutScale, which the
// test holds together.
const EnvScale = "VINCENT_TEST_TIMEOUT_SCALE"

// maxScale is apiclient.MaxTimeoutScale, which the test holds together; this
// package does not import apiclient.
const maxScale = 100

// DefaultBudget is Until's unscaled budget: generous, because a passing test
// waits only as long as its condition takes.
const DefaultBudget = 20 * time.Second

// pollInterval paces every poll loop here.
const pollInterval = 10 * time.Millisecond

// Scale is the factor in EnvScale, capped at apiclient.MaxTimeoutScale, or 1
// when it is unset or not a positive number (NaN included).
func Scale() float64 {
	f, err := strconv.ParseFloat(os.Getenv(EnvScale), 64)
	if err != nil || math.IsNaN(f) || f <= 0 {
		return 1
	}
	return min(f, maxScale)
}

// Timeout is d multiplied by Scale.
func Timeout(d time.Duration) time.Duration {
	return time.Duration(float64(d) * Scale())
}

// Until polls cond until it holds, and fails the test with "timed out
// waiting for what" once DefaultBudget, scaled, has passed.
func Until(t testing.TB, what string, cond func() bool) {
	t.Helper()
	UntilWithin(t, DefaultBudget, what, cond)
}

// UntilWithin is Until for a caller that keeps its own unscaled budget d.
// cond runs on the test's goroutine, so it may call t.Fatal itself.
func UntilWithin(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	if !Poll(d, pollInterval, cond) {
		t.Fatalf("timed out waiting for %s", what)
	}
}

// Poll calls cond every interval until it holds or Timeout(d) has passed,
// and reports whether it held. It is for the caller whose failure message
// carries what it saw on the last poll, or whose cond is too costly to run
// every pollInterval — one that spawns the vincent binary, say.
func Poll(d, every time.Duration, cond func() bool) bool {
	end := time.Now().Add(Timeout(d))
	for !cond() {
		if time.Now().After(end) {
			return false
		}
		time.Sleep(every)
	}
	return true
}
