package chatrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/transcript"
	"github.com/lezli01/vincent/internal/worktree"
)

// Turn failure reasons. They are the shared snake_case vocabulary
// internal/taskrun and internal/worktree own — a `session_lost` means the same
// thing wherever it originated — with one addition that only a chat can
// produce.
const (
	// ReasonSessionLost is a turn whose stored session id the CLI no longer
	// knows (decision 4). The chat stays usable; whether to start a fresh
	// conversation is a human decision, because an agent answering with none
	// of the thread in context reads exactly like one that has it.
	ReasonSessionLost = "session_lost"
	// ReasonAgentError is the generic "the run failed" reason, spelled the
	// same as the engine's.
	ReasonAgentError = "agent_error"
	// ReasonAgentUnavailable is an adapter whose CLI is not installed.
	ReasonAgentUnavailable = "agent_unavailable"
	// ReasonCanceled is a turn a human stopped.
	ReasonCanceled = "canceled"
	// ReasonTranscriptIOError is a turn whose transcript could not be
	// written. As on the task path, a run that cannot record what happened
	// does not get to claim success.
	ReasonTranscriptIOError = "transcript_io_error"
)

// ErrChatCapReached is a send refused because `max_parallel_chats` chats
// already hold a live agent process (§11, decision 1). It is a refusal, never
// a queue: a chat turn is a foreground reply, and parking it behind other
// people's conversations would be exactly the wait chats exist to avoid.
var ErrChatCapReached = errors.New("too many chats are running")

// ErrNoLiveTurn is an answer or a cancel for a chat with nothing running.
var ErrNoLiveTurn = errors.New("this chat has no live turn")

// Deps is what a Runner needs. It is the taskrun Deps minus everything only a
// workflow has — no Shells, no Catalog, no MCP: a chat runs one agent, and
// §13.4 keeps chat routes off the tool surface anyway (decision 2).
type Deps struct {
	Store     *store.Store
	Config    func() config.Config
	Worktrees *worktree.Manager
	Agents    *agent.Registry
	DataDir   string
	Logger    *slog.Logger
	// Events receives live output chunks for the per-chat SSE stream
	// (§13.3). Nil is tolerated; the transcript stays the durable copy.
	Events *events.Broker
	// Now is the clock, nil meaning time.Now. Injected by tests.
	Now func() time.Time
}

// Runner owns every live chat turn.
type Runner struct {
	deps   Deps
	cancel context.CancelFunc
	wg     sync.WaitGroup
	base   context.Context //nolint:containedctx // the runner's own lifetime
	// persist is detached from shutdown, so a turn's final state still
	// reaches the database after the run context is canceled.
	persist context.Context //nolint:containedctx // deliberately outlives the run

	mu   sync.Mutex
	live map[int64]*liveTurn
}

// liveTurn is one running turn: the handle to stop it and the id of the row
// it is writing.
type liveTurn struct {
	turnID int64
	handle agent.RunHandle
	cancel context.CancelFunc
}

// New returns a runner over deps.
func New(deps Deps) *Runner {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Runner{deps: deps, live: map[int64]*liveTurn{}}
}

// Start makes the runner able to accept sends. It starts no goroutine of its
// own: unlike the task runner there is no admission loop to run, because a
// turn begins when a human sends it and never before.
func (r *Runner) Start(ctx context.Context) {
	r.base, r.cancel = context.WithCancel(ctx)
	r.persist = context.WithoutCancel(ctx)
}

// Stop cancels every live turn and waits for their goroutines. A turn stopped
// this way is `interrupted`, not `failed`: the daemon went away, the agent did
// not misbehave (§12.4, decision 5).
func (r *Runner) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
}

// Send starts a turn on chat id with the human's message. It refuses rather
// than queues when the cap is reached, and it is the only producer of
// `idle → running` (§5.5).
func (r *Runner) Send(ctx context.Context, chatID int64, prompt string) (*store.ChatTurn, error) {
	chat, err := r.deps.Store.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if !chatstate.Allowed(chat.State, chatstate.Send) {
		return nil, store.ErrInvalidChatAction
	}
	// The cap is checked before the row is written, and it counts chats
	// rather than turns because a chat holds exactly one process at a time.
	// It is racy only against another send in the same instant; the store is
	// a single writer, so the window is one statement wide and the worst
	// outcome is one extra process — the same tolerance §11's own tally has.
	n, err := r.deps.Store.CountChatsHoldingProcess(ctx)
	if err != nil {
		return nil, err
	}
	if limit := r.maxParallelChats(); n >= limit {
		return nil, fmt.Errorf("%w: %d of %d chats are already running", ErrChatCapReached, n, limit)
	}
	turn, err := r.deps.Store.CreateChatTurn(ctx, chatID, prompt)
	if err != nil {
		return nil, err
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.runTurn(chat, turn)
	}()
	return turn, nil
}

func (r *Runner) maxParallelChats() int {
	if r.deps.Config == nil {
		return 1
	}
	if n := r.deps.Config().MaxParallelChats; n > 0 {
		return n
	}
	return 1
}

// Answer answers the chat's pending §7.4 request through the adapter's own
// Respond — the same flow, the same normalization and the same adapter-side
// queueing the task path uses (decision 8).
func (r *Runner) Answer(ctx context.Context, chatID int64, resp agent.InputResponse) error {
	r.mu.Lock()
	live := r.live[chatID]
	r.mu.Unlock()
	if live == nil {
		return ErrNoLiveTurn
	}
	if err := live.handle.Respond(resp); err != nil {
		return fmt.Errorf("answer chat %d: %w", chatID, err)
	}
	if _, err := r.deps.Store.SetChatState(ctx, chatID, chatstate.Running); err != nil {
		return err
	}
	return nil
}

// Cancel stops a chat's live turn and kills its process tree.
func (r *Runner) Cancel(_ context.Context, chatID int64) error {
	r.mu.Lock()
	live := r.live[chatID]
	r.mu.Unlock()
	if live == nil {
		return ErrNoLiveTurn
	}
	live.cancel()
	return nil
}

// Running reports whether this runner owns a live turn for the chat. The API
// uses it to tell "no live turn here" from "no live turn anywhere".
func (r *Runner) Running(chatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live[chatID] != nil
}

// runTurn is the actor: one goroutine, sole writer of this chat's state and
// this turn's row, living for exactly one turn.
func (r *Runner) runTurn(chat *store.Chat, turn *store.ChatTurn) {
	ctx, cancel := context.WithCancel(r.base)
	defer cancel()
	adapter, ok := r.deps.Agents.Get(chat.Agent)
	if !ok {
		r.finish(turn, chatstate.TurnFailed, ReasonAgentUnavailable,
			fmt.Sprintf("no adapter named %q", chat.Agent), nil)
		return
	}
	tr, err := transcript.Open(r.transcriptDir(chat.ID), fmt.Sprintf("%d.jsonl", turn.Seq))
	if err != nil {
		r.finish(turn, chatstate.TurnFailed, ReasonTranscriptIOError, err.Error(), nil)
		return
	}
	defer tr.Close()

	spec := agent.RunSpec{
		Prompt:          turn.Prompt,
		WorkDir:         chat.WorktreePath,
		Model:           chat.Model,
		Effort:          chat.Effort,
		PermissionMode:  agent.PermissionMode(chat.PermissionMode),
		OnInput:         agent.InputWait,
		ResumeSessionID: chat.SessionID,
	}
	handle, err := adapter.Start(ctx, spec)
	if err != nil {
		reason := ReasonAgentError
		if errors.Is(err, agent.ErrResumeUnsupported) {
			reason = ReasonSessionLost
		}
		r.finish(turn, chatstate.TurnFailed, reason, err.Error(), nil)
		return
	}
	r.track(chat.ID, &liveTurn{turnID: turn.ID, handle: handle, cancel: cancel})
	defer r.untrack(chat.ID)
	r.journal(turn, handle)

	r.consume(ctx, chat, turn, handle, tr)

	res, waitErr := handle.Wait()
	turn.ExitCode = &res.ExitCode
	turn.InputTokens, turn.OutputTokens, turn.CostUSD = res.InputTokens, res.OutputTokens, res.CostUSD
	turn.ResultText = res.ResultText
	if res.SessionID != "" {
		turn.SessionID = res.SessionID
	}
	switch {
	case ctx.Err() != nil && r.base.Err() != nil:
		// The daemon is going away under a live turn. It is interrupted, not
		// failed, and it is never re-run (decision 5).
		r.finish(turn, chatstate.TurnInterruptedState, "", "", chat)
	case ctx.Err() != nil:
		r.finish(turn, chatstate.TurnFailed, ReasonCanceled, "canceled", chat)
	case waitErr != nil:
		r.finish(turn, chatstate.TurnFailed, ReasonAgentError, waitErr.Error(), chat)
	case res.Failure != nil && res.Failure.Kind == agent.FailureSessionLost:
		// The stored session is gone. The chat stays usable and keeps its
		// id: clearing it here would silently convert the next turn into a
		// fresh conversation, which is the behaviour decision 4 rejects.
		r.finish(turn, chatstate.TurnFailed, ReasonSessionLost, res.ErrorMessage, chat)
	case res.IsError:
		r.finish(turn, chatstate.TurnFailed, ReasonAgentError, res.ErrorMessage, chat)
	default:
		r.finish(turn, chatstate.TurnDone, "", "", chat)
	}
}

// consume drains the run's normalized events into the transcript and the live
// output stream, and applies the §7.4 states as they arrive.
func (r *Runner) consume(
	ctx context.Context, chat *store.Chat, turn *store.ChatTurn,
	handle agent.RunHandle, tr *transcript.Writer,
) {
	for ev := range handle.Events() {
		if len(ev.Raw) > 0 {
			at := tr.Raw(ev.Raw)
			if r.deps.Events != nil {
				r.deps.Events.PublishOutput(chatOutputKey(chat.ID), events.Chunk{
					Type: "output",
					Payload: map[string]any{
						"chat_id": chat.ID, "turn_id": turn.ID,
						"raw": string(ev.Raw), "offset": at,
					},
				})
			}
		}
		switch ev.Type {
		case agent.EventInputRequest:
			req, err := json.Marshal(ev.Request)
			if err != nil {
				continue
			}
			if _, err := r.deps.Store.SetChatPendingInput(ctx, chat.ID, req); err != nil {
				r.deps.Logger.Warn("store chat pending input", "chat", chat.ID, "error", err)
				continue
			}
			if _, err := r.deps.Store.SetChatState(ctx, chat.ID, chatstate.AwaitingInput); err != nil {
				r.deps.Logger.Warn("chat awaiting input", "chat", chat.ID, "error", err)
			}
		case agent.EventInputCanceled:
			if _, err := r.deps.Store.SetChatState(ctx, chat.ID, chatstate.Running); err != nil {
				r.deps.Logger.Warn("chat input closed", "chat", chat.ID, "error", err)
			}
		case agent.EventUsage, agent.EventResult, agent.EventOutput, agent.EventToolUse,
			agent.EventToolResult, agent.EventThinking, agent.EventRunHeader,
			agent.EventError, agent.EventUnknown:
			// Rendering is the client's job: every one of these is already
			// in the transcript and on the stream, and internal/tui's
			// outputlines.go decides what a verbosity level shows.
		}
	}
}

// journal records the process identity beside the pid, so §12.4 recovery can
// prove a surviving pid is still the process this row started before killing
// it — the same PID-reuse guard `step_runs` carries (migration 0013).
func (r *Runner) journal(turn *store.ChatTurn, handle agent.RunHandle) {
	pid := handle.PID()
	if pid <= 0 {
		return
	}
	turn.PID = &pid
	if id, err := procx.Identity(pid); err == nil && id != "" {
		turn.ProcIdentity = &id
	}
	if err := r.deps.Store.UpdateChatTurn(r.persist, turn); err != nil {
		r.deps.Logger.Warn("journal chat turn", "turn", turn.ID, "error", err)
	}
}

// finish writes the turn's ending and returns the chat to idle, in that
// order: a reader must never see an idle chat with a turn still running.
func (r *Runner) finish(
	turn *store.ChatTurn, state chatstate.TurnState, reason, msg string, chat *store.Chat,
) {
	ctx := r.persist
	if ctx == nil {
		ctx = context.Background()
	}
	now := r.deps.Now()
	turn.State, turn.FailReason, turn.ErrorMessage = state, reason, msg
	turn.EndedAt = &now
	ms := now.Sub(turn.StartedAt).Milliseconds()
	turn.DurationMS = &ms
	turn.PID = nil
	if err := r.deps.Store.UpdateChatTurn(ctx, turn); err != nil {
		r.deps.Logger.Error("finish chat turn", "turn", turn.ID, "error", err)
	}
	if chat == nil {
		return
	}
	if turn.SessionID != "" && turn.SessionID != chat.SessionID {
		if _, err := r.deps.Store.SetChatSession(ctx, chat.ID, turn.SessionID); err != nil {
			r.deps.Logger.Error("store chat session", "chat", chat.ID, "error", err)
		}
	}
	if _, err := r.deps.Store.SetChatState(ctx, chat.ID, chatstate.Idle); err != nil {
		r.deps.Logger.Error("chat back to idle", "chat", chat.ID, "error", err)
	}
}

func (r *Runner) track(chatID int64, lt *liveTurn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live[chatID] = lt
}

func (r *Runner) untrack(chatID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, chatID)
}

func (r *Runner) transcriptDir(chatID int64) string {
	return filepath.Join(r.deps.DataDir, "transcripts", worktree.ChatOwner(chatID).Dir())
}

// chatOutputKey is the broker key a chat's live output rides on. The broker is
// keyed by int64 because tasks are, so chats take the negative half of the
// space: a chat can never collide with a task, and a task subscriber can never
// be handed a chat's bytes.
func chatOutputKey(chatID int64) int64 { return -chatID }

// ChatOutputKey exposes the mapping to the API, which subscribes on behalf of
// an SSE client.
func ChatOutputKey(chatID int64) int64 { return chatOutputKey(chatID) }
