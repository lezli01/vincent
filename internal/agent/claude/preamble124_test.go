package claude

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// contextBlocksFixture is a real claude 2.1.277 run (issue #499, task 124.3)
// in vincent's input-mode argv, plus `--model haiku --setting-sources
// project` to keep it off the capturing user's settings. Its stdin was one
// user line of two text blocks — "You are joining vincent task 1, which is
// blocked.\n\nTitle: Probe task\n", then "/manual-only zebra" — and the
// worktree held a `disable-model-invocation: true` skill whose body asks for
// "CTX-PROBE: <the title of the vincent task described earlier> $ARGUMENTS".
// The cwd, the init line's skill, command and MCP lists and its local paths
// are rewritten; every other line is verbatim.
//
// The same bytes fused into one block, as a linked first turn used to send
// them, drew "I don't recognize that as a standard skill" from the same build.
const contextBlocksFixture = "stream_blocks_context_2.1.277.jsonl"

// TestUserMessageLineKeepsTheMessageLast pins the input-mode line: a preamble
// is its own text block ahead of the prompt's, the prompt block is the last
// one and exactly the prompt's bytes, and no preamble is the one block every
// run sent before.
func TestUserMessageLineKeepsTheMessageLast(t *testing.T) {
	for _, tt := range []struct {
		name, preamble, prompt string
		want                   []string
	}{
		{"no preamble", "", "/manual-only zebra", []string{"/manual-only zebra"}},
		{
			"preamble", "Title: Probe task\n", "/manual-only zebra\n",
			[]string{"Title: Probe task\n", "/manual-only zebra\n"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			line, err := userMessageLine(tt.preamble, tt.prompt)
			if err != nil {
				t.Fatalf("userMessageLine: %v", err)
			}
			var msg struct {
				Type    string `json:"type"`
				Message struct {
					Role    string `json:"role"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal(line, &msg); err != nil {
				t.Fatalf("decode %q: %v", line, err)
			}
			if msg.Type != "user" || msg.Message.Role != "user" {
				t.Fatalf("line = %s, want one user message", line)
			}
			var got []string
			for _, c := range msg.Message.Content {
				if c.Type != "text" {
					t.Fatalf("block type %q, want text: %s", c.Type, line)
				}
				got = append(got, c.Text)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("blocks = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFixtureContextBlockStream is the parser over the two-block capture: a
// skill the model may not invoke ran as the CLI's own expansion, answered
// from the context block, and the run ends in one successful result.
func TestFixtureContextBlockStream(t *testing.T) {
	events, r := fixtureEvents(t, contextBlocksFixture)
	var results int
	for i, ev := range events {
		switch ev.Type {
		case agent.EventError, agent.EventInputRequest:
			t.Errorf("line %d = %+v, want neither an error nor an input request", i+1, ev)
		case agent.EventResult:
			results++
		}
	}
	if results != 1 {
		t.Fatalf("results = %d, want 1: one user message is one turn", results)
	}
	last := events[len(events)-1]
	if last.Type != agent.EventResult || last.Result == nil || last.Result.IsError {
		t.Fatalf("stream must end in a success result, got %+v", last)
	}
	const want = "CTX-PROBE: Probe task zebra"
	if last.Result.ResultText != want {
		t.Errorf("ResultText = %q, want %q: the skill's body, filled from the context block", last.Result.ResultText, want)
	}
	if r.pending != nil {
		t.Errorf("pending = %+v, want nothing asked", r.pending)
	}
}
