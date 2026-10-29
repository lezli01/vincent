package copilot

import (
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// unauthenticatedMarkers are the phrasings that mean "this CLI is not logged
// in", lowercased. Fixture-verified: logged_out_1.0.105.txt is what 1.0.105
// prints on stderr, before any stdout, when it has no credential at all.
//
// There are deliberately no usage-limit or quota markers. The texts that
// exist — `402 … "quota_exceeded"`, "You have exceeded your premium request
// quota", "rate limit" wordings — come only from issue reports
// (github/copilot-cli #2696, #2336, #3431); no throttled account produced one
// at capture (task 138). Task 003 decision 2 (§7.1) recognizes nothing that is
// neither documented nor captured, so a quota stop fails the ordinary way, on
// its exit code or a missing result line, and usage_limit_auto_continue does
// not apply to copilot.
var unauthenticatedMarkers = []string{
	"no authentication information found",
}

// classify names a CLI stop this adapter recognizes, matched case-folded
// over the error message, the result text and the stderr tail — claude's and
// codex's shape. A successful run is never classified, whatever its text says:
// an agent that quotes the logged-out message back in a clean result did not
// stop on it.
func classify(res agent.RunResult, stderr string) *agent.Failure {
	if !res.IsError && res.ExitCode == 0 {
		return nil
	}
	text := strings.ToLower(strings.Join([]string{res.ErrorMessage, res.ResultText, stderr}, "\n"))
	for _, m := range unauthenticatedMarkers {
		if strings.Contains(text, m) {
			return &agent.Failure{Kind: agent.FailureUnauthenticated}
		}
	}
	return nil
}

// streamFailure is a stream vincent could not read to the end (#139). It
// outranks whatever the exit code and classify say — the run may well have
// finished cleanly, but the record of it is missing lines, and §7.1 success
// is a claim about both.
func streamFailure(res *agent.RunResult, err error) {
	res.IsError = true
	res.ErrorMessage = "agent stream capture failed: " + err.Error()
	res.Failure = &agent.Failure{Kind: agent.FailureStreamError}
}
