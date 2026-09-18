package main_test

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// The `chat-reply` scenario exists for scripts/screenshots.sh, which CI does
// not run — so what the documentation screenshots depend on is pinned here
// instead. Two properties, and both are load-bearing:
//
//   - the answer is a markdown document with a fenced block and links in it,
//     because the chat workspace's copy picker offers "the markdown", "the
//     plain text" and "each code block" and the link picker lists what the
//     prose points at (task 076). A reply without those parts photographs an
//     empty popup.
//   - a resumed turn answers differently, which is what makes a two-turn
//     conversation read as a conversation rather than as the same paragraph
//     twice.
func TestChatReplyIsAMarkdownDocument(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)

	out := runAgent(t, bin, claudeArgs, "FAKEAGENT_SCENARIO=chat-reply")
	for _, want := range []string{"## ", "```go", "https://www.rfc-editor.org/"} {
		if !strings.Contains(out, want) {
			t.Errorf("the first chat answer has no %q in it:\n%s", want, out)
		}
	}
	// The recall line is what a continuity test reads; prose is what this
	// scenario is for, and the two must not be mixed into one document.
	if strings.Contains(out, "recalled: ") {
		t.Errorf("chat-reply emitted the recall line:\n%s", out)
	}
}

func TestChatReplyMovesOnWhenResumed(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	dir := t.TempDir()
	env := []string{"FAKEAGENT_SCENARIO=chat-reply", "FAKEAGENT_SESSION_DIR=" + dir}

	first := runAgent(t, bin, claudeArgs, env...)
	// The store names its first conversation, so the resume id is known
	// without parsing the stream for it.
	second := runAgent(t, bin, append(claudeArgs, "--resume", "fake-session-1"), env...)

	if first == second {
		t.Fatalf("the resumed turn repeated the first answer:\n%s", first)
	}
	if !strings.Contains(second, "```json") {
		t.Errorf("the resumed turn is not the second document:\n%s", second)
	}
	if strings.Contains(second, "recalled: ") {
		t.Errorf("chat-reply emitted the recall line on resume:\n%s", second)
	}
}

// claudeArgs is the run argv of the dialect a chat is held on.
var claudeArgs = []string{"-p", "--output-format", "stream-json"}
