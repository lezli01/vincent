// Package taskrun executes tasks. In M1 it is the interim runner (phase 1
// decision): admission bounded by the global max_parallel_tasks cap using
// the T1.2 scheduler query, and a hardcoded single-agent-step execution path
// — worktree create → adapter run → StepRun + transcript → done/blocked.
// T2.3–T2.5 replace the hardcoded path with the real state machine, step
// executors, and scheduler.
package taskrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// DefaultAgent is the agent of the synthesized adhoc workflow (M1 decision).
const DefaultAgent = "claude"

// Failure/block reasons introduced by the run path; worktree reasons
// (worktree.Reason*) share the same vocabulary end to end.
const (
	ReasonTimeout          = "timeout"
	ReasonInterrupted      = "interrupted"
	ReasonNonzeroExit      = "nonzero_exit"
	ReasonAgentError       = "agent_error"
	ReasonAgentUnavailable = "agent_unavailable"
	ReasonInternalError    = "internal_error"
)

// tickInterval is the admission safety net: normally Wake drives the loop,
// but a tick also picks up config hot-reloads of max_parallel_tasks.
const tickInterval = 5 * time.Second

// stopGrace bounds how long Stop waits for executors after the kill.
const stopGrace = 20 * time.Second

// resultSummaryLimit caps result_summary rows; full text lives in the
// transcript.
const resultSummaryLimit = 4096

// Deps are the daemon facilities the runner works with.
type Deps struct {
	Store     *store.Store
	Config    func() config.Config
	Worktrees *worktree.Manager
	Agents    *agent.Registry
	DataDir   string
	Logger    *slog.Logger
}

// Runner admits queued tasks up to the global cap and executes them.
type Runner struct {
	deps   Deps
	wake   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New returns a stopped runner.
func New(deps Deps) *Runner {
	return &Runner{deps: deps, wake: make(chan struct{}, 1)}
}

// Start sweeps stop/crash leftovers (running rows → interrupted/blocked, M1
// interim for §12.4) and begins admitting queued tasks until ctx is
// canceled or Stop is called.
func (r *Runner) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	if n, err := r.deps.Store.SweepInterrupted(ctx); err != nil {
		r.deps.Logger.Error("startup sweep failed", "error", err)
	} else if n > 0 {
		r.deps.Logger.Warn("swept interrupted tasks from a previous run", "tasks", n)
	}
	r.wg.Add(1)
	go r.loop(ctx)
}

// Stop kills in-flight runs (via context cancellation → process-tree kill)
// and waits for executors to persist their interrupted state.
func (r *Runner) Stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopGrace):
		r.deps.Logger.Error("task executors did not stop in time; continuing shutdown")
	}
}

// Wake nudges the admission loop; safe from any goroutine, never blocks.
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runner) loop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		r.admit(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-ticker.C:
		}
	}
}

// admit starts queued tasks while the global cap has room, in scheduler
// order (priority DESC, created_at ASC — the T1.2 query; per-project caps
// and re-evaluation semantics arrive with T2.5).
func (r *Runner) admit(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	limit := r.deps.Config().MaxParallelTasks
	running, err := r.deps.Store.CountRunning(ctx)
	if err != nil {
		r.deps.Logger.Error("admission: count running", "error", err)
		return
	}
	if running >= limit {
		return
	}
	queued, err := r.deps.Store.ListQueuedInOrder(ctx)
	if err != nil {
		r.deps.Logger.Error("admission: list queued", "error", err)
		return
	}
	for i := range queued {
		if running >= limit || ctx.Err() != nil {
			return
		}
		t := queued[i]
		now := time.Now()
		t.State = store.TaskRunning
		t.StartedAt = &now
		if err := r.deps.Store.UpdateTask(ctx, &t); err != nil {
			r.deps.Logger.Error("admission: mark running", "task", t.ID, "error", err)
			return
		}
		running++
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer r.Wake() // slot freed → re-admit
			r.execute(ctx, &t)
		}()
	}
}

// persistCtx returns a context for final DB writes: shutdown cancels ctx,
// but the interrupted/blocked outcome must still be persisted.
func persistCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// execute is the hardcoded M1 single-agent-step run (T1.8): worktree →
// adapter run → StepRun + transcript → done/blocked. No retry — the adhoc
// snapshot pins max_retries 0 (T1.7–T1.9 decision).
func (r *Runner) execute(ctx context.Context, task *store.Task) {
	log := r.deps.Logger.With("task", task.ID)

	project, err := r.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		r.blockTask(task, ReasonInternalError, log)
		log.Error("execute: load project", "error", err)
		return
	}

	if task.BranchName == "" { // fallback for a crash between insert and branch update
		task.BranchName = worktree.BranchName(task.ID, task.Title)
	}
	if task.WorktreePath == "" {
		path, err := r.deps.Worktrees.Create(ctx, project.Path, task.ID, task.BranchName, task.BaseBranch)
		if err != nil {
			// A shutdown mid-create must not masquerade as a git failure.
			if ctx.Err() != nil {
				r.blockTask(task, ReasonInterrupted, log)
				return
			}
			reason := worktree.ReasonOf(err)
			if reason == "" {
				reason = worktree.ReasonGitError
			}
			r.blockTask(task, reason, log)
			log.Error("execute: worktree create", "reason", reason, "error", err)
			return
		}
		task.WorktreePath = path
		if err := r.updateTask(task); err != nil {
			log.Error("execute: persist worktree path", "error", err)
		}
	}

	// §8.6 resolution, M1 subset: the adhoc workflow's only step declares
	// nothing, its defaults name claude — so task override > default agent,
	// and override model/effort ride along (the full engine is T2.11).
	agentName := task.AgentOverride
	if agentName == "" {
		agentName = DefaultAgent
	}
	adapter, ok := r.deps.Agents.Get(agentName)
	if !ok {
		r.blockTask(task, ReasonAgentUnavailable, log)
		log.Error("execute: agent not registered", "agent", agentName)
		return
	}

	run := &store.StepRun{
		TaskID:    task.ID,
		StepIndex: 0,
		StepID:    "run",
		StepType:  "agent",
		Attempt:   1,
		State:     store.StepRunning,
		Agent:     agentName,
		Model:     task.ModelOverride,
		Effort:    task.EffortOverride,
	}
	transcriptPath, transcript, err := r.openTranscript(task.ID, run)
	if err != nil {
		r.blockTask(task, ReasonInternalError, log)
		log.Error("execute: open transcript", "error", err)
		return
	}
	defer func() { _ = transcript.Close() }()
	run.TranscriptPath = transcriptPath
	{
		pctx, cancel := persistCtx()
		err := r.deps.Store.CreateStepRun(pctx, run)
		cancel()
		if err != nil {
			r.blockTask(task, ReasonInternalError, log)
			log.Error("execute: create step run", "error", err)
			return
		}
	}

	writeVincentLine(transcript, map[string]any{
		"type": "vincent.step_started", "task_id": task.ID, "step_id": run.StepID,
		"attempt": run.Attempt, "agent": agentName, "model": run.Model, "effort": run.Effort,
	})

	timeout := r.deps.Config().Defaults.AgentTimeout.Std()
	runCtx, cancelRun := context.WithTimeout(ctx, timeout)
	defer cancelRun()
	handle, err := adapter.Start(runCtx, agent.RunSpec{
		Prompt:         adhocPrompt(task),
		WorkDir:        task.WorktreePath,
		Model:          task.ModelOverride,
		Effort:         task.EffortOverride,
		PermissionMode: agent.FullAuto,
		OnInput:        agent.InputWait,
	})
	if err != nil {
		writeVincentLine(transcript, map[string]any{"type": "vincent.error", "error": err.Error()})
		r.finishStep(run, store.StepFailed, ReasonAgentUnavailable, log)
		r.blockTask(task, ReasonAgentUnavailable, log)
		log.Error("execute: adapter start", "agent", agentName, "error", err)
		return
	}
	pid := handle.PID()
	now := time.Now()
	run.PID = &pid
	run.ProcStartedAt = &now
	if err := r.updateStepRun(run); err != nil {
		log.Error("execute: persist pid", "error", err)
	}
	log.Info("agent step running", "agent", agentName, "pid", pid,
		"model", run.Model, "effort", run.Effort)

	for ev := range handle.Events() {
		if _, err := transcript.Write(append(ev.Raw, '\n')); err != nil {
			log.Error("execute: transcript write", "error", err)
		}
	}
	res, waitErr := handle.Wait()

	state, reason := classify(ctx, runCtx, &res, waitErr)
	writeVincentLine(transcript, map[string]any{
		"type": "vincent.step_finished", "state": string(state),
		"exit_code": res.ExitCode, "failure_reason": reason,
	})

	run.ExitCode = &res.ExitCode
	run.FailureReason = reason
	run.ResultSummary = truncate(res.ResultText, resultSummaryLimit)
	if state == store.StepFailed && run.ResultSummary == "" {
		run.ResultSummary = truncate(res.ErrorMessage, resultSummaryLimit)
	}
	if res.InputTokens > 0 || res.OutputTokens > 0 {
		run.InputTokens = &res.InputTokens
		run.OutputTokens = &res.OutputTokens
	}
	run.CostUSD = res.CostUSD
	run.PID = nil
	r.finishStep(run, state, reason, log)

	switch state {
	case store.StepSucceeded:
		finished := time.Now()
		task.State = store.TaskDone
		task.FinishedAt = &finished
		task.BlockReason = ""
		if err := r.updateTask(task); err != nil {
			log.Error("execute: persist done", "error", err)
			return
		}
		log.Info("task done", "cost_usd", res.CostUSD,
			"input_tokens", res.InputTokens, "output_tokens", res.OutputTokens)
	default:
		r.blockTask(task, reason, log)
		log.Warn("task blocked", "reason", reason, "exit_code", res.ExitCode,
			"error", res.ErrorMessage)
	}
}

// classify maps a finished run to its StepRun state and failure reason
// (§7.1 success rule; T1.7–T1.9 decisions for timeout/interrupt).
func classify(daemonCtx, runCtx context.Context, res *agent.RunResult, waitErr error) (store.StepRunState, string) {
	switch {
	case daemonCtx.Err() != nil:
		return store.StepInterrupted, ReasonInterrupted
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return store.StepFailed, ReasonTimeout
	case waitErr != nil:
		return store.StepFailed, ReasonInternalError
	case res.ExitCode != 0:
		return store.StepFailed, ReasonNonzeroExit
	case res.IsError:
		return store.StepFailed, ReasonAgentError
	default:
		return store.StepSucceeded, ""
	}
}

func (r *Runner) finishStep(run *store.StepRun, state store.StepRunState, reason string, log *slog.Logger) {
	now := time.Now()
	run.State = state
	run.FailureReason = reason
	run.FinishedAt = &now
	if err := r.updateStepRun(run); err != nil {
		log.Error("persist step run", "run", run.ID, "error", err)
	}
}

func (r *Runner) blockTask(task *store.Task, reason string, log *slog.Logger) {
	task.State = store.TaskBlocked
	task.BlockReason = reason
	if err := r.updateTask(task); err != nil {
		log.Error("persist blocked task", "error", err)
	}
}

func (r *Runner) updateTask(task *store.Task) error {
	ctx, cancel := persistCtx()
	defer cancel()
	return r.deps.Store.UpdateTask(ctx, task)
}

func (r *Runner) updateStepRun(run *store.StepRun) error {
	ctx, cancel := persistCtx()
	defer cancel()
	return r.deps.Store.UpdateStepRun(ctx, run)
}

// openTranscript creates {data_dir}/transcripts/{task_id}/{step}-{attempt}.jsonl
// (spec §12.2).
func (r *Runner) openTranscript(taskID int64, run *store.StepRun) (string, *os.File, error) {
	dir := filepath.Join(r.deps.DataDir, "transcripts", strconv.FormatInt(taskID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create transcript dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%d-%d.jsonl", run.StepIndex, run.Attempt))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("create transcript: %w", err)
	}
	return path, f, nil
}

// writeVincentLine appends a namespaced vincent.* annotation to the
// transcript (phase 1 decision: agent lines verbatim, vincent's own lines
// namespaced).
func writeVincentLine(f *os.File, fields map[string]any) {
	fields["ts"] = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(fields)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

// adhocPrompt is the fixed M1 prompt embedding title and description — what
// T2.2's template engine will produce from the AdhocSnapshot definition.
func adhocPrompt(task *store.Task) string {
	prompt := adhocIntro + "\n\nTask: " + task.Title
	if task.Description != "" {
		prompt += "\n\n" + task.Description
	}
	return prompt
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
