package cursor

import "testing"

// TestQuotaAndAuthStopsStayUnclassified pins the cursor half of task 003's
// decision, for the reason this adapter already records about `status`: the
// wordings are not fixture-verified — probing them means signing the developer
// out or burning a real quota window — so nothing is recognized and a run
// stopped by either reads exactly as it did before.
//
// The §9.5 `logged_in` probe is unaffected: an unauthenticated cursor is still
// flagged before a task is created. This is only about a run that has failed.
//
// Task 070 left this whole-hearted "nothing" intact, unlike codex's, and for a
// stronger reason than a missing fixture: cursor has no session-lost refusal
// either. Handed a `--resume` id it has never seen it starts a fresh chat and
// exits 0 (testdata/resume_unknown_2026.08.11.jsonl), so there is no wording
// to match, and TestUnknownSessionIsAdoptedNotRefused says so directly.
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
