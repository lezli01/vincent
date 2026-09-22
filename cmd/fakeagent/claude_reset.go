package main

// claude's conversation reset (task 124.20): what a `/clear` passed through a
// chat verbatim (124.6, decision 78) makes the CLI write.
//
// Captured from 2.1.278 in
// internal/agent/claude/testdata/stream_conversation_reset_2.1.278.jsonl. The
// line is a *top-level type* rather than a `system` subtype, it carries a
// `new_conversation_id` beside the session id it is leaving, and every line
// after it carries a new session id — which vincent stores last-wins (§9.2),
// so the next turn resumes an empty conversation.
//
// The scenario keeps that shape from the reset onward: the reset line under
// the id the run was resuming, then a second `init` and the result under a
// fresh one. The leading `init` this binary emits for every claude run comes
// first because it is hoisted above the scenario switch; the fixture is where
// the real stream's order is pinned.
//
// The real run ends with an empty `success` carrying `num_turns: 0` — a
// `/clear` produces no model turn — so this one does too.
func conversationReset() {
	leaving := sessionID
	emit(map[string]any{
		"type":                "conversation_reset",
		"new_conversation_id": "fake-conversation-2",
		"session_id":          leaving,
	})
	// Every later line carries the new id, which is what vincent stores and
	// resumes next turn. Minted rather than taken from new_conversation_id:
	// the real CLI's two ids differ, and a fake that fused them would hide an
	// adapter reading the wrong one.
	sessionID = leaving + "-after-reset"
	emit(claudeInitLine())
	emit(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": "", "num_turns": 0,
		"usage": map[string]int64{"input_tokens": 3, "output_tokens": 0},
	})
}
