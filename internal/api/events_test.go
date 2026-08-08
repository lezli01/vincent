package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// sseHarness is a server with a real store wired to a real broker — no
// runner; events are appended directly and reach subscribers through the
// same post-commit hook the daemon uses.
type sseHarness struct {
	ts        *httptest.Server
	handler   http.Handler
	st        *store.Store
	broker    *events.Broker
	projectID int64
	taskID    int64
}

func newSSEHarness(t *testing.T) *sseHarness {
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
	p := &store.Project{Name: "sse", Path: "/nowhere", DefaultBranch: "main"}
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

	s := New(Deps{
		Token:       testToken,
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
	return &sseHarness{
		ts: ts, handler: s.Handler(), st: st, broker: broker,
		projectID: p.ID, taskID: task.ID,
	}
}

// append writes one durable event for the harness task; the hook publishes it.
func (h *sseHarness) append(t *testing.T, evType string) *store.Event {
	t.Helper()
	return h.appendFor(t, evType, h.taskID, h.projectID)
}

func (h *sseHarness) appendFor(t *testing.T, evType string, taskID, projectID int64) *store.Event {
	t.Helper()
	e := &store.Event{Type: evType, TaskID: &taskID, ProjectID: &projectID}
	if err := h.st.AppendEvent(context.Background(), e); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	return e
}

type sseFrame struct {
	id    string
	event string
	data  string
}

// sseClient reads one SSE stream on a pump goroutine so tests can apply
// deadlines to blocking reads.
type sseClient struct {
	resp   *http.Response
	frames chan sseFrame
}

// streamClient has no timeout: SSE responses never end on their own.
var streamClient = &http.Client{}

func openSSE(t *testing.T, url, lastEventID string) *sseClient {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("stream status = %d, body %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	c := &sseClient{resp: resp, frames: make(chan sseFrame, 256)}
	t.Cleanup(c.close)
	go c.pump()
	return c
}

func (c *sseClient) close() { _ = c.resp.Body.Close() }

func (c *sseClient) pump() {
	defer close(c.frames)
	scanner := bufio.NewScanner(c.resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var f sseFrame
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if f != (sseFrame{}) {
				c.frames <- f
			}
			f = sseFrame{}
		case strings.HasPrefix(line, ":"): // heartbeat comment
		case strings.HasPrefix(line, "id: "):
			f.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// next returns the next frame or fails the test.
func (c *sseClient) next(t *testing.T) sseFrame {
	t.Helper()
	select {
	case f, ok := <-c.frames:
		if !ok {
			t.Fatal("SSE stream ended unexpectedly")
		}
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("no SSE frame within 10s")
	}
	return sseFrame{}
}

// expectNone asserts no frame arrives within d.
func (c *sseClient) expectNone(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case f, ok := <-c.frames:
		if ok {
			t.Fatalf("unexpected frame %+v", f)
		}
		t.Fatal("SSE stream ended unexpectedly")
	case <-time.After(d):
	}
}

func TestEventsColdStartIsLiveOnly(t *testing.T) {
	h := newSSEHarness(t)
	h.append(t, "task.state_changed") // history the stream must not replay
	h.append(t, "step.started")

	c := openSSE(t, h.ts.URL+"/v1/events", "")
	// The response is only committed after the subscription is registered,
	// so an event appended now must be the first frame.
	live := h.append(t, "task.state_changed")

	f := c.next(t)
	if f.id != fmt.Sprint(live.ID) || f.event != "task.state_changed" {
		t.Fatalf("first frame = %+v, want live event id %d (no history replay)", f, live.ID)
	}
	var body eventJSON
	if err := json.Unmarshal([]byte(f.data), &body); err != nil {
		t.Fatalf("frame data not JSON: %v", err)
	}
	if body.ID != live.ID || body.Type != "task.state_changed" ||
		body.TaskID == nil || *body.TaskID != h.taskID {
		t.Errorf("frame body = %+v, want event %d for task %d", body, live.ID, h.taskID)
	}
}

// TestEventsResumeMissesNothing is T2.7's done criterion: a client that
// drops and reconnects with Last-Event-ID sees every state event exactly
// once, in order, across the replay-to-live seam.
func TestEventsResumeMissesNothing(t *testing.T) {
	h := newSSEHarness(t)

	c := openSSE(t, h.ts.URL+"/v1/events", "")
	var written []int64
	for range 5 {
		written = append(written, h.append(t, "task.state_changed").ID)
	}
	var lastSeen int64
	for i := range 3 {
		f := c.next(t)
		if f.id != fmt.Sprint(written[i]) {
			t.Fatalf("frame %d id = %s, want %d", i, f.id, written[i])
		}
		lastSeen = written[i]
	}
	c.close() // the connection drops mid-stream

	for range 3 { // events keep committing while disconnected
		written = append(written, h.append(t, "task.state_changed").ID)
	}

	re := openSSE(t, h.ts.URL+"/v1/events", fmt.Sprint(lastSeen))
	live := h.append(t, "task.state_changed") // lands during/after replay
	written = append(written, live.ID)

	var got []string
	for range written[3:] {
		got = append(got, re.next(t).id)
	}
	var want []string
	for _, id := range written[3:] {
		want = append(want, fmt.Sprint(id))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("resumed ids = %v, want %v (no gaps, no duplicates)", got, want)
	}
	re.expectNone(t, 200*time.Millisecond)
}

func TestEventsFilters(t *testing.T) {
	h := newSSEHarness(t)
	ctx := context.Background()
	other := &store.Project{Name: "other", Path: "/elsewhere", DefaultBranch: "main"}
	if err := h.st.CreateProject(ctx, other); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Backlog: one matching, one wrong type, one wrong project.
	match := h.append(t, "task.state_changed")
	h.append(t, "step.started")
	h.appendFor(t, "task.state_changed", h.taskID, other.ID)

	url := fmt.Sprintf("%s/v1/events?types=task.state_changed&project_id=%d", h.ts.URL, h.projectID)
	c := openSSE(t, url, "1") // cursor before the backlog: exercise replay filtering

	if f := c.next(t); f.id != fmt.Sprint(match.ID) {
		t.Fatalf("replayed frame id = %s, want %d (filters apply to replay)", f.id, match.ID)
	}

	// Live: same three shapes; only the matching one arrives.
	h.append(t, "step.started")
	h.appendFor(t, "task.state_changed", h.taskID, other.ID)
	liveMatch := h.append(t, "task.state_changed")
	if f := c.next(t); f.id != fmt.Sprint(liveMatch.ID) {
		t.Fatalf("live frame id = %s, want %d (filters apply live)", f.id, liveMatch.ID)
	}
	c.expectNone(t, 200*time.Millisecond)
}

func TestTaskStreamResumesDurableEventsForThatTaskOnly(t *testing.T) {
	h := newSSEHarness(t)
	ctx := context.Background()
	task2 := &store.Task{
		ProjectID: h.projectID, Title: "t2", WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "b2", State: store.TaskQueued,
	}
	if err := h.st.CreateTask(ctx, task2); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	mine := h.append(t, "task.state_changed")
	h.appendFor(t, "task.state_changed", task2.ID, h.projectID)
	mine2 := h.append(t, "step.started")

	url := fmt.Sprintf("%s/v1/tasks/%d/events", h.ts.URL, h.taskID)
	c := openSSE(t, url, "2") // after the task.created baseline

	if f := c.next(t); f.id != fmt.Sprint(mine.ID) {
		t.Fatalf("replay frame = %+v, want id %d", f, mine.ID)
	}
	if f := c.next(t); f.id != fmt.Sprint(mine2.ID) {
		t.Fatalf("replay frame = %+v, want id %d (other task's event skipped)", f, mine2.ID)
	}

	// Live output interleaves on the same stream, with no id.
	h.broker.PublishOutput(h.taskID, events.Chunk{
		Type: "command.output", Payload: map[string]any{"text": "hello", "stream": "stdout"},
	})
	f := c.next(t)
	if f.event != "command.output" || f.id != "" {
		t.Fatalf("output frame = %+v, want command.output without id", f)
	}
	if !strings.Contains(f.data, `"text":"hello"`) {
		t.Errorf("output data = %s, want the chunk payload", f.data)
	}

	// Another task's durable events do not leak in.
	h.appendFor(t, "task.state_changed", task2.ID, h.projectID)
	c.expectNone(t, 200*time.Millisecond)
}

// countingWriter is an http.ResponseWriter + Flusher that records flushes,
// for asserting the ~100 ms output coalescing (§13.3).
type countingWriter struct {
	mu      sync.Mutex
	header  http.Header
	buf     bytes.Buffer
	status  int
	flushes int
}

func (w *countingWriter) Header() http.Header {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *countingWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = status
}

func (w *countingWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushes++
}

func (w *countingWriter) snapshot() (int, int, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status, w.flushes, w.buf.String()
}

func TestTaskStreamCoalescesOutput(t *testing.T) {
	h := newSSEHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/tasks/%d/events", h.taskID), nil)
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := &countingWriter{}

	done := make(chan struct{})
	go func() {
		h.handler.ServeHTTP(w, req)
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if status, _, _ := w.snapshot(); status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stream did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}

	const chunks = 50
	for i := range chunks {
		h.broker.PublishOutput(h.taskID, events.Chunk{
			Type: "command.output", Payload: map[string]any{"seq": i},
		})
	}
	state := h.append(t, "task.state_changed")

	// Several coalescing windows elapse; without coalescing there would be
	// one flush per chunk.
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	_, flushes, body := w.snapshot()
	if got := strings.Count(body, "event: command.output"); got != chunks {
		t.Errorf("output frames = %d, want %d", got, chunks)
	}
	if !strings.Contains(body, fmt.Sprintf("id: %d", state.ID)) {
		t.Errorf("state event %d missing from the stream", state.ID)
	}
	// Order is preserved chunk-to-chunk.
	last := -1
	for _, part := range strings.Split(body, `{"seq":`)[1:] {
		var seq int
		if _, err := fmt.Sscanf(part, "%d", &seq); err != nil {
			t.Fatalf("unparsable seq in %q: %v", part, err)
		}
		if seq <= last {
			t.Fatalf("chunk order broken: %d after %d", seq, last)
		}
		last = seq
	}
	if flushes >= chunks {
		t.Errorf("flushes = %d for %d chunks; want coalescing well below one flush per chunk", flushes, chunks)
	}
	if flushes < 2 {
		t.Errorf("flushes = %d, want at least the initial and one output flush", flushes)
	}
}

func TestEventsBadCursorAndUnknownTask(t *testing.T) {
	h := newSSEHarness(t)

	req, _ := http.NewRequest(http.MethodGet, h.ts.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Last-Event-ID", "not-a-number")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad cursor status = %d, want 400", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, h.ts.URL+"/v1/tasks/99999/events", nil)
	req2.Header.Set("Authorization", "Bearer "+testToken)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("unknown task status = %d, want 404", resp2.StatusCode)
	}
}
