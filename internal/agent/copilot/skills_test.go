package copilot

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestSkillNotInvokedFromTheMessage pins why copilot is neither a
// SkillInvoker nor a SkillLister (§9.9, task 138.13 decisions B and D),
// against the captures that decided it: on stdin, `/fixture-greeting` at the
// start of the message and in the middle of it both reach the model as
// typed — `transformedContent` is the prompt behind the datetime preamble,
// no skill content beside it — and the skill runs only because the model
// chose to call its `skill` tool. That is the model reading prose, which any
// adapter's model may do, not the CLI recognizing a syntax; a picker that
// promised it would be promising a guess.
//
// This is a refusal pinned to a real CLI on purpose: it is what the 1.0.105
// captures show, and it inverts with them — recapture both when
// testedVersions moves, and implement agent.SkillInvoker the day the CLI
// expands the invocation itself.
func TestSkillNotInvokedFromTheMessage(t *testing.T) {
	a := New(func() string { return "" })
	if agent.CanInvokeSkills(a) || agent.CanListSkills(a) {
		t.Errorf("CanInvokeSkills, CanListSkills = %v, %v, want false, false",
			agent.CanInvokeSkills(a), agent.CanListSkills(a))
	}
	for name, prompt := range map[string]string{
		"skill_invoke_leading_1.0.105.jsonl": "/fixture-greeting",
		"skill_invoke_mid_1.0.105.jsonl":     "Please run /fixture-greeting now.",
	} {
		lines := fixtureLines(t, name)
		msg := userMessage(t, name, lines)
		if msg.Data.Content != prompt {
			t.Errorf("%s: content = %q, want %q", name, msg.Data.Content, prompt)
		}
		if !strings.HasSuffix(msg.Data.TransformedContent, "\n\n"+prompt) ||
			strings.Contains(msg.Data.TransformedContent, "FIXTURE GREETING OK") {
			t.Errorf("%s: transformedContent = %q, want the prompt as typed and no skill body",
				name, msg.Data.TransformedContent)
		}
		var calls []string
		for _, l := range lines {
			if l.Type == "tool.execution_start" {
				calls = append(calls, l.Data.ToolName+":"+l.Data.Arguments.Skill)
			}
		}
		if len(calls) != 1 || calls[0] != "skill:fixture-greeting" {
			t.Errorf("%s: tool calls %v, want the model's one skill call", name, calls)
		}
		if out := outputText(t, name); out != "FIXTURE GREETING OK" {
			t.Errorf("%s: answer = %q", name, out)
		}
	}
}
