package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

type waitHarness struct {
	srv    *Server
	store  *store.Store
	broker *events.Broker
	proj   int64
}

func newWaitHarness(t *testing.T, maxParallel int) *waitHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	// Post-commit fan-out, exactly as the daemon wires it (§13.3).
	st.SetEventHook(broker.Publish)

	h := &waitHarness{
		store:  st,
		broker: broker,
		srv: New(Deps{
			Handler: &stubHandler{status: http.StatusOK, body: "{}"},
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Broker:  broker,
			Store:   st,
			Config: func() config.Config {
				c := config.Default()
				c.MaxParallelTasks = maxParallel
				return c
			},
		}),
	}
	p := &store.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	h.proj = p.ID
	return h
}

func (h *waitHarness) task(t *testing.T, title string, state store.TaskState) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: h.proj, Title: title,
		WorkflowName: "test", WorkflowSnapshot: "name: test\nsteps: []\n",
		BaseBranch: "main", BranchName: "vincent/" + title,
		State: state,
	}
	if err := h.store.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func (h *waitHarness) wait(ctx context.Context, t *testing.T, args string) waitResult {
	t.Helper()
	res, err := h.srv.handleWait(ctx, &sdk.CallToolRequest{
		Params: &sdk.CallToolParamsRaw{Name: WaitTool, Arguments: json.RawMessage(args)},
	})
	if err != nil {
		t.Fatalf("handleWait: %v", err)
	}
	if res.IsError {
		t.Fatalf("wait returned an error: %s", text(t, res))
	}
	var out waitResult
	if err := json.Unmarshal([]byte(text(t, res)), &out); err != nil {
		t.Fatalf("wait result is not the documented object: %v (%s)", err, text(t, res))
	}
	return out
}

// TestWaitReturnsOnEveryWakeState covers all six states decision 5 names, and
// does it with no progress notifications delivered at all — the request
// carries no progress token, so every notification is dropped by definition.
// A client that ignores progress gets the same answer.
func TestWaitReturnsOnEveryWakeState(t *testing.T) {
	t.Parallel()
	for _, state := range []store.TaskState{
		store.TaskDone, store.TaskAborted, store.TaskArchived,
		store.TaskAwaitingInput, store.TaskBlocked, store.TaskAwaitingGate,
	} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			h := newWaitHarness(t, 4)
			task := h.task(t, "t-"+string(state), state)
			got := h.wait(t.Context(), t, `{"task_id":`+itoa(task.ID)+`}`)
			if !got.Woke || got.TimedOut {
				t.Errorf("result = %+v, want woke and not timed out", got)
			}
			if got.State != string(state) {
				t.Errorf("state = %q, want %q", got.State, state)
			}
		})
	}
}

// TestWaitHonoursItsCeiling: an unreachable wake state must not hang. A
// running task never wakes on its own, so this is the timeout path, and the
// result still reports the task's real state.
func TestWaitHonoursItsCeiling(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t, 4)
	task := h.task(t, "runner", store.TaskRunning)
	start := time.Now()
	got := h.wait(t.Context(), t, `{"task_id":`+itoa(task.ID)+`,"timeout_seconds":1}`)
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("wait took %s; the ceiling is not bounding it", elapsed)
	}
	if got.Woke || !got.TimedOut {
		t.Errorf("result = %+v, want a timeout that did not wake", got)
	}
	if got.State != string(store.TaskRunning) {
		t.Errorf("state = %q, want the task's real state on a timeout", got.State)
	}
}

// TestWaitCapsTheRequestedTimeout: a client asking for a day gets MaxWait, not
// an error — the call still returns a usable answer, just sooner.
func TestWaitCapsTheRequestedTimeout(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t, 4)
	task := h.task(t, "settled", store.TaskDone)
	got := h.wait(t.Context(), t, `{"task_id":`+itoa(task.ID)+`,"timeout_seconds":86400}`)
	if !got.Woke {
		t.Errorf("result = %+v, want an immediate wake on a settled task", got)
	}
}

// TestWaitWakesOnATransition is the live half: the target moves while the call
// is open, and the wait returns on it.
func TestWaitWakesOnATransition(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t, 4)
	task := h.task(t, "mover", store.TaskRunning)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _, _ = h.store.InterruptTask(context.Background(), task.ID,
			store.TaskRunning, store.TaskBlocked, "test")
	}()
	got := h.wait(t.Context(), t, `{"task_id":`+itoa(task.ID)+`,"timeout_seconds":20}`)
	if !got.Woke || got.State != string(store.TaskBlocked) {
		t.Errorf("result = %+v, want a wake in blocked", got)
	}
}

// TestWaitRefusesASelfBlockingWait is decision 5's refusal, proven the way the
// decision states it: max_parallel_tasks 1, the caller holds the only slot,
// and the target is queued behind it. The call must return an error at once
// rather than park until its ceiling.
func TestWaitRefusesASelfBlockingWait(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t, 1)
	caller := h.task(t, "caller", store.TaskRunning)
	target := h.task(t, "target", store.TaskQueued)

	sess, err := h.srv.OpenStep(1, caller.ID, "step-1")
	if err != nil {
		t.Fatalf("OpenStep: %v", err)
	}
	ctx := withStep(t.Context(), sess)
	res, err := h.srv.handleWait(ctx, &sdk.CallToolRequest{
		Params: &sdk.CallToolParamsRaw{
			Name:      WaitTool,
			Arguments: json.RawMessage(`{"task_id":` + itoa(target.ID) + `,"timeout_seconds":600}`),
		},
	})
	if err != nil {
		t.Fatalf("handleWait: %v", err)
	}
	if !res.IsError {
		t.Fatalf("a self-blocking wait was accepted: %s", text(t, res))
	}
	body := text(t, res)
	for _, want := range []string{"would_deadlock", "deadlock"} {
		if !strings.Contains(body, want) {
			t.Errorf("error = %s, want it to name %q", body, want)
		}
	}
}

// TestWaitAllowsAWaitThatCanBeAdmitted: the same shape with a free slot is not
// a deadlock, and must not be refused — the refusal is about the one case that
// hangs forever, not about waiting from a step.
func TestWaitAllowsAWaitThatCanBeAdmitted(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t, 4)
	caller := h.task(t, "caller", store.TaskRunning)
	target := h.task(t, "target", store.TaskDone)
	sess, err := h.srv.OpenStep(2, caller.ID, "step-1")
	if err != nil {
		t.Fatalf("OpenStep: %v", err)
	}
	got := h.wait(withStep(t.Context(), sess), t, `{"task_id":`+itoa(target.ID)+`}`)
	if !got.Woke {
		t.Errorf("result = %+v, want the settled target reported", got)
	}
}

// TestWaitFromTheSharedEndpointIsNeverRefused: a client on `/mcp` holds no
// slot, so the deadlock the refusal exists for cannot happen to it.
func TestWaitFromTheSharedEndpointIsNeverRefused(t *testing.T) {
	t.Parallel()
	h := newWaitHarness(t, 1)
	h.task(t, "holder", store.TaskRunning)
	target := h.task(t, "target", store.TaskQueued)
	got := h.wait(t.Context(), t, `{"task_id":`+itoa(target.ID)+`,"timeout_seconds":1}`)
	if got.Woke {
		t.Errorf("result = %+v, want a timeout rather than a wake", got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
