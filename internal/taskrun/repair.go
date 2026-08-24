package taskrun

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
)

// RepairStepID is the reserved step id every ad-hoc repair run is recorded
// under (§5.4, task 025). The row sits at the *blocked step's* index, so the
// composite key `(task_id, step_index, step_id, iteration)` that
// store.CountStepAttempts already keys on is what keeps a repair out of that
// step's retry budget — no query changes, and no `kind` column.
//
// It starts with an underscore, which no workflow step id may (ids are slugs,
// §8.1), so it can never collide with a step somebody wrote. `attempt`
// numbers repairs of one step independently: a second repair is attempt 2 of
// the repair, not attempt N+1 of the step.
const RepairStepID = "__repair"

// repairTranscriptLines is how much of the failed attempt's transcript is
// inlined in the repair prompt. It is §8.4's existing failure-block bound
// reused rather than invented: the whole file is capped at
// `transcript_max_bytes`, which is megabytes, and its path goes in the prompt
// so the agent can read the rest itself.
const repairTranscriptLines = 200

// repairTranscriptTailBytes bounds how much of the transcript file is read to
// find those lines. A runaway attempt can leave megabytes of JSONL, and only
// the end of it is wanted.
const repairTranscriptTailBytes = 512 << 10

// runRepair executes one ad-hoc repair admission (§6, task 025) and returns
// the task to `blocked`.
//
// The repair is an ordinary agent step in every mechanical respect: it goes
// through runStepWithRetries, so it gets a step_runs row, a transcript,
// step.started/step.finished events and token and cost accounting for free.
// That is the same shape the fan-out `on_conflict: agent` resolver already
// uses (join.go) — a synthetic step run through the ordinary executor with
// `inGroup` set.
//
// What it deliberately does *not* do is decide anything. Whatever the agent
// exits with, the task goes back to `blocked` at the same step carrying the
// reason it was blocked with, and the human retries, repairs again, skips or
// cancels. Auto-retrying on success was rejected: the operator would never
// see the repair's diff before the step re-ran, and a repair agent's exit
// code is not the right thing to authorize more agent spend with (§7.2 — a
// human decides what a machine could not).
func (r *Runner) runRepair(
	ctx context.Context, task *store.Task, project *store.Project,
	wf *workflow.Workflow, req store.RepairRequest, log *slog.Logger,
) {
	// A cancel or shutdown that landed between admission and here must not
	// spawn a process for a task that is already ending; the request stays
	// set, so the repair re-runs when the task is admitted again.
	if ctx.Err() != nil || r.interrupting(task.ID) {
		r.interrupt(task, log)
		return
	}
	index := task.CurrentStep
	if index < 0 {
		index = 0
	}
	if index >= len(wf.Steps) {
		// The cursor is past the end: nothing failed here, so there is no
		// failure context to gather and nothing sensible to repair.
		log.Warn("repair requested at a step that does not exist", "step_index", index)
		r.finishRepair(task, req, log)
		return
	}
	log = log.With("repair_step_index", index)

	prompt := r.repairPrompt(ctx, task, project, wf, index, req)
	noRetries := 0
	env := &stepEnv{
		task: task, project: project, wf: wf,
		index: index,
		// inGroup is what gives the row and its transcript a name of their
		// own at an index another step already owns, exactly as a `parallel`
		// sub-step gets one (task 014 decision 16).
		inGroup: true,
		step: workflow.Step{
			ID:   RepairStepID,
			Name: "repair",
			Type: workflow.StepAgent,
			// Literal text, escaped: the operator typed prose at a form, and
			// §8.4 renders with missingkey=error (task 025).
			Prompt: workflow.EscapeTemplate(prompt),
			// A failed repair fails fast rather than silently paying for a
			// second agent run — the built-in `adhoc` workflow's reasoning
			// (phase 2 decision) applied to the same shape of one-off run.
			MaxRetries: &noRetries,
			// The request stands in for the step level of §8.6's chain:
			// request > task override > workflow `defaults` > adapter
			// default. The blocked step's own selection is deliberately not
			// the base — a `command` step has none.
			Agent:  req.Agent,
			Model:  req.Model,
			Effort: req.Effort,
		},
		log: log.With("step", RepairStepID),
	}
	outcome := r.runStepWithRetries(ctx, env)
	if outcome.state == store.StepInterrupted {
		// Crash, shutdown or cancel. The request is *not* drained, so the
		// next admission runs a repair again rather than silently turning it
		// into a plain retry of the blocked step (§12.4, task 025).
		if outcome.reason == ReasonUsageLimit {
			r.holdForUsageLimit(task, outcome.agentName, outcome.retryAfter, log)
			return
		}
		r.interrupt(task, log)
		return
	}
	log.Info("repair finished; returning the task to blocked",
		"outcome", string(outcome.state), "reason", outcome.reason)
	r.finishRepair(task, req, log)
}

// finishRepair returns the task to `blocked` at the same step with the reason
// it was blocked with before, draining the request in the same transition.
//
// Restoring the reason is not cosmetic: applyChange clears `block_reason` on
// any move off `blocked`, so without it a repaired task would come back with
// no reason at all and every client that keys off one would lose the thread.
func (r *Runner) finishRepair(task *store.Task, req store.RepairRequest, log *slog.Logger) {
	reason := req.BlockReason
	var drained store.RepairRequest // empty clears the column
	ch := store.TaskChange{BlockReason: &reason, PendingRepair: &drained}
	if r.transition(task, taskstate.Fail, ch, log) {
		log.Warn("task blocked again after a repair", "reason", reason)
	}
}

// repairPrompt assembles what the repair agent is told: the task's own
// context, the blocked step's definition and failure, the tail of the failed
// attempt's transcript and where the rest of it is, then the operator's
// prompt.
//
// Everything but the last part is written by the daemon. The operator's text
// is appended verbatim (the caller escapes the whole thing once), because it
// is prose rather than a workflow-authoring surface — which is what separates
// a repair prompt from `edit + retry`, whose override lands in the task's
// snapshot.
func (r *Runner) repairPrompt(
	ctx context.Context, task *store.Task, project *store.Project,
	wf *workflow.Workflow, index int, req store.RepairRequest,
) string {
	step := wf.Steps[index]
	var sb strings.Builder
	sb.WriteString("You are repairing a blocked task in a git worktree.\n\n")
	sb.WriteString("A workflow step failed and the task is waiting for a human. " +
		"Investigate and change the worktree so that step can succeed on its next run. " +
		"You are working in the task's existing worktree, on its branch: the files are " +
		"as the failed step left them.\n\n")
	sb.WriteString("Do not re-run the workflow and do not try to mark the step passed. " +
		"When you are done, the task returns to its blocked state and a human decides " +
		"whether to retry the step.\n\n")

	fmt.Fprintf(&sb, "<task id=%q>\n", fmt.Sprint(task.ID))
	fmt.Fprintf(&sb, "title: %s\n", task.Title)
	if task.Description != "" {
		sb.WriteString("description:\n")
		sb.WriteString(task.Description)
		if !strings.HasSuffix(task.Description, "\n") {
			sb.WriteString("\n")
		}
	}
	if len(task.Fields) > 0 {
		sb.WriteString("fields:\n")
		for _, k := range sortedKeys(task.Fields) {
			fmt.Fprintf(&sb, "  %s: %s\n", k, task.Fields[k])
		}
	}
	fmt.Fprintf(&sb, "workflow: %s\n", wf.Name)
	fmt.Fprintf(&sb, "project: %s (%s)\n", project.Name, project.Path)
	fmt.Fprintf(&sb, "branch: %s (from %s)\n", task.BranchName, task.BaseBranch)
	fmt.Fprintf(&sb, "worktree: %s\n", task.WorktreePath)
	sb.WriteString("</task>\n\n")

	fmt.Fprintf(&sb, "<blocked-step index=%q id=%q type=%q>\n",
		fmt.Sprint(index+1), step.ID, step.Type)
	if req.BlockReason != "" {
		fmt.Fprintf(&sb, "block reason: %s\n", req.BlockReason)
	}
	run := r.lastAttemptAt(ctx, task.ID, index)
	if run != nil {
		if run.FailureReason != "" && run.FailureReason != req.BlockReason {
			fmt.Fprintf(&sb, "failure reason: %s\n", run.FailureReason)
		}
		if run.ExitCode != nil {
			fmt.Fprintf(&sb, "exit code: %d\n", *run.ExitCode)
		}
		if run.CheckExitCode != nil {
			fmt.Fprintf(&sb, "check exit code: %d\n", *run.CheckExitCode)
		}
	}
	if body, field := r.renderBlockedStep(ctx, task, project, wf, index); body != "" {
		fmt.Fprintf(&sb, "--- %s ---\n", field)
		sb.WriteString(strings.TrimSuffix(body, "\n"))
		sb.WriteString("\n")
	}
	if step.Check != "" {
		sb.WriteString("--- check ---\n")
		sb.WriteString(strings.TrimSuffix(step.Check, "\n") + "\n")
	}
	if run != nil && run.ResultSummary != "" {
		sb.WriteString("--- result ---\n")
		sb.WriteString(strings.TrimSuffix(run.ResultSummary, "\n") + "\n")
	}
	if run != nil && run.TranscriptPath != "" {
		fmt.Fprintf(&sb, "--- transcript: %s ---\n", run.TranscriptPath)
		if tail := tailLines(run.TranscriptPath, repairTranscriptLines, repairTranscriptTailBytes); tail != "" {
			fmt.Fprintf(&sb, "(last %d lines; read the file above for the rest)\n", repairTranscriptLines)
			sb.WriteString(tail + "\n")
		}
	}
	sb.WriteString("</blocked-step>\n\n")

	sb.WriteString("<repair-instructions>\n")
	sb.WriteString(strings.TrimSuffix(req.Prompt, "\n"))
	sb.WriteString("\n</repair-instructions>\n")
	return sb.String()
}

// renderBlockedStep renders the blocked step's prompt or command against the
// §8.4 context, so the repair agent reads what the step actually ran rather
// than its un-substituted template. A template that no longer renders falls
// back to the raw text: the point is to show the agent what it is looking at,
// and a render failure is itself worth seeing.
func (r *Runner) renderBlockedStep(
	ctx context.Context, task *store.Task, project *store.Project,
	wf *workflow.Workflow, index int,
) (body, field string) {
	step := wf.Steps[index]
	switch step.Type {
	case workflow.StepAgent:
		body, field = step.Prompt, "prompt"
	case workflow.StepCommand:
		body, field = step.Run, "command"
	case workflow.StepManual:
		body, field = step.Instructions, "instructions"
	default:
		return "", ""
	}
	if body == "" {
		return "", field
	}
	env := &stepEnv{
		task: task, project: project, wf: wf, step: step, index: index,
		log: r.deps.Logger,
	}
	rc, err := r.renderContext(ctx, env, 1, stepOutcome{})
	if err != nil {
		return body, field
	}
	rendered, err := workflow.Render(field, body, rc)
	if err != nil {
		return body, field
	}
	return rendered, field
}

// lastAttemptAt is the most recent real attempt of the blocked step — the one
// whose failure the repair is about. Repair rows are skipped: an earlier
// repair is not what blocked the task, and quoting its transcript back at the
// next repair would replace the evidence with a copy of the last attempt to
// remove it.
func (r *Runner) lastAttemptAt(ctx context.Context, taskID int64, index int) *store.StepRun {
	runs, err := r.deps.Store.ListStepRunsAt(ctx, taskID, index)
	if err != nil {
		r.deps.Logger.Warn("repair: step history unavailable", "task", taskID, "error", err)
		return nil
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].StepID == RepairStepID {
			continue
		}
		return &runs[i]
	}
	return nil
}

// sortedKeys orders a field map so the prompt is byte-identical across runs
// with the same inputs — Go map order is not.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tailLines returns the last n lines of the file at path, reading at most
// maxBytes from its end. A file that cannot be read yields "" — the prompt
// still carries the path, and a missing transcript must not fail a repair the
// human asked for.
func tailLines(path string, n int, maxBytes int64) string {
	f, err := os.Open(path) //nolint:gosec // the path comes from this daemon's own step_runs row
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	partial := false
	if size := info.Size(); size > maxBytes {
		if _, err := f.Seek(size-maxBytes, io.SeekStart); err != nil {
			return ""
		}
		// The seek lands mid-line; that fragment is not a transcript record
		// and would be handed to the agent as malformed JSON.
		partial = true
	}
	sc := bufio.NewScanner(f)
	// A JSONL transcript line can be long (a whole agent message); the
	// default 64 KiB token bound would stop the scan at the first one.
	sc.Buffer(make([]byte, 0, 64<<10), int(maxBytes))
	// maxBytes, not §8.4's own tail bound: this reader has already narrowed
	// the file to the window the repair prompt asked for.
	lines := newOutputTailBytes(n, int(maxBytes))
	for sc.Scan() {
		if partial {
			partial = false
			continue
		}
		lines.add(sc.Text())
	}
	return lines.String()
}
