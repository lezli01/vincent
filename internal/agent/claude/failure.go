package claude

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// resetHorizon bounds a reset timestamp the CLI reported. A quota window is
// hours, not months, so a value beyond this is a misparse rather than a long
// wait — and acting on it would park the task past any point a human would
// wait for. Past timestamps are rejected for the mirror-image reason: a hold
// that has already expired admits the task on the next tick, which is the
// tight respawn loop this whole feature exists to stop.
const resetHorizon = 7 * 24 * time.Hour

// usageLimitMarkers are the phrasings that mean "the quota for this window is
// spent". Matched case-insensitively against the terminal result text, the
// error message and the stderr tail.
//
// **Not fixture-verified.** Capturing a genuine quota exhaustion means burning
// a real five-hour window, the same impracticality cursor's logged-out probe
// records (cursor.go's loggedIn). So the parse follows the precedent that set:
// recognize the shapes that are documented, and fall through to nil for
// everything else rather than guess. An unrecognized quota stop keeps today's
// behaviour exactly — it fails as nonzero_exit or agent_error — which is the
// regression that matters most here.
var usageLimitMarkers = []string{
	"usage limit reached",
	"5-hour limit reached",
	"weekly limit reached",
}

// unauthenticatedMarkers are the phrasings that mean "this CLI is not logged
// in". Same caveat as above: layered and conservative, never a guess.
var unauthenticatedMarkers = []string{
	"invalid api key",
	"please run /login",
	"not logged in",
	"oauth token has expired",
	"authentication_error",
}

// sessionLostMarkers are the phrasings that mean "the session id you asked me
// to resume is not one I know". Same conservative rule as the markers above:
// recognize documented shapes, fall through to the generic verdict otherwise.
// They are only ever consulted for a run that actually passed --resume, so a
// task step can never be misdiagnosed as a lost session.
var sessionLostMarkers = []string{
	"no conversation found with session id",
	"no conversation found",
	"session not found",
	"could not resume",
}

// resetEpochRe matches the reset time claude appends to its usage-limit
// message as unix seconds: `Claude AI usage limit reached|1755100000`.
var resetEpochRe = regexp.MustCompile(`limit reached\|(\d{9,11})`)

// classify inspects a finished run for a condition vincent can act on: a spent
// usage quota, which the engine turns into an admission hold (§11), or a CLI
// that is not authenticated, which blocks under the normal retry budget (§18).
//
// It reads the terminal result and the stderr tail together because which one
// carries the wording is not fixture-verified either — the same reason cursor's
// login probe reads both of its streams.
//
// The order matters only for a message that somehow says both: a quota stop is
// the recoverable one, so it wins and the task waits rather than blocking on a
// human who has nothing to do.
func classify(res agent.RunResult, stderr string) *agent.Failure {
	text := strings.ToLower(strings.Join([]string{res.ErrorMessage, res.ResultText, stderr}, "\n"))
	switch {
	case containsAny(text, usageLimitMarkers):
		return &agent.Failure{Kind: agent.FailureUsageLimit, RetryAfter: parseReset(text, time.Now())}
	case containsAny(text, unauthenticatedMarkers):
		return &agent.Failure{Kind: agent.FailureUnauthenticated}
	default:
		return nil
	}
}

// classifyResume names a resume the CLI refused (task 063 decision 4). It
// returns nil for a run that did not resume at all, and for a resumed run that
// succeeded — a chat turn's ordinary case.
//
// It is a second pass rather than a branch inside classify because it needs
// something classify has no business knowing: whether this run was a resume.
// A quota stop or a logged-out CLI still wins nothing here; the caller applies
// this one last, since a lost session is the actionable verdict and the only
// one that says "this conversation cannot continue".
func classifyResume(res agent.RunResult, stderr string, resuming bool) *agent.Failure {
	if !resuming || !res.IsError {
		return nil
	}
	text := strings.ToLower(strings.Join([]string{res.ErrorMessage, res.ResultText, stderr}, "\n"))
	if containsAny(text, sessionLostMarkers) {
		return &agent.Failure{Kind: agent.FailureSessionLost}
	}
	return nil
}

func containsAny(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// parseReset extracts the reset timestamp claude appends to its usage-limit
// message. Anything that does not parse, or that lands outside resetHorizon in
// either direction, yields nil — the interval fallback then decides, which is
// the honest answer when the CLI told us nothing usable.
func parseReset(text string, now time.Time) *time.Time {
	m := resetEpochRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	secs, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return nil
	}
	at := time.Unix(secs, 0).UTC()
	if !at.After(now) || at.After(now.Add(resetHorizon)) {
		return nil
	}
	return &at
}
