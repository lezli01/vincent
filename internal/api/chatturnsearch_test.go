package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/worktree"
)

// sendAndWait runs one chat turn to its end.
func (h *chatHarness) sendAndWait(t *testing.T, id int64, message string, want int) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost,
		fmt.Sprintf("/v1/chats/%d/send", id), map[string]any{"message": message})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send = %d %s", resp.StatusCode, body)
	}
	waitUntil(t, func() bool {
		var read struct {
			Turns []struct {
				State string `json:"state"`
			} `json:"turns"`
		}
		_, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d", id), nil)
		if json.Unmarshal(body, &read) != nil || len(read.Turns) != want {
			return false
		}
		return read.Turns[want-1].State == "done"
	})
}

func (h *chatHarness) search(t *testing.T, id int64, q string) (int, chatSearchDTO, []byte) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet,
		fmt.Sprintf("/v1/chats/%d/search?q=%s", id, url.QueryEscape(q)), nil)
	var out chatSearchDTO
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out, body
}

// TestChatSearchCountsTurnsAndFallsBackToResultText is task 160 decision 2:
// the route names the matching turns with a count each, case-insensitively,
// and a turn whose transcript has gone to retention is still searched
// through its row's result_text.
func TestChatTurnSearchCountsTurnsAndFallsBackToResultText(t *testing.T) {
	h := newChatHarness(t)
	_, created := h.create(t, "claude")
	id := int64(created["id"].(float64))
	h.sendAndWait(t, id, "alpha Needle here", 1)
	h.sendAndWait(t, id, "beta only", 2)

	code, got, body := h.search(t, id, "NEEDLE")
	if code != http.StatusOK {
		t.Fatalf("search = %d %s", code, body)
	}
	// fakeagent's resumed turn may replay the session, so turn 2 can match
	// too; turn 1 is the one that must.
	if len(got.Turns) == 0 || got.Turns[0].Seq != 1 || got.Turns[0].Source != "transcript" {
		t.Fatalf("search = %s, want turn 1 first, from its transcript", body)
	}
	sum := 0
	for _, turn := range got.Turns {
		sum += turn.Matches
	}
	// The prompt and fakeagent's echo of it in the transcript, at least.
	if got.Turns[0].Matches < 2 || got.Total != sum {
		t.Fatalf("search = %s, want the prompt and the transcript counted", body)
	}

	// Retention removes turn 1's transcript: the row still answers.
	path := filepath.Join(h.dataDir, "transcripts", worktree.ChatOwner(id).Dir(), "1.jsonl")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, got, body = h.search(t, id, "needle")
	if len(got.Turns) == 0 || got.Turns[0].Seq != 1 || got.Turns[0].Source != "result_text" {
		t.Fatalf("search after retention = %s, want turn 1 from result_text", body)
	}
	if got.Turns[0].Matches < 2 {
		t.Fatalf("search after retention = %s, want prompt + result_text", body)
	}

	_, got, body = h.search(t, id, "beta")
	if len(got.Turns) != 1 || got.Turns[0].Seq != 2 {
		t.Fatalf("search beta = %s", body)
	}
	_, got, body = h.search(t, id, "absent-everywhere")
	if got.Total != 0 || got.Turns == nil || len(got.Turns) != 0 {
		t.Fatalf("search miss = %s, want an empty list", body)
	}
}

// TestChatSearchRefusals: an empty or oversized query is a snake_case
// validation error, and an unknown chat a 404.
func TestChatTurnSearchRefusals(t *testing.T) {
	h := newChatHarness(t)
	_, created := h.create(t, "claude")
	id := int64(created["id"].(float64))
	for _, q := range []string{"", "   ", strings.Repeat("x", maxChatSearchQuery+1)} {
		code, _, body := h.search(t, id, q)
		if code != http.StatusBadRequest || !strings.Contains(string(body), CodeValidationFailed) {
			t.Fatalf("search %q = %d %s, want 400 validation_failed", q, code, body)
		}
	}
	code, _, body := h.search(t, id+1000, "x")
	if code != http.StatusNotFound || !strings.Contains(string(body), CodeNotFound) {
		t.Fatalf("unknown chat = %d %s, want 404", code, body)
	}
}
