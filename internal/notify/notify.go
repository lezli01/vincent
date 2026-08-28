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

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

const (
	// EnvelopeType is the `type` every envelope carries. It is the durable
	// event type the selector reads, not a new one (§13.3).
	EnvelopeType = store.EventTaskStateChanged

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
type Envelope struct {
	EventID int64  `json:"event_id"`
	TS      string `json:"ts"`
	Type    string `json:"type"`

	TaskID       int64  `json:"task_id"`
	Title        string `json:"title"`
	From         string `json:"from"`
	To           string `json:"to"`
	BlockReason  string `json:"block_reason"`
	QueuedReason string `json:"queued_reason"`
	CurrentStep  int    `json:"current_step"`
	StepsTotal   int    `json:"steps_total"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`

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
	taskID  int64
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
	if e == nil || e.TaskID == nil || e.Type != store.EventTaskStateChanged {
		return
	}
	var payload struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return
	}
	to := taskstate.State(payload.To)
	cfg := n.deps.Config()
	if !cfg.Notify.Fires(to) {
		return
	}
	j := job{
		eventID: e.ID,
		ts:      e.TS,
		taskID:  *e.TaskID,
		from:    payload.From,
		to:      payload.To,
		argv:    append([]string(nil), cfg.Notify.Command...),
		env:     cfg.Environment.ResolveProcess(),
	}
	// The §7.4 transition already carries the normalized request's kind and
	// summary in its event payload; nothing else does, so this is the only
	// state that gets an `input` object.
	if to == taskstate.AwaitingInput && (payload.Kind != "" || payload.Summary != "") {
		j.input = &Input{Kind: payload.Kind, Summary: payload.Summary}
	}
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
			"task", j.taskID, "to", j.to, "event", j.eventID, "capacity", queueCapacity)
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
	log := n.deps.Logger.With("task", j.taskID, "to", j.to, "event", j.eventID)
	env, ok := n.envelope(ctx, j, log)
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
		EventID:      j.eventID,
		TS:           j.ts.UTC().Format(time.RFC3339),
		Type:         EnvelopeType,
		TaskID:       task.ID,
		Title:        task.Title,
		From:         j.from,
		To:           j.to,
		QueuedReason: task.QueuedReason,
		CurrentStep:  task.CurrentStep,
		StepsTotal:   n.deps.StepCount(task.WorkflowSnapshot),
		WorktreePath: task.WorktreePath,
		Branch:       task.BranchName,
		ProjectID:    task.ProjectID,
		Workflow:     task.WorkflowName,
		Input:        j.input,
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
