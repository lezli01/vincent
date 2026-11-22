package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// compareStub answers POST /v1/chats/7/compare, recording the body, with a
// claude side that runs restricted and a codex side that could not.
func compareStub(t *testing.T, status int, errBody string) (http.HandlerFunc, func() map[string]any) {
	t.Helper()
	var (
		mu   sync.Mutex
		body map[string]any
	)
	return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/health":
				_, _ = io.WriteString(w, `{"status":"ok"}`)
			case "/v1/chats/7/compare":
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				_ = json.Unmarshal(b, &body)
				mu.Unlock()
				if status != 0 {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, errBody)
					return
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"compare":{"id":3,"source_chat_id":7,"seq":2,"prompt":"which way?\nmore",
					"left":{"chat_id":12,"agent":"claude","permission_mode":"restricted"},
					"right":{"chat_id":13,"agent":"codex","permission_mode":"full-auto",
					 "mode_note":"agent \"codex\" cannot keep a resumed chat turn restricted"},
					"kept_chat_id":null,"created_at":"2026-11-22T10:00:00Z"},
					"left":{"chat":{"id":12,"agent":"claude"},"turn":{"id":1,"chat_id":12,"seq":1},"seeded":false,"skipped":[]},
					"right":{"chat":{"id":13,"agent":"codex"},"turn":{"id":2,"chat_id":13,"seq":1},"seeded":true,"skipped":[]}}`)
			default:
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no such route"}}`)
			}
		}, func() map[string]any {
			mu.Lock()
			defer mu.Unlock()
			return body
		}
}

// TestChatCompare (task 171.5): the command sends the turn, the two agents
// and the prompt, and prints each side with the mode it really runs under.
func TestChatCompare(t *testing.T) {
	h, sent := compareStub(t, 0, "")
	stdout, stderr, code := runChatSend(t, h, "chat", "compare", "7", "--turn", "2", "--agents", "claude,codex")
	if code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	body := sent()
	if body["turn"] != 2.0 || body["prompt"] != nil || len(body["agents"].([]any)) != 2 {
		t.Errorf("body = %v; want the turn and agents, no prompt", body)
	}
	for _, want := range []string{
		"compare 3: chat 7, from before turn 2",
		"prompt: which way? …",
		"left   chat 12  claude  restricted\n",
		"right  chat 13  codex  full-auto  (seeded, not resumed)",
		"full-auto because: agent \"codex\" cannot keep",
		"follow: vincent chat show 12; vincent chat show 13",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}

	h, sent = compareStub(t, 0, "")
	if _, stderr, code := runChatSend(t, h, "chat", "compare", "7", "--turn", "2", "--agents", "claude,codex",
		"--prompt", "why?", "--permission-mode", "full-auto"); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if body := sent(); body["prompt"] != "why?" || body["permission_mode"] != "full-auto" {
		t.Errorf("body = %v", body)
	}

	if _, stderr, code := runChatSend(t, h, "chat", "compare", "7", "--turn", "2", "--agents", "claude"); code == 0 ||
		!strings.Contains(stderr, "exactly two") {
		t.Errorf("one agent: exit %d stderr %q", code, stderr)
	}

	h, _ = compareStub(t, http.StatusConflict,
		`{"error":{"code":"chat_cap_reached","message":"too many chats are running: 1 of 1 chats are already running"}}`)
	if _, stderr, code := runChatSend(t, h, "chat", "compare", "7", "--turn", "2", "--agents", "claude,codex"); code != 1 ||
		!strings.Contains(stderr, "max_parallel_chats") || !strings.Contains(stderr, "nothing was created") {
		t.Errorf("cap: exit %d stderr %q", code, stderr)
	}
}

// keepStub is chat 12, the left side of compare 3: GET, its compare, and a
// keep that archives chat 13 and deletes its branch.
func keepStub(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	const cmp = `{"id":3,"source_chat_id":7,"seq":2,"prompt":"which?",
		"left":{"chat_id":12,"agent":"claude","permission_mode":"restricted"},
		"right":{"chat_id":13,"agent":"codex","permission_mode":"full-auto","mode_note":"codex cannot"},
		"kept_chat_id":null,"created_at":"2026-11-22T10:00:00Z"}`
	switch {
	case r.URL.Path == "/v1/health":
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	case r.URL.Path == "/v1/chats/12" && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `{"chat":{"id":12,"title":"t","state":"idle","agent":"claude",
			"forked_from":{"chat_id":7,"seq":2,"agent":"claude"}},"turns":[]}`)
	case r.URL.Path == "/v1/chats/12/compare" && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `{"compare":`+cmp+`}`)
	case r.URL.Path == "/v1/chats/12/compare/keep" && r.Method == http.MethodPost:
		_, _ = io.WriteString(w, `{"compare":`+strings.Replace(cmp, `"kept_chat_id":null`, `"kept_chat_id":12`, 1)+`,
			"kept":{"id":12,"agent":"claude"},
			"archived":{"id":13,"agent":"codex","state":"archived","branch":"vincent/13-t"},"branch_deleted":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no such route"}}`)
	}
}

// TestChatKeepAndShow (task 171.6): `chat keep` says what was kept and what
// was archived with its branch; `chat show` on a side names the pair.
func TestChatKeepAndShow(t *testing.T) {
	stdout, stderr, code := runChatSend(t, keepStub, "chat", "keep", "12")
	if code != 0 || !strings.Contains(stdout, "kept chat 12 (claude) from compare 3") ||
		!strings.Contains(stdout, "archived chat 13 (codex); branch vincent/13-t deleted") {
		t.Fatalf("keep: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	stdout, stderr, code = runChatSend(t, keepStub, "chat", "show", "12")
	want := "compare 3 of chat 7, before turn 2: left chat 12 (this one), claude, restricted; right chat 13, codex, full-auto"
	if code != 0 || !strings.Contains(stdout, want) {
		t.Fatalf("show: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}
