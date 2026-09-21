package chatrun

import (
	"slices"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
)

// The classification half of task 124.16 (#512): a claude turn's init line
// names the skills its process loaded, and the runner hands those names to the
// skill cache, which is the only thing that can tell claude's bundled skills
// from its built-in commands.

// report is one ReportBundledSkills call.
type report struct {
	agent string
	names []string
}

type reports struct {
	mu    sync.Mutex
	calls []report
}

func (r *reports) get() []report {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]report(nil), r.calls...)
}

// recordReports wires ReportBundledSkills to note each call. The adapter is
// kept by name: what the cache does with it is keyed off the binary and the
// place, which is the cache's own test.
func (h *harness) recordReports() *reports {
	rep := &reports{}
	h.runner.deps.ReportBundledSkills = func(a agent.Adapter, _ string, names []string) {
		rep.mu.Lock()
		rep.calls = append(rep.calls, report{agent: a.Name(), names: slices.Clone(names)})
		rep.mu.Unlock()
	}
	return rep
}

// TestClaudeTurnReportsItsInitSkills: one report, from the turn's own adapter,
// carrying the init line's names in the CLI's order.
func TestClaudeTurnReportsItsInitSkills(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_CLAUDE_INIT_SKILLS", "simplify,proj-skill")
	h := newHarness(t)
	c := h.chat(t)
	rep := h.recordReports()

	if turn := h.sendAndWait(t, c.ID, "hello"); turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s/%s (%s), want done", turn.State, turn.FailReason, turn.ErrorMessage)
	}
	h.waitIdle(t, c.ID)

	calls := rep.get()
	if len(calls) != 1 {
		t.Fatalf("ReportBundledSkills called %d time(s), want once: %+v", len(calls), calls)
	}
	if calls[0].agent != "claude" {
		t.Errorf("reported under %q, want the turn's own adapter", calls[0].agent)
	}
	if want := []string{"simplify", "proj-skill"}; !slices.Equal(calls[0].names, want) {
		t.Errorf("names = %q, want the init line's array in order: %q", calls[0].names, want)
	}
}

// TestATurnWithNoInitSkillsReportsNothing covers both ways a turn has nothing
// to classify: a claude build too old to send the array, and an adapter whose
// stream carries no such thing at all. Neither may report an empty set —
// "this CLI bundles nothing" is a claim no turn ever makes.
func TestATurnWithNoInitSkillsReportsNothing(t *testing.T) {
	for _, agentName := range []string{"claude", "codex", "cursor"} {
		t.Run(agentName, func(t *testing.T) {
			t.Setenv("FAKEAGENT_SCENARIO", "success")
			// claude's own no-array case; codex emits no init line and
			// cursor's carries no skills, so for those it changes nothing.
			t.Setenv("FAKEAGENT_CLAUDE_INIT_SKILLS", "none")
			h := newHarness(t)
			c := h.chatOn(t, agentName)
			rep := h.recordReports()

			if turn := h.sendAndWait(t, c.ID, "hello"); turn.State != chatstate.TurnDone {
				t.Fatalf("turn = %s/%s (%s), want done", turn.State, turn.FailReason, turn.ErrorMessage)
			}
			h.waitIdle(t, c.ID)
			if calls := rep.get(); len(calls) != 0 {
				t.Errorf("a turn with no init skills reported %+v", calls)
			}
		})
	}
}

// TestNilReportBundledSkillsIsTolerated is a runner built without a cache,
// which is every test before task 124.16 and any daemon that wires none.
func TestNilReportBundledSkillsIsTolerated(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_CLAUDE_INIT_SKILLS", "simplify")
	h := newHarness(t)
	c := h.chat(t)
	if turn := h.sendAndWait(t, c.ID, "hello"); turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s, want done", turn.State)
	}
	h.waitIdle(t, c.ID)
}

// TestReportingDoesNotRelaxInvalidation is the guard on the optimization
// #512 declined: a turn reporting exactly the names already cached still
// invalidates its directory.
//
// The init line describes the turn's *start*, so a turn that writes
// `.claude/skills/foo/SKILL.md` reports a set identical to the cache's, and
// keeping the entry on that basis would hide `foo` until the next turn or the
// five-minute TTL — which is the very case the unconditional invalidation of
// task 124.9 exists for. The init line is a classifier, never a change
// detector.
func TestReportingDoesNotRelaxInvalidation(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_CLAUDE_INIT_SKILLS", "simplify")
	h := newHarness(t)
	c := h.chat(t)
	inv := h.recordInvalidations(c.ID)
	rep := h.recordReports()

	if turn := h.sendAndWait(t, c.ID, "hello"); turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s/%s (%s), want done", turn.State, turn.FailReason, turn.ErrorMessage)
	}
	h.waitIdle(t, c.ID)

	if calls := rep.get(); len(calls) != 1 {
		t.Fatalf("ReportBundledSkills called %d time(s), want once: %+v", len(calls), calls)
	}
	assertInvalidatedOnce(t, inv, c.WorktreePath)
}
