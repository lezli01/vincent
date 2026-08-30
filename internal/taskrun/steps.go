package taskrun

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/container"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
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
	inputTimeout := resolveInputTimeout(env.step, env.wf.Defaults, r.deps.Config())
	onInput := resolveInputPolicy(env.step, env.wf.Defaults)

	// The §7.4 `require` pre-flight (task 013). It runs before Start rather
	// than as an adapter error because `require` is a precondition, not a
	// behaviour: once the process is up, `require` and `wait` are the same
	// thing, so the adapter has no reason to know which it was given. Only a
	// positive "cannot" fails the step — an absent binary is
	// agent_unavailable's business, and an unprobed one is nobody's.
	if env.wf.StepRequiresInput(env.step) &&
		r.deps.Catalog.InputVerdict(ctx, sel.Agent) == agent.InputUnsupported {
		tr.Note("error", map[string]any{
			"error": "agent " + sel.Agent + " cannot take mid-run input, which this step requires",
		})
		env.log.Error("step requires mid-run input", "agent", sel.Agent)
		return stepOutcome{state: store.StepFailed, reason: ReasonInputUnsupported}
	}

	// The step clock is actor-managed (input.go) so it can pause while the
	// task waits in awaiting_input (§7.4); a plain context deadline can't.
	// Cancel causes tell classifyAgent why the tree was killed.
	runCtx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(nil)

	// §13.4's per-step endpoint (task 057): minted here so its secret lives
	// exactly as long as the attempt, and released on every exit path.
	var mcpSrv *agent.MCPServer
	if r.deps.MCPForStep != nil {
		srv, release := r.deps.MCPForStep(run.ID, env.task.ID, env.step.ID)
		if release != nil {
			defer release()
		}
		mcpSrv = srv
	}

	handle, err := adapter.Start(runCtx, agent.RunSpec{
		Prompt:         prompt,
		WorkDir:        env.task.WorktreePath,
		Model:          sel.Model,
		Effort:         sel.Effort,
		PermissionMode: resolvePermission(env.wf, env.step),
		OnInput:        onInput,
		// Always explicit, even when the policy inherits everything (T4.23).
		// Passing nil would hand the adapter the ambient environment again,
		// and "decided" is the whole point: what a step runs under is now a
		// value this engine computed, not whatever the daemon was started
		// from.
		//
		// It carries the §8.5 VINCENT_* block too, as of task 036: an agent
		// step used to see the resolved base and none of the run facts, so
		// an agent could not name the step it was running even to the daemon
		// that started it. `vincent status` addresses itself with
		// VINCENT_TASK_ID and VINCENT_STEP_ID, which is what made the gap
		// load-bearing. Same precedence rule as a command step's — the
		// variables layer over the policy, so `environment.unset` cannot
		// reach them. There is no step-level `env:` here: `env:` is a
		// command-step field (§8.1).
		Env: commandEnv(r.childEnv(), rc, nil),
		MCP: mcpSrv,
	})
	if err != nil {
		tr.Note("error", map[string]any{"error": err.Error()})
		env.log.Error("start agent", "agent", sel.Agent, "error", err)
		// An adapter that cannot restrict on this platform is not a missing
		// adapter (§9.4); saying so keeps the user from reinstalling a CLI
		// that is present and working.
		reason := ReasonAgentUnavailable
		switch {
		case errors.Is(err, agent.ErrRestrictedUnsupported):
			reason = ReasonRestrictedUnsupported
		case errors.Is(err, agent.ErrMCPUnsupported):
			// Loud rather than silent (§13.4, decision 8): a step wired to
			// the vincent tools that cannot have them has not run correctly,
			// even if the CLI would happily start without them.
			reason = ReasonMCPUnsupported
		}
		return stepOutcome{state: store.StepFailed, reason: reason}
	}
	// Register the live process so a cancel can reach it (§6).
	defer r.setProc(env.task.ID, handle)()

	// The §12.3 debug record: what this step actually resolved to and how the
	// process was actually invoked. Written into the transcript rather than
	// the daemon log so it travels with the run a user pastes into an issue.
	if r.deps.Config().Debug {
		tr.Note("debug", map[string]any{
			"agent":           sel.Agent,
			"model":           sel.Model,
			"effort":          sel.Effort,
			"permission_mode": string(resolvePermission(env.wf, env.step)),
			"on_input":        string(onInput),
			"workdir":         env.task.WorktreePath,
			"argv":            redactMCPToken(handle.Argv(), mcpSrv),
			"timeout":         timeout.String(),
			"attempt":         run.Attempt,
		})
	}

	pid := handle.PID()
	started := time.Now()
	run.PID = &pid
	run.ProcStartedAt = &started
	run.ProcIdentity = journalIdentity(pid, env.log)
	if err := r.deps.Store.UpdateStepRun(r.persistCtx(), run); err != nil {
		env.log.Error("journal agent pid", "error", err)
	}
	env.log.Info("agent step running",
		"agent", sel.Agent, "pid", pid, "model", sel.Model, "effort", sel.Effort)

	clock := newStepClock(timeout, r.now, func() { cancelCause(errStepTimeout) })
	defer clock.stop()

	var answers chan agent.InputResponse
	if lr, ok := r.lookupRun(env.task.ID); ok {
		answers = lr.answers
	}

	tail := newOutputTail(outputTailLines)
	events := handle.Events()
	var (
		parked      bool // task is in awaiting_input; this actor stays (§7.4)
		inputTimer  *time.Timer
		inputTimerC <-chan time.Time
		waitStart   time.Time
		inputWaitMS int64
	)
	// endWait leaves the parked state: account the wait, resume the clock.
	endWait := func() {
		if inputTimer != nil {
			inputTimer.Stop()
			inputTimer, inputTimerC = nil, nil
		}
		inputWaitMS += r.now().Sub(waitStart).Milliseconds()
		clock.resume()
		parked = false
	}
	// consume transcripts and publishes one event like the plain loop did.
	// The transcript write comes first so the published offset is a position
	// the file has actually reached (§13.3).
	consume := func(ev agent.Event) {
		offset := tr.Raw(ev.Raw)
		if ev.Type == agent.EventOutput && ev.Text != "" {
			tail.add(ev.Text)
		}
		r.publishAgentEvent(env.task.ID, run.ID, offset, ev)
		// An agent that will not stop talking is killed at the cap rather
		// than allowed to fill the disk (§12.3, §18). The stream is left to
		// drain so Wait still reports an exit; classifyAgent turns the cause
		// into transcript_limit.
		if tr.Exceeded() {
			tr.NoteOverLimit("transcript_limit", map[string]any{
				"max_bytes": r.deps.Config().TranscriptMaxBytes.Bytes(),
			})
			env.log.Warn("transcript limit exceeded", "task", env.task.ID, "step", env.step.ID)
			cancelCause(errTranscriptLimit)
		}
		// A transcript that stopped landing is polled in the same place and
		// kills for the same reason: the record this run is being kept for
		// is no longer being kept, so running on produces nothing (§12.2,
		// #139). classifyAgent turns the cause into transcript_io_error.
		if err := tr.Err(); err != nil {
			env.log.Error("transcript i/o error", "task", env.task.ID, "step", env.step.ID, "error", err)
			cancelCause(errTranscriptIO)
		}
	}
	// protocolError fails the attempt rather than wait on a request vincent
	// cannot render (§18): kill the tree, let the stream drain.
	protocolError := func(msg string) {
		tr.Note("input_protocol_error", map[string]any{"error": msg})
		env.log.Error("input protocol error", "error", msg)
		cancelCause(errInputProtocol)
	}

loop:
	for {
		if !parked {
			ev, ok := <-events
			if !ok {
				break loop
			}
			consume(ev)
			switch ev.Type { //nolint:exhaustive // consume() handled the rest
			case agent.EventInputRequest:
				switch {
				case ev.Request == nil:
					protocolError(ev.Message)
				case onInput == agent.InputDeny:
					r.autoDeny(env, handle, ev.Request, tr)
				default: // on_input: wait
					pendingJSON, err := encodePending(ev.Request)
					if err != nil {
						protocolError(err.Error())
						continue
					}
					summary := summarize(ev.Request)
					tr.Note("input_request", map[string]any{
						"kind": ev.Request.Kind, "summary": summary, "policy": string(agent.InputWait),
					})
					ch := store.TaskChange{
						PendingInput: &pendingJSON,
						EventPayload: map[string]any{"kind": ev.Request.Kind, "summary": summary},
					}
					// A lost CAS means a human canceled first; keep draining —
					// the kill is already on its way.
					if r.transition(env.task, taskstate.RequestInput, ch, env.log) {
						clock.pause()
						waitStart = r.now()
						inputTimer = time.NewTimer(inputTimeout)
						inputTimerC = inputTimer.C
						parked = true
						env.log.Info("task awaiting input", "kind", ev.Request.Kind)
					}
				}
			case agent.EventInputCanceled:
				// Nothing was pending engine-side (not parked): tolerated.
			}
			continue
		}

		// Parked: the agent is idle on its request. Wait for the answer, the
		// input_timeout, or the process/stream ending on its own.
		select {
		case resp := <-answers:
			// The handler already CASed awaiting_input → running (clearing
			// pending_input); mirror it so later engine CAS's see the truth.
			env.task.State = store.TaskRunning
			if err := handle.Respond(resp); err != nil {
				// The process died as the answer landed; the stream is about
				// to close and the attempt resolves from its exit (§18).
				env.log.Warn("deliver answer", "error", err)
				tr.Note("input_response_failed", map[string]any{"error": err.Error()})
			} else {
				tr.Note("input_response", map[string]any{
					"source": "human", "answers": resp.Answers,
					"allow": resp.Allow, "response": resp.Response,
				})
			}
			endWait()
		case <-inputTimerC:
			// Expiry races the answer's CAS; the transition decides (§7.4).
			if r.transition(env.task, taskstate.InputClosed, store.TaskChange{}, env.log) {
				tr.Note("input_timeout", map[string]any{"timeout": inputTimeout.String()})
				env.log.Warn("input request timed out", "timeout", inputTimeout)
				endWait()
				cancelCause(errInputTimeout)
			} else {
				// An answer won; it is already on the channel.
				inputTimerC = nil
			}
		case ev, ok := <-events:
			if !ok {
				// The process died while awaiting input: the request is void
				// and the attempt fails from its exit code (§18). The CAS can
				// lose only to a concurrent answer or cancel — either way the
				// state is already where it should be.
				if r.transition(env.task, taskstate.InputClosed, store.TaskChange{}, env.log) {
					tr.Note("input_request_aborted", map[string]any{"reason": "agent process exited"})
				}
				endWait()
				break loop
			}
			consume(ev)
			switch ev.Type { //nolint:exhaustive // consume() handled the rest
			case agent.EventInputCanceled:
				// The agent withdrew its request; the run continues (§7.4).
				if r.transition(env.task, taskstate.InputClosed, store.TaskChange{}, env.log) {
					tr.Note("input_request_withdrawn", map[string]any{})
					env.log.Info("input request withdrawn by the agent")
					endWait()
				}
			case agent.EventInputRequest:
				// The adapter guarantees serial requests; anything here is a
				// protocol violation (nil Request) or a contract break.
				if r.transition(env.task, taskstate.InputClosed, store.TaskChange{}, env.log) {
					endWait()
				}
				msg := ev.Message
				if msg == "" {
					msg = "second input request while one is pending"
				}
				protocolError(msg)
			}
		}
	}
	res, waitErr := handle.Wait()
	run.InputWaitMS = inputWaitMS

	outcome := classifyAgent(ctx, runCtx, r.interrupting(env.task.ID), &res, waitErr)
	// Which adapter this ran on travels with the outcome so a quota stop can
	// be recorded per agent rather than per task (task 026). collectGroup
	// returns an interrupted lane's whole outcome, so a stop inside a
	// `parallel` group keeps its attribution too.
	outcome.agentName = sel.Agent
	if outcome.state == store.StepSucceeded {
		// First-hand evidence that this adapter's window is open. It retires
		// an observation the daemon is still showing — most importantly one
		// whose reset was never reported by the CLI and is therefore only an
		// estimate. The body's success is enough: a `check` that fails
		// afterwards says nothing about the quota.
		r.clearUsageLimit(sel.Agent, r.now(), env.log)
	}
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
// an abrupt exit then is an interruption, not a failure to retry. The run
// context's cancel cause distinguishes the engine's own kills (§7.4).
//
// The adapter's own verdict (res.Failure, task 003) is consulted *after* all
// of those and *before* the generic exit-code and IsError buckets. The order
// is the point: a cancel or a shutdown that lands on a quota-stopped run is
// still a cancel, a step that ran past its timeout is still a timeout, and
// only a run that would otherwise have been flattened into nonzero_exit or
// agent_error gets the more specific reason.
func classifyAgent(daemonCtx, runCtx context.Context, interrupting bool, res *agent.RunResult, waitErr error) stepOutcome {
	switch {
	case daemonCtx.Err() != nil:
		return stepOutcome{state: store.StepInterrupted, reason: ReasonInterrupted}
	case causeIs(runCtx, errStepTimeout):
		return stepOutcome{state: store.StepFailed, reason: ReasonTimeout}
	case causeIs(runCtx, errInputTimeout):
		return stepOutcome{state: store.StepFailed, reason: ReasonInputTimeout}
	case causeIs(runCtx, errInputProtocol):
		return stepOutcome{state: store.StepFailed, reason: ReasonInputProtocolError}
	case causeIs(runCtx, errTranscriptLimit):
		return stepOutcome{state: store.StepFailed, reason: ReasonTranscriptLimit}
	case causeIs(runCtx, errTranscriptIO):
		return stepOutcome{state: store.StepFailed, reason: ReasonTranscriptIOError}
	case interrupting && (waitErr != nil || res.ExitCode != 0 || res.IsError):
		return stepOutcome{state: store.StepInterrupted, reason: ReasonInterrupted}
	case waitErr != nil:
		return stepOutcome{state: store.StepFailed, reason: ReasonInternalError}
	case res.Failure != nil && res.Failure.Kind == agent.FailureUsageLimit:
		// Interruption-shaped on purpose: runStepWithRetries returns any
		// non-StepFailed outcome without touching the budget, which is what
		// makes "consumes no retry" true with no change there (§7.2).
		return stepOutcome{
			state:      store.StepInterrupted,
			reason:     ReasonUsageLimit,
			retryAfter: res.Failure.RetryAfter,
		}
	case res.Failure != nil && res.Failure.Kind == agent.FailureUnauthenticated:
		return stepOutcome{state: store.StepFailed, reason: ReasonAgentUnauthenticated}
	case res.Failure != nil && res.Failure.Kind == agent.FailureStreamError:
		// vincent's own reader stopped before the stream did, so the
		// transcript is missing lines the CLI wrote (§9.1, #139). Named
		// separately from agent_error, which would blame a CLI that did
		// nothing wrong.
		return stepOutcome{state: store.StepFailed, reason: ReasonAgentProtocolError}
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
		env:      commandEnv(r.stepBaseEnv(env.task.ID), rc, env.step.Env),
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
		env:      commandEnv(r.stepBaseEnv(env.task.ID), rc, env.step.Env),
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
	tc := r.taskContainerOf(env.task.ID)
	shellName, argv := "", []string(nil)
	if tc.active() {
		// A containerized `run:` body executes under the *container's*
		// /bin/sh — the inverse of §8.3, accepted and documented rather than
		// translated (task 061 decision 8). The host's shell resolver is not
		// consulted at all: what it found says nothing about the image, and a
		// `pwsh` pin is refused at load or at task creation, before a step
		// ever reaches here.
		shellName = workflow.ShellSh
		argv = []string{"/bin/sh", "-c", sc.script}
	} else {
		sh, err := r.deps.Shells.For(sc.shellPin)
		if err != nil {
			tr.Note("error", map[string]any{"error": err.Error()})
			env.log.Error("resolve shell", "shell", sc.shellPin, "error", err)
			return stepOutcome{state: store.StepFailed, reason: ReasonShellUnavailable, result: err.Error()}
		}
		shellName, argv = sh.Name, sh.command(sc.script)
	}
	sh := struct {
		Name string
		Path string
	}{Name: shellName, Path: argv[0]}
	tr.Note("command_started", map[string]any{
		"phase": sc.phase, "shell": sh.Name, "command": sc.script, "cwd": sc.workDir,
	})
	if r.deps.Config().Debug {
		// The shell a command step gets is platform-resolved (§8.3), so
		// "which shell ran this" is exactly as invisible as an agent's argv
		// was, and just as often the answer.
		tr.Note("debug", map[string]any{
			"phase": sc.phase, "shell": sh.Name, "shell_path": sh.Path,
			"argv": argv, "cwd": sc.workDir, "timeout": sc.timeout.String(),
		})
	}

	runCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	key := execKey(run.ID)
	if tc.active() {
		// The step's own environment goes in as `--env` flags, and the host
		// process is the runtime client — which needs the daemon's own
		// environment to find its socket. Two environments, and the step's is
		// the one layered on the image's (decision 7).
		argv = tc.rt.Exec(tc.id, container.ExecSpec{
			Key: key, Argv: argv, Env: sc.env, WorkDir: sc.workDir, User: container.HostUser(),
		})
		sc.workDir, sc.env = "", nil
	}
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

	// Journal the PID and the process's native identity before any output
	// can arrive, so crash recovery can find and kill an orphaned command
	// tree (§12.4 covers any step process; a check overwrites the body's PID
	// — the column means "running now").
	pid := cmd.Process.Pid
	started := time.Now()
	run.PID = &pid
	run.ProcStartedAt = &started
	run.ProcIdentity = journalIdentity(pid, env.log)
	if tc.active() {
		// The host PID above names the runtime client, not the process inside
		// the container; the container id is what §12.4 recovery can act on
		// (task 061 decision 4, migration 0021).
		id := tc.id
		run.ContainerID = &id
	}
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
			killOnce.Do(func() {
				// Inside first, then the client. Killing only the host-side
				// runtime client leaves the process running in the container
				// (task 061 decision 9); the container itself survives, so a
				// retry finds what an earlier step installed.
				if tc.active() {
					stopInContainer(tc, key, env.log)
				}
				_ = proc.Kill()
			})
		case <-killed:
		}
	}()
	defer close(killed)

	tail := newOutputTail(outputTailLines)
	var wg sync.WaitGroup
	var mu sync.Mutex
	// limitOnce keeps the cap's annotation and kill to one, though both
	// stream goroutines can observe the overflow. ioOnce does the same for a
	// transcript that stopped landing.
	var limitOnce, ioOnce sync.Once
	// captureErr latches a stream vincent could not read to the end. Output
	// the process wrote and vincent never saw is evidence loss, exactly like
	// a transcript write that failed, and the classification below refuses to
	// call it a success (#139).
	var captureErr error
	stream := func(rd io.Reader, name string) {
		defer wg.Done()
		err := scanOutput(rd, func(text string, more bool) {
			// more marks a piece of a line longer than outputChunkBytes:
			// the next record on this stream continues it (#139). Marked
			// rather than joined silently, so a reader can tell one long
			// line from several short ones.
			fields := outputFields(sc.phase, name, text, more)
			offset := tr.Note("output", fields)
			r.publishOutput(env.task.ID, run.ID, offset, "command.output", fields)
			mu.Lock()
			tail.add(text)
			mu.Unlock()
			// A command that floods stdout is capped like a runaway agent
			// (§12.3, §18). Cancelling here rather than after the reader
			// finishes is the point: the whole failure mode is a process that
			// never stops producing, so waiting for it to stop is waiting
			// forever. Cancelling runCtx wakes the tree-killer above.
			if tr.Exceeded() {
				limitOnce.Do(func() {
					tr.NoteOverLimit("transcript_limit", map[string]any{
						"max_bytes": r.deps.Config().TranscriptMaxBytes.Bytes(),
					})
					env.log.Warn("transcript limit exceeded", "task", env.task.ID, "step", env.step.ID)
					cancel()
				})
			}
			// A transcript that stopped landing kills the run for a related
			// reason: the attempt is already doomed (the classification below
			// fails it either way), and every further minute of a 15-minute
			// command produces work that nothing records and a retry redoes.
			// The agent path does the same at its own Exceeded poll.
			if err := tr.Err(); err != nil {
				ioOnce.Do(func() {
					env.log.Error("transcript i/o error",
						"task", env.task.ID, "step", env.step.ID, "error", err)
					cancel()
				})
			}
		})
		if err != nil {
			// The pipe itself failed. Draining it cannot help — a reader that
			// errored will error again — and the process is about to be
			// waited on, so record the loss and let the classification below
			// terminalize the attempt.
			tr.Note("error", map[string]any{
				"error": "output capture stopped for " + name + ": " + err.Error(),
			})
			mu.Lock()
			if captureErr == nil {
				captureErr = err
			}
			mu.Unlock()
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
	// Ahead of the exit-code cases: the process was killed *because* of the
	// cap, so its nonzero exit is a consequence, not the diagnosis (§18).
	case tr.Exceeded():
		outcome.state, outcome.reason = store.StepFailed, ReasonTranscriptLimit
	// Below the cap, above the exit code: an attempt whose output was not
	// captured or not persisted cannot be judged from its exit status alone
	// (§7.1, §12.2, #139). It sits under transcript_limit because a run the
	// cap killed may well break its pipe on the way out, and the cap is the
	// diagnosis there.
	case captureErr != nil || tr.Err() != nil:
		outcome.state, outcome.reason = store.StepFailed, ReasonTranscriptIOError
		env.log.Error("step output incomplete", "task", env.task.ID, "step", env.step.ID,
			"capture_error", captureErr, "transcript_error", tr.Err())
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
func (r *Runner) publishAgentEvent(taskID, runID, offset int64, ev agent.Event) {
	switch ev.Type {
	case agent.EventOutput:
		if ev.Text == "" {
			return
		}
		r.publishOutput(taskID, runID, offset, "agent.output", map[string]any{"text": ev.Text})
	case agent.EventToolUse:
		r.publishOutput(taskID, runID, offset, "agent.tool_use",
			map[string]any{"tools": toolChunks(ev.Tools)})
	case agent.EventToolResult:
		r.publishOutput(taskID, runID, offset, "agent.tool_result",
			map[string]any{"results": resultChunks(ev.Results)})
	case agent.EventThinking:
		// Thinking goes live like everything else (T4.16). §9.7 held it back
		// when it meant one chunk per token; coalescing removed that, and a
		// record that appears only after a step finishes reads as output that
		// went missing while the step was running.
		if ev.Text == "" {
			return
		}
		r.publishOutput(taskID, runID, offset, "agent.thinking", map[string]any{"text": ev.Text})
	case agent.EventUsage:
		// Usage payloads are adapter-native; the raw line is the honest shape.
		r.publishOutput(taskID, runID, offset, "agent.usage", map[string]any{"raw": string(ev.Raw)})
	case agent.EventInputRequest, agent.EventInputCanceled,
		agent.EventResult, agent.EventError, agent.EventUnknown:
		// Input requests surface via the state change (§13.3); results and
		// errors surface as step outcomes.
	}
}

// toolChunks maps tool uses onto the §13.3 live-chunk shape. It must match
// what api.normalizeLine writes for the same event: a client renders the
// live tail and the fetched scrollback through one path, so a difference
// here shows up as output that changes when a step finishes.
func toolChunks(tools []agent.ToolUse) []map[string]string {
	out := make([]map[string]string, 0, len(tools))
	for _, t := range tools {
		chunk := map[string]string{"name": t.Name}
		if t.Summary != "" {
			chunk["summary"] = t.Summary
		}
		if t.CallID != "" {
			chunk["call_id"] = t.CallID
		}
		out = append(out, chunk)
	}
	return out
}

// resultChunks maps tool results onto the §13.3 live-chunk shape, matching
// what api.normalizeLine writes for the same event.
func resultChunks(results []agent.ToolResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		chunk := map[string]any{}
		for k, v := range map[string]string{
			"call_id": r.CallID, "name": r.Name, "summary": r.Summary,
		} {
			if v != "" {
				chunk[k] = v
			}
		}
		if r.IsError {
			chunk["is_error"] = true
		}
		out = append(out, chunk)
	}
	return out
}

// childEnv resolves the §12.3 environment policy against this process's own
// environment (T4.23).
//
// It is resolved per spawn rather than cached at startup because the config
// hot-reloads: a step that starts after a reload must run under the policy in
// force when it started, not the one the daemon booted with. Resolving is a
// map build over os.Environ() — cheaper by orders of magnitude than the
// process it is about to launch.
func (r *Runner) childEnv() []string {
	return r.deps.Config().Environment.ResolveProcess()
}

// stepBaseEnv is childEnv for a host step and the image-based environment for
// a containerized one (task 061 decision 7). It is one function rather than a
// branch at each call site because "which base" is a property of the task, and
// a call site that forgot to ask would silently ship a macOS PATH into a Linux
// image.
func (r *Runner) stepBaseEnv(taskID int64) []string {
	if !r.taskContainerOf(taskID).active() {
		return r.childEnv()
	}
	LogContainerEnvironmentOnce(r.deps.Logger, r.deps.Config().Environment)
	return r.containerEnv()
}

// commandEnv builds a step's environment: the §12.3 resolved base, then the
// §8.5 VINCENT_* variables, then the step's own `env:`. The order is the
// precedence — Go's exec keeps the last of any duplicate name — and it is why
// `environment.unset` cannot reach a VINCENT_* variable. Those are facts
// about the run, not inherited state.
//
// It serves `agent` steps as well as `command` and `check` ones (task 036);
// only the latter pass a stepEnvVars map, since `env:` is a command-step
// field.
func commandEnv(base []string, rc workflow.RenderContext, stepEnvVars map[string]string) []string {
	out := append([]string(nil), base...)
	out = append(out, workflow.Env(rc)...)
	for k, v := range stepEnvVars {
		out = append(out, k+"="+v)
	}
	return out
}

// redactMCPToken removes the §13.4 per-step bearer token from the argv the
// §12.3 debug record writes. claude carries its MCP configuration inline on
// the command line (§9.2), and a debug transcript is something people paste
// into issues — so the one place vincent copies that argv is the one place it
// has to be scrubbed. The token is short-lived and loopback-only, which bounds
// the cost; it does not make publishing it fine.
func redactMCPToken(argv []string, srv *agent.MCPServer) []string {
	if srv == nil || srv.Token == "" {
		return argv
	}
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = strings.ReplaceAll(a, srv.Token, "[redacted]")
	}
	return out
}
