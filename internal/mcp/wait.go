package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// Wake states: the wait returns when its target reaches one of these — a
// terminal state, or one where the task is asking a human for something
// (task 057 decision 5). They are exactly the states from which nothing more
// happens without an outside action, which is what makes the wait bounded by
// the run rather than by its timeout.
var wakeStates = map[store.TaskState]bool{
	taskstate.Done:          true,
	taskstate.Aborted:       true,
	taskstate.Archived:      true,
	taskstate.AwaitingInput: true,
	taskstate.Blocked:       true,
	taskstate.AwaitingGate:  true,
}

// Wait timeouts. The ceiling is hard: a tool call cannot hang forever, and a
// client that asks for longer gets the ceiling rather than an error — the
// call still returns a usable answer, just sooner than it hoped.
const (
	DefaultWait = 5 * time.Minute
	MaxWait     = 30 * time.Minute
)

// errSelfBlocking is the typed refusal a step gets when the task it wants to
// wait for cannot start while the caller holds its own §11 slot (decision 5).
//
// Refusal rather than release: a step parked in a wait keeps its slot, because
// its agent process is live. Releasing it would create a §6 state that owns a
// live agent process *and* holds no slot — a quadrant no state occupies today,
// and one that would leave live-but-uncounted agent CLIs accumulating past the
// very caps §11 exists to enforce.
var errSelfBlocking = errors.New(
	"waiting here would deadlock: this step holds a concurrency slot the task you are waiting for needs")

// waitArgs is the wait tool's payload.
type waitArgs struct {
	TaskID         int64 `json:"task_id"`
	TimeoutSeconds int   `json:"timeout_seconds,omitempty"`
}

// waitResult is what the caller gets back. It is a complete answer on its own:
// a client that dropped every progress notification learns the same thing from
// this object as one that rendered them all (decision 5).
type waitResult struct {
	TaskID int64  `json:"task_id"`
	State  string `json:"state"`
	// Woke reports whether the task reached a wake state, as against the call
	// hitting its timeout. A client must branch on this and not on State: a
	// timed-out wait reports the state the task was last seen in, which is a
	// true fact about a task that is still running.
	Woke        bool   `json:"woke"`
	TimedOut    bool   `json:"timed_out"`
	BlockReason string `json:"block_reason,omitempty"`
	// WaitedSeconds is how long the call actually blocked.
	WaitedSeconds int `json:"waited_seconds"`
}

func waitSchema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{ //nolint:errchkjson // plain values
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{
				"type":        "integer",
				"description": "the task to wait for",
			},
			"timeout_seconds": map[string]any{
				"type": "integer",
				"description": fmt.Sprintf(
					"how long to block, in seconds (default %d, capped at %d)",
					int(DefaultWait.Seconds()), int(MaxWait.Seconds())),
			},
		},
		"required": []string{"task_id"},
	})
	return b
}

const waitDescription = "Block until a task reaches a terminal or human-blocking state " +
	"(done, aborted, archived, awaiting_input, blocked, awaiting_gate), or until the timeout " +
	"elapses. Returns the task's state either way; check `woke` to tell the two apart. " +
	"Step transitions arrive as progress notifications while the call is open, but the result " +
	"is complete without them."

// handleWait implements the wait tool.
//
// The order here is load-bearing. The subscription opens *before* the current
// state is read, so a transition landing between the two is seen on the
// channel rather than missed: the broker's fan-out is post-commit, so an event
// that arrives after the read describes a state at least as new as the one
// read.
func (s *Server) handleWait(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	var args waitArgs
	raw := req.Params.Arguments
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("invalid_arguments", "arguments are not a JSON object: "+err.Error(), nil), nil
	}
	if args.TaskID <= 0 {
		return toolError("invalid_arguments", "task_id is required and must be a positive integer", nil), nil
	}
	if s.deps.Broker == nil || s.deps.Store == nil {
		return toolError("unavailable", "this daemon serves no event stream", nil), nil
	}

	timeout := DefaultWait
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	timeout = min(timeout, MaxWait)

	if caller, ok := stepFrom(ctx); ok {
		blocked, err := s.wouldDeadlock(ctx, caller.TaskID, args.TaskID)
		if err != nil {
			return toolError("internal", err.Error(), nil), nil
		}
		if blocked {
			return toolError("would_deadlock", errSelfBlocking.Error(),
				json.RawMessage(fmt.Sprintf(`{"caller_task_id":%d,"target_task_id":%d}`,
					caller.TaskID, args.TaskID))), nil
		}
	}

	sub := s.deps.Broker.SubscribeEvents(64)
	defer sub.Close()

	task, err := s.deps.Store.GetTask(ctx, args.TaskID)
	if err != nil {
		return toolError("not_found", err.Error(), nil), nil
	}
	started := s.now()
	if wakeStates[task.State] {
		return waitReply(task, true, false, 0)
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	last := task
	for {
		select {
		case <-ctx.Done():
			return waitReply(last, false, false, s.now().Sub(started))
		case <-deadline.C:
			// Re-read rather than trusting the last event seen: the
			// subscription may have been dropped for falling behind, and a
			// timed-out wait must still report the task's real state.
			if t, gerr := s.deps.Store.GetTask(ctx, args.TaskID); gerr == nil {
				last = t
			}
			return waitReply(last, wakeStates[last.State], true, s.now().Sub(started))
		case ev, open := <-sub.C:
			if !open {
				// Dropped for falling behind (§13.3). Fall back to a read:
				// the durable state is the answer, the stream was only the
				// notification.
				t, gerr := s.deps.Store.GetTask(ctx, args.TaskID)
				if gerr != nil {
					return toolError("internal", gerr.Error(), nil), nil
				}
				return waitReply(t, wakeStates[t.State], false, s.now().Sub(started))
			}
			if ev.TaskID == nil || *ev.TaskID != args.TaskID {
				continue
			}
			s.notifyProgress(ctx, req, ev)
			if ev.Type != store.EventTaskStateChanged {
				continue
			}
			t, gerr := s.deps.Store.GetTask(ctx, args.TaskID)
			if gerr != nil {
				return toolError("internal", gerr.Error(), nil), nil
			}
			last = t
			if wakeStates[t.State] {
				return waitReply(t, true, false, s.now().Sub(started))
			}
		}
	}
}

func waitReply(t *store.Task, woke, timedOut bool, waited time.Duration) (*sdk.CallToolResult, error) {
	res := waitResult{
		TaskID:        t.ID,
		State:         string(t.State),
		Woke:          woke,
		TimedOut:      timedOut,
		BlockReason:   t.BlockReason,
		WaitedSeconds: int(waited.Round(time.Second).Seconds()),
	}
	b, err := json.Marshal(res)
	if err != nil {
		return toolError("internal", "wait result is not encodable", nil), nil
	}
	return &sdk.CallToolResult{
		Content:           []sdk.Content{&sdk.TextContent{Text: string(b)}},
		StructuredContent: res,
	}, nil
}

// notifyProgress relays one of the target's events as an MCP progress
// notification. Failures are swallowed on purpose: progress is an enhancement
// to the wait, never the means of delivering its result, so a client that
// cannot take one must not turn the wait into an error.
func (s *Server) notifyProgress(ctx context.Context, req *sdk.CallToolRequest, ev *store.Event) {
	token := req.Params.GetProgressToken()
	if token == nil || req.Session == nil {
		return
	}
	_ = req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{
		ProgressToken: token,
		Message:       ev.Type,
	})
}

// wouldDeadlock reports whether waiting would park the caller behind its own
// slot: the target is not running yet, and admission is saturated by tasks the
// caller is one of (decision 5).
//
// It is deliberately a *conservative* check on the global cap and the target's
// project cap, not a simulation of the scheduler. The scheduler is the only
// place admission happens (its own invariant), and reimplementing its walk
// here would be a second answer to "may this run" that drifts from the first.
// What this needs to be is right about the one case that hangs forever.
func (s *Server) wouldDeadlock(ctx context.Context, callerID, targetID int64) (bool, error) {
	if callerID == 0 || callerID == targetID {
		return false, nil
	}
	target, err := s.deps.Store.GetTask(ctx, targetID)
	if err != nil {
		return false, fmt.Errorf("read target task: %w", err)
	}
	if target.State != store.TaskQueued {
		// Already running, already settled, or parked on a human: the wait
		// resolves without needing a slot the caller is holding.
		return false, nil
	}
	cfg := s.deps.Config()
	held, err := s.deps.Store.CountSlotHolders(ctx)
	if err != nil {
		return false, fmt.Errorf("count slot holders: %w", err)
	}
	if cfg.MaxParallelTasks > 0 && held >= cfg.MaxParallelTasks {
		return true, nil
	}
	proj, err := s.deps.Store.GetProject(ctx, target.ProjectID)
	if err != nil {
		return false, fmt.Errorf("read target project: %w", err)
	}
	if proj.MaxParallelTasks != nil && *proj.MaxParallelTasks > 0 {
		n, cerr := s.deps.Store.CountSlotHoldersByProject(ctx, target.ProjectID)
		if cerr != nil {
			return false, fmt.Errorf("count slot holders for project: %w", cerr)
		}
		if n >= *proj.MaxParallelTasks {
			return true, nil
		}
	}
	return false, nil
}
