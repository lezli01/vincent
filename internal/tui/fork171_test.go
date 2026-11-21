package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestTurnZeroBlock (task 171 decision 4): a seeded fork shows where it came
// from and, under the turn-0 heading, the context its first turn receives;
// a natively resumed fork says so instead; a plain chat shows nothing.
func TestTurnZeroBlock(t *testing.T) {
	seeded := &apiclient.Chat{
		ID: 9, ResumePending: "seeded", PendingPreamble: "<earlier-turns>\nthe summary\n</earlier-turns>\n",
		ForkedFrom: &apiclient.ChatForkedFrom{ChatID: 4, Seq: 3, Agent: "claude"},
	}
	got := ansi.Strip(strings.Join(turnZeroLines(seeded, 80), "\n"))
	for _, want := range []string{
		"forked from #4 (claude), before turn 3", "turn 0 · seeded context (summary)",
		"the summary", "vincent chat preamble 9",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("seeded fork: no %q in\n%s", want, got)
		}
	}
	resumed := &apiclient.Chat{ID: 10, ResumePending: "resumed", ForkedFrom: &apiclient.ChatForkedFrom{ChatID: 4, Seq: 3}}
	got = ansi.Strip(strings.Join(turnZeroLines(resumed, 80), "\n"))
	if !strings.Contains(got, "forked from #4, before turn 3") || !strings.Contains(got, "resumed from turn 2") ||
		strings.Contains(got, "turn 0") {
		t.Errorf("resumed fork:\n%s", got)
	}
	if lines := turnZeroLines(&apiclient.Chat{ID: 1}, 80); len(lines) != 0 {
		t.Errorf("plain chat: %q", lines)
	}
	// A seeded rewind keeps its turns: its context is still shown, under
	// the pending line after the last turn, with the edit hint (review F2).
	rewound := chatViewFixture()
	rewound.chat.ResumePending = "seeded"
	rewound.chat.PendingPreamble = "<earlier-turns>\nthe rewound summary\n</earlier-turns>\n"
	rewound.applyLoaded(chatLoadedMsg{id: 1, chat: rewound.chat, turns: []apiclient.ChatTurn{
		{Seq: 1, Prompt: "kept turn", State: "succeeded"},
	}})
	got = ansi.Strip(strings.Join(rewound.bodyLines(100), "\n"))
	kept, pending, ctx := strings.Index(got, "kept turn"), strings.Index(got, "seeded, not resumed"), strings.Index(got, "turn 0 · seeded context (summary)")
	if kept < 0 || pending < kept || ctx < pending ||
		!strings.Contains(got, "the rewound summary") || !strings.Contains(got, "vincent chat preamble 1") {
		t.Errorf("seeded rewind with turns: want the turn, the pending line, then turn 0:\n%s", got)
	}
	long := &apiclient.Chat{ID: 11, ResumePending: "seeded", PendingPreamble: strings.Repeat("line\n", 100)}
	got = ansi.Strip(strings.Join(turnZeroLines(long, 80), "\n"))
	if !strings.Contains(got, "… 60 more lines") {
		t.Errorf("long preamble not bounded:\n%s", got)
	}
}
