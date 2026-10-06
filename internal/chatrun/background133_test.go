package chatrun

import (
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
)

// The report behind task 133: the agent starts the suite in the background,
// says it will report, and the turn used to end there as `done` with the
// suite killed — the human had to type "continue". With the default wait
// the turn ends on what the agent said once the suite finished.
func TestTurnWaitsForBackgroundWork(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKEAGENT_SCENARIO", "background")
	c := h.chat(t)

	turn := h.sendAndWait(t, c.ID, "run the suite")
	if turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s/%s (%s), want done", turn.State, turn.FailReason, turn.ErrorMessage)
	}
	if turn.ResultText != "the suite passed" {
		t.Errorf("result = %q, want the answer given after the suite finished", turn.ResultText)
	}
	if turn.InputTokens != 110 || turn.OutputTokens != 47 {
		t.Errorf("tokens = %d/%d, want both turns' usage summed to 110/47", turn.InputTokens, turn.OutputTokens)
	}
}

// Zero is the opt-out, and it is exactly the old behaviour.
func TestZeroBackgroundWaitEndsTheTurnAtOnce(t *testing.T) {
	h := newHarness(t)
	h.cfg.Defaults.BackgroundWait = config.Duration(0)
	t.Setenv("FAKEAGENT_SCENARIO", "background")
	c := h.chat(t)

	turn := h.sendAndWait(t, c.ID, "run the suite")
	if turn.State != chatstate.TurnDone || turn.ResultText != "waiting on the suite" {
		t.Fatalf("turn = %s %q, want done on the first answer", turn.State, turn.ResultText)
	}
}
