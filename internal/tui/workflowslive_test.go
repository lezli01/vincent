package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

const liveWorkflowYAML = `name: publish
description: Build then publish.
steps:
  - id: build
    type: command
    run: make build
`

// brokenLiveYAML fails strict decode the same way a typo would: no steps.
const brokenLiveYAML = `name: publish
description: Build then publish.
`

// registryEventHook wires registry reloads to the store's durable
// workflow.registry_changed event the way daemon.Run does (§13.3) — and,
// unlike the daemon, whose store outlives its registry, retires the hook
// before the store closes.
//
// The watcher reloads on its own goroutine behind a 100 ms debounce, so a
// write made at the end of a test is still in flight when cleanups begin: the
// reload lands after the store's cleanup has closed the database and the
// append fails with "sql: database is closed". The daemon only logs that at
// warn level; a test calls t.Errorf and goes red. Windows is where it
// actually fires, because removing a t.TempDir there is slow enough — and
// noisy enough on the watch — to keep the goroutine busy well into teardown.
//
// The retiring cleanup is registered here rather than beside the store's so
// that it runs first: cleanups are LIFO, and this helper is always called
// after the store's own t.Cleanup. Taking the same lock a fire holds is what
// makes it a barrier and not a second race — once it returns, no hook call is
// in AppendEvent and none ever will be again.
func registryEventHook(t *testing.T, st *store.Store, registry *workflow.Registry) {
	t.Helper()
	var mu sync.Mutex
	live := true
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		live = false
	})
	registry.OnChange(func() {
		mu.Lock()
		defer mu.Unlock()
		if !live {
			return
		}
		if err := st.AppendEvent(context.Background(),
			&store.Event{Type: store.EventWorkflowRegistryChanged}); err != nil {
			t.Errorf("AppendEvent: %v", err)
		}
	})
}

// TestWorkflowsViewReflectsFileEdit is the T3.6 done-when: a workflow file
// edited on disk updates the view without a restart.
//
// It is the one end-to-end this PR needs because it is the only way to
// exercise the chain the PR depends on and does not own: fsnotify sees the
// write, the registry reloads, the daemon writes workflow.registry_changed,
// the SSE stream carries it, the root broadcasts it and the view refetches.
// Every link is real here — real watcher, real registry, real handlers, real
// client, real shell — and no unit test can stand in for any of them.
func TestWorkflowsViewReflectsFileEdit(t *testing.T) {
	const token = "workflows-live-token"
	// The watcher's context is separate from the shell's, and the shell's is
	// created after the server's cleanup is registered. Cleanups run LIFO, so
	// the stream has to be canceled before httptest closes — otherwise Close
	// blocks on a connection that is still, correctly, subscribed.
	watchCtx := testCtx(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "app", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(context.Background(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	globalDir := filepath.Join(t.TempDir(), "workflows")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	agents := agent.NewRegistry()
	registry := workflow.NewRegistry(globalDir, workflow.Options{
		KnownAgents: agents.Names(),
	}, nil)
	registry.Reload()
	// Registered after the initial load, exactly as the daemon wires it, so
	// boot churn stays out of the event log.
	registryEventHook(t, st, registry)
	if err := registry.Watch(watchCtx); err != nil {
		t.Fatalf("Watch: %v", err)
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
		Git:         gitx.New(),
		Agents:      agents,
		Workflows:   registry,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	m := newRoot(testCtx(t), liveConnector(t, ts.URL, token), ackedDir(t))
	msg := runCmd(t, m.Init(), 10*time.Second)
	if _, ok := msg.(connectedMsg); !ok {
		t.Fatalf("probe = %T, want connectedMsg", msg)
	}
	_, cmd := m.Update(msg)
	p := newPump(t, m, cmd)
	// The subscription has to be live before the file is written: without a
	// Last-Event-ID the stream starts at the *next* event (§13.3), so writing
	// first would race the reload event into oblivion.
	p.until(10*time.Second, "the event stream to go live", func() bool { return m.streamLive })

	_, cmd = m.Update(selectViewMsg{id: viewWorkflows})
	p.push(cmd)
	p.until(10*time.Second, "the workflows view to load", func() bool {
		return strings.Contains(content(m), "global")
	})

	// The edit: a file appearing on disk, which is what `e` and an external
	// editor both amount to.
	path := filepath.Join(globalDir, "publish.yaml")
	writeFile(t, path, liveWorkflowYAML)
	p.until(15*time.Second, "the new workflow to reach the view", func() bool {
		return strings.Contains(content(m), "publish")
	})
	if strings.Contains(content(m), "invalid") {
		t.Fatalf("a valid workflow rendered as invalid: %q", content(m))
	}

	// Breaking the same file has to reach the view the same way, carrying the
	// registry's own finding rather than a verdict the client invented.
	writeFile(t, path, brokenLiveYAML)
	p.until(15*time.Second, "the broken workflow to reach the view", func() bool {
		return strings.Contains(content(m), "invalid")
	})
	if !strings.Contains(content(m), "publish") {
		t.Errorf("the broken entry was hidden instead of flagged: %q", content(m))
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// liveConnector points the shell at a real test server; only the on-disk
// discovery (§12.2) is faked.
func liveConnector(t *testing.T, baseURL, token string) connector {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("test server port: %v", err)
	}
	return connector{
		resolveDataDir: func() (string, error) { return t.TempDir(), nil },
		readRuntime: func(string) (daemon.RuntimeInfo, error) {
			return daemon.RuntimeInfo{PID: 1, Port: port, StartedAt: time.Now()}, nil
		},
		checkHealth: daemon.CheckHealth,
		newClient: func(string) (*apiclient.Client, error) {
			return apiclient.New(baseURL, token), nil
		},
		startDetached: func() (int, error) { t.Fatal("auto-start must not trigger"); return 0, nil },
		startTimeout:  time.Second,
		pollInterval:  time.Millisecond,
	}
}
