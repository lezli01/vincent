package codex

import (
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// threadLostMarkers are the phrasings that mean "the thread id you asked me
// to resume is not one I know". Captured from codex-cli 0.150.1 refusing an
// id it had never minted (testdata/resume_lost_0.150.1.txt):
//
//	Error: thread/resume: thread/resume failed: no rollout found for thread id
//	00000000-0000-4000-8000-000000000000 (code -32600)
//
// The refusal arrives on stderr and stdout carries no JSONL at all, so Wait's
// no-result branch is what folds it into the error message; classifyResume
// reads the stderr tail directly as well, for the same reason claude's does.
//
// Unlike claude's usage-limit and unauthenticated markers, these *are*
// fixture-verified — a bad id costs nothing to reproduce, which is precisely
// why this is the one condition codex classifies and the quota wordings still
// are not (task 072 decision 2).
var threadLostMarkers = []string{
	"no rollout found for thread id",
	"thread/resume failed",
}

// classifyResume names a resume codex refused (task 072 decision 2). It
// returns nil for a run that did not resume at all, and for a resumed run
// that succeeded — a chat turn's ordinary case.
//
// The `resuming` guard is not defensive: it is §9.2's rule verbatim. A
// workflow step never passes a thread id, so no step can be misdiagnosed as a
// lost session however its output is worded.
func classifyResume(res agent.RunResult, stderr string, resuming bool) *agent.Failure {
	if !resuming || !res.IsError {
		return nil
	}
	text := strings.ToLower(strings.Join([]string{res.ErrorMessage, res.ResultText, stderr}, "\n"))
	for _, m := range threadLostMarkers {
		if strings.Contains(text, m) {
			return &agent.Failure{Kind: agent.FailureSessionLost}
		}
	}
	return nil
}
