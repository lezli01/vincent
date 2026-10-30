package copilot

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// rawTypes are every `type` the 1.0.105 captures carry, each with the
// normalized types its lines may become (task 138.7). A type with two has a
// shape that stays unmodeled: a message that only requests tools has no
// text, a reasoning block can close empty (resume_1.0.105.jsonl), and a
// subagent's user.message is its parent's prompt rather than an echo of
// vincent's. Everything else listed as unknown is deliberately so — the
// deltas (coalesced, or superseded by the closing line), the live shell
// chunks (the completion carries the body), and the CLI's bookkeeping.
var rawTypes = map[string][]agent.EventType{
	"assistant.message":        {agent.EventOutput, agent.EventUnknown},
	"assistant.reasoning":      {agent.EventThinking, agent.EventUnknown},
	"user.message":             {agent.EventInputEcho, agent.EventUnknown},
	"tool.execution_start":     {agent.EventToolUse},
	"tool.execution_complete":  {agent.EventToolResult},
	"subagent.started":         {agent.EventSubagentStarted},
	"subagent.completed":       {agent.EventSubagentFinished},
	"session.usage_checkpoint": {agent.EventUsage},
	"session.error":            {agent.EventError},
	"result":                   {agent.EventResult},

	"abort": {agent.EventUnknown}, "agent.interrupted": {agent.EventUnknown},
	"assistant.idle": {agent.EventUnknown}, "assistant.message_delta": {agent.EventUnknown},
	"assistant.message_start": {agent.EventUnknown}, "assistant.reasoning_delta": {agent.EventUnknown},
	"assistant.tool_call_delta": {agent.EventUnknown}, "assistant.turn_end": {agent.EventUnknown},
	"assistant.turn_start": {agent.EventUnknown}, "model.call_final_result": {agent.EventUnknown},
	"model.call_finished": {agent.EventUnknown}, "model.call_start": {agent.EventUnknown},
	"prompt_cache_break": {agent.EventUnknown}, "session.auto_mode_resolved": {agent.EventUnknown},
	"session.background_tasks_changed": {agent.EventUnknown}, "session.indexed_search": {agent.EventUnknown},
	"session.info": {agent.EventUnknown}, "session.mcp_server_status_changed": {agent.EventUnknown},
	"session.mcp_server_tools_loaded": {agent.EventUnknown}, "session.mcp_servers_loaded": {agent.EventUnknown},
	"session.tools_updated": {agent.EventUnknown}, "subagent.configured": {agent.EventUnknown},
	"subagent.selected": {agent.EventUnknown}, "tool.execution_partial_result": {agent.EventUnknown},
	"tool.shell_output": {agent.EventUnknown},
}

// TestFixtureEventTypesArePinned holds every 1.0.105 capture to the events it
// normalizes to: the count of each modeled type per fixture, and every raw
// type to its pinned normalization. A fixture or a raw type nobody pinned
// fails the test. The pin is this per-fixture inventory rather than cursor's
// full sequence string because a capture runs to hundreds of deltas.
func TestFixtureEventTypesArePinned(t *testing.T) {
	type counts map[agent.EventType]int
	// base is what every completed run carries: the prompt's echo, one
	// usage checkpoint and the result.
	base := func(extra counts) counts {
		c := counts{"input_echo": 1, "usage": 1, "result": 1}
		for k, v := range extra {
			c[k] = v
		}
		return c
	}
	want := map[string]counts{
		"denied_1.0.105.jsonl":                base(counts{"output": 1, "thinking": 1, "tool_use": 1, "tool_result": 1}),
		"mention_1.0.105.jsonl":               base(counts{"output": 1}),
		"mention_tools_1.0.105.jsonl":         base(counts{"output": 1, "tool_use": 1, "tool_result": 1}),
		"edit_1.0.105.jsonl":                  base(counts{"output": 1, "tool_use": 2, "tool_result": 2}),
		"mcp_1.0.105.jsonl":                   base(counts{"output": 1, "tool_use": 1, "tool_result": 1}),
		"reasoning_1.0.105.jsonl":             base(counts{"output": 1, "thinking": 1}),
		"restricted_1.0.105.jsonl":            base(counts{"output": 1, "thinking": 1, "tool_use": 9, "tool_result": 9}),
		"restricted_mcp_1.0.105.jsonl":        base(counts{"output": 1, "thinking": 1, "tool_use": 1, "tool_result": 1}),
		"restricted_resume_1.0.105.jsonl":     base(counts{"output": 1, "tool_use": 2, "tool_result": 2}),
		"restricted_write_1.0.105.jsonl":      base(counts{"output": 1, "thinking": 3, "tool_use": 4, "tool_result": 4}),
		"resume_1.0.105.jsonl":                base(counts{"output": 1}),
		"session_id_1.0.105.jsonl":            base(counts{"output": 1}),
		"session_id_unknown_1.0.105.jsonl":    base(counts{"output": 1}),
		"shell_1.0.105.jsonl":                 base(counts{"output": 1, "tool_use": 1, "tool_result": 1}),
		"sigterm_1.0.105.jsonl":               base(counts{"tool_use": 2, "tool_result": 1}),
		"simple_1.0.105.jsonl":                base(counts{"output": 1}),
		"skill_1.0.105.jsonl":                 base(counts{"output": 1, "tool_use": 1, "tool_result": 1}),
		"skill_invoke_leading_1.0.105.jsonl":  base(counts{"output": 1, "tool_use": 1, "tool_result": 1}),
		"skill_invoke_mid_1.0.105.jsonl":      base(counts{"output": 1, "tool_use": 1, "tool_result": 1}),
		"skill_store_1.0.105.jsonl":           base(counts{"output": 1, "tool_use": 1, "tool_result": 1}),
		"skill_store_linked_1.0.105.jsonl":    base(counts{"output": 1, "tool_use": 1, "tool_result": 1}),
		"skill_store_relocated_1.0.105.jsonl": base(counts{"output": 1, "thinking": 2, "tool_use": 1, "tool_result": 1}),
		"subagent_1.0.105.jsonl": base(counts{
			"output": 2, "thinking": 1, "tool_use": 3, "tool_result": 3,
			"subagent_started": 1, "subagent_finished": 1,
		}),
		"unavailable_model_1.0.105.jsonl":  {},
		"unsupported_effort_1.0.105.jsonl": {},
	}
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range names {
		name := filepath.Base(path)
		exp, ok := want[name]
		if !ok {
			t.Errorf("%s: no pinned event types; add the capture here", name)
			continue
		}
		got := counts{}
		for _, ev := range parseFixture(t, name) {
			allowed, ok := rawTypes[rawType(t, ev)]
			if !ok {
				t.Errorf("%s: raw type %q is not pinned", name, rawType(t, ev))
			} else if !containsType(allowed, ev.Type) {
				t.Errorf("%s: %q normalized to %q, want one of %v", name, rawType(t, ev), ev.Type, allowed)
			}
			if ev.Type != agent.EventUnknown {
				got[ev.Type]++
			}
		}
		if !equalCounts(got, exp) {
			t.Errorf("%s: modeled events %v, want %v", name, got, exp)
		}
	}
}

// rawType is the `type` of the line an event came from.
func rawType(t *testing.T, ev agent.Event) string {
	t.Helper()
	var line struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(ev.Raw, &line); err != nil {
		t.Fatalf("%v: %s", err, ev.Raw)
	}
	return line.Type
}

func containsType(ts []agent.EventType, t agent.EventType) bool {
	for _, x := range ts {
		if x == t {
			return true
		}
	}
	return false
}

func equalCounts(a, b map[agent.EventType]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// allFixtures runs every capture through its own parser.
func allFixtures(t *testing.T, each func(name string, i int, ev agent.Event)) {
	t.Helper()
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil || len(names) == 0 {
		t.Fatalf("fixtures: %v, %v", names, err)
	}
	for _, path := range names {
		name := filepath.Base(path)
		for i, ev := range parseFixture(t, name) {
			each(name, i, ev)
		}
	}
}

// TestInputEchoIsTheRootPrompt: the root agent's user.message is the echo of
// vincent's prompt, carrying nothing; the subagent's, which is the prompt its
// parent wrote it, is not an echo and is stamped with the spawning call.
func TestInputEchoIsTheRootPrompt(t *testing.T) {
	allFixtures(t, func(name string, i int, ev agent.Event) {
		if ev.Type == agent.EventInputEcho && (ev.Text != "" || ev.ParentCallID != "") {
			t.Errorf("%s line %d: echo carries %+v, want nothing", name, i, ev)
		}
	})
	var sub []agent.Event
	for _, ev := range parseFixture(t, "subagent_1.0.105.jsonl") {
		if rawType(t, ev) == "user.message" {
			sub = append(sub, ev)
		}
	}
	if len(sub) != 2 || sub[0].Type != agent.EventInputEcho ||
		sub[1].Type != agent.EventUnknown || sub[1].ParentCallID != "call_QsZsx9R4Ejvfr1Bo1qxMLfSp" {
		t.Errorf("user.message lines = %+v, want the root's echo then the subagent's prompt under its call", sub)
	}
}

// TestNoConversationResetOrSkill pins what no capture carries: a
// conversation reset, and a skill load. A `skill` tool call stays a tool_use
// — in skill_1.0.105.jsonl, and in skill_invoke_leading_1.0.105.jsonl and
// skill_invoke_mid_1.0.105.jsonl, where the message named the skill and the
// model still had to call the tool (task 138.13 decision E): `skill.invoked`
// is never on stdout on 1.0.105, and nothing synthesizes a skill event from
// a tool call. Input requests are absent too: copilot's InputSupport is
// never.
func TestNoConversationResetOrSkill(t *testing.T) {
	allFixtures(t, func(name string, i int, ev agent.Event) {
		switch ev.Type { //nolint:exhaustive // only the absent events matter here
		case agent.EventConversationReset, agent.EventSkill,
			agent.EventInputRequest, agent.EventInputCanceled:
			t.Errorf("%s line %d: produced %q", name, i, ev.Type)
		}
		if ev.Skill != nil || ev.Request != nil {
			t.Errorf("%s line %d: carries a skill or input request", name, i)
		}
	})
}

// TestNoRunHeader: 1.0.105 writes no cwd and no tool list to stdout —
// session.start goes only to the on-disk log, and session.tools_updated
// carries the model alone — so copilot produces no run_header, as codex
// does not (§9.9). Nothing fills one from the spawn spec.
func TestNoRunHeader(t *testing.T) {
	allFixtures(t, func(name string, i int, ev agent.Event) {
		if ev.Type == agent.EventRunHeader || ev.Header != nil {
			t.Errorf("%s line %d: produced a run header", name, i)
		}
	})
}
