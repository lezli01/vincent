package chatrun

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatexport"
	"github.com/lezli01/vincent/internal/store"
)

// TestCrossAgentForkIsAlwaysSeeded (task 171 decision 2): a claude source
// whose last kept turn has a resume anchor would resume natively on a
// same-agent fork, but a fork onto codex has no session to resume — it is
// seeded, takes the request's model and effort rather than the source's, and
// its seeded context says whose conversation it was.
func TestCrossAgentForkIsAlwaysSeeded(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	one := h.turnDone(t, c.ID, "alpha")
	h.turnDone(t, c.ID, "beta")
	if _, err := h.store.UpdateChatSettings(t.Context(), c.ID, store.ChatSettingsPatch{
		Model: ptr("opus"), Effort: ptr("high"),
	}); err != nil {
		t.Fatal(err)
	}

	same, err := h.runner.PlanFork(t.Context(), c.ID, 2)
	if err != nil {
		t.Fatalf("PlanFork: %v", err)
	}
	if same.Seeded || same.SessionID != one.SessionID || same.ResumeAt != one.ResumeAnchor || same.Agent != "claude" {
		t.Fatalf("same-agent plan changed: %+v", same)
	}

	plan, err := h.runner.PlanForkTo(t.Context(), c.ID, 2, ForkTarget{Agent: "codex"})
	if err != nil {
		t.Fatalf("PlanForkTo: %v", err)
	}
	if !plan.Seeded || plan.SessionID != "" || plan.ResumeAt != "" {
		t.Errorf("cross-agent plan not seeded: %+v", plan)
	}
	if plan.Agent != "codex" || plan.Model != "" || plan.Effort != "" {
		t.Errorf("agent %q model %q effort %q; want codex with its own defaults", plan.Agent, plan.Model, plan.Effort)
	}
	if !strings.Contains(plan.Preamble, "alpha") || strings.Contains(plan.Preamble, "beta") {
		t.Errorf("preamble keeps the wrong turns:\n%s", plan.Preamble)
	}
	if !strings.Contains(plan.Preamble, "held with claude, not with you (codex)") {
		t.Errorf("preamble does not name the source agent:\n%s", plan.Preamble)
	}

	withModel, err := h.runner.PlanForkTo(t.Context(), c.ID, 2, ForkTarget{Agent: "codex", Model: "gpt-5", Effort: "low"})
	if err != nil || withModel.Model != "gpt-5" || withModel.Effort != "low" {
		t.Errorf("requested model/effort not carried: %+v, %v", withModel, err)
	}

	// Turn 1 keeps nothing: a fresh session, seeded with nothing.
	first, err := h.runner.PlanForkTo(t.Context(), c.ID, 1, ForkTarget{Agent: "codex"})
	if err != nil || first.Seeded || first.Preamble != "" || first.SessionID != "" {
		t.Errorf("seq 1 cross-agent plan = %+v, %v", first, err)
	}
}

func ptr(s string) *string { return &s }

// TestSeededPreambleSummarizesToolCalls (task 171 decision 3): each kept
// turn carries a <tools> block of one ToolSummary line per call, between its
// prompt and its reply, and the intro says output is left out.
func TestSeededPreambleSummarizesToolCalls(t *testing.T) {
	r := &Runner{deps: Deps{TurnTools: func(_ *store.Chat, turn *store.ChatTurn) []chatexport.ToolCall {
		if turn.Seq != 1 {
			return nil
		}
		return []chatexport.ToolCall{
			{Name: "Read", Arg: "main.go"},
			{Name: "Bash", Arg: "go test\n./...", Failed: true},
		}
	}}}
	chat := &store.Chat{Agent: "claude"}
	got := r.seededPreamble(chat, []store.ChatTurn{
		{Seq: 1, Prompt: "fix it", ResultText: "fixed"},
		{Seq: 2, Prompt: "thanks", ResultText: "welcome"},
	}, "")
	want := "<human>\nfix it\n</human>\n<tools>\n" +
		chatexport.ToolSummary("Read", "main.go") + "\n" +
		"Bash go test ./... (failed)\n</tools>\n<reply>\nfixed\n</reply>"
	if !strings.Contains(got, want) {
		t.Errorf("turn 1 not rendered with its tools:\n%s", got)
	}
	if strings.Count(got, "<tools>") != 1 {
		t.Errorf("a turn without tool calls got a tools block:\n%s", got)
	}
	if !strings.Contains(got, "Tool output and intermediate messages") || strings.Contains(got, "held with") {
		t.Errorf("intro wrong for a same-agent seed:\n%s", got)
	}
}

// TestSeededPreambleDropsToolBlocksFirst: over the bound, whole tool blocks
// go before any prompt or reply, oldest first; only when none is left is the
// oldest history cut.
func TestSeededPreambleDropsToolBlocksFirst(t *testing.T) {
	calls := make([]chatexport.ToolCall, 30)
	for i := range calls {
		calls[i] = chatexport.ToolCall{Name: "Read", Arg: fmt.Sprintf("file%d.go", i)}
	}
	r := &Runner{deps: Deps{TurnTools: func(*store.Chat, *store.ChatTurn) []chatexport.ToolCall { return calls }}}
	chat := &store.Chat{Agent: "claude"}
	// 10 turns × (30 tool lines + 10 framing lines) is 400 lines, over the
	// 200-line bound; without tool blocks it is 100.
	var kept []store.ChatTurn
	for i := 1; i <= 10; i++ {
		kept = append(kept, store.ChatTurn{Seq: i, Prompt: fmt.Sprintf("prompt %d", i), ResultText: fmt.Sprintf("reply %d", i)})
	}
	got := r.seededPreamble(chat, kept, "")
	for i := 1; i <= 10; i++ {
		if !strings.Contains(got, fmt.Sprintf("reply %d\n", i)) || !strings.Contains(got, fmt.Sprintf("prompt %d\n", i)) {
			t.Fatalf("turn %d's prompt or reply was cut while tool blocks remained:\n%s", i, got)
		}
	}
	if !strings.Contains(got, "<tools>") {
		t.Error("every tool block dropped; the newest ones fit")
	}
	if strings.Index(got, "<tools>") < strings.Index(got, "reply 5\n") {
		t.Error("an early turn kept its tool block while a later one lost it")
	}
	if !strings.Contains(got, "tool calls of the earliest") {
		t.Error("no note says tool calls were omitted")
	}

	// Far over the bound even bare: the old tail cut, every tool block gone.
	kept = nil
	for i := 1; i <= 60; i++ {
		kept = append(kept, store.ChatTurn{Seq: i, Prompt: "p", ResultText: "r"})
	}
	got = r.seededPreamble(chat, kept, "")
	if strings.Contains(got, "<tools>") || !strings.Contains(got, "earlier turns and all tool calls omitted") {
		t.Errorf("tail cut kept tool blocks or lost its note:\n%.400s", got)
	}
	if n := strings.Count(got, "\n"); n > seededLines+20 {
		t.Errorf("tail cut left %d lines", n)
	}
}
