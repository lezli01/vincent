package main

import (
	"fmt"
	"io"
	"os"
)

// cursorVersion is the calver+sha shape cursor-agent reports (§9.7) — never
// semver, which is exactly what the adapter must record verbatim.
const cursorVersion = "2026.08.04-fake000"

// cursorModels is the `cursor-agent models` output shape: a heading, `id -
// Display Name` lines, and a trailing tip paragraph the parser must not
// mistake for a model.
const cursorModels = `Available models

auto - Auto (current, default)
fake-model-high - Fake Model High
fake-model-low - Fake Model Low

Tip: use --model <id> (or /model <id> in interactive mode) to switch.
`

// cursorMain is the cursor dialect (T5.2): argv shaped like
// `cursor-agent -p --output-format stream-json --trust …`, prompt from stdin,
// cursor stream-json on stdout. The emitted shapes mirror the fixtures
// captured from a real cursor-agent 2026.08.04-aaa8809
// (internal/agent/cursor/testdata).
func cursorMain(scenario string) {
	prompt, _ := io.ReadAll(os.Stdin)

	emit(map[string]any{
		"type": "system", "subtype": "init", "apiKeySource": "login",
		"session_id": "fake-session-1", "model": "Fake", "permissionMode": "default",
	})
	emit(map[string]any{"type": "user", "message": map[string]any{
		"role":    "user",
		"content": []any{map[string]any{"type": "text", "text": string(prompt)}},
	}, "session_id": "fake-session-1"})

	switch scenario {
	case "error-event":
		emitCursorText("something went wrong, giving up")
		emit(map[string]any{
			"type": "result", "subtype": "error", "is_error": true,
			"result": "fake cursor failed on purpose",
			"usage":  map[string]int64{"inputTokens": 50, "outputTokens": 7},
		})
		os.Exit(1)
	case "no-result":
		// The everyday cursor failure: an invalid model id exits nonzero with
		// the message on stderr and no result event at all (§9.7).
		fmt.Fprintln(os.Stderr, `ActionRequiredError: AI Model Not Found Model name is not valid: "fake-nonexistent"`)
		os.Exit(1)
	case "nonzero-exit":
		emitCursorText("about to crash")
		emitCursorResult(prompt, 100, 42)
		os.Exit(3)
	case "hang":
		if os.Getenv("FAKEAGENT_SPAWN_CHILD") == "1" {
			spawnChild()
		}
		emitCursorText("hanging until killed")
		block()
	case "big-usage":
		emitCursorText("burning tokens")
		emitCursorResult(prompt, 2_500_000, 1_200_000)
	default: // success
		emit(map[string]any{
			"type": "thinking", "subtype": "delta",
			"text": "thinking about it", "session_id": "fake-session-1",
		})
		emit(map[string]any{"type": "thinking", "subtype": "completed", "session_id": "fake-session-1"})
		emitCursorText("Working on: " + firstLine(string(prompt)))
		emit(map[string]any{
			"type": "tool_call", "subtype": "started", "call_id": "tool_fake_1",
			"tool_call": map[string]any{"editToolCall": map[string]any{
				"args": map[string]any{"path": "fake.txt"},
			}},
		})
		emit(map[string]any{
			"type": "tool_call", "subtype": "completed", "call_id": "tool_fake_1",
			"tool_call": map[string]any{"editToolCall": map[string]any{
				"result": map[string]any{"success": map[string]any{}},
			}},
		})
		emit(map[string]any{"type": "fake_marker", "note": "unknown event type for tolerant-parsing tests"})
		workFor(emitCursorText)
		if f := os.Getenv("FAKEAGENT_EDIT_FILE"); f != "" {
			editFile(f)
		}
		emitCursorResult(prompt, 100, 42)
	}
}

// emitCursorText emits an assistant message — whole, never a delta (§9.7).
func emitCursorText(text string) {
	emit(map[string]any{"type": "assistant", "message": map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": text}},
	}, "session_id": "fake-session-1"})
}

// emitCursorResult ends the turn. Usage keys are camelCase and no cost is
// reported — both real cursor properties the adapter is written against.
func emitCursorResult(prompt []byte, inTok, outTok int64) {
	emit(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"duration_ms": 1, "result": "done: " + flatten(string(prompt), 1000),
		"usage": map[string]int64{
			"inputTokens": inTok, "outputTokens": outTok,
			"cacheReadTokens": 0, "cacheWriteTokens": 0,
		},
	})
}
