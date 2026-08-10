package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// TestLiveChangeFromRealServer is the T3.1 done-when in automated form: the
// shell, connected to the real handlers, re-renders when the daemon commits
// a state change the TUI didn't make.
func TestLiveChangeFromRealServer(t *testing.T) {
	const token = "live-token"
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	p := &store.Project{Name: "live", Path: "/nowhere", DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task := &store.Task{
		ProjectID: p.ID, Title: "t", WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "b", State: store.TaskQueued,
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("test server port: %v", err)
	}

	// Real health check and real client against the real server; only the
	// on-disk discovery is faked.
	cn := connector{
		resolveDataDir: func() (string, error) { return t.TempDir(), nil },
		readRuntime: func(string) (daemon.RuntimeInfo, error) {
			return daemon.RuntimeInfo{PID: 1, Port: port, StartedAt: time.Now()}, nil
		},
		checkHealth: daemon.CheckHealth,
		newClient: func(string) (*apiclient.Client, error) {
			return apiclient.New(ts.URL, token), nil
		},
		startDetached: func() (int, error) { t.Fatal("auto-start must not trigger"); return 0, nil },
		startTimeout:  time.Second,
		pollInterval:  time.Millisecond,
	}

	m := newRoot(testCtx(t), cn, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	msg := runCmd(t, m.Init(), 10*time.Second)
	if _, ok := msg.(connectedMsg); !ok {
		t.Fatalf("probe = %T, want connectedMsg", msg)
	}
	_, cmd := m.Update(msg)

	// The stream has to be established before the change is made: a
	// subscription without Last-Event-ID starts live at the *next* committed
	// event (§13.3), so appending first would race the event into oblivion.
	p2 := newPump(t, m, cmd)
	p2.until(10*time.Second, "the event stream to go live", func() bool { return m.streamLive })

	// The board rendered the task from its initial load; prove the *event*
	// path by changing state behind the TUI's back and watching the row
	// re-render — the refresh is event-driven, never polled (§13.3).
	p2.until(15*time.Second, "the initial board load", func() bool {
		return strings.Contains(content(m), "queued")
	})
	if _, _, err := st.TransitionTask(ctx, task.ID,
		store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}

	p2.until(15*time.Second, "the external state change to render", func() bool {
		return strings.Contains(content(m), "running")
	})
}
