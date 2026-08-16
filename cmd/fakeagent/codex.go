package main

import (
	"fmt"
	"io"
	"os"
	"time"
)

// codexLoginStatus answers `codex login status`, the §9.5 auth probe the
// codex adapter gained in task 006. The legs are the four the parse has to
// tell apart, and the environment picks between them so argv stays exactly
// what the real CLI is asked:
//
//	FAKEAGENT_CODEX_LOGGED_OUT=1        explicit negative, exit 1
//	FAKEAGENT_CODEX_LOGIN_UNKNOWN=1     output the parse must refuse to read
//	FAKEAGENT_CODEX_LOGIN_HANG=1        never answers — the T4.22 timeout leg
//
// The default is the positive answer.
func codexLoginStatus() {
	switch {
	case os.Getenv("FAKEAGENT_CODEX_LOGIN_HANG") == "1":
		// Deliberately longer than any probe bound; the caller's deadline is
		// what ends this process, which is the situation being pinned.
		time.Sleep(10 * time.Minute)
	case os.Getenv("FAKEAGENT_CODEX_LOGGED_OUT") == "1":
		fmt.Println("Not logged in")
		os.Exit(1)
	case os.Getenv("FAKEAGENT_CODEX_LOGIN_UNKNOWN") == "1":
		// Exits 0 and says nothing the parse recognizes: unknown, never a guess.
		fmt.Println("codex-cli 0.142.5 (fake)")
	default:
		fmt.Println("Logged in using ChatGPT (fake@example.com)")
	}
}

// codexMain is the codex dialect (T2.9): argv shaped like
// `codex exec --json …`, prompt from stdin, `codex exec --json` JSONL on
// stdout. The emitted shapes mirror the fixtures captured from a real
// codex-cli 0.142.5 (internal/agent/codex/testdata).
func codexMain(scenario string) {
	prompt, _ := io.ReadAll(os.Stdin)

	emit(map[string]any{"type": "thread.started", "thread_id": "fake-thread-1"})
	emit(map[string]any{"type": "turn.started"})
	switch scenario {
	case "error-event":
		emitCodexMessage("something went wrong, giving up")
		emit(map[string]any{"type": "error", "message": "fake codex failed on purpose"})
		emit(map[string]any{"type": "turn.failed", "error": map[string]any{
			"message": "fake codex failed on purpose",
		}})
		os.Exit(1) // real codex exits nonzero on a failed turn
	case "nonzero-exit":
		emitCodexMessage("about to crash")
		emitCodexTurnCompleted(prompt, 100, 42)
		os.Exit(3)
	case "hang":
		if os.Getenv("FAKEAGENT_SPAWN_CHILD") == "1" {
			spawnChild()
		}
		emitCodexMessage("hanging until killed")
		block()
	case "big-usage":
		emitCodexMessage("burning tokens")
		emitCodexTurnCompleted(prompt, 2_500_000, 1_200_000)
	case "usage-limit":
		// The codex leg of task 003. The codex adapter deliberately classifies
		// nothing — its wording is not fixture-verified — so this scenario
		// exists to *prove* that: a quota stop here still reads as it always
		// did, an ordinary failed turn under the §7.2 budget.
		if usageLimitSpent() {
			emitCodexMessage("stopping: the usage limit for this window is spent")
			emit(map[string]any{"type": "turn.failed", "error": map[string]any{
				"message": usageLimitMessage(),
			}})
			os.Exit(1)
		}
		codexSuccess(prompt)
	case "unauthenticated":
		emit(map[string]any{"type": "turn.failed", "error": map[string]any{
			"message": unauthenticatedMessage,
		}})
		os.Exit(1)
	default: // success
		codexSuccess(prompt)
	}
}

// codexSuccess is the `success` body, shared with a usage-limit run whose
// window has reopened.
func codexSuccess(prompt []byte) {
	emit(map[string]any{"type": "item.started", "item": map[string]any{
		"id": "item_0", "type": "command_execution",
		"command": "echo fake", "status": "in_progress",
	}})
	emit(map[string]any{"type": "item.completed", "item": map[string]any{
		"id": "item_0", "type": "command_execution",
		"command": "echo fake", "aggregated_output": "fake\n",
		"exit_code": 0, "status": "completed",
	}})
	emit(map[string]any{"type": "fake_marker", "note": "unknown event type for tolerant-parsing tests"})
	workFor(emitCodexMessage)
	if f := os.Getenv("FAKEAGENT_EDIT_FILE"); f != "" {
		editFile(f)
	}
	emitCodexMessage("done: " + flatten(string(prompt), 1000))
	emitCodexTurnCompleted(prompt, 100, 42)
}

// emitCodexMessage emits an agent_message item — codex's assistant text.
func emitCodexMessage(text string) {
	emit(map[string]any{"type": "item.completed", "item": map[string]any{
		"id": "item_msg", "type": "agent_message", "text": text,
	}})
}

// emitCodexTurnCompleted ends the turn with usage; codex reports no cost.
func emitCodexTurnCompleted(_ []byte, inTok, outTok int64) {
	emit(map[string]any{"type": "turn.completed", "usage": map[string]int64{
		"input_tokens": inTok, "cached_input_tokens": 0,
		"output_tokens": outTok, "reasoning_output_tokens": 0,
	}})
}
