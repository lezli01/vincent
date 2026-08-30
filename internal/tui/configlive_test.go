package tui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

// The config editor against the real handlers over httptest (the *live_test.go
// convention, task 060). An in-process form talking to a fake would prove the
// keystrokes and nothing about the wire; what is worth proving here is that a
// keypress ends as an edited config.yaml and a re-rendered block.

// configLiveHarness is a daemon view wired to a real API server whose config
// dir holds the shipped template.
type configLiveHarness struct {
	view *daemonView
	path string
}

func newConfigLive(t *testing.T) *configLiveHarness {
	t.Helper()
	const token = "config-live-token"
	dir := t.TempDir()
	if _, err := config.EnsureDefaultFile(dir); err != nil {
		t.Fatalf("seed config.yaml: %v", err)
	}
	current := config.Default()
	srv := api.New(api.Deps{
		Token:       token,
		Config:      func() config.Config { return current },
		StartedAt:   time.Now().Add(-time.Minute),
		ListenAddr:  "127.0.0.1:0",
		Dirs:        config.Dirs{Config: dir},
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ApplyConfig: func(next config.Config) { current = next },
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	client := apiclient.New(ts.URL, token)

	d := newDaemonView()
	d.setDataDir(t.TempDir())
	d.width, d.height = 120, 40
	cfg, err := client.Config(t.Context())
	if err != nil {
		t.Fatalf("GET /v1/config: %v", err)
	}
	d.client = client
	d.connected = true
	d.update(daemonConfigMsg{config: cfg})
	return &configLiveHarness{view: d, path: filepath.Join(dir, config.FileName)}
}

// press feeds a key and drains whatever command it produced, so an editor
// that answers over HTTP is fully settled when press returns.
func (h *configLiveHarness) press(t *testing.T, key string) {
	t.Helper()
	_, cmd := h.view.update(namedKey(key))
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return
		}
		_, cmd = h.view.update(msg)
	}
}

func (h *configLiveHarness) open(t *testing.T, path string) {
	t.Helper()
	for i, k := range h.view.keys {
		if k.path == path {
			h.view.cursor = i
			h.view.focusConfig = true
			h.press(t, "enter")
			return
		}
	}
	t.Fatalf("no config key %q", path)
}

func (h *configLiveHarness) file(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	return string(b)
}

// The whole loop: select a key, edit it, apply, and the daemon has it.
func TestConfigEditorWritesThroughTheRealAPI(t *testing.T) {
	h := newConfigLive(t)
	h.open(t, "log_level")
	if h.view.form == nil {
		t.Fatal("enter did not open the editor")
	}
	if !h.view.capturesInput() {
		t.Error("the view does not capture input while the editor is open; " +
			"every single-key global would fire into it")
	}
	// log_level is a chooser: walk it to "warn".
	for h.view.form.value() != "warn" {
		h.press(t, "right")
	}
	h.press(t, "enter")
	if h.view.form != nil {
		t.Fatalf("the editor stayed open after a successful save: %+v", h.view.form)
	}
	if h.view.config.LogLevel != "warn" {
		t.Errorf("the block did not adopt the new configuration: %q", h.view.config.LogLevel)
	}
	if !strings.Contains(h.file(t), "log_level: warn") {
		t.Errorf("config.yaml was not written:\n%s", h.file(t))
	}
	// The file is still the documented template it was.
	if !strings.Contains(h.file(t), "# Daemon log verbosity: debug | info | warn | error.") {
		t.Error("the key's documentation was flattened by the edit")
	}
	if !h.view.capturesInput() {
		return // the editor closed, which is what the assertion above wanted
	}
}

// A refusal renders against the field and keeps the editor open, which is the
// only way the value that caused it is still on screen to fix.
func TestConfigEditorRendersAValidationErrorAgainstTheField(t *testing.T) {
	h := newConfigLive(t)
	h.open(t, "defaults.agent_timeout")
	h.view.form.input.SetValue("soon")
	h.press(t, "enter")
	if h.view.form == nil {
		t.Fatal("a rejected value closed the editor")
	}
	if h.view.form.err == "" {
		t.Error("the daemon's refusal is not rendered against the field")
	}
	out := strings.Join(h.view.form.render(120), "\n")
	if !strings.Contains(out, "soon") {
		t.Errorf("the value that was refused is no longer on screen:\n%s", out)
	}
	if strings.Contains(h.file(t), "soon") {
		t.Error("a refused value reached config.yaml")
	}
}

// The four keys that decide what the daemon executes or exposes. Agents run
// full-auto by default (§16); a stray keystroke must not change the argv the
// daemon spawns as you, or the address it binds.
func TestConfigEditorRefusesDangerousKeysWithoutConfirmation(t *testing.T) {
	for _, tc := range []struct{ path, value, wantInFile string }{
		{"notify.command", "/bin/echo hi", "/bin/echo"},
		{"environment.set", "TZ=Etc/UTC", "Etc/UTC"},
		{"agents.claude.path", "/opt/claude", "/opt/claude"},
		{"listen", "127.0.0.1:9999", "127.0.0.1:9999"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			h := newConfigLive(t)
			h.open(t, tc.path)
			h.view.form.input.SetValue(tc.value)
			h.press(t, "enter")
			if h.view.form == nil || !h.view.form.confirming {
				t.Fatalf("%s applied without a confirmation step", tc.path)
			}
			if strings.Contains(h.file(t), tc.wantInFile) {
				t.Errorf("%s was written before it was confirmed", tc.path)
			}
			// esc goes back to the field, not out of the editor: someone who
			// reads the confirmation and changes their mind about the value
			// should not have to reopen it.
			h.press(t, "esc")
			if h.view.form == nil || h.view.form.confirming {
				t.Fatalf("esc on the confirmation did not return to the field")
			}
			h.press(t, "enter")
			h.press(t, "y")
			if !strings.Contains(h.file(t), tc.wantInFile) {
				t.Errorf("%s did not apply after confirmation:\n%s", tc.path, h.file(t))
			}
		})
	}
}

// listen is written and does not take effect. The modal has to say so before
// it is applied, and the block must not present the written value as the one
// in force.
func TestConfigEditorSaysListenNeedsARestart(t *testing.T) {
	h := newConfigLive(t)
	h.open(t, "listen")
	out := strings.Join(h.view.form.render(120), "\n")
	if !strings.Contains(out, "restart") {
		t.Errorf("the listen editor does not say it needs a restart:\n%s", out)
	}
}
