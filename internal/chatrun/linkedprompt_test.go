package chatrun

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// linkedOpeningContext stands in for what taskrun assembles at open (task
// 119). Its content is not under test, only where it rides; the trailing
// newline is the one taskrun's context ends with.
const linkedOpeningContext = "You are joining vincent task 1, which is blocked.\n\nTitle: Probe task\n"

// linkedChatOn opens a chat on agentName linked to a blocked task working in
// the harness repo, carrying linkedOpeningContext, and routes every turn of it
// through rec — the seam where a linked turn's launcher comes from (task 119
// decision 3), and so the one place a test can read the CLI's stdin verbatim.
func (h *harness) linkedChatOn(t *testing.T, agentName string, rec *agenttest.RecordingLauncher) *store.Chat {
	t.Helper()
	ctx := t.Context()
	task := &store.Task{
		ProjectID: h.project.ID, Title: "Probe task", WorkflowName: "t", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/1-probe-task", State: store.TaskBlocked,
		WorktreePath: h.repo,
	}
	if err := h.store.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	c := &store.Chat{
		Title: "why", Agent: agentName, PermissionMode: string(agent.FullAuto),
		OpeningContext: linkedOpeningContext,
	}
	if err := h.store.OpenLinkedChat(ctx, task.ID, store.TaskBlocked, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	h.runner.deps.Launchers = func(context.Context, int64, int64) (
		agent.Launcher, string, []string, error,
	) {
		return rec, "", nil, nil
	}
	return c
}

// userLineTexts decodes the one stream-json user line claude's input mode
// reads its prompt from (§9.2) into its text blocks, in order.
func userLineTexts(t *testing.T, stdin []byte) []string {
	t.Helper()
	line, _, _ := bytes.Cut(stdin, []byte("\n"))
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Type != "user" {
		t.Fatalf("stdin does not open with a stream-json user message (%v): %q", err, stdin)
	}
	texts := make([]string, 0, len(msg.Message.Content))
	for i, c := range msg.Message.Content {
		if c.Type != "text" {
			t.Fatalf("content block %d is %q, want text: %q", i, c.Type, line)
		}
		texts = append(texts, c.Text)
	}
	return texts
}

// TestLinkedFirstTurnKeepsASkillInvocationLeading is issue #499 (task 124.3).
// claude expands `/name` only when it starts the message's last text block
// (captured against 2.1.277: a context block ahead of the message expands,
// the message wrapped after the context in one block does not). A linked
// chat's first turn used to send the opening context and the human's message
// as one block, `<context>\n\n<message>\n/name …\n</message>\n`, so the
// invocation a reviewer is most likely to type on a blocked task reached the
// CLI mid-text and was never expanded.
//
// On claude's input path the context therefore rides as its own block ahead
// of the message, and the message block is the human's bytes and nothing
// else (025 decision 5). Turn 2 carries no context (§5.5), so it is one block.
func TestLinkedFirstTurnKeepsASkillInvocationLeading(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newHarness(t)
	rec := &agenttest.RecordingLauncher{}
	c := h.linkedChatOn(t, "claude", rec)

	const first = "/manual-only zebra"
	if turn := h.sendAndWait(t, c.ID, first); turn.State != chatstate.TurnDone {
		t.Fatalf("turn 1 = %s (%s: %s)", turn.State, turn.FailReason, turn.ErrorMessage)
	}
	h.waitIdle(t, c.ID)
	const second = "/manual-only again"
	if turn := h.sendAndWait(t, c.ID, second); turn.State != chatstate.TurnDone {
		t.Fatalf("turn 2 = %s (%s: %s)", turn.State, turn.FailReason, turn.ErrorMessage)
	}

	launches := rec.Launches()
	if len(launches) != 2 {
		t.Fatalf("launcher saw %d turns, want 2", len(launches))
	}
	if !launches[0].Command.StdinPipe {
		t.Fatal("turn 1 did not start in input mode; fakeagent's default version passes the gate")
	}

	blocks := userLineTexts(t, launches[0].Stdin())
	if len(blocks) != 2 {
		t.Fatalf("turn 1 sent %d text block(s), want 2 — the context, then the message:\n%q", len(blocks), blocks)
	}
	if strings.TrimSpace(blocks[0]) != strings.TrimSpace(linkedOpeningContext) {
		t.Errorf("turn 1 block 1 = %q, want the opening context %q", blocks[0], linkedOpeningContext)
	}
	if blocks[1] != first {
		t.Errorf("turn 1 block 2 = %q, want the human's message byte for byte, %q", blocks[1], first)
	}

	blocks = userLineTexts(t, launches[1].Stdin())
	if len(blocks) != 1 || blocks[0] != second {
		t.Errorf("turn 2 sent %q, want the one block %q and no context", blocks, second)
	}
}

// TestLinkedFirstTurnStdinOffClaudeInputMode pins the other side of issue
// #499: every path that takes the prompt as one string — claude below the
// §7.4 input gate, codex and cursor — keeps receiving exactly the bytes a
// linked first turn has always sent. codex and cursor find an invocation
// anywhere in the prompt, and a claude outside the gate degrades to the
// model's mediation, as it already loses §7.4.
func TestLinkedFirstTurnStdinOffClaudeInputMode(t *testing.T) {
	const msg = "/manual-only zebra"
	want := strings.TrimSuffix(linkedOpeningContext, "\n") + "\n\n<message>\n" + msg + "\n</message>\n"
	for _, tt := range []struct {
		name, agent, version string
	}{
		{name: "claude below the input gate", agent: "claude", version: "1.0.0"},
		{name: "codex", agent: "codex"},
		{name: "cursor", agent: "cursor"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FAKEAGENT_SCENARIO", "success")
			if tt.version != "" {
				t.Setenv("FAKEAGENT_VERSION", tt.version)
			}
			h := newHarness(t)
			rec := &agenttest.RecordingLauncher{}
			c := h.linkedChatOn(t, tt.agent, rec)
			if turn := h.sendAndWait(t, c.ID, msg); turn.State != chatstate.TurnDone {
				t.Fatalf("turn 1 = %s (%s: %s)", turn.State, turn.FailReason, turn.ErrorMessage)
			}
			launches := rec.Launches()
			if len(launches) != 1 {
				t.Fatalf("launcher saw %d turns, want 1", len(launches))
			}
			if launches[0].Command.StdinPipe {
				t.Fatal("turn 1 started in claude's input mode; this test is about the plain paths")
			}
			if got := string(launches[0].Stdin()); got != want {
				t.Errorf("stdin = %q, want today's %q", got, want)
			}
		})
	}
}
