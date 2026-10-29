package copilot

import (
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestQuotaAndAuthStopsStayUnclassified pins what 138.5 leaves to 138.6
// (#858): a logged-out CLI and a spent premium-request quota fail the
// ordinary way, with no Failure kind, until that item classifies them.
func TestQuotaAndAuthStopsStayUnclassified(t *testing.T) {
	for _, scenario := range []string{"usage-limit", "unauthenticated"} {
		t.Run(scenario, func(t *testing.T) {
			h := startRun(t, scenario)
			drain(t, h)
			res, err := h.Wait()
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if res.Failure != nil {
				t.Fatalf("Failure = %+v, want nil until task 138.6", res.Failure)
			}
			if !res.IsError && res.ExitCode == 0 {
				t.Errorf("run reported success; want the ordinary failure path (exit %d, is_error %v)",
					res.ExitCode, res.IsError)
			}
		})
	}
}

// TestStreamFailureVerdict is the one verdict this adapter makes (#139).
func TestStreamFailureVerdict(t *testing.T) {
	res := agent.RunResult{ResultText: "done"}
	streamFailure(&res, errors.New("bufio.Scanner: token too long"))
	if !res.IsError || res.Failure == nil || res.Failure.Kind != agent.FailureStreamError {
		t.Errorf("result = %+v, want a stream_error failure", res)
	}
}
