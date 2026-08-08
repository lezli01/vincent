package taskrun

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// runAgentStep runs one agent attempt: render the prompt, spawn the adapter,
// stream its events into the transcript, and classify the result (§7.1).
func (r *Runner) runAgentStep(
	ctx context.Context, env *stepEnv, sel agent.Selection,
	rc workflow.RenderContext, run *store.StepRun, tr *transcript,
) stepOutcome {
	prompt, err := workflow.Render("prompt", env.step.Prompt, rc)
	if err != nil {
		// §8.4: a render failure fails the step before anything is spawned.
		tr.Note("error", map[string]any{"error": err.Error()})
		return stepOutcome{state: store.StepFailed, reason: ReasonTemplateError, result: err.Error()}
	}
	prompt = workflow.AppendFailureBlock(prompt, rc.Step.Attempt, rc.LastFailure)

	adapter, ok := r.deps.Agents.Get(sel.Agent)
	if !ok {
		tr.Note("error", map[string]any{"error": "agent " + sel.Agent + " is not registered"})
		return stepOutcome{state: store.StepFailed, reason: ReasonAgentUnavailable}
	}

	timeout := resolveTimeout(env.step, env.wf.Defaults, r.deps.Config())
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	handle, err := adapter.Start(runCtx, agent.RunSpec{
		Prompt:         prompt,
		WorkDir:        env.task.WorktreePath,
		Model:          sel.Model,
		Effort:         sel.Effort,
		PermissionMode: resolvePermission(env.step, env.wf.Defaults),
		OnInput:        resolveInputPolicy(env.step, env.wf.Defaults),
	})
	if err != nil {
		tr.Note("error", map[string]any{"error": err.Error()})
		env.log.Error("start agent", "agent", sel.Agent, "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonAgentUnavailable}
	}
	// Register the live process so a cancel can reach it (§6).
	defer r.setProc(env.task.ID, handle)()

	pid := handle.PID()
	started := time.Now()
	run.PID = &pid
	run.ProcStartedAt = &started
	if err := r.deps.Store.UpdateStepRun(r.persistCtx(), run); err != nil {
		env.log.Error("journal agent pid", "error", err)
	}
	env.log.Info("agent step running",
		"agent", sel.Agent, "pid", pid, "model", sel.Model, "effort", sel.Effort)

	tail := newOutputTail(outputTailLines)
	for ev := range handle.Events() {
		tr.Raw(ev.Raw)
		if ev.Type == agent.EventOutput && ev.Text != "" {
			tail.add(ev.Text)
		}
		r.publishAgentEvent(env.task.ID, ev)
	}
	res, waitErr := handle.Wait()

	outcome := classifyAgent(ctx, runCtx, r.interrupting(env.task.ID), &res, waitErr)
	outcome.exitCode = &res.ExitCode
	outcome.result = res.ResultText
	if outcome.result == "" {
		outcome.result = res.ErrorMessage
	}
	outcome.output = tail.String()
	if res.InputTokens > 0 || res.OutputTokens > 0 {
		run.InputTokens, run.OutputTokens = &res.InputTokens, &res.OutputTokens
	}
	run.CostUSD = res.CostUSD
	return outcome
}

// classifyAgent maps a finished agent run to its attempt state and reason:
// §7.1 requires exit 0 and a non-error terminal result. interrupting marks
// a human cancel or a graceful shutdown in progress — either one Terminates
// the process before the run context is canceled (§6's grace, §12.4's), so
// an abrupt exit then is an interruption, not a failure to retry.
func classifyAgent(daemonCtx, runCtx context.Context, interrupting bool, res *agent.RunResult, waitErr error) stepOutcome {
	switch {
	case daemonCtx.Err() != nil:
		return stepOutcome{state: store.StepInterrupted, reason: ReasonInterrupted}
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return stepOutcome{state: store.StepFailed, reason: ReasonTimeout}
	case interrupting && (waitErr != nil || res.ExitCode != 0 || res.IsError):
		return stepOutcome{state: store.StepInterrupted, reason: ReasonInterrupted}
	case waitErr != nil:
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	case res.ExitCode != 0:
		return stepOutcome{state: store.StepFailed, reason: ReasonNonzeroExit}
	case res.IsError:
		return stepOutcome{state: store.StepFailed, reason: ReasonAgentError}
	default:
		return stepOutcome{state: store.StepSucceeded}
	}
}

// runCommandStep runs one command attempt in the worktree (§8.3, §8.5).
func (r *Runner) runCommandStep(
	ctx context.Context, env *stepEnv, rc workflow.RenderContext, run *store.StepRun, tr *transcript,
) stepOutcome {
	script, err := workflow.Render("run", env.step.Run, rc)
	if err != nil {
		tr.Note("error", map[string]any{"error": err.Error()})
		return stepOutcome{state: store.StepFailed, reason: ReasonTemplateError, result: err.Error()}
	}
	timeout := resolveTimeout(env.step, env.wf.Defaults, r.deps.Config())
	return r.runShellCommand(ctx, env, run, tr, shellCommand{
		phase:    "run",
		script:   script,
		shellPin: env.step.Shell,
		env:      commandEnv(rc, env.step.Env),
		workDir:  env.task.WorktreePath,
		timeout:  timeout,
	})
}

// runCheck runs a step's `check` command after its body succeeded; a
// non-zero check fails the attempt (§7.1).
func (r *Runner) runCheck(
	ctx context.Context, env *stepEnv, rc workflow.RenderContext, run *store.StepRun, tr *transcript, outcome stepOutcome,
) stepOutcome {
	script, err := workflow.Render("check", env.step.Check, rc)
	if err != nil {
		tr.Note("error", map[string]any{"error": err.Error()})
		return stepOutcome{state: store.StepFailed, reason: ReasonTemplateError, result: err.Error()}
	}
	checkOutcome := r.runShellCommand(ctx, env, run, tr, shellCommand{
		phase:    "check",
		script:   script,
		shellPin: env.step.Shell,
		env:      commandEnv(rc, env.step.Env),
		workDir:  env.task.WorktreePath,
		timeout:  resolveCheckTimeout(env.step, r.deps.Config()),
	})
	outcome.checkExitCode = checkOutcome.exitCode
	if checkOutcome.state == store.StepSucceeded {
		return outcome
	}
	// The check's failure replaces the step's outcome, but the step's own
	// result summary is what later steps and the retry block care about.
	outcome.state = checkOutcome.state
	outcome.reason = checkFailureReason(checkOutcome.reason)
	outcome.output = checkOutcome.output
	return outcome
}

// checkFailureReason keeps interruption and timeout distinguishable while
// mapping an ordinary non-zero check to its own reason.
func checkFailureReason(reason string) string {
	if reason == ReasonNonzeroExit {
		return ReasonCheckFailed
	}
	return reason
}

// shellCommand is one rendered command to run under a shell.
type shellCommand struct {
	phase    string // "run" | "check" — tags transcript lines
	script   string
	shellPin string // step's `shell:` pin; empty = platform default
	env      []string
	workDir  string
	timeout  time.Duration
}

// runShellCommand executes one command, streaming its output into the
// transcript and tree-killing it on timeout or shutdown.
func (r *Runner) runShellCommand(
	ctx context.Context, env *stepEnv, run *store.StepRun, tr *transcript, sc shellCommand,
) stepOutcome {
	sh, err := r.deps.Shells.For(sc.shellPin)
	if err != nil {
		tr.Note("error", map[string]any{"error": err.Error()})
		env.log.Error("resolve shell", "shell", sc.shellPin, "error", err)
		return stepOutcome{state: store.StepFailed, reason: ReasonShellUnavailable, result: err.Error()}
	}
	argv := sh.command(sc.script)
	tr.Note("command_started", map[string]any{
		"phase": sc.phase, "shell": sh.Name, "command": sc.script, "cwd": sc.workDir,
	})

	runCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the workflow author's own command, by design (§9.4)
	cmd.Dir = sc.workDir
	cmd.Env = sc.env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError, result: err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError, result: err.Error()}
	}
	proc, err := procx.Start(cmd)
	if err != nil {
		tr.Note("error", map[string]any{"error": err.Error()})
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError, result: err.Error()}
	}
	defer proc.Release()
	// Register the live process so a cancel can reach it (§6).
	defer r.setProc(env.task.ID, proc)()

	// Journal the PID before any output can arrive, so crash recovery can
	// find and kill an orphaned command tree (§12.4 covers any step process;
	// a check overwrites the body's PID — the column means "running now").
	pid := cmd.Process.Pid
	started := time.Now()
	run.PID = &pid
	run.ProcStartedAt = &started
	if err := r.deps.Store.UpdateStepRun(r.persistCtx(), run); err != nil {
		env.log.Error("journal command pid", "error", err)
	}

	// Tree-kill on timeout or daemon shutdown: killing only the shell would
	// leave its children running in the worktree.
	killed := make(chan struct{})
	var killOnce sync.Once
	go func() {
		select {
		case <-runCtx.Done():
			killOnce.Do(func() { _ = proc.Kill() })
		case <-killed:
		}
	}()
	defer close(killed)

	tail := newOutputTail(outputTailLines)
	var wg sync.WaitGroup
	var mu sync.Mutex
	stream := func(rd io.Reader, name string) {
		defer wg.Done()
		scanner := bufio.NewScanner(rd)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			tr.Output(sc.phase, name, line)
			r.publishOutput(env.task.ID, "command.output", map[string]any{
				"phase": sc.phase, "stream": name, "text": line,
			})
			mu.Lock()
			tail.add(line)
			mu.Unlock()
		}
		if err := scanner.Err(); err != nil {
			// A single line past the buffer cap stops the scanner but not the
			// process. Keep draining the pipe: left full, it would block the
			// child until the timeout kill and misreport the attempt.
			tr.Note("error", map[string]any{
				"error": "output capture stopped for " + name + ": " + err.Error(),
			})
			_, _ = io.Copy(io.Discard, rd)
		}
	}
	wg.Add(2)
	go stream(stdout, "stdout")
	go stream(stderr, "stderr")
	wg.Wait()
	waitErr := cmd.Wait()

	exitCode := exitCodeOf(waitErr)
	outcome := stepOutcome{exitCode: &exitCode, output: tail.String(), result: tail.String()}
	switch {
	case ctx.Err() != nil:
		outcome.state, outcome.reason = store.StepInterrupted, ReasonInterrupted
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		outcome.state, outcome.reason = store.StepFailed, ReasonTimeout
	case exitCode != 0 && r.interrupting(env.task.ID):
		// A cancel's or shutdown's Terminate reaches the process before the
		// run context is canceled (§6's grace, §12.4's) — an abrupt exit then
		// is an interruption, not a failure to retry.
		outcome.state, outcome.reason = store.StepInterrupted, ReasonInterrupted
	case exitCode != 0:
		outcome.state, outcome.reason = store.StepFailed, ReasonNonzeroExit
	default:
		outcome.state = store.StepSucceeded
	}
	return outcome
}

// exitCodeOf extracts the process exit code from Wait's error.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// publishAgentEvent maps a normalized agent stream event onto the §13.3
// live-output chunk types. Events that carry nothing a client renders
// (results, errors — those surface as step outcomes) are skipped.
func (r *Runner) publishAgentEvent(taskID int64, ev agent.Event) {
	switch ev.Type {
	case agent.EventOutput:
		if ev.Text == "" {
			return
		}
		r.publishOutput(taskID, "agent.output", map[string]any{"text": ev.Text})
	case agent.EventToolUse:
		names := make([]string, 0, len(ev.Tools))
		for _, t := range ev.Tools {
			names = append(names, t.Name)
		}
		r.publishOutput(taskID, "agent.tool_use", map[string]any{"tools": names})
	case agent.EventUsage:
		// Usage payloads are adapter-native; the raw line is the honest shape.
		r.publishOutput(taskID, "agent.usage", map[string]any{"raw": string(ev.Raw)})
	case agent.EventInputRequest, agent.EventResult, agent.EventError, agent.EventUnknown:
	}
}

// commandEnv builds the environment of a command or check step: the
// daemon's own environment, the §8.5 vincent variables, then the step's
// declared `env` (which wins, so a workflow can override anything).
func commandEnv(rc workflow.RenderContext, stepEnvVars map[string]string) []string {
	out := os.Environ()
	out = append(out, workflow.Env(rc)...)
	for k, v := range stepEnvVars {
		out = append(out, k+"="+v)
	}
	return out
}
