package codex

import "testing"

// TestQuotaAndAuthStopsStayUnclassified pins the codex half of task 003's
// decision: this adapter recognizes nothing, because its usage-limit and
// unauthenticated wordings are not fixture-verified, so a run stopped by
// either reads exactly as it did before — an ordinary failure under §7.2.
//
// The test exists rather than the classification because the risk here is the
// opposite of a missing feature: an adapter that starts guessing would send a
// genuinely failed task into a wait it never recovers from.
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
				t.Fatalf("Failure = %+v, want nil until a fixture exists", res.Failure)
			}
			if !res.IsError && res.ExitCode == 0 {
				t.Errorf("run reported success; want the ordinary failure path (exit %d, is_error %v)",
					res.ExitCode, res.IsError)
			}
		})
	}
}
