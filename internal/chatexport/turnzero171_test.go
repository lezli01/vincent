package chatexport

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestReadNormalizedKeepsTheSeededContext (task 171 decision 4): a seeded
// turn's `vincent.seeded` note carries the context its session was given,
// and the reader keeps it beside the tool calls.
func TestReadNormalizedKeepsTheSeededContext(t *testing.T) {
	src := strings.Join([]string{
		`{"type":"vincent.seeded","resumed":false,"context":"<earlier-turns>\nalpha\n</earlier-turns>"}`,
		`{"type":"agent.tool_use","tools":[{"name":"Read","summary":"a.go","call_id":"1"}]}`,
	}, "\n")
	got, err := ReadNormalized(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if got.SeededContext != "<earlier-turns>\nalpha\n</earlier-turns>" || len(got.Tools) != 1 {
		t.Fatalf("read = %+v", got)
	}
	bare, _ := ReadNormalized(strings.NewReader(`{"type":"vincent.seeded","resumed":false}`))
	if bare.SeededContext != "" {
		t.Fatalf("a seed with nothing to give read %q", bare.SeededContext)
	}
}

// TestTurnZeroIsExported: a fork's provenance is in the metadata, a seeded
// turn shows the context it was given ahead of its prompt, and a pending
// turn 0 closes the conversation — in Markdown, HTML and JSON alike.
func TestTurnZeroIsExported(t *testing.T) {
	e := &Export{
		Version: Version,
		Chat: Chat{
			ID: 9, Title: "a fork", Agent: "codex", State: "idle",
			ForkedFrom:      &ForkedFrom{ChatID: 4, Seq: 3, Agent: "claude"},
			PendingPreamble: "<earlier-turns>\nthe pending summary\n</earlier-turns>\n",
		},
		Turns: []Turn{{
			Seq: 1, State: "succeeded", Prompt: "go on", Answer: "done", Seeded: true,
			SeededContext: "<earlier-turns>\nthe consumed summary\n</earlier-turns>", Tools: []ToolCall{},
		}},
	}
	md := Markdown(e)
	for _, want := range []string{
		"- **forked from:** #4 (claude), before turn 3",
		"### " + TurnZeroHeading + "\n\n```\n<earlier-turns>\nthe consumed summary",
		"## " + TurnZeroHeading + "\n\n_" + NotePendingPreamble + "_",
		"the pending summary",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Index(md, "the consumed summary") > strings.Index(md, "### Prompt") {
		t.Error("a seeded turn's context comes after its prompt")
	}
	if strings.Index(md, "the pending summary") < strings.Index(md, "## Turn 1") {
		t.Error("the pending turn 0 is not after the turns that ran")
	}

	var page bytes.Buffer
	if err := Render(&page, e, FormatHTML); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"the consumed summary", "the pending summary", "#4 (claude), before turn 3", TurnZeroHeading} {
		if !strings.Contains(page.String(), want) {
			t.Errorf("html lacks %q", want)
		}
	}

	var js bytes.Buffer
	if err := Render(&js, e, FormatJSON); err != nil {
		t.Fatal(err)
	}
	var back Export
	if err := json.Unmarshal(js.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.Chat.PendingPreamble != e.Chat.PendingPreamble || back.Turns[0].SeededContext != e.Turns[0].SeededContext ||
		back.Chat.ForkedFrom == nil || back.Chat.ForkedFrom.Agent != "claude" {
		t.Errorf("json round trip lost turn 0: %+v", back)
	}
}
