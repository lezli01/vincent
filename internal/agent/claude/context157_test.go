package claude

import (
	"bufio"
	"bytes"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// finalResult runs a captured stream through one parser, the way a live run
// and the transcript route both do, and returns its last result event.
func finalResult(t *testing.T, fixture string) *agent.RunResult {
	t.Helper()
	p := new(streamParser)
	var res *agent.RunResult
	sc := bufio.NewScanner(bytes.NewReader(readFixture(t, fixture)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		if ev := p.parse(append([]byte(nil), sc.Bytes()...)); ev.Type == agent.EventResult {
			res = ev.Result
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatalf("%s: no result event", fixture)
	}
	return res
}

// TestContextFigures is task 157 decisions 1 and 2 against captured runs: the
// window is the one modelUsage reports for the main loop's model, and the
// prompt size is the last top-level assistant call's input plus its cache.
func TestContextFigures(t *testing.T) {
	tests := []struct {
		fixture        string
		tokens, window int64
	}{
		// A subagent's haiku sits in modelUsage beside the main loop's
		// opus, and its calls are the stream's last assistant lines but
		// one: neither may win.
		{"stream_subagent_async_2.1.268.jsonl", 140675, 1000000},
		{"stream_subagent_sync_2.1.263.jsonl", 35802, 1000000},
		// The assistant lines say `claude-opus-5`, the key `[1m]`.
		{"stream_skill_model_2.1.277.jsonl", 30845, 1000000},
		{"stream_question_2.1.226.jsonl", 33633, 200000},
		// The forked skill's only top-level assistant line is claude's own
		// `<synthetic>` reply, which is no model call: no prompt size, and
		// the window falls back to the one entry that spent anything.
		{"stream_skill_fork_2.1.277.jsonl", 0, 1000000},
		// No assistant line and an empty modelUsage: neither is reported.
		{"stream_conversation_reset_2.1.278.jsonl", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			res := finalResult(t, tt.fixture)
			if res.ContextTokens != tt.tokens || res.ContextWindow != tt.window {
				t.Errorf("context = %d of %d, want %d of %d",
					res.ContextTokens, res.ContextWindow, tt.tokens, tt.window)
			}
			if tt.tokens != 0 && res.ContextTokens == res.InputTokens+res.CacheReadTokens+res.CacheCreationTokens {
				t.Errorf("context %d equals the run's totals: it must be the last call's", res.ContextTokens)
			}
		})
	}
}

// TestContextWindowFallsBackToLargestSpend proves the no-model leg directly:
// with nothing to match, the entry that carried the most tokens is the main
// loop's, never a small subagent's.
func TestContextWindowFallsBackToLargestSpend(t *testing.T) {
	m := map[string]streamModelUsage{
		"claude-haiku-4-5":  {InputTokens: 3000, ContextWindow: 200000},
		"claude-opus-5[1m]": {InputTokens: 1500, CacheReadInputTokens: 400000, ContextWindow: 1000000},
	}
	if got := contextWindow(m, ""); got != 1000000 {
		t.Errorf("window = %d, want the opus entry's", got)
	}
	if got := contextWindow(m, "claude-haiku-4-5"); got != 200000 {
		t.Errorf("window = %d, want the named model's", got)
	}
	if got := contextWindow(nil, "x"); got != 0 {
		t.Errorf("window = %d, want 0 with no modelUsage", got)
	}
}
