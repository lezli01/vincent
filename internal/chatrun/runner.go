package chatrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// knows (task 063 decision 4). The chat stays usable; whether to start a
	// fresh conversation is a human decision, because an agent answering with
	// none of the thread in context reads exactly like one that has it.
	//
	// Only an adapter that recognizes its CLI's refusal can reach this:
	// claude and codex do, cursor cannot, because cursor-agent adopts an
	// unknown resume id and answers rather than refusing (§9.7, task 070
	// decision 2). Nothing here compensates for that — the runner acts on the
	// adapter's verdict and never guesses at one.
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
	// ReasonTimeout is a turn that ran past `defaults.agent_timeout`
	// (§7.2, task 067 decision 1). It is the engine's constant spelled the
	// same, not a chat-shaped variant: a chat turn is bounded by the same
	// number a step is, and a second vocabulary for one clock would make
	// §12.3 carry two names for it.
	ReasonTimeout = "timeout"
	// ReasonInputTimeout is a chat that sat in `awaiting_input` past
	// `defaults.input_timeout` (§7.4, task 067 decision 1). This is the hole
	// §11 named in its own words — a live agent CLI holding a
	// `max_parallel_chats` slot for a human who walked away.
	ReasonInputTimeout = "input_timeout"
	// ReasonTranscriptLimit is a turn that hit `transcript_max_bytes`
	// (§12.3). It is a failure and not a truncation for the reason the task
	// engine gives: past the cap the turn stops being recorded, and a run
	// nobody can read afterwards does not get to claim success.
	ReasonTranscriptLimit = "transcript_limit"
	// ReasonCheckpointFailed is a turn whose worktree checkpoint could not
	// be taken or recorded (task 167.2). The agent never starts: a turn runs
	// protected or not at all, so a later rewind is never missing the state
	// in front of it.
	ReasonCheckpointFailed = "checkpoint_failed"
)

// The cancel causes the turn's own clocks raise. They are internal: the
// reason a client sees is the snake_case constant above.
var (
	errTurnTimeout     = errors.New("turn timeout")
	errInputTimeout    = errors.New("input timeout")
	errTranscriptLimit = errors.New("transcript limit")
	errCanceled        = errors.New("canceled")
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
	// Launchers is where a linked chat's turn runs (task 119 decision 3):
	// the task's container when its workflow runs in one, the host
	// otherwise. internal/taskrun implements it and the daemon wires it, so
	// this package never imports taskrun. Nil — and every free chat — means
	// the host, which keeps "chats run on the host" (§16) true for them.
	//
	// It answers three things about that place, not one (task 124.17): the
	// launcher, an opaque name for where the CLI runs ("" is the host, and
	// the daemon passes the task container's id), and the environment the CLI
	// runs with. The environment is decision 3: a containerized turn carried
	// none until 124.17, so the CLI ran under the image's HOME and never read
	// the agent configuration `container.mount_agent_config` had mounted for
	// it. The place is decision 2: it is what a turn's init line is filed
	// under, so a container turn never classifies the host listing's
	// `builtin` rows.
	Launchers func(ctx context.Context, taskID, turnID int64) (
		l agent.Launcher, place string, env []string, err error)
	// StopOrphan is §12.4's container-aware kill for a linked turn a dead
	// daemon left running (061 decision 9's pid file). It reports whether
	// the task runs in a container at all. Nil means no container kill.
	StopOrphan func(ctx context.Context, taskID, turnID int64) bool
	// SkillPlace is where a linked chat's *skill probe* runs (task 124.17,
	// §5.5, §9.6): the same shape Launchers answers, under a pid-file key of
	// the probe's own, and keyed by chat because a probe has no turn.
	//
	// Unlike InContainer, which it replaces, it asks the container runtime:
	// finding the container needs a lookup, and so does the honest answer
	// that a configured container is gone — which it reports as an error
	// (taskrun.ErrTaskContainerMissing) rather than as a host placement.
	// internal/taskrun implements it and the daemon wires it, so this package
	// never imports taskrun. Nil — and every free chat — means the host.
	SkillPlace func(ctx context.Context, taskID, chatID int64) (
		l agent.Launcher, place string, env []string, err error)
	// StopSkillProbe is §12.4's container-aware kill for a skill probe a dead
	// daemon left exec'd inside a task's container (task 124.17 decision 4).
	// It reports whether there was a container to signal into. Nil means no
	// probe kill.
	StopSkillProbe func(ctx context.Context, taskID, chatID int64) bool
	// InvalidateSkills drops the skill cache's entries for a directory a turn
	// just ran in (task 124.9): a turn is the one event vincent observes that
	// can have written a skill or installed a plugin. Nil means no cache.
	InvalidateSkills func(workDir string)
	// ReportBundledSkills hands the skill cache the names a turn's run header
	// reported (task 124.16): claude's init line names the skills that turn
	// loaded, which is what tells its bundled skills from its built-in
	// commands. It is called with the turn's own adapter, because what is
	// bundled is a property of the installed CLI — and with the place that
	// turn ran, Launchers' own word for it, because what a container's claude
	// bundles is the image's property and never the host binary's (task
	// 124.17 decision 2).
	//
	// Nil means no cache. It is only ever called with names, so a codex or
	// cursor turn — neither CLI reporting any — calls nothing. A closure
	// rather than the cache itself, so this package never imports it.
	ReportBundledSkills func(a agent.Adapter, place string, names []string)
	// PullRequest resolves a `#pr-N` mention in the chat's project (task 166
	// decision 3) through the GitHub client the pull request views use. It
	// answers ErrNoPull, or any error, when there is nothing to resolve
	// against, and the mention stays prose. Nil means no pull request
	// mention ever resolves. A closure, so this package never imports
	// internal/github or internal/api.
	PullRequest func(ctx context.Context, projectID int64, number int) (MentionPullRequest, error)
	// Shell resolves a chat shell command (task 170) to the process that
	// runs it where the chat's turns run: under the §8.3 shell with the
	// §8.5 base environment on the host, inside the task's container for a
	// linked chat on a containerized task. taskID is 0 for a chat that is
	// not linked. internal/taskrun implements it and the daemon wires it,
	// so this package never imports taskrun. Nil fails every run.
	Shell func(ctx context.Context, taskID, runID int64, workDir, command string) (ShellExec, error)
	// StopShellOrphan is §12.4's container-aware kill for a shell command a
	// dead daemon left running in a task's container. Nil means no
	// container kill.
	StopShellOrphan func(ctx context.Context, taskID, runID int64) bool
	// InContainer reports whether a linked chat's turns on taskID run in a
	// container, from the task's settings (task 170): a stored image is not
	// reachable in there, so such a chat is refused images. Nil — and every
	// chat that is not linked — means the host.
	InContainer func(ctx context.Context, taskID int64) (bool, error)
}

// ErrLinkedTaskNoWorktree is a linked-chat turn whose task no longer names a
// worktree. The turn fails; the chat never creates one (task 119).
var ErrLinkedTaskNoWorktree = errors.New("the linked task has no worktree")

// Runner owns every live chat turn.
type Runner struct {
	deps   Deps
	cancel context.CancelFunc
	wg     sync.WaitGroup
	base   context.Context //nolint:containedctx // the runner's own lifetime
	// persist is detached from shutdown, so a turn's final state still
	// reaches the database after the run context is canceled.
	persist context.Context //nolint:containedctx // deliberately outlives the run

	mu sync.Mutex
	// chatRefs caches Refs; the zero value is "not read yet".
	chatRefs ChatRefs
	live     map[int64]*liveTurn
	// turns is every accepted turn from Send until its goroutine returns,
	// including the window before the process starts that live does not
	// cover. StopTurn is what needs it: closing a chat must wait for a turn
	// that has not yet reached track (task 119).
	turns map[int64]*turnCtl
	// shells is each chat's running shell command (task 170), at most one,
	// from RunShell's acceptance until its goroutine has written the ending.
	shells map[int64]*shellCtl
	// shellHolds counts, per chat, the callers between HoldShell and its
	// release: an archive, handoff or delete about to end the chat. RunShell
	// refuses while one is held.
	shellHolds map[int64]int
	// shellTimeout is a test seam, zero in production: it replaces
	// ShellTimeout so a test of the kill does not wait two minutes.
	shellTimeout time.Duration

	// starts serializes, per chat, the two places a turn is accepted
	// (SubmitWith and a queue resume) with WhileNoTurn (task 169 decision 7).
	// One lock per chat, made on first use and dropped with its last holder
	// or waiter, under mu: WhileNoTurn holds its chat's for a whole reject —
	// a snapshot of the worktree, a diff and an apply — and that must never
	// delay another chat's send, as a shared stripe did (review F7).
	starts map[int64]*startGate

	// beforeCheckpoint is a test seam, nil in production: it runs on a
	// chat's actor goroutine as a turn's checkpoint starts, with the turn's
	// context, so a test can hold the turn in that window (review F3).
	beforeCheckpoint func(ctx context.Context, chatID int64)

	// handover is a test seam, nil in production: it runs on a chat's actor
	// goroutine at each queue handover (task 156), after a `done` ending has
	// popped the next turn and before that turn is checked against a stop or
	// a shutdown — the window those two checks exist for.
	handover func(chatID int64)
	// resumeRead is the same kind of seam for ResumeQueue: it runs after a
	// resume has read the chat and before the store lifts the pause.
	resumeRead func(chatID int64)
}

// turnCtl cancels one accepted turn and reports when its goroutine is gone.
//
// cancel is the turn currently running; a queue drain replaces it for each
// turn it pops (task 156). Both fields are guarded by Runner.mu. stopped
// makes a StopTurn that lands during a handover stop the next turn too.
type turnCtl struct {
	cancel  context.CancelCauseFunc
	stopped bool
	done    chan struct{}
}

// liveTurn is one running turn: the handle to stop it and the id of the row
// it is writing.
type liveTurn struct {
	turnID int64
	handle agent.RunHandle
	cancel context.CancelFunc
	// answered wakes the turn's clock loop when a human answers a §7.4
	// request, so the `input_timeout` stops and the `agent_timeout` resumes.
	// Buffered so Answer never blocks on a loop that is between selects, and
	// depth 1 because a coalesced wake is still a wake.
	answered chan struct{}
}

// New returns a runner over deps.
func New(deps Deps) *Runner {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Runner{
		deps: deps, live: map[int64]*liveTurn{}, turns: map[int64]*turnCtl{},
		shells:     map[int64]*shellCtl{},
		shellHolds: map[int64]int{},
	}
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

// Send starts a turn on chat id with the human's message, or queues it
// behind the chat's live turn (task 156). It returns the turn it started,
// nil when the message was queued; Submit says which.
func (r *Runner) Send(ctx context.Context, chatID int64, prompt string) (*store.ChatTurn, error) {
	turn, _, err := r.Submit(ctx, chatID, prompt)
	return turn, err
}

// Submit is a human message to chat id. On an idle chat it starts a turn and
// is the only producer of `idle → running` (§5.5); it refuses rather than
// queues when the cap is reached (task 063 decision 1, which task 156 leaves
// standing). On a live chat — `running` or `awaiting_input` — it appends the
// message to the chat's queue and returns the queued row: it never answers a
// pending §7.4 request (task 156 decision 2).
func (r *Runner) Submit(
	ctx context.Context, chatID int64, prompt string,
) (*store.ChatTurn, *store.ChatQueuedMessage, error) {
	return r.SubmitWith(ctx, chatID, prompt, store.TurnSettings{})
}

// SubmitWith is Submit with per-turn overrides (task 162 decision 6): the
// turn it starts — or the queued message, when it drains — runs with ov's
// non-empty fields in place of the chat's own, and the chat keeps its
// defaults. The caller has validated ov; the runner only carries it.
func (r *Runner) SubmitWith(
	ctx context.Context, chatID int64, prompt string, ov store.TurnSettings,
) (*store.ChatTurn, *store.ChatQueuedMessage, error) {
	// Held from the read that finds the chat idle to launch, so WhileNoTurn
	// never runs between the two (task 169 decision 7).
	defer r.lockStart(chatID)()
	chat, err := r.deps.Store.GetChat(ctx, chatID)
	if err != nil {
		return nil, nil, err
	}
	if !chatstate.AllowedFor(chat.Kind(), chat.State, chatstate.Send) {
		return nil, nil, store.ErrInvalidChatAction
	}
	if chatstate.HoldsProcess(chat.State) {
		m, err := r.deps.Store.EnqueueChatMessageWith(ctx, chatID, prompt, ov)
		if err == nil {
			return nil, m, nil
		}
		if !errors.Is(err, store.ErrInvalidChatAction) {
			return nil, nil, err
		}
		// The live turn ended between the read and the enqueue, and the
		// chat is idle now: this message starts the next turn instead.
		if chat, err = r.deps.Store.GetChat(ctx, chatID); err != nil {
			return nil, nil, err
		}
	}
	if err := r.checkCap(ctx); err != nil {
		return nil, nil, err
	}
	turn, err := r.deps.Store.CreateChatTurnWith(ctx, chatID, prompt, ov)
	if err != nil {
		return nil, nil, err
	}
	r.launch(chat, turn)
	return turn, nil, nil
}

// checkCap refuses a new turn on an idle chat when `max_parallel_chats` chats
// already hold a process. The cap is checked before the row is written, and
// it counts chats rather than turns because a chat holds exactly one process
// at a time. It is racy only against another send in the same instant; the
// store is a single writer, so the window is one statement wide and the worst
// outcome is one extra process — the same tolerance §11's own tally has.
//
// A queue drain never comes here: it inherits the finishing turn's slot
// (task 156 decision 1).
func (r *Runner) checkCap(ctx context.Context) error {
	n, err := r.deps.Store.CountChatsHoldingProcess(ctx)
	if err != nil {
		return err
	}
	if limit := r.maxParallelChats(); n >= limit {
		return fmt.Errorf("%w: %d of %d chats are already running", ErrChatCapReached, n, limit)
	}
	return nil
}

// ResumeQueue lifts a paused queue (task 156 decision 3). On an idle chat
// with messages queued the oldest starts at once, which takes a slot the way
// a send does and is refused the same way when the cap is full; on a live
// chat the queue drains when that turn ends `done`.
//
// start is decided from a read taken before the store's transaction, so a
// live turn can end between the two: its ending saw the pause still set and
// went idle, and this resume, having read a live chat, lifts the pause and
// starts nothing — an idle chat with an unpaused queue nothing will drain
// (review F4). The chat is read again after the commit, and a resume that
// finds it in exactly that state starts the queue as one on an idle chat
// would have.
func (r *Runner) ResumeQueue(ctx context.Context, chatID int64) (*store.ChatTurn, error) {
	turn, started, err := r.resumeQueueOnce(ctx, chatID)
	if err != nil || turn != nil || started {
		return turn, err
	}
	chat, err := r.deps.Store.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if chat.State != chatstate.Idle || chat.QueuePaused || chat.QueuedCount == 0 {
		return nil, nil
	}
	turn, _, err = r.resumeQueueOnce(ctx, chatID)
	return turn, err
}

// resumeQueueOnce is one resume: it reads the chat, checks the cap when it
// means to start a turn, and has the store lift the pause. started reports
// whether it asked the store to start one.
func (r *Runner) resumeQueueOnce(ctx context.Context, chatID int64) (turn *store.ChatTurn, started bool, err error) {
	defer r.lockStart(chatID)()
	chat, err := r.deps.Store.GetChat(ctx, chatID)
	if err != nil {
		return nil, false, err
	}
	if chatstate.Terminal(chat.State) {
		return nil, false, store.ErrInvalidChatAction
	}
	start := chat.State == chatstate.Idle && chat.QueuedCount > 0
	if r.resumeRead != nil {
		r.resumeRead(chatID)
	}
	if start {
		if err := r.checkCap(ctx); err != nil {
			return nil, true, err
		}
	}
	turn, err = r.deps.Store.ResumeChatQueue(ctx, chatID, start)
	if err != nil || turn == nil {
		return nil, start, err
	}
	r.launch(chat, turn)
	return turn, start, nil
}

// launch starts the chat's actor goroutine on turn. The goroutine runs every
// turn its own `done` endings pop from the queue, so a drain never leaves the
// chat's slot (task 156 decision 1).
func (r *Runner) launch(chat *store.Chat, turn *store.ChatTurn) {
	chatID := chat.ID
	ctl := &turnCtl{done: make(chan struct{})}
	r.mu.Lock()
	r.turns[chatID] = ctl
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			if r.turns[chatID] == ctl {
				delete(r.turns, chatID)
			}
			r.mu.Unlock()
			close(ctl.done)
		}()
		for popped := false; turn != nil; popped = true {
			if popped && r.handover != nil {
				r.handover(chatID)
			}
			ctx, cancelCause := context.WithCancelCause(r.base)
			r.mu.Lock()
			ctl.cancel = cancelCause
			stopped := ctl.stopped
			r.mu.Unlock()
			if r.base.Err() != nil {
				// The daemon is going away during a handover: the popped
				// message is a turn now, so it is interrupted and never
				// re-run, and the rest of the queue stays paused (§12.4).
				cancelCause(errCanceled)
				r.finishUnstarted(turn, chatstate.TurnInterruptedState, "", "", chat)
				return
			}
			if stopped {
				// StopTurn ran while the previous turn was handing over:
				// this one never starts, and its ending pauses the rest.
				cancelCause(errCanceled)
				r.finishUnstarted(turn, chatstate.TurnFailed, ReasonCanceled, "canceled", chat)
				return
			}
			next := r.runTurn(ctx, cancelCause, chat, turn)
			cancelCause(errCanceled)
			if next == nil {
				return
			}
			fresh, err := r.deps.Store.GetChat(r.persist, chatID)
			if err != nil {
				r.deps.Logger.Error("reload chat for queued turn", "chat", chatID, "error", err)
				r.finish(next, chatstate.TurnFailed, ReasonAgentError, err.Error(), chat)
				return
			}
			chat, turn = fresh, next
		}
	}()
}

// StopTurn cancels the chat's accepted turn, if it has one, and waits until
// its goroutine has written the turn's ending (task 119). It is what closing a
// linked chat, and cancelling the task it locks, do first.
func (r *Runner) StopTurn(ctx context.Context, chatID int64) {
	// A shell command the human left running goes with the turn (task 170):
	// every caller is about to end the chat or its task.
	r.StopShell(ctx, chatID)
	r.mu.Lock()
	ctl := r.turns[chatID]
	var cancel context.CancelCauseFunc
	if ctl != nil {
		ctl.stopped = true
		cancel = ctl.cancel
	}
	r.mu.Unlock()
	if ctl == nil {
		return
	}
	if cancel != nil {
		cancel(errCanceled)
	}
	select {
	case <-ctl.done:
	case <-ctx.Done():
	}
}

// Close ends a chat linked to a task (task 119) or opened on an issue (task
// 140): a live turn is cancelled first, then the chat moves `idle → closed`.
// Neither worktree nor branch is touched — a linked chat's are the task's,
// and an issue chat's stay for the issue's next main-worktree holder.
func (r *Runner) Close(ctx context.Context, chatID int64) (*store.Chat, error) {
	chat, err := r.deps.Store.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if (!chat.Linked() && !chat.OnIssue()) || chatstate.Terminal(chat.State) {
		return nil, store.ErrInvalidChatAction
	}
	r.StopTurn(ctx, chatID)
	return r.deps.Store.CloseChat(ctx, chatID)
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
	select {
	case live.answered <- struct{}{}:
	default:
	}
	return nil
}

// Cancel stops a chat's live turn and kills its process tree.
//
// A turn that has been accepted but has no process yet — it is being placed,
// or its checkpoint is running, which on a large worktree is the long part of
// its start — is cancelled through its turnCtl, the way StopTurn reaches it
// (review F3): its context ends, and runTurn finishes it `canceled` before
// anything spawns. Marking the control stopped also covers a queue handover,
// where the popped turn has not taken its context yet.
func (r *Runner) Cancel(_ context.Context, chatID int64) error {
	r.mu.Lock()
	live := r.live[chatID]
	var pending context.CancelCauseFunc
	if live == nil {
		if ctl := r.turns[chatID]; ctl != nil {
			ctl.stopped = true
			pending = ctl.cancel
			if pending == nil {
				pending = func(error) {}
			}
		}
	}
	r.mu.Unlock()
	// The chat's stop reaches a running shell command too (task 170), and a
	// chat with only that to stop has still been stopped.
	shell := r.cancelShell(chatID)
	switch {
	case live != nil:
		live.cancel()
	case pending != nil:
		pending(errCanceled)
	case shell != nil:
	default:
		return ErrNoLiveTurn
	}
	return nil
}

// Running reports whether this runner owns a live turn for the chat. The API
// uses it to tell "no live turn here" from "no live turn anywhere".
func (r *Runner) Running(chatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live[chatID] != nil
}

// TurnWritesNoTranscript reports whether a turn that failed for reason ended
// before its transcript file existed. It is a statement about runTurn's
// ordering: the adapter lookup and transcript.Open are the only two failures
// ahead of the writer, and every later ending — Start's agent_error and
// session_lost included — leaves a file behind, even an empty one. A popped
// queue turn a stop or a shutdown ends before runTurn (task 156) gets an
// empty file too, from finishUnstarted.
//
// It is exported for a client holding the turn row, which is how `vincent chat
// transcript` tells "there never was a transcript" from "the file is gone"
// without a second 404 vocabulary on the wire (task 103 decision 4). It lives
// here rather than beside that command because a reordering of runTurn is what
// would make it false; its test pins it to exactly these two reasons.
func TurnWritesNoTranscript(reason string) bool {
	return reason == ReasonAgentUnavailable || reason == ReasonTranscriptIOError
}

// runTurn runs one turn on the chat's actor goroutine, the sole writer of this
// chat's state and this turn's row. It returns the queued message its `done`
// ending popped into the next turn (task 156), which the same goroutine runs
// without releasing the chat's slot, or nil when the chat went idle.
func (r *Runner) runTurn(
	ctx context.Context, cancelCause context.CancelCauseFunc, chat *store.Chat, turn *store.ChatTurn,
) (next *store.ChatTurn) {
	// Cancel is a cause-setting cancel so the ending can tell a human's stop
	// from a clock's (§7.2, §7.4). The deferred one is last and therefore
	// never the cause a live path observed.
	cancel := func() { cancelCause(errCanceled) }
	defer cancelCause(errCanceled)
	adapter, ok := r.deps.Agents.Get(chat.Agent)
	if !ok {
		next = r.finish(turn, chatstate.TurnFailed, ReasonAgentUnavailable,
			fmt.Sprintf("no adapter named %q", chat.Agent), chat)
		return next
	}
	tr, err := transcript.Open(r.transcriptDir(chat.ID), fmt.Sprintf("%d.jsonl", turn.Seq))
	if err != nil {
		next = r.finish(turn, chatstate.TurnFailed, ReasonTranscriptIOError, err.Error(), chat)
		return next
	}
	defer tr.Close()
	// The cap is read per turn, not cached: config hot-reloads (§12.3), and
	// an operator who lowers it after a runaway turn should see the new value
	// on the next send rather than after a daemon restart. Only the task
	// engine used to do this, which left chat transcripts unbounded.
	tr.SetMax(r.cfg().TranscriptMaxBytes.Bytes())

	workDir, launcher, place, env, err := r.turnPlace(ctx, chat, turn)
	if err != nil {
		next = r.finish(turn, chatstate.TurnFailed, ReasonAgentError, err.Error(), chat)
		return next
	}
	skipped, err := r.checkpoint(ctx, chat, turn, workDir)
	if err != nil {
		switch {
		case ctx.Err() != nil && r.base.Err() != nil:
			// The daemon went away while git ran: interrupted, as the
			// ending switch below records a shutdown under a live turn
			// (decision 5), not a stop nobody issued (review F4).
			next = r.finish(turn, chatstate.TurnInterruptedState, "", "", chat)
		case ctx.Err() != nil:
			// A stop that landed while git ran is the human's, not git's.
			next = r.finish(turn, chatstate.TurnFailed, ReasonCanceled, err.Error(), chat)
		default:
			next = r.finish(turn, chatstate.TurnFailed, ReasonCheckpointFailed, err.Error(), chat)
		}
		return next
	}
	if len(skipped) > 0 {
		// Said where the human reads the turn: these files are not in its
		// checkpoint, so a rewind to it cannot bring them back.
		tr.Note("checkpoint_skipped", map[string]any{"paths": skipped})
	}
	env = withChatMarker(env, chat.ID)
	// The turn's mentions are expanded here, when it starts — a queued
	// message too, since this is when it reaches the agent — and the block is
	// recorded before the agent sees it (task 166 decision 2). The prompt
	// column keeps the typed text; this snapshot is what the agent was given.
	if exp := r.ExpandMentions(ctx, chat, turn.Prompt); exp.Context != "" {
		turn.MentionContext = exp.Context
		if err := r.deps.Store.SetChatTurnMentionContext(ctx, turn.ID, exp.Context); err != nil {
			r.deps.Logger.Warn("chat turn mention context not recorded", "chat", chat.ID, "turn", turn.ID, "error", err)
		}
		tr.Note("mentions", map[string]any{
			"resolved": len(exp.Resolved), "bytes": len(exp.Context), "context": exp.Context,
		})
	}
	// The `[image N]` chips are resolved at the same moment and for the same
	// reason (task 170). A send was refused when it could not resolve; a
	// queued message is only checked here, so its turn fails saying why
	// rather than running without the image it names.
	chips, images, err := r.resolveImages(ctx, chat, turn.Prompt)
	if err != nil {
		// Every ending past the checkpoint records head_after, a turn that
		// ran nothing included: the rewind chain check reads a missing one
		// as "cannot prove" and would refuse every code rewind at or before
		// this turn for good (review F3).
		r.recordHeadAfter(turn, workDir)
		next = r.finish(turn, chatstate.TurnFailed, ReasonAgentError, err.Error(), chat)
		return next
	}
	if len(images) > 0 {
		tr.Note("images", map[string]any{"numbers": chips, "paths": images})
	}
	plan := r.turnPlan(ctx, chat, turn, adapter, tr)
	opening, shellRuns := r.openingPreamble(ctx, chat, turn), r.takeShellRuns(ctx, chat, turn, tr)
	// A rewind's seeded context, the opening context, the mention context,
	// then the shell runs the human marked for this prompt (task 170) — all
	// of it ahead of the message and none of it in it.
	preamble := func(seeded string) string {
		return joinPreamble(joinPreamble(joinPreamble(seeded, opening), turn.MentionContext), shellRuns)
	}
	spec := agent.RunSpec{
		Prompt:   turn.Prompt,
		Preamble: preamble(plan.preamble),
		Images:   images,
		WorkDir:  workDir,
		Launcher: launcher,
		// nil for a host turn, which is the daemon's own environment and what
		// every chat carried before task 124.17; the task's container
		// environment for a turn inside one (decision 3), so the CLI reads
		// the configuration `container.mount_agent_config` mounted for it.
		Env: env,
		// What the turn row recorded (task 162 decision 7), not the chat's
		// current settings: a PATCH during a live turn — or while a drained
		// message waited — applies from the next turn written, and a
		// per-turn override is this turn's alone.
		Model:           runSettings(chat, turn).Model,
		Effort:          runSettings(chat, turn).Effort,
		PermissionMode:  agent.PermissionMode(runSettings(chat, turn).PermissionMode),
		OnInput:         agent.InputWait,
		ResumeSessionID: plan.sessionID,
		ResumeAt:        plan.resumeAt,
		// Every turn, not only one whose message opens with the sigil (task
		// 124.10 decision 66): one rule, and vincent never reads the human's
		// message to decide an argv flag. The cost is that the turn's own
		// prompt echo — a linked first turn's preamble among it — and its
		// §7.4 answers are in the transcript too, as agent.input_echo
		// records no client draws, under the per-turn cap set above.
		ReportInvocations: true,
		// Read per turn, like the transcript cap above: a hot-reloaded value
		// reaches the next send (task 133).
		BackgroundWait: r.cfg().Defaults.BackgroundWait.Std(),
	}
	handle, err := adapter.Start(ctx, spec)
	if errors.Is(err, agent.ErrResumeAtUnsupported) {
		// The capability said yes and the CLI said no — an upgrade or a
		// downgrade between the probe and this run. The turn is seeded
		// instead, and says so; resuming the whole session would be the
		// faked rewind decision 1 forbids.
		plan = r.seedInstead(ctx, chat, turn, tr)
		spec.ResumeSessionID, spec.ResumeAt = "", ""
		spec.Preamble = preamble(plan.preamble)
		handle, err = adapter.Start(ctx, spec)
	}
	if err != nil {
		reason := ReasonAgentError
		if errors.Is(err, agent.ErrResumeUnsupported) {
			reason = ReasonSessionLost
		}
		// Start can fail after the CLI is already spawned — claude's prompt
		// is written to a live process's stdin — so this ending invalidates
		// like every other one past placement, and ahead of finish for the
		// reason given below.
		r.invalidateSkills(workDir)
		r.recordHeadAfter(turn, workDir) // review F3, as above
		next = r.finish(turn, chatstate.TurnFailed, reason, err.Error(), chat)
		return next
	}
	live := &liveTurn{
		turnID: turn.ID, handle: handle, cancel: cancel,
		answered: make(chan struct{}, 1),
	}
	r.track(chat.ID, live)
	defer r.untrack(chat.ID)
	r.journal(turn, handle)

	r.consume(ctx, cancelCause, live, chat, turn, adapter, place, handle, tr)

	res, waitErr := handle.Wait()
	turn.ExitCode = &res.ExitCode
	turn.InputTokens, turn.OutputTokens, turn.CostUSD = res.InputTokens, res.OutputTokens, res.CostUSD
	turn.ResultText = res.ResultText
	if res.SessionID != "" {
		turn.SessionID = res.SessionID
		r.consumePlan(chat, res.SessionID)
	}
	turn.ResumeAnchor = res.ResumeAnchor
	recordContext(turn, adapter, res)
	r.recordHeadAfter(turn, workDir)
	if agent.UsageIsSessionTotal(adapter) {
		r.turnShare(turn, res)
	}
	// The process is gone, so nothing later in this turn can write a skill.
	// This is ahead of every ending below rather than deferred: finish's
	// chat.turn_changed is the cue a client refetches the skill list on, and
	// a refetch racing an invalidation that had not happened yet would
	// re-cache the pre-turn list (task 124.9, §9.6).
	r.invalidateSkills(workDir)
	switch cause := context.Cause(ctx); {
	case ctx.Err() != nil && r.base.Err() != nil:
		// The daemon is going away under a live turn. It is interrupted, not
		// failed, and it is never re-run (decision 5).
		next = r.finish(turn, chatstate.TurnInterruptedState, "", "", chat)
	case errors.Is(cause, errTurnTimeout):
		next = r.finish(turn, chatstate.TurnFailed, ReasonTimeout,
			fmt.Sprintf("turn ran past agent_timeout (%s)", r.agentTimeout()), chat)
	case errors.Is(cause, errTranscriptLimit):
		next = r.finish(turn, chatstate.TurnFailed, ReasonTranscriptLimit,
			"the turn's transcript reached transcript_max_bytes", chat)
	case errors.Is(cause, errInputTimeout):
		next = r.finish(turn, chatstate.TurnFailed, ReasonInputTimeout,
			fmt.Sprintf("no answer within input_timeout (%s)", r.inputTimeout()), chat)
	case ctx.Err() != nil:
		next = r.finish(turn, chatstate.TurnFailed, ReasonCanceled, "canceled", chat)
	case waitErr != nil:
		next = r.finish(turn, chatstate.TurnFailed, ReasonAgentError, waitErr.Error(), chat)
	case res.Failure != nil && res.Failure.Kind == agent.FailureSessionLost:
		// The stored session is gone. The chat stays usable and keeps its
		// id: clearing it here would silently convert the next turn into a
		// fresh conversation, which is the behaviour decision 4 rejects.
		next = r.finish(turn, chatstate.TurnFailed, ReasonSessionLost, res.ErrorMessage, chat)
	case res.IsError:
		next = r.finish(turn, chatstate.TurnFailed, ReasonAgentError, res.ErrorMessage, chat)
	default:
		next = r.finish(turn, chatstate.TurnDone, "", "", chat)
	}
	return next
}

// Workspace resolves the directory chat's next turn would start in, without
// starting one. A free chat runs in its own worktree. A linked chat runs in
// its task's worktree, read through the store on every call because the task
// owns that claim (task 119 decision 1).
//
// It answers the directory alone. *Where* that directory is read from — the
// host, or the task's container — is turnPlace's and SkillPlace's answer, and
// both ask the container runtime for it (task 124.17 decision 1, amending
// task 124 decision 56).
func (r *Runner) Workspace(ctx context.Context, chat *store.Chat) (string, error) {
	if !chat.Linked() {
		return chat.WorktreePath, nil
	}
	task, err := r.deps.Store.GetTask(ctx, *chat.LinkedTaskID)
	if err != nil {
		return "", err
	}
	if task.WorktreePath == "" {
		return "", fmt.Errorf("task %d: %w", task.ID, ErrLinkedTaskNoWorktree)
	}
	return task.WorktreePath, nil
}

// turnPlace resolves where a turn runs: Workspace's directory, and for a
// linked chat the launcher, place and environment the task's container
// settings call for (task 119 decision 3, task 124.17 decisions 2 and 3). The
// launcher is the turn's source of truth: a configured container that has
// gone away fails the turn there rather than running it on the host.
func (r *Runner) turnPlace(
	ctx context.Context, chat *store.Chat, turn *store.ChatTurn,
) (dir string, l agent.Launcher, place string, env []string, err error) {
	dir, err = r.Workspace(ctx, chat)
	if err != nil {
		return "", nil, "", nil, err
	}
	if !chat.Linked() || r.deps.Launchers == nil {
		return dir, nil, "", nil, nil
	}
	l, place, env, err = r.deps.Launchers(ctx, *chat.LinkedTaskID, turn.ID)
	if err != nil {
		return "", nil, "", nil, err
	}
	return dir, l, place, env, nil
}

// SkillPlace resolves where a listing of chat's skills would be obtained
// (task 124.17, §5.5, §9.6, §13.2): Workspace's directory, and for a linked
// chat the launcher, place and environment its probe runs under.
//
// It is the turn's placement resolved through the same helper in
// internal/taskrun, one pid-file key aside, so the list and the next turn can
// never disagree about which CLI, which home and which machine the answer is
// about. A configured container that is gone comes back as
// taskrun.ErrTaskContainerMissing, and the caller reports that rather than
// listing the host: a host list would name skills the agent in the container
// never loads, which §9 forbids.
func (r *Runner) SkillPlace(
	ctx context.Context, chat *store.Chat,
) (dir string, l agent.Launcher, place string, env []string, err error) {
	dir, err = r.Workspace(ctx, chat)
	if err != nil {
		return "", nil, "", nil, err
	}
	if !chat.Linked() || r.deps.SkillPlace == nil {
		return dir, nil, "", nil, nil
	}
	l, place, env, err = r.deps.SkillPlace(ctx, *chat.LinkedTaskID, chat.ID)
	if err != nil {
		return "", nil, "", nil, err
	}
	return dir, l, place, env, nil
}

// invalidateSkills drops the skill cache's entries for dir, the directory a
// turn just ran in (task 124.9). Callers run it before finish, never after:
// see runTurn.
func (r *Runner) invalidateSkills(dir string) {
	if r.deps.InvalidateSkills != nil {
		r.deps.InvalidateSkills(dir)
	}
}

// reportBundledSkills hands the skill cache the names this turn's run header
// reported (task 124.16). It does not relax invalidateSkills: the header
// describes the turn's *start*, so a turn that wrote a skill reports a set
// identical to the cached one, and reading the two as "nothing changed" would
// hide that skill until the next turn.
func (r *Runner) reportBundledSkills(a agent.Adapter, place string, names []string) {
	if r.deps.ReportBundledSkills != nil {
		r.deps.ReportBundledSkills(a, place, names)
	}
}

// turnPreamble is what rides ahead of the human's message: a linked chat's
// opening context on the first turn and on no later one (task 119). It is
// handed to the adapter apart from the message rather than fused with it,
// because claude expands a leading `/name` only when it starts its own text
// block (issue #499); agent.RunSpec.JoinedPrompt is the fused form for every
// path that takes one string. The message is literal, never a template (025
// decision 5).
func turnPreamble(chat *store.Chat, turn *store.ChatTurn) string {
	if turn.Seq != 1 {
		return ""
	}
	return chat.OpeningContext
}

// consume drains the run's normalized events into the transcript and the live
// output stream, applies the §7.4 states as they arrive, and runs the turn's
// two clocks (task 067 decision 1).
//
// The clocks are a pair, not a sum. `agent_timeout` bounds *work*: it runs
// while the agent is running and stops while the chat sits in
// `awaiting_input`, because a human thinking is not the agent burning time.
// `input_timeout` bounds *that wait*, and is the clock §11 was missing — an
// unanswered request used to hold a `max_parallel_chats` slot forever. Either
// expiry cancels the run context with its own cause, which kills the process
// tree; runTurn maps the cause onto the failure reason and returns the chat
// to idle, releasing the slot.
//
// It is a select loop rather than a `range` over the event channel because a
// range cannot also watch a timer. The events channel closing is what ends
// the loop, exactly as before.
//
// Every *other* way out goes through drain first, and none of them may
// simply return — see drain.
func (r *Runner) consume(
	ctx context.Context, cancelCause context.CancelCauseFunc, live *liveTurn,
	chat *store.Chat, turn *store.ChatTurn, adapter agent.Adapter, place string,
	handle agent.RunHandle, tr *transcript.Writer,
) {
	events := handle.Events()
	work := time.NewTimer(r.agentTimeout())
	defer work.Stop()
	wait := time.NewTimer(r.inputTimeout())
	// The input clock starts stopped: nothing is pending until the agent
	// asks. stopTimer drains so a later Reset cannot fire an already-queued
	// tick.
	stopTimer(wait)
	defer wait.Stop()
	parked := false
	for {
		select {
		case <-ctx.Done():
			// The daemon is going away, or a human canceled. Either way the
			// adapter's stream is about to close; runTurn classifies from the
			// cause.
			r.drain(events, chat, turn, tr)
			return
		case <-work.C:
			tr.Note("timeout", map[string]any{"timeout": r.agentTimeout().String()})
			r.deps.Logger.Warn("chat turn timed out",
				"chat", chat.ID, "turn", turn.ID, "timeout", r.agentTimeout())
			cancelCause(errTurnTimeout)
			r.drain(events, chat, turn, tr)
			return
		case <-wait.C:
			tr.Note("input_timeout", map[string]any{"timeout": r.inputTimeout().String()})
			r.deps.Logger.Warn("chat input request timed out",
				"chat", chat.ID, "turn", turn.ID, "timeout", r.inputTimeout())
			cancelCause(errInputTimeout)
			r.drain(events, chat, turn, tr)
			return
		case <-live.answered:
			// A human answered through Answer, which already CAS'd
			// awaiting_input → running. The wait clock stops and the work
			// clock resumes from a full budget: the agent is starting fresh
			// work on the answer, not resuming a run it was halfway through.
			if parked {
				parked = false
				stopTimer(wait)
				resetTimer(work, r.agentTimeout())
			}
		case ev, chOpen := <-events:
			if !chOpen {
				return
			}
			r.record(chat, turn, tr, ev)
			if tr.Exceeded() {
				r.deps.Logger.Warn("chat transcript hit its cap",
					"chat", chat.ID, "turn", turn.ID,
					"limit", r.cfg().TranscriptMaxBytes.Bytes())
				cancelCause(errTranscriptLimit)
				r.drain(events, chat, turn, tr)
				return
			}
			switch ev.Type {
			case agent.EventInputRequest:
				if !r.park(ctx, chat, ev) {
					continue
				}
				parked = true
				stopTimer(work)
				resetTimer(wait, r.inputTimeout())
			case agent.EventInputCanceled:
				if _, err := r.deps.Store.SetChatState(ctx, chat.ID, chatstate.Running); err != nil {
					r.deps.Logger.Warn("chat input closed", "chat", chat.ID, "error", err)
				}
				if parked {
					parked = false
					stopTimer(wait)
					resetTimer(work, r.agentTimeout())
				}
			case agent.EventRunHeader:
				// The one thing a header is read for rather than only
				// rendered (task 124.16): claude names the skills this turn
				// loaded, which is what the skill cache classifies its
				// `builtin` rows against. An adapter that reports none — and
				// a claude build too old to — hands over nothing.
				if ev.Header != nil && len(ev.Header.Skills) > 0 {
					r.reportBundledSkills(adapter, place, ev.Header.Skills)
				}
			case agent.EventUsage, agent.EventResult, agent.EventOutput, agent.EventToolUse,
				agent.EventToolResult, agent.EventThinking,
				agent.EventPlan, agent.EventCommandOutput,
				agent.EventSubagentStarted, agent.EventSubagentProgress, agent.EventSubagentFinished,
				agent.EventError, agent.EventUnknown:
				// Rendering is the client's job: every one of these is already
				// in the transcript and on the stream, and internal/tui's
				// outputlines.go decides what a verbosity level shows.
			}
		}
	}
}

// drain empties the adapter's event channel until the stream closes, and is
// what every early exit from consume owes the run it is abandoning.
//
// An adapter hands its events over on a channel written by goroutines that
// block on the send — internal/agent/claude's readLoop feeds a mux which
// feeds the events channel — and its Wait blocks until those goroutines are
// finished. Cancelling the context kills the process tree, but it does not
// unblock a goroutine already parked mid-handover: a consumer that stopped
// reading leaves the reader waiting on a channel nobody empties and Wait
// waiting on the reader, so runTurn never returns, the turn never reaches a
// terminal state and its §11 slot is held until the daemon restarts. The
// buffers are what hid it — 128 events fit between the CLI and the loop, so
// the deadlock only lands when the agent got that far ahead before the kill,
// which is why it showed up as a rare hang under load rather than as a red
// test. internal/taskrun keeps its loop running for the same reason ("the
// stream is left to drain so Wait still reports an exit", steps.go).
//
// Draining records rather than discards, exactly as the task engine does: the
// lines were written, the cap suppresses them once it is reached, and a
// transcript that stops mid-sentence for a reason nobody can see is the thing
// §12.3 exists to avoid.
func (r *Runner) drain(events <-chan agent.Event, chat *store.Chat, turn *store.ChatTurn, tr *transcript.Writer) {
	for ev := range events {
		r.record(chat, turn, tr, ev)
	}
}

// record writes one event's raw line to the transcript and republishes it on
// the chat's live-output key. The offset it returns is what a client seams a
// transcript fetch against (§13.3): chunks at or before the fetch's
// X-Next-Offset are the ones it already has.
//
// The chunk carries §13.3's normalized fields, the same ones a step's chunks
// carry and under the same type names (task 071 decision 1), with the verbatim
// line kept alongside them as `raw`. Normalizing here rather than in the client
// is what makes a live line and the same line refetched from
// GET /v1/chats/{id}/turns/{seq}/transcript render identically: the client
// would otherwise have to know which dialect this chat speaks.
func (r *Runner) record(chat *store.Chat, turn *store.ChatTurn, tr *transcript.Writer, ev agent.Event) {
	if len(ev.Raw) == 0 {
		return
	}
	at := tr.Raw(ev.Raw)
	if r.deps.Events == nil {
		return
	}
	chunks := agent.LiveChunks(ev)
	switch {
	case len(chunks) > 0:
	case agent.UnmodeledLine(ev):
		// A line vincent's parsers do not model still reaches the tail, as
		// `agent.raw` — the type the transcript route gives the same line
		// back under. A chat has no timeline of steps beside it, so a turn
		// whose stream is all unmodeled lines would otherwise show nothing
		// at all while it runs; the client collapses them behind a count
		// below its verbose level rather than the daemon hiding them (§12.2).
		chunks = []agent.Chunk{{
			Type: "agent.raw", Payload: map[string]any{"line": string(ev.Raw)},
		}}
	default:
		// A result or an error: the turn's own outcome carries it, and the
		// finished turn's transcript is where it renders (task 071
		// decision 6). Publishing a chunk here would put a line on screen
		// that the refetch then disagrees with.
		//
		// Or an input echo — cursor repeating this turn's message (task
		// 124.2). The message is already on screen as the human's own, and
		// the refetch gives the line back as agent.input_echo, which no
		// client draws, so a chunk here would draw it twice.
		return
	}
	for _, c := range chunks {
		c.Payload["chat_id"] = chat.ID
		c.Payload["turn_id"] = turn.ID
		// raw is kept beside the normalized fields: the chat's chunk has
		// always carried it and dropping it would break a consumer for no
		// gain. Both chunks of a split line carry the same one, which is the
		// line they were both read from.
		c.Payload["raw"] = string(ev.Raw)
		c.Payload["offset"] = at
		r.deps.Events.PublishOutput(chatOutputKey(chat.ID), events.Chunk{
			Type:    c.Type,
			Payload: c.Payload,
		})
	}
}

// park records a §7.4 request and moves the chat to awaiting_input. It reports
// whether the chat actually parked: a request that could not be stored is not
// one a human can answer, so the input clock must not start on it.
func (r *Runner) park(ctx context.Context, chat *store.Chat, ev agent.Event) bool {
	req, err := json.Marshal(ev.Request)
	if err != nil {
		return false
	}
	if _, err := r.deps.Store.SetChatPendingInput(ctx, chat.ID, req); err != nil {
		r.deps.Logger.Warn("store chat pending input", "chat", chat.ID, "error", err)
		return false
	}
	if _, err := r.deps.Store.SetChatState(ctx, chat.ID, chatstate.AwaitingInput); err != nil {
		r.deps.Logger.Warn("chat awaiting input", "chat", chat.ID, "error", err)
		return false
	}
	return true
}

// stopTimer stops a timer and drains a tick that already fired, so a later
// Reset starts from now rather than firing immediately.
func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// resetTimer restarts a timer at d, draining first for the same reason.
func resetTimer(t *time.Timer, d time.Duration) {
	stopTimer(t)
	t.Reset(d)
}

// cfg returns the current effective configuration, tolerating a runner built
// without one (tests) by falling back to the shipped defaults.
func (r *Runner) cfg() config.Config {
	if r.deps.Config == nil {
		return config.Default()
	}
	return r.deps.Config()
}

// agentTimeout is §7.2's clock, applied verbatim to a chat turn. There is no
// per-turn override: §8.2's `timeout` is a workflow step field, and a chat has
// no workflow (task 067 decision 1).
func (r *Runner) agentTimeout() time.Duration {
	if d := r.cfg().Defaults.AgentTimeout.Std(); d > 0 {
		return d
	}
	return config.Default().Defaults.AgentTimeout.Std()
}

// inputTimeout is §7.4's clock, likewise verbatim and likewise unoverridable.
func (r *Runner) inputTimeout() time.Duration {
	if d := r.cfg().Defaults.InputTimeout.Std(); d > 0 {
		return d
	}
	return config.Default().Defaults.InputTimeout.Std()
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

// recordContext copies the run's cache counts and context figures onto the
// turn (task 157). Zero is "not reported" in a RunResult and stays nil here,
// so a reader can tell it from a reported zero; the cache counts are recorded
// only when the adapter reports any, and never for an adapter whose input
// already counts them (agent.CacheInInput), which the heading would
// otherwise count twice.
func recordContext(turn *store.ChatTurn, adapter agent.Adapter, res agent.RunResult) {
	turn.CacheReadTokens, turn.CacheCreationTokens = nil, nil
	if (res.CacheReadTokens != 0 || res.CacheCreationTokens != 0) && !agent.InputIncludesCache(adapter) {
		cr, cw := res.CacheReadTokens, res.CacheCreationTokens
		turn.CacheReadTokens, turn.CacheCreationTokens = &cr, &cw
	}
	turn.ContextTokens, turn.ContextWindow = positive(res.ContextTokens), positive(res.ContextWindow)
}

// positive is n as a pointer, nil when n is not positive.
func positive(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

// turnShare turns an adapter's session totals into this turn's own spend
// (task 138.11 decision 1, §9.9). Copilot reports the whole session's running
// total, so the turn's figures are the totals minus those the chat's latest
// earlier turn on the same session recorded. With no such turn — the first
// one, or a fresh session after `session_lost` — the totals are the turn's.
// Totals below the previous figures mean the session was reset under it, so
// they are taken as they are rather than as a negative spend. A run that
// reported no usage at all records no totals, so the next turn measures from
// the last figure the session actually reported.
//
// Spend that another client of the same session adds between two vincent
// turns lands in the second turn's share: nothing in the stream tells them
// apart, and §9.9 says so rather than working around it.
func (r *Runner) turnShare(turn *store.ChatTurn, res agent.RunResult) {
	if res.InputTokens == 0 && res.OutputTokens == 0 {
		return
	}
	in, out := res.InputTokens, res.OutputTokens
	turn.SessionInputTokens, turn.SessionOutputTokens = &in, &out
	// The cache totals are a session's running figures too, and take the
	// same share subtraction under the same rules (task 157 decision 3).
	cr, cw := res.CacheReadTokens, res.CacheCreationTokens
	if turn.CacheReadTokens != nil {
		turn.SessionCacheReadTokens, turn.SessionCacheCreationTokens = &cr, &cw
	}
	ctx := r.persist
	if ctx == nil {
		ctx = context.Background()
	}
	prev, ok, err := r.deps.Store.PreviousSessionUsage(ctx, turn.ChatID, turn.Seq, turn.SessionID)
	if err != nil {
		// The totals are still stored; only this turn's share is overstated.
		r.deps.Logger.Error("previous session usage", "turn", turn.ID, "error", err)
		return
	}
	if !ok || in < prev.InputTokens || out < prev.OutputTokens {
		return
	}
	turn.InputTokens, turn.OutputTokens = in-prev.InputTokens, out-prev.OutputTokens
	if turn.CacheReadTokens != nil && cr >= prev.CacheReadTokens && cw >= prev.CacheCreationTokens {
		dr, dw := cr-prev.CacheReadTokens, cw-prev.CacheCreationTokens
		turn.CacheReadTokens, turn.CacheCreationTokens = &dr, &dw
	}
}

// finishUnstarted ends a popped turn that never reached runTurn — a stop or
// a shutdown landing during a queue handover (task 156). It opens the turn's
// transcript first, empty, so that TurnWritesNoTranscript stays true: only
// runTurn's two early failures leave a turn with no file (review F5). A
// transcript that cannot be opened ends the turn the way runTurn's would.
func (r *Runner) finishUnstarted(turn *store.ChatTurn, state chatstate.TurnState, reason, msg string, chat *store.Chat) {
	tr, err := transcript.Open(r.transcriptDir(chat.ID), fmt.Sprintf("%d.jsonl", turn.Seq))
	if err != nil {
		r.finish(turn, chatstate.TurnFailed, ReasonTranscriptIOError, err.Error(), chat)
		return
	}
	tr.Close()
	r.finish(turn, state, reason, msg, chat)
}

// finish writes the turn's ending and returns the chat to idle, in that
// order: a reader must never see an idle chat with a turn still running.
func (r *Runner) finish(
	turn *store.ChatTurn, state chatstate.TurnState, reason, msg string, chat *store.Chat,
) *store.ChatTurn {
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
	if chat == nil {
		if err := r.deps.Store.UpdateChatTurn(ctx, turn); err != nil {
			r.deps.Logger.Error("finish chat turn", "turn", turn.ID, "error", err)
		}
		return nil
	}
	// One transaction records the ending, stores the session id and decides
	// what the chat does next: drain the queue on a `done` turn, or go idle
	// and pause a non-empty queue on any other ending (task 156 decisions 1
	// and 3).
	pause := reason
	if state == chatstate.TurnInterruptedState {
		pause = store.QueuePauseInterrupted
	}
	next, err := r.deps.Store.FinishChatTurn(ctx, turn, pause)
	if err != nil {
		r.deps.Logger.Error("finish chat turn", "turn", turn.ID, "error", err)
		if _, err := r.deps.Store.SetChatState(ctx, chat.ID, chatstate.Idle); err != nil {
			r.deps.Logger.Error("chat back to idle", "chat", chat.ID, "error", err)
		}
		return nil
	}
	return next
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

// envChatID marks a chat agent's process (task 130.10 decision 2):
// internal/apiclient turns it into a marker header, so a `vincent issue
// close` the agent runs is an agent's and is refused on an issue that
// writes back to GitHub, the way an MCP call is. apiclient.EnvChatID is the
// same string; chatrun does not import the client.
const envChatID = "VINCENT_CHAT_ID"

// withChatMarker is env plus the chat marker. A nil env — a host turn — is
// the daemon's own environment, which is what it stood for.
func withChatMarker(env []string, chatID int64) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, envChatID+"=") {
			out = append(out, kv)
		}
	}
	return append(out, envChatID+"="+strconv.FormatInt(chatID, 10))
}

// SteerUnavailable is why a chat cannot steer its running turn (task 156
// decision 4), or "" when it can. Steering ships only on an adapter whose CLI
// has been captured folding a second message into the live turn before that
// turn's result; no adapter has such a capture yet, so every chat answers
// with a reason, and the message can only be queued. It is never emulated by
// cancel-and-resend.
func SteerUnavailable(agentName string) string {
	return fmt.Sprintf("%s cannot steer a running turn; send queues the message to run after it", agentName)
}

// runSettings is what turn runs with: the settings recorded on its row when it
// was written (task 162 decision 7). A row from before migration 0062 records
// none, and one that recorded no permission mode is the chat's — the only way
// to reach either is a turn written by an older daemon, and the chat's value
// was then the only value there was.
func runSettings(chat *store.Chat, turn *store.ChatTurn) store.TurnSettings {
	if turn.PermissionMode == "" {
		return store.TurnSettings{}.Resolve(chat)
	}
	return store.TurnSettings{Model: turn.Model, Effort: turn.Effort, PermissionMode: turn.PermissionMode}
}
