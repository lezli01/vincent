package copilot

import (
	"errors"
	"os"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestUnauthenticatedIsClassified is the logged-out stop, both as the fake
// replays it through a real run and as the captured stderr fed to classify.
func TestUnauthenticatedIsClassified(t *testing.T) {
	t.Run("run", func(t *testing.T) {
		h := startRun(t, "unauthenticated")
		drain(t, h)
		res, err := h.Wait()
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if res.Failure == nil || res.Failure.Kind != agent.FailureUnauthenticated {
			t.Errorf("Failure = %+v, want unauthenticated", res.Failure)
		}
	})
	t.Run("fixture", func(t *testing.T) {
		b, err := os.ReadFile("testdata/logged_out_1.0.105.txt")
		if err != nil {
			t.Fatal(err)
		}
		f := classify(agent.RunResult{ExitCode: 1, IsError: true}, string(b))
		if f == nil || f.Kind != agent.FailureUnauthenticated {
			t.Errorf("classify(logged_out_1.0.105.txt) = %+v, want unauthenticated", f)
		}
	})
}

// TestUnrecognizedStopsStayUnclassified: a quota stop, whose wording is
// known only from issue reports, and the refusals vincent never predicts —
// an unavailable model, an unsupported effort — fail the ordinary way, with
// no Failure kind (task 003 decision 2).
func TestUnrecognizedStopsStayUnclassified(t *testing.T) {
	t.Run("usage-limit", func(t *testing.T) {
		h := startRun(t, "usage-limit")
		drain(t, h)
		res, err := h.Wait()
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if res.Failure != nil {
			t.Fatalf("Failure = %+v, want nil: the quota wording is unverified", res.Failure)
		}
		if !res.IsError && res.ExitCode == 0 {
			t.Errorf("run reported success; want the ordinary failure path (exit %d, is_error %v)",
				res.ExitCode, res.IsError)
		}
	})
	for _, fixture := range []string{"unavailable_model_1.0.105.txt", "unsupported_effort_1.0.105.txt"} {
		t.Run(fixture, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + fixture)
			if err != nil {
				t.Fatal(err)
			}
			res := assemble(1, "id", nil, nil, string(b))
			if res.Failure != nil {
				t.Errorf("Failure = %+v, want nil", res.Failure)
			}
			if !res.IsError {
				t.Error("IsError = false, want the no-result failure carrying stderr")
			}
		})
	}
}

// TestClassifyIgnoresSuccess: an agent that quotes the logged-out message in
// a clean result did not stop on it.
func TestClassifyIgnoresSuccess(t *testing.T) {
	if f := classify(agent.RunResult{ResultText: "Error: No authentication information found."}, ""); f != nil {
		t.Errorf("classify(success) = %+v, want nil", f)
	}
}

// TestStreamFailureOutranksClassify: a stream vincent could not read to the
// end wins over every other verdict (#139), a recognized stop included.
func TestStreamFailureOutranksClassify(t *testing.T) {
	b, err := os.ReadFile("testdata/logged_out_1.0.105.txt")
	if err != nil {
		t.Fatal(err)
	}
	res := assemble(1, "id", nil, errors.New("bufio.Scanner: token too long"), string(b))
	if res.Failure == nil || res.Failure.Kind != agent.FailureStreamError {
		t.Errorf("Failure = %+v, want stream_error", res.Failure)
	}
}

// TestStreamFailureVerdict is the stream verdict alone (#139).
func TestStreamFailureVerdict(t *testing.T) {
	res := agent.RunResult{ResultText: "done"}
	streamFailure(&res, errors.New("bufio.Scanner: token too long"))
	if !res.IsError || res.Failure == nil || res.Failure.Kind != agent.FailureStreamError {
		t.Errorf("result = %+v, want a stream_error failure", res)
	}
}
