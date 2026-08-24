package apiclient_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/worktree"
)

const testToken = "test-token"

// noteTimeout bounds one expected stream delivery; loopback is fast, CI is
// not always.
const noteTimeout = 10 * time.Second

// harness is a real store wired to a real broker behind the real handlers —
// the client is exercised against the same server the daemon runs (Phase 3
// decision: client-owned wire types, drift caught here).
type harness struct {
	ts        *httptest.Server
	st        *store.Store
	broker    *events.Broker
	projectID int64
	taskID    int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	p := &store.Project{Name: "client", Path: "/nowhere", DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task := &store.Task{
		ProjectID: p.ID, Title: "t", WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "b", State: store.TaskQueued,
	}
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// The runner is never started: the action endpoints under test drive a
	// task that has no live actor, which is the ordinary case for a queued or
	// parked one.
	git := gitx.New()
	dataDir := t.TempDir()
	agents := agent.NewRegistry(claude.New(func() string { return "" }))
	runner := taskrun.New(taskrun.Deps{
		Store:     st,
		Config:    config.Default,
		Worktrees: worktree.NewManager(git, dataDir),
		Agents:    agents,
		DataDir:   dataDir,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
		Git:         git,
		Runner:      runner,
		WakeRunner:  func() {},
		// The registry is what lets the transcript endpoint normalize a
		// recorded run with the same parser that read it live.
		Agents: agents,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &harness{ts: ts, st: st, broker: broker, projectID: p.ID, taskID: task.ID}
}

// append writes one durable event; the store hook publishes it to the broker.
func (h *harness) append(t *testing.T, evType string) *store.Event {
	t.Helper()
	e := &store.Event{Type: evType, TaskID: &h.taskID, ProjectID: &h.projectID}
	if err := h.st.AppendEvent(context.Background(), e); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	return e
}

// setState moves a task directly, for the states an endpoint is meant to be
// driven from.
func (h *harness) setState(t *testing.T, id int64, to store.TaskState) {
	t.Helper()
	task, err := h.st.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if _, _, err := h.st.TransitionTask(t.Context(), id, task.State, to, store.TaskChange{}); err != nil {
		t.Fatalf("set state %s: %v", to, err)
	}
}

// block puts the harness task into `blocked` with a reason — the one state a
// §6 repair may be asked for from (task 025).
func (h *harness) block(t *testing.T, reason string) {
	t.Helper()
	h.setState(t, h.taskID, store.TaskRunning)
	if _, _, err := h.st.TransitionTask(t.Context(), h.taskID,
		store.TaskRunning, store.TaskBlocked, store.TaskChange{BlockReason: &reason}); err != nil {
		t.Fatalf("block: %v", err)
	}
}

// snapshotWorkflow is a three-step snapshot covering every step type that
// carries editable text, plus the manual gate that carries none.
const snapshotWorkflow = `name: three
steps:
  - id: implement
    type: agent
    prompt: write the thing
  - id: review
    type: manual
    instructions: look at the diff
  - id: publish
    type: command
    run: git push
`

// snapshotTask creates a task whose snapshot actually parses, which the
// default harness task's placeholder deliberately does not.
func (h *harness) snapshotTask(t *testing.T) int64 {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: "snapshot", WorkflowName: "three",
		WorkflowSnapshot: snapshotWorkflow,
		BaseBranch:       "main", BranchName: "vincent/2-snapshot", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task.ID
}

// taskInWorktree creates a running task pointed at a real repository, so the
// diff endpoint has something to diff.
func (h *harness) taskInWorktree(t *testing.T, repo string) int64 {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: "diffable", WorkflowName: "adhoc",
		WorkflowSnapshot: snapshotWorkflow, BaseBranch: "main",
		BranchName: "vincent/3-diffable", WorktreePath: repo, State: store.TaskRunning,
	}
	if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task.ID
}

func (h *harness) client() *apiclient.Client {
	return apiclient.New(h.ts.URL, testToken)
}

// testStreamOptions keeps reconnect pacing fast enough for tests.
func testStreamOptions() apiclient.StreamOptions {
	return apiclient.StreamOptions{
		InitialBackoff: 20 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	}
}

func nextNote(t *testing.T, ch <-chan apiclient.Note) apiclient.Note {
	t.Helper()
	select {
	case n, ok := <-ch:
		if !ok {
			t.Fatal("note channel closed unexpectedly")
		}
		return n
	case <-time.After(noteTimeout):
		t.Fatal("timed out waiting for a stream note")
		return nil
	}
}

func TestHealth(t *testing.T) {
	h := newHarness(t)
	got, err := h.client().Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("Health.Status = %q, want %q", got.Status, "ok")
	}
}

func TestStreamEventsBadTokenSurfacesEnvelope(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := apiclient.New(h.ts.URL, "wrong").StreamEvents(ctx, testStreamOptions())
	n := nextNote(t, ch)
	d, ok := n.(apiclient.DisconnectedNote)
	if !ok {
		t.Fatalf("first note = %T, want DisconnectedNote", n)
	}
	var apiErr *apiclient.Error
	if !errors.As(d.Err, &apiErr) {
		t.Fatalf("DisconnectedNote.Err = %v, want *apiclient.Error", d.Err)
	}
	if apiErr.Code != "unauthorized" || apiErr.Status != http.StatusUnauthorized {
		t.Errorf("error = code %q status %d, want unauthorized/401", apiErr.Code, apiErr.Status)
	}
}

// TestStreamEventsLiveResumeAcrossReconnect is the §13.3 client contract:
// live delivery, a dropped connection surfaces as a note, and the automatic
// reconnect resumes from the last seen event id so nothing durable is lost.
func TestStreamEventsLiveResumeAcrossReconnect(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := h.client().StreamEvents(ctx, testStreamOptions())
	if _, ok := nextNote(t, ch).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note is not ConnectedNote")
	}

	ev1 := h.append(t, "task.state_changed")
	n := nextNote(t, ch)
	en, ok := n.(apiclient.EventNote)
	if !ok {
		t.Fatalf("note = %T, want EventNote", n)
	}
	if en.Event.ID != ev1.ID || en.Event.Type != "task.state_changed" {
		t.Errorf("event = #%d %q, want #%d task.state_changed", en.Event.ID, en.Event.Type, ev1.ID)
	}
	if en.Event.TaskID == nil || *en.Event.TaskID != h.taskID {
		t.Errorf("event task id = %v, want %d", en.Event.TaskID, h.taskID)
	}

	// Drop every established connection; the subscription must notice and
	// reconnect on its own, resuming after ev1.
	h.ts.CloseClientConnections()
	ev2 := h.append(t, "step.started")

	sawDisconnect := false
	for {
		switch n := nextNote(t, ch).(type) {
		case apiclient.DisconnectedNote:
			sawDisconnect = true
		case apiclient.ConnectedNote:
			if n.Cursor != ev1.ID {
				t.Errorf("reconnect cursor = %d, want %d", n.Cursor, ev1.ID)
			}
		case apiclient.EventNote:
			if n.Event.ID != ev2.ID || n.Event.Type != "step.started" {
				t.Fatalf("resumed event = #%d %q, want #%d step.started", n.Event.ID, n.Event.Type, ev2.ID)
			}
			if !sawDisconnect {
				t.Error("stream delivered ev2 without reporting the disconnect")
			}
			return
		}
	}
}

func TestStreamEventsTypeFilter(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := testStreamOptions()
	opts.Types = []string{"gate.waiting"}
	ch := h.client().StreamEvents(ctx, opts)
	if _, ok := nextNote(t, ch).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note is not ConnectedNote")
	}

	h.append(t, "task.state_changed") // filtered out server-side
	want := h.append(t, "gate.waiting")
	n := nextNote(t, ch)
	en, ok := n.(apiclient.EventNote)
	if !ok {
		t.Fatalf("note = %T, want EventNote", n)
	}
	// Events deliver in id order: receiving the later, matching event first
	// proves the earlier one was filtered.
	if en.Event.ID != want.ID || en.Event.Type != "gate.waiting" {
		t.Errorf("event = #%d %q, want #%d gate.waiting", en.Event.ID, en.Event.Type, want.ID)
	}
}

// TestStreamEventsSpecFraming exercises SSE details the real server doesn't
// produce today but the spec allows: comment heartbeats and data split
// across multiple data: lines.
func TestStreamEventsSpecFraming(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, ":heartbeat\n\n")
		_, _ = io.WriteString(w, "id: 5\nevent: spec.frame\n")
		_, _ = io.WriteString(w, "data: {\"id\":5,\"ts\":\"2026-08-08T00:00:00Z\",\n")
		_, _ = io.WriteString(w, "data: \"type\":\"spec.frame\",\"payload\":null}\n\n")
		fl.Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := apiclient.New(ts.URL, testToken).StreamEvents(ctx, testStreamOptions())

	if _, ok := nextNote(t, ch).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note is not ConnectedNote")
	}
	n := nextNote(t, ch)
	en, ok := n.(apiclient.EventNote)
	if !ok {
		t.Fatalf("note = %T, want EventNote", n)
	}
	if en.Event.ID != 5 || en.Event.Type != "spec.frame" {
		t.Errorf("event = #%d %q, want #5 spec.frame", en.Event.ID, en.Event.Type)
	}
}

func TestDiscover(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := apiclient.Discover(dataDir); err == nil {
		t.Error("Discover on an empty data dir succeeded; want error")
	}

	ri := daemon.RuntimeInfo{Port: 4242, PID: 1, StartedAt: time.Now()}
	if err := daemon.WriteRuntimeInfo(dataDir, ri); err != nil {
		t.Fatalf("WriteRuntimeInfo: %v", err)
	}
	if _, err := daemon.EnsureToken(dataDir); err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	c, err := apiclient.Discover(dataDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if want := "http://127.0.0.1:4242"; c.BaseURL() != want {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL(), want)
	}
}
