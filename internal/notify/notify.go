package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

const (
	// EnvelopeType is the `type` every task envelope carries. It is the
	// durable event type the selector reads, not a new one (§13.3). A chat
	// envelope carries the chat event it came from instead (task 156).
	EnvelopeType = store.EventTaskStateChanged

	// KindTask and KindChat are the envelope's `kind` (task 156): which of
	// the two selectors, `notify.on` or `notify.chat_on`, fired it. Every
	// task envelope says "task", so a notifier written before chats could
	// fire tells the two apart by one field rather than by which ids are
	// zero.
	KindTask = "task"
	// KindChat is the `kind` of an envelope `notify.chat_on` fired.
	KindChat = "chat"

	// seenCapacity bounds the event ids remembered for the no-replay rule.
	// It only has to cover ids that could be handed over twice in quick
	// succession, so it is sized like the queue in front of the workers.
	seenCapacity = 256

	// perChildTimeout bounds one notifier process. Fixed, not configurable:
	// this is the pruner's posture (internal/taskrun/prune.go), and a daemon
	// that stops serving because a notifier hung has its priorities backwards.
	// Ten seconds is generous for `curl` to a webhook and short enough that a
	// wedged notifier cannot occupy a worker while real notifications queue.
	perChildTimeout = 10 * time.Second

	// maxWorkers is how many notifier processes may run at once.
	maxWorkers = 4

	// queueCapacity is the bounded backlog. The issue proposed dropping
	// everything past four in flight; that loses a notification whenever five
	// tasks block at once, which is precisely when the feature earns its keep
	// (task 046 decision 3). A FIFO in front of the workers makes the ordinary
	// burst lossless while keeping the backlog bounded, and absorbs a fan-out
	// tree cleanly — lanes are enqueued and discarded by a worker after one
	// indexed read each.
	queueCapacity = 64

	// stderrTail is how much of a failed child's stderr rides the log line.
	// Enough to carry "command not found" or a curl error, short enough that
	// a chatty notifier cannot flood daemon.log.
	stderrTail = 512
)

// Store is the slice of the store a notifier reads. Both calls happen on a
// worker, never on the publishing goroutine.
type Store interface {
	GetTask(ctx context.Context, id int64) (*store.Task, error)
	GetProject(ctx context.Context, id int64) (*store.Project, error)
	// GetChat is the chat read behind a `notify.chat_on` envelope (task 156).
	GetChat(ctx context.Context, id int64) (*store.Chat, error)
}

// Deps are the notifier's dependencies.
type Deps struct {
	Store  Store
	Config func() config.Config
	Logger *slog.Logger
	// StepCount reports how many steps a task's workflow snapshot holds. It
	// is injected the way taskrun.Deps injects its functions, so this package
	// does not import internal/workflow for one integer. The snapshot is the
	// honest `n` for that run, unlike a registry-derived count, because the
	// registry may have been edited since the task was created.
	StepCount func(snapshot string) int
}

// Input is the agent's question, carried only on a transition into
// awaiting_input (§7.4). It is lifted from the fields that transition already
// puts in its event payload.
type Input struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

// Envelope is the JSON object written to the child's stdin.
//
// It is enriched rather than the raw events-table row: a notifier handed
// `{task_id, to}` cannot write a message without calling back into the API
// with a bearer token, which defeats the point of a one-line shell script
// (task 046 decision 5).
//
// A chat envelope (task 156) is the same object, so one script handles both:
// `kind` is "chat", `chat_id` names it, `from`/`to` are the chat's state and
// the moment from `notify.chat_on`, and every task-only field — task_id,
// block_reason, queued_reason, admit_not_before, the step cursor, workflow —
// is zero or null.
type Envelope struct {
	EventID int64  `json:"event_id"`
	TS      string `json:"ts"`
	Type    string `json:"type"`
	// Kind is KindTask or KindChat (task 156). Additive: a task envelope is
	// otherwise exactly what it was.
	Kind string `json:"kind"`

	TaskID       int64  `json:"task_id"`
	ChatID       int64  `json:"chat_id"`
	Title        string `json:"title"`
	From         string `json:"from"`
	To           string `json:"to"`
	BlockReason  string `json:"block_reason"`
	QueuedReason string `json:"queued_reason"`
	// AdmitNotBefore is when a held task may next be admitted (§11): the
	// *until* beside queued_reason's *why*, so a one-line notifier can say
	// "waiting until 14:20" without calling the API back (task 106 decision
	// 6). RFC3339, and null whenever the task carries no hold.
	AdmitNotBefore *string `json:"admit_not_before"`
	CurrentStep    int     `json:"current_step"`
	StepsTotal     int     `json:"steps_total"`
	WorktreePath   string  `json:"worktree_path"`
	Branch         string  `json:"branch"`

	ProjectID int64  `json:"project_id"`
	Project   string `json:"project"`
	Workflow  string `json:"workflow"`

	Input *Input `json:"input,omitempty"`
}

// job is one enqueued transition. The argv and the environment policy are
// captured at enqueue time so a config edit mid-burst cannot half-apply: the
// event that matched the old `on:` is delivered by the old `command`.
type job struct {
	eventID int64
	ts      time.Time
	evType  string
	kind    string
	taskID  int64
	chatID  int64
	from    string
	to      string
	input   *Input
	argv    []string
	env     []string
}

// Notifier spawns the configured command for matching transitions. The zero
// value is not usable; call New.
type Notifier struct {
	deps  Deps
	queue chan job
	wg    sync.WaitGroup

	stopOnce sync.Once
	stopped  chan struct{}

	// seen is the no-replay rule made literal: the ids of the last
	// seenCapacity events that matched, so an event handed over a second time
	// — a resubscription, a duplicate publish — is not a second notification.
	// A ring rather than a high-water mark because two writers' post-commit
	// publishes may arrive out of id order, and dropping the lower one would
	// lose a real notification.
	seenMu   sync.Mutex
	seen     map[int64]bool
	seenRing []int64
	seenNext int

	// timeout is perChildTimeout. It is a field rather than a constant read
	// directly so a test can watch the kill happen without spending ten real
	// seconds; nothing outside this package can reach it, which is what keeps
	// it un-configurable (task 046 decision 7).
	timeout time.Duration

	// spawn is the child-process launcher, replaced in tests that need to
	// observe concurrency without counting real processes.
	spawn func(context.Context, job, []byte) error
}

// New returns a stopped notifier. Call Start before registering OnEvent.
func New(deps Deps) *Notifier {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.StepCount == nil {
		deps.StepCount = func(string) int { return 0 }
	}
	n := &Notifier{
		deps:    deps,
		queue:   make(chan job, queueCapacity),
		stopped: make(chan struct{}),
		timeout: perChildTimeout,
		seen:    make(map[int64]bool, seenCapacity),
	}
	n.spawn = n.run
	return n
}

// Start launches the worker pool. It returns immediately.
func (n *Notifier) Start(ctx context.Context) {
	for range maxWorkers {
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			n.worker(ctx)
		}()
	}
}

// Stop closes the queue and waits for in-flight children. Idempotent, and
// safe to call without Start. It belongs before broker.Close() in the
// shutdown order: the broker is what feeds OnEvent.
func (n *Notifier) Stop() {
	n.stopOnce.Do(func() { close(n.stopped) })
	n.wg.Wait()
}

// OnEvent is the broker subscriber. It runs on the publishing goroutine and
// must not block (internal/events, store.SetEventHook), so it does only
// in-memory work and a non-blocking enqueue.
func (n *Notifier) OnEvent(e *store.Event) {
	if e == nil {
		return
	}
	cfg := n.deps.Config()
	var (
		j  job
		ok bool
	)
	switch e.Type {
	case store.EventTaskStateChanged:
		j, ok = taskJob(e, cfg)
	case store.EventChatState, store.EventChatTurn:
		j, ok = chatJob(e, cfg)
	}
	if !ok {
		return
	}
	j.eventID, j.ts, j.evType = e.ID, e.TS, e.Type
	j.argv = append([]string(nil), cfg.Notify.Command...)
	j.env = cfg.Environment.ResolveProcess()
	if !n.firstSighting(e.ID) {
		return
	}
	n.enqueue(j)
}

// taskJob is the task selector (task 046): a task.state_changed whose `to` is
// listed in `notify.on`.
func taskJob(e *store.Event, cfg config.Config) (job, bool) {
	if e.TaskID == nil {
		return job{}, false
	}
	var payload struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return job{}, false
	}
	to := taskstate.State(payload.To)
	if !cfg.Notify.Fires(to) {
		return job{}, false
	}
	j := job{kind: KindTask, taskID: *e.TaskID, from: payload.From, to: payload.To}
	// The §7.4 transition already carries the normalized request's kind and
	// summary in its event payload; nothing else does, so this is the only
	// state that gets an `input` object.
	if to == taskstate.AwaitingInput && (payload.Kind != "" || payload.Summary != "") {
		j.input = &Input{Kind: payload.Kind, Summary: payload.Summary}
	}
	return j, true
}

// chatJob is the chat selector (task 156): a chat.state_changed into
// awaiting_input, or a chat.turn_changed whose turn ended done or failed,
// when that moment is listed in `notify.chat_on`.
//
// `to` is the moment's own name from chat_on, so a script switches on one
// vocabulary; `from` is the chat state the moment interrupted. A chat only
// enters awaiting_input from running (§5.5), and a turn's ending is written
// while the chat is still in its live state (internal/chatrun's finish), so
// the event's own `state` is that state.
func chatJob(e *store.Event, cfg config.Config) (job, bool) {
	var payload struct {
		ID        int64  `json:"id"`
		State     string `json:"state"`
		TurnState string `json:"turn_state"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil || payload.ID == 0 {
		return job{}, false
	}
	var (
		moment chatstate.NotifyState
		from   string
	)
	switch e.Type {
	case store.EventChatState:
		if chatstate.State(payload.State) != chatstate.AwaitingInput {
			return job{}, false
		}
		moment, from = chatstate.NotifyAwaitingInput, string(chatstate.Running)
	case store.EventChatTurn:
		m, ok := chatstate.NotifyForTurn(chatstate.TurnState(payload.TurnState))
		if !ok {
			return job{}, false
		}
		moment, from = m, payload.State
	default:
		return job{}, false
	}
	if !cfg.Notify.FiresChat(moment) {
		return job{}, false
	}
	return job{kind: KindChat, chatID: payload.ID, from: from, to: string(moment)}, true
}

// firstSighting records id and reports whether it is new. An event with no id
// was never persisted, so there is nothing to have seen.
func (n *Notifier) firstSighting(id int64) bool {
	if id == 0 {
		return true
	}
	n.seenMu.Lock()
	defer n.seenMu.Unlock()
	if n.seen[id] {
		return false
	}
	if len(n.seenRing) < seenCapacity {
		n.seenRing = append(n.seenRing, id)
	} else {
		delete(n.seen, n.seenRing[n.seenNext])
		n.seenRing[n.seenNext] = id
		n.seenNext = (n.seenNext + 1) % seenCapacity
	}
	n.seen[id] = true
	return true
}

// enqueue hands j to the workers without blocking.
func (n *Notifier) enqueue(j job) {
	select {
	case <-n.stopped:
		return
	default:
	}
	select {
	case n.queue <- j:
	default:
		// Only a full *queue* drops, never a busy worker (decision 3).
		n.deps.Logger.Warn("notification dropped: notifier queue full",
			"kind", j.kind, "task", j.taskID, "chat", j.chatID, "to", j.to,
			"event", j.eventID, "capacity", queueCapacity)
	}
}

func (n *Notifier) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.stopped:
			return
		case j := <-n.queue:
			n.handle(ctx, j)
		}
	}
}

// handle does the expensive half: the reads, the root-task check, the
// envelope and the spawn.
func (n *Notifier) handle(ctx context.Context, j job) {
	var (
		env Envelope
		ok  bool
	)
	log := n.deps.Logger.With("task", j.taskID, "to", j.to, "event", j.eventID)
	if j.kind == KindChat {
		log = n.deps.Logger.With("chat", j.chatID, "to", j.to, "event", j.eventID)
		env, ok = n.chatEnvelope(ctx, j, log)
	} else {
		env, ok = n.envelope(ctx, j, log)
	}
	if !ok {
		return
	}
	body, err := json.Marshal(env)
	if err != nil {
		log.Warn("notification not sent: envelope could not be encoded", "error", err)
		return
	}
	body = append(body, '\n')
	log.Debug("notification firing", "command", j.argv[0])
	if err := n.spawn(ctx, j, body); err != nil {
		log.Warn("notification command failed", "command", j.argv[0], "error", err)
	}
}

// envelope assembles the payload, or reports that this transition should not
// notify at all.
func (n *Notifier) envelope(ctx context.Context, j job, log *slog.Logger) (Envelope, bool) {
	task, err := n.deps.Store.GetTask(ctx, j.taskID)
	if err != nil {
		log.Warn("notification skipped: task could not be read", "error", err)
		return Envelope{}, false
	}
	if task == nil {
		return Envelope{}, false
	}
	// Only root tasks notify (task 046 decision 2). A `fan_out` lane is an
	// ordinary task row (§7.6, task 014 decision 1), so a twenty-lane tree
	// reaching `done` would produce twenty child transitions on top of the
	// parent's. The parent's own awaiting_children → running → done is the
	// human-meaningful signal; a lane finishing is machinery.
	if task.ParentTaskID != nil {
		log.Debug("notification skipped: fan-out lane, not a root task",
			"parent", *task.ParentTaskID)
		return Envelope{}, false
	}
	env := Envelope{
		EventID:        j.eventID,
		TS:             j.ts.UTC().Format(time.RFC3339),
		Type:           EnvelopeType,
		Kind:           KindTask,
		TaskID:         task.ID,
		Title:          task.Title,
		From:           j.from,
		To:             j.to,
		QueuedReason:   task.QueuedReason,
		AdmitNotBefore: admitNotBefore(task),
		CurrentStep:    task.CurrentStep,
		StepsTotal:     n.deps.StepCount(task.WorkflowSnapshot),
		WorktreePath:   task.WorktreePath,
		Branch:         task.BranchName,
		ProjectID:      task.ProjectID,
		Workflow:       task.WorkflowName,
		Input:          j.input,
	}
	// block_reason means "set while blocked" (§14); carrying one on any other
	// state would lie to whoever reads it.
	if j.to == string(taskstate.Blocked) {
		env.BlockReason = task.BlockReason
	}
	// A project that vanished between the transition and this read is not
	// worth losing the notification over: the task fields still say what
	// happened.
	if p, perr := n.deps.Store.GetProject(ctx, task.ProjectID); perr != nil {
		log.Debug("notification project name unavailable", "error", perr)
	} else if p != nil {
		env.Project = p.Name
	}
	return env, true
}

// chatEnvelope assembles a chat's envelope (task 156), or reports that it
// cannot be sent. There is no root check: a chat has no lanes.
func (n *Notifier) chatEnvelope(ctx context.Context, j job, log *slog.Logger) (Envelope, bool) {
	chat, err := n.deps.Store.GetChat(ctx, j.chatID)
	if err != nil {
		log.Warn("notification skipped: chat could not be read", "error", err)
		return Envelope{}, false
	}
	if chat == nil {
		return Envelope{}, false
	}
	env := Envelope{
		EventID:      j.eventID,
		TS:           j.ts.UTC().Format(time.RFC3339),
		Type:         j.evType,
		Kind:         KindChat,
		ChatID:       chat.ID,
		Title:        chat.Title,
		From:         j.from,
		To:           j.to,
		WorktreePath: chat.WorktreePath,
		Branch:       chat.Branch,
		ProjectID:    chat.ProjectID,
	}
	// A chat's state event carries only its id, title and state, so the §7.4
	// request is read from the row it was stored on before the chat parked.
	// A question answered before this worker ran has been retired with it,
	// and then there is honestly no input to report.
	if j.to == string(chatstate.NotifyAwaitingInput) {
		env.Input = pendingInput(chat.PendingInput)
	}
	if p, perr := n.deps.Store.GetProject(ctx, chat.ProjectID); perr != nil {
		log.Debug("notification project name unavailable", "error", perr)
	} else if p != nil {
		env.Project = p.Name
	}
	return env, true
}

// pendingInput lifts a chat's stored §7.4 request into the envelope's input:
// the same kind and summary the task path's event payload carries, summarized
// by the same rule internal/taskrun applies — a permission names its tool, a
// question is its first question's text (with a count of the rest), anything
// else is its kind. The row is agent.InputRequest marshalled with Go's field
// names, which is what the anonymous struct reads; this package does not
// import internal/agent for one shape.
func pendingInput(raw json.RawMessage) *Input {
	if len(raw) == 0 {
		return nil
	}
	var req struct {
		Kind       string
		Questions  []struct{ Text string }
		Permission *struct {
			Tool    string
			Summary string
		}
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Kind == "" {
		return nil
	}
	in := &Input{Kind: req.Kind, Summary: req.Kind}
	switch {
	case req.Permission != nil:
		in.Summary = "permission: " + req.Permission.Tool + " — " + req.Permission.Summary
	case len(req.Questions) > 0:
		s := req.Questions[0].Text
		if len(req.Questions) > 1 {
			s += " (+" + strconv.Itoa(len(req.Questions)-1) + " more)"
		}
		in.Summary = truncate(s, 200)
	}
	return in
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// admitNotBefore renders the task's admission hold for the envelope, or nil
// when it has none — `retry_backoff` and `usage_limit` holds alike.
func admitNotBefore(task *store.Task) *string {
	if task.AdmitNotBefore == nil {
		return nil
	}
	at := task.AdmitNotBefore.UTC().Format(time.RFC3339)
	return &at
}

// run spawns one notifier child, feeds it the envelope and waits out the
// timeout.
//
// procx.Start is what gives this NoWindow on Windows and a killable process
// *tree* on both — the daemon is normally console-less (a detached `daemon
// start`, or the Scheduled Task), and a console-subsystem child of a
// console-less parent is handed a window unless its creator says otherwise.
func (n *Notifier) run(ctx context.Context, j job, body []byte) error {
	// G204: the argv is config.yaml's, which belongs to the invoking user, and
	// running it is the entire point of the feature (§16). Nothing from a task,
	// an agent or the API reaches it.
	cmd := exec.Command(j.argv[0], j.argv[1:]...) //nolint:gosec // G204: see above
	cmd.Stdin = bytes.NewReader(body)
	// §12.3's environment policy governs every process the daemon spawns, this
	// one included. It gets no VINCENT_* variables: those are §8.5's contract
	// for command steps, and the envelope on stdin is this hook's.
	cmd.Env = j.env
	var stderr tailBuffer
	cmd.Stderr = &stderr

	proc, err := procx.Start(cmd)
	if err != nil {
		return err
	}
	defer proc.Release()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(n.timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return &ExitError{Code: exitErr.ExitCode(), Stderr: stderr.String()}
			}
			return err
		}
		return nil
	case <-timer.C:
		_ = proc.Kill()
		<-done
		return &TimeoutError{After: n.timeout}
	case <-ctx.Done():
		_ = proc.Kill()
		<-done
		return ctx.Err()
	}
}

// ExitError is a notifier that ran and failed. It is never retried: a
// notification is only interesting while it is fresh, and a retry policy here
// is a queue with a persistence story attached.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr == "" {
		return "exit status " + strconv.Itoa(e.Code)
	}
	return "exit status " + strconv.Itoa(e.Code) + ": " + e.Stderr
}

// TimeoutError is a notifier killed at perChildTimeout, process tree and all.
type TimeoutError struct{ After time.Duration }

func (e *TimeoutError) Error() string {
	return "killed after " + e.After.String() + ": notification command did not exit"
}

// tailBuffer keeps the last stderrTail bytes a child wrote. The tail, not the
// head: a script that logs before it fails puts the reason last.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > stderrTail {
		b.buf = b.buf[len(b.buf)-stderrTail:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}
