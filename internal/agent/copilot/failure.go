package copilot

import "github.com/lezli01/vincent/internal/agent"

// streamFailure is the one failure verdict this adapter makes: a stream
// vincent could not read to the end (#139). It outranks whatever the exit code
// says — the run may well have finished cleanly, but the record of it is
// missing lines, and §7.1 success is a claim about both.
//
// Classifying the CLI's own stops — unauthenticated, a spent premium-request
// quota, an unavailable model — is task 138.6's (#858). Until then those runs
// fail the ordinary way, on their exit code or a missing result line.
func streamFailure(res *agent.RunResult, err error) {
	res.IsError = true
	res.ErrorMessage = "agent stream capture failed: " + err.Error()
	res.Failure = &agent.Failure{Kind: agent.FailureStreamError}
}
