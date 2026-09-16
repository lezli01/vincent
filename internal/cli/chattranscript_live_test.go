package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// `vincent chat transcript` (task 103) against the same real handlers
// `vincent task transcript` is tested against. Chat and turn rows are written
// straight into the store and the files laid down where the route derives
// them; no chat runner is started.

// addChat creates an idle claude chat on the harness's project.
func (h *liveHarness) addChat(t *testing.T) *store.Chat {
	t.Helper()
	c := &store.Chat{
		ProjectID: h.projectID, Title: "talk", State: chatstate.Idle,
		Agent: "claude", BaseBranch: "main", Branch: "vincent/1-talk",
	}
	if err := h.st.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

// addTurn appends a turn in state, failing for reason when one is given, and
// writes its transcript when lines are given. The chat is put back to idle so
// the next turn can be added, which lets a test hold a running turn beside a
// newer finished one — the case selection has to get right.
func (h *liveHarness) addTurn(
	t *testing.T, chat *store.Chat, state chatstate.TurnState, reason string, lines ...string,
) *store.ChatTurn {
	t.Helper()
	ctx := t.Context()
	turn, err := h.st.CreateChatTurn(ctx, chat.ID, "go")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	if len(lines) > 0 {
		h.writeTurnTranscript(t, chat.ID, turn.Seq, lines...)
	}
	if state != chatstate.TurnRunning {
		h.setTurnState(t, turn, state, reason)
	}
	if _, err := h.st.SetChatState(ctx, chat.ID, chatstate.Idle); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
	return turn
}

func (h *liveHarness) setTurnState(t *testing.T, turn *store.ChatTurn, state chatstate.TurnState, reason string) {
	t.Helper()
	turn.State, turn.FailReason = state, reason
	if err := h.st.UpdateChatTurn(t.Context(), turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
}

// turnTranscriptPath is {data}/transcripts/chat-{id}/{seq}.jsonl, the path
// the route derives.
func (h *liveHarness) turnTranscriptPath(chatID int64, seq int) string {
	return filepath.Join(h.dataDir, "transcripts", worktree.ChatOwner(chatID).Dir(),
		fmt.Sprintf("%d.jsonl", seq))
}

// writeTurnTranscript appends lines to a turn's transcript, creating it.
func (h *liveHarness) writeTurnTranscript(t *testing.T, chatID int64, seq int, lines ...string) {
	t.Helper()
	path := h.turnTranscriptPath(chatID, seq)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func chatArgs(chat *store.Chat, extra ...string) []string {
	return append([]string{"chat", "transcript", strconv.FormatInt(chat.ID, 10)}, extra...)
}

// A chat turn is rendered by the printer a task attempt is (task 103 decision
// 5), so the same captured claude run reads identically through both
// commands: the run header, the output, the tool lines and the result line.
func TestChatTranscriptRendersLikeATaskAttempt(t *testing.T) {
	h := newLiveHarness(t)
	lines := fixtureLines(t, filepath.Join("..", "agent", "claude", "testdata",
		"stream_permission_deny_2.1.226.jsonl"))
	run := h.addRunAs(t, "claude", h.taskID, "implement", store.StepSucceeded, lines...)
	chat := h.addChat(t)
	h.addTurn(t, chat, chatstate.TurnDone, "", lines...)

	want := strings.Join(renderedTranscript(t, h, run), "\n")
	out, errOut, code := runCLI(t, chatArgs(chat)...)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, errOut)
	}
	got := strings.TrimRight(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	if got != want {
		t.Errorf("chat turn rendered\n%s\nwant what the task attempt renders:\n%s", got, want)
	}
	for _, prefix := range []string{"# ", "> ", "= "} {
		if !strings.Contains("\n"+got, "\n"+prefix) {
			t.Errorf("output has no %q line:\n%s", prefix, got)
		}
	}
}

// --raw is the file's own bytes, garbage line included; --json is NDJSON with
// the annotations' own fields kept; and the two are refused together.
func TestChatTranscriptRawAndJSON(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	turn := h.addTurn(t, chat, chatstate.TurnDone, "",
		lineOutput, lineGarbage, lineUnknownAnnotation, lineResult)

	out, _, code := runCLI(t, chatArgs(chat, "--raw")...)
	if code != 0 {
		t.Fatalf("--raw exit = %d, want 0", code)
	}
	file, err := os.ReadFile(h.turnTranscriptPath(chat.ID, turn.Seq))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if out != string(file) {
		t.Errorf("--raw output = %q, want the file verbatim %q", out, file)
	}

	out, _, code = runCLI(t, chatArgs(chat, "--json")...)
	if code != 0 {
		t.Fatalf("--json exit = %d, want 0", code)
	}
	var types []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		var rec apiclient.TranscriptRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %q is not a TranscriptRecord: %v", line, err)
		}
		types = append(types, rec.Type)
	}
	// The line that is not JSON reaches --json as the normalizer's agent.raw.
	if got, want := strings.Join(types, ","), "agent.output,agent.raw,vincent.parked,agent.result"; got != want {
		t.Errorf("types = %s, want %s", got, want)
	}
	if !strings.Contains(out, `"reason":"quota"`) {
		t.Errorf("--json dropped an annotation field:\n%s", out)
	}

	_, errOut, code := runCLI(t, chatArgs(chat, "--json", "--raw")...)
	if code == 0 {
		t.Errorf("--json with --raw was accepted")
	}
	if !strings.Contains(errOut, "json") || !strings.Contains(errOut, "raw") {
		t.Errorf("stderr = %q, want it to name the two flags", errOut)
	}
}

// With --turn omitted: the running turn over a newer finished one, the newest
// when nothing runs. An unknown turn and a chat with no turns are both exit 1.
func TestChatTranscriptSelectsTheTurn(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	running := h.addTurn(t, chat, chatstate.TurnRunning, "",
		`{"type":"assistant","message":{"content":[{"type":"text","text":"turn one speaking"}]}}`)
	h.addTurn(t, chat, chatstate.TurnDone, "",
		`{"type":"assistant","message":{"content":[{"type":"text","text":"turn two speaking"}]}}`)

	out, _, code := runCLI(t, chatArgs(chat)...)
	if code != 0 || !strings.Contains(out, "turn one speaking") {
		t.Errorf("exit %d; default selection did not pick the running turn:\n%s", code, out)
	}

	h.setTurnState(t, running, chatstate.TurnDone, "")
	out, _, code = runCLI(t, chatArgs(chat)...)
	if code != 0 || !strings.Contains(out, "turn two speaking") {
		t.Errorf("exit %d; with nothing running, selection did not pick the newest turn:\n%s", code, out)
	}

	out, _, code = runCLI(t, chatArgs(chat, "--turn", "1")...)
	if code != 0 || !strings.Contains(out, "turn one speaking") {
		t.Errorf("exit %d; --turn 1 did not print turn 1:\n%s", code, out)
	}

	out, errOut, code := runCLI(t, chatArgs(chat, "--turn", "9")...)
	if code != 1 || out != "" {
		t.Errorf("unknown turn: exit %d, stdout %q; want 1 and nothing", code, out)
	}
	if want := fmt.Sprintf("turn 9 not found on chat %d", chat.ID); !strings.Contains(errOut, want) {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}

	empty := h.addChat(t)
	_, errOut, code = runCLI(t, chatArgs(empty)...)
	if code != 1 {
		t.Errorf("chat with no turns: exit %d, want 1", code)
	}
	if want := fmt.Sprintf("chat %d has no turns yet", empty.ID); !strings.Contains(errOut, want) {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
}

// A turn that failed before its file was opened has no transcript, and the
// turn row says so without a request; a finished turn whose file is gone was
// pruned, which is a failure naming the setting that prunes.
func TestChatTranscriptNoTranscriptVersusPrunedFile(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	h.addTurn(t, chat, chatstate.TurnFailed, chatrun.ReasonAgentUnavailable)

	out, errOut, code := runCLI(t, chatArgs(chat)...)
	if code != 0 {
		t.Errorf("exit on a turn with no transcript = %d, want 0", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if want := "turn 1 (agent_unavailable) has no transcript"; !strings.Contains(errOut, want) {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
	if n := h.transcriptCalls.Load(); n != 0 {
		t.Errorf("%d transcript requests; the answer was in the turn row already", n)
	}

	pruned := h.addTurn(t, chat, chatstate.TurnDone, "", lineOutput)
	if err := os.Remove(h.turnTranscriptPath(chat.ID, pruned.Seq)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	_, errOut, code = runCLI(t, chatArgs(chat)...)
	if code != 1 {
		t.Errorf("exit on a pruned transcript = %d, want 1", code)
	}
	if !strings.Contains(errOut, "transcript_retention_days") {
		t.Errorf("stderr = %q, want it to name the retention setting", errOut)
	}
}

// Following a turn resumes from X-Next-Offset with no record twice and none
// missed, and ends once the turn leaves running — printing what it wrote on
// the way out first.
func TestChatTranscriptFollowEndsWithTheTurn(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	turn := h.addTurn(t, chat, chatstate.TurnRunning, "", lineCommandStarted)
	c, err := apiclient.Discover(h.dataDir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	var out, errOut lockedBuffer
	p := &transcriptPrinter{out: &out, errOut: &errOut}
	src := chatTurnSource{c: c, chatID: chat.ID, seq: turn.Seq}
	next, err := p.printFrom(t.Context(), src, apiclient.TranscriptOptions{Tail: apiclient.DefaultTailBytes})
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.followFrom(ctx, src, next, followPoll) }()

	h.writeTurnTranscript(t, chat.ID, turn.Seq, lineOutput)
	waitFor(t, func() bool { return strings.Contains(out.String(), "looking at the failing test") },
		"the first appended record")
	h.writeTurnTranscript(t, chat.ID, turn.Seq, lineTool)
	waitFor(t, func() bool { return strings.Contains(out.String(), "> Bash") },
		"the second appended record")

	h.writeTurnTranscript(t, chat.ID, turn.Seq, lineResult)
	h.setTurnState(t, turn, chatstate.TurnDone, "")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not end when the turn left running")
	}

	want := []string{
		"$ go build ./...",
		"looking at the failing test",
		"> Bash go test ./internal/store",
		"= done ($0.0125)",
	}
	if got := strings.TrimRight(out.String(), "\n"); got != strings.Join(want, "\n") {
		t.Errorf("followed output =\n%s\nwant each record exactly once, in order:\n%s",
			got, strings.Join(want, "\n"))
	}
	if got := errOut.String(); !strings.Contains(got, "turn 1 is done") {
		t.Errorf("follow ended without saying why: %q", got)
	}
}
