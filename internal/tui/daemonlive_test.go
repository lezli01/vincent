package tui

import (
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// TestDaemonViewReflectsLiveDaemon is the T3.7 done-when: the view reflects
// live daemon info.
//
// It is the end-to-end this PR needs because it is the only assertion that
// makes two endpoints, a path the client derives for itself, and the render
// all agree about one running process. The log especially: the daemon writes
// it through its own rotating logger, and the view reads it back off the
// filesystem — a seam with no HTTP in it, which nothing else in the suite
// crosses.
func TestDaemonViewReflectsLiveDaemon(t *testing.T) {
	const token = "daemon-live-token"

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	// The daemon's own logging shape (daemon.newLogger): a slog TextHandler
	// over a size-rotated {data_dir}/logs/daemon.log. Every request the
	// shell makes below lands in this file, which is what the pane reads.
	lj := &lumberjack.Logger{
		Filename: daemon.LogPath(dataDir), MaxSize: 10, MaxBackups: 3,
	}
	t.Cleanup(func() { _ = lj.Close() })
	logger := slog.New(slog.NewTextHandler(lj, nil))

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	// One adapter that resolves and one that does not, so the availability
	// column is exercised in both directions against a real probe.
	fake := agenttest.BuildFakeAgent(t)
	missing := filepath.Join(dataDir, "no-codex-here")
	reg := agent.NewRegistry(
		claude.New(func() string { return fake }),
		codex.New(func() string { return missing }),
	)
	startedAt := time.Now().Add(-90 * time.Minute)
	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   startedAt,
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      logger,
		Store:       st,
		Broker:      broker,
		Git:         gitx.New(),
		Agents:      reg,
		Catalog:     agent.NewCatalogCache(reg),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	if err := acknowledgeNotice(dataDir); err != nil {
		t.Fatalf("acknowledgeNotice: %v", err)
	}
	m := newRoot(testCtx(t), liveConnectorIn(t, ts.URL, token, dataDir), dataDir)
	m.width, m.height = 140, 60

	msg := runCmd(t, m.Init(), 10*time.Second)
	if _, ok := msg.(connectedMsg); !ok {
		t.Fatalf("probe = %T, want connectedMsg", msg)
	}
	_, cmd := m.Update(msg)
	p := newPump(t, m, cmd)

	_, cmd = m.Update(selectViewMsg{id: viewDaemon})
	p.push(cmd)
	p.until(10*time.Second, "the daemon view to load", func() bool {
		out := content(m)
		return strings.Contains(out, "config in effect") &&
			strings.Contains(out, "adapters") &&
			!strings.Contains(out, "loading…")
	})

	// What the endpoints actually say, fetched independently of the view.
	client := apiclient.New(ts.URL, token)
	info, err := client.Info(t.Context())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	cfg, err := client.Config(t.Context())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}

	out := content(m)
	for _, want := range []string{
		info.Version,
		strconv.Itoa(info.PID),
		info.Listen,
		strconv.Itoa(cfg.MaxParallelTasks),
		cfg.LogLevel,
		cfg.Defaults.AgentTimeout,
		dataDir,
	} {
		if want == "" {
			t.Fatalf("the daemon reported an empty field; info=%+v cfg=%+v", info, cfg)
		}
		if !strings.Contains(out, want) {
			t.Errorf("the view does not show %q:\n%s", want, out)
		}
	}
	// Uptime is ticked from started_at, so a daemon up for 90 minutes must
	// not render as the seconds since the view opened.
	if !strings.Contains(out, "1h 30m") {
		t.Errorf("uptime is not derived from the daemon's started_at:\n%s", out)
	}
	// Availability comes off a real probe in both directions.
	if !strings.Contains(out, "claude") || !strings.Contains(out, fake) {
		t.Errorf("the resolved adapter is not shown with its path:\n%s", out)
	}
	if !strings.Contains(out, "codex") {
		t.Errorf("the unavailable adapter is missing:\n%s", out)
	}

	// The log pane: lines this very server wrote, read back off disk with no
	// endpoint in between.
	p.until(15*time.Second, "the daemon's own log lines to reach the pane", func() bool {
		return strings.Contains(content(m), "http request")
	})
}

// liveConnectorIn is liveConnector pinned to a data dir the test owns, so
// the view's log path and the daemon's are the same file.
func liveConnectorIn(t *testing.T, baseURL, token, dataDir string) connector {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("test server port: %v", err)
	}
	cn := liveConnector(t, baseURL, token)
	cn.resolveDataDir = func() (string, error) { return dataDir, nil }
	cn.readRuntime = func(string) (daemon.RuntimeInfo, error) {
		return daemon.RuntimeInfo{PID: 1, Port: port, StartedAt: time.Now()}, nil
	}
	return cn
}
