package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// tui.chat is served, written and put into force (task 170). The chat view
// reads both keys from nowhere else, so a key the PATCH dropped would be a
// setting the settings view shows and cannot change.
func TestConfigPatchRoundTripsTUIChat(t *testing.T) {
	h := newConfigHarness(t)
	_, getBody := doRequest(t, h.ts, http.MethodGet, "/v1/config", testToken)
	if !strings.Contains(string(getBody), `"chat":{"status_line":"","vim":false}`) {
		t.Fatalf("GET /v1/config does not serve tui.chat at its defaults: %s", getBody)
	}

	// A template with a quote in it: the value crosses JSON, the line editor
	// and YAML, and has to come back as the bytes that went in.
	const tmpl = `{{.Model}} "{{.State}}" · {{.ContextPercent}}%`
	body, err := json.Marshal(map[string]any{"tui": map[string]any{
		"chat": map[string]any{"status_line": tmpl, "vim": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resp, out := h.patch(t, string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: status = %d, want 200 (body %s)", resp.StatusCode, out)
	}
	var answered configResponse
	if err := json.Unmarshal(out, &answered); err != nil {
		t.Fatalf("parse patch response: %v", err)
	}
	if answered.TUI.Chat.StatusLine != tmpl || !answered.TUI.Chat.Vim {
		t.Errorf("the patch response says %+v", answered.TUI.Chat)
	}
	if got := h.cur.Load().TUI.Chat; got.StatusLine != tmpl || !got.Vim {
		t.Errorf("the applied config says %+v", got)
	}
	if file := string(h.bytes(t)); !strings.Contains(file, "vim: true") || !strings.Contains(file, "status_line: ") {
		t.Errorf("config.yaml does not carry the block:\n%s", file)
	}
	if h.cur.Load().TUI.Board.GroupBy == nil {
		t.Error("patching tui.chat lost tui.board.group_by")
	}

	// One key alone leaves the other, and "" is how the line is turned off.
	resp, out = h.patch(t, `{"tui":{"chat":{"status_line":""}}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clearing patch: status = %d, want 200 (body %s)", resp.StatusCode, out)
	}
	if got := h.cur.Load().TUI.Chat; got.StatusLine != "" || !got.Vim {
		t.Errorf("after clearing the status line the config says %+v, want it empty and vim still on", got)
	}
}

// A template the chat view could not draw is refused with the validation
// envelope, and the file is not touched.
func TestConfigPatchRefusesABadChatStatusLine(t *testing.T) {
	for name, tmpl := range map[string]string{
		"unknown field": "{{.Tokens}}",
		"parse error":   "{{.Model",
		"newline":       "{{.Model}}\n{{.Cost}}",
	} {
		t.Run(name, func(t *testing.T) {
			h := newConfigHarness(t)
			before := h.bytes(t)
			body, err := json.Marshal(map[string]any{"tui": map[string]any{
				"chat": map[string]any{"status_line": tmpl},
			}})
			if err != nil {
				t.Fatal(err)
			}
			resp, out := h.patch(t, string(body))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, out)
			}
			if !strings.Contains(string(out), "tui.chat.status_line") {
				t.Errorf("the refusal does not name the key: %s", out)
			}
			if string(h.bytes(t)) != string(before) {
				t.Error("a refused patch changed config.yaml")
			}
			if h.applied.Load() != 0 {
				t.Error("a refused patch was applied")
			}
		})
	}
}
