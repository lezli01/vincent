package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// turnZeroStub is chat 9, a seeded fork: its next turn is pending with a
// seeded context (pending=true), or its first turn has run with the context
// recorded on its transcript's vincent.seeded note.
func turnZeroStub(pending bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case r.URL.Path == "/v1/chats/9" && pending:
			_, _ = io.WriteString(w, `{"chat":{"id":9,"state":"idle","agent":"codex","resume_pending":"seeded",
				"pending_preamble":"<earlier-turns>\nthe summary\n</earlier-turns>\n",
				"forked_from":{"chat_id":4,"seq":3,"agent":"claude"}},"turns":[]}`)
		case r.URL.Path == "/v1/chats/9":
			_, _ = io.WriteString(w, `{"chat":{"id":9,"state":"idle","agent":"codex",
				"forked_from":{"chat_id":4,"seq":3,"agent":"claude"}},
				"turns":[{"id":1,"chat_id":9,"seq":1,"prompt":"go","state":"done","seeded":true,
				"started_at":"2026-11-22T10:00:00Z"}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/chats/9/turns/1/"):
			_, _ = io.WriteString(w, `{"type":"vincent.seeded","resumed":false,"context":"<earlier-turns>\nthe summary\n</earlier-turns>"}`+
				"\n"+`{"type":"agent.output","text":"hello"}`+"\n")
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no such route"}}`)
		}
	}
}

// TestChatTranscriptTurnZero (task 171 decision 4): --turn 0 prints a
// pending seeded context, as text or JSON; with none pending it names the
// seeded turn whose transcript holds the one that ran; --raw is refused.
func TestChatTranscriptTurnZero(t *testing.T) {
	stdout, stderr, code := runChatSend(t, turnZeroStub(true), "chat", "transcript", "9", "--turn", "0")
	if code != 0 || !strings.HasPrefix(stdout, turnZeroHeading+"\n<earlier-turns>\nthe summary\n</earlier-turns>\n") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}

	stdout, _, code = runChatSend(t, turnZeroStub(true), "chat", "transcript", "9", "--turn", "0", "--json")
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &got) != nil || got["turn"] != 0.0 ||
		!strings.Contains(got["context"].(string), "the summary") {
		t.Fatalf("json: exit %d %q", code, stdout)
	}

	_, stderr, code = runChatSend(t, turnZeroStub(true), "chat", "transcript", "9", "--turn", "0", "--raw")
	if code != 1 || !strings.Contains(stderr, "--raw") {
		t.Errorf("--raw: exit %d stderr %q", code, stderr)
	}

	_, stderr, code = runChatSend(t, turnZeroStub(false), "chat", "transcript", "9", "--turn", "0")
	if code != 1 || !strings.Contains(stderr, "no pending turn 0") || !strings.Contains(stderr, "--turn 1") {
		t.Errorf("consumed: exit %d stderr %q", code, stderr)
	}
}

// TestChatTranscriptOpensWithTurnZero: a seeded turn's transcript prints the
// context its session was given, whole, under the turn-0 heading, before the
// turn's own records.
func TestChatTranscriptOpensWithTurnZero(t *testing.T) {
	stdout, stderr, code := runChatSend(t, turnZeroStub(false), "chat", "transcript", "9", "--turn", "1")
	if code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	want := turnZeroHeading + "\n<earlier-turns>\nthe summary\n</earlier-turns>\n" + turnZeroEnd + "\nhello"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout:\n%s\nwant it to hold:\n%s", stdout, want)
	}
}
