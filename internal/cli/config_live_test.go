package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
)

// `vincent config get/set tui.keys` against the real PATCH /v1/config over
// httptest (task 118): the refusal the command prints is the daemon's, and a
// refused keymap leaves config.yaml byte-identical.
func TestConfigSetTUIKeysAgainstTheRealHandler(t *testing.T) {
	dataDir := t.TempDir()
	token, err := daemon.EnsureToken(dataDir)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	configDir := t.TempDir()
	if _, err := config.EnsureDefaultFile(configDir); err != nil {
		t.Fatalf("seed config.yaml: %v", err)
	}
	path := filepath.Join(configDir, config.FileName)
	var cur atomic.Pointer[config.Config]
	initial := config.Default()
	cur.Store(&initial)
	s := api.New(api.Deps{
		Token:       token,
		Config:      func() config.Config { return *cur.Load() },
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		Dirs:        config.Dirs{Config: configDir},
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ApplyConfig: func(next config.Config) { cur.Store(&next) },
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	publishDaemon(t, dataDir, ts.URL)

	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv(config.EnvDataDir, dataDir)
	run := func(args ...string) (string, int) {
		t.Helper()
		var buf bytes.Buffer
		root := newRootCmd()
		root.SilenceErrors = true
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(append([]string{"config"}, args...))
		code := asExitCode(root.ExecuteContext(context.Background()))
		return buf.String(), code
	}
	read := func() []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		return b
	}

	if out, code := run("get", "tui.keys"); code != 0 || strings.TrimSpace(out) != "" {
		t.Errorf("get tui.keys by default: code %d, out %q; want the empty shipped keymap", code, out)
	}
	if out, code := run("set", "tui.keys", "refresh=ctrl+e help=f2"); code != 0 ||
		!strings.Contains(out, "tui.keys = help=f2 refresh=ctrl+e") {
		t.Fatalf("set tui.keys: code %d, out %q", code, out)
	}
	if out, code := run("get", "tui.keys"); code != 0 || strings.TrimSpace(out) != "help=f2 refresh=ctrl+e" {
		t.Errorf("get tui.keys after set: code %d, out %q", code, out)
	}
	if got := cur.Load().TUI.Keys; got["refresh"] != "ctrl+e" || got["help"] != "f2" {
		t.Errorf("the keymap in force is %v", got)
	}

	before := read()
	out, code := run("set", "tui.keys", "refresh=q")
	if code != 1 || !strings.Contains(out, "tui.keys: refresh:") || !strings.Contains(out, "quit") {
		t.Errorf("set a refused keymap: code %d, out %q; want 1 naming tui.keys, refresh and quit", code, out)
	}
	if !bytes.Equal(before, read()) {
		t.Error("a refused keymap changed config.yaml")
	}
	if got := cur.Load().TUI.Keys; got["refresh"] != "ctrl+e" {
		t.Errorf("a refused keymap replaced the one in force: %v", got)
	}

	if out, code := run("set", "tui.keys", ""); code != 0 || len(cur.Load().TUI.Keys) != 0 {
		t.Errorf("clearing tui.keys: code %d, out %q, in force %v", code, out, cur.Load().TUI.Keys)
	}
}
