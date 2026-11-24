package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// A task a `condition` step ended is done, not failed (§7.7): the step
// evaluated perfectly and its answer was "stop". It read like a failure all
// the same — the step list showed a failed command and a stopped row with no
// status — and a chat was opened to ask why the task "did not pick any
// issue" (#1068). The outcome card now says it ended early, at which
// condition, and what the work last reported before it.

// endedEarly is the condition step that ended a done task, the last status
// any attempt reported before it, and the failed attempt just ahead of it
// whose exit the condition read; ok is false for a task no condition ended.
type endedEarly struct {
	stop    apiclient.StepRun
	status  string
	allowed *apiclient.StepRun
}

func conditionEnded(task apiclient.TaskDetail) (endedEarly, bool) {
	if task.State != stateDone {
		return endedEarly{}, false
	}
	runs := append([]apiclient.StepRun(nil), task.Steps...)
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	at := -1
	for i, r := range runs {
		// A loop body's condition ends one pass, not the task.
		if r.State == stepStateStopped && r.Iteration == 0 {
			at = i
		}
	}
	if at < 0 {
		return endedEarly{}, false
	}
	e := endedEarly{stop: runs[at]}
	for i := at - 1; i >= 0; i-- {
		if s := statusOf(runs[i]); s != "" {
			e.status = s
			break
		}
	}
	if at > 0 && runs[at-1].State == "failed" && runs[at-1].StepIndex < e.stop.StepIndex {
		prev := runs[at-1]
		e.allowed = &prev
	}
	return e, true
}

// endedEarlyLines are the outcome card's lines for it.
func endedEarlyLines(e endedEarly) []string {
	out := []string{styleWarn.Render(fmt.Sprintf("  ■ ended early at step %d %s — its condition was false",
		e.stop.StepIndex+1, stepLabel(e.stop)))}
	if e.stop.RenderedIf != nil && strings.TrimSpace(*e.stop.RenderedIf) != "" {
		out = append(out, styleDim.Render("    if: "+oneLine(*e.stop.RenderedIf)))
	}
	if e.allowed != nil {
		what := "failed"
		if e.allowed.ExitCode != nil {
			what = fmt.Sprintf("exited %d", *e.allowed.ExitCode)
		}
		out = append(out, styleDim.Render(fmt.Sprintf("    step %d %s %s, which the condition read — not a failure",
			e.allowed.StepIndex+1, stepLabel(*e.allowed), what)))
	}
	if e.status != "" {
		out = append(out, styleDim.Render("    last status before it  “"+e.status+"”"))
	}
	return out
}

// allowedFailure is whether a step's newest attempt failed and the task went
// on past it anyway — which the engine does only for a step whose
// `allow_failure:` turned the failure into an advance (§7.7). The rail and
// the pips draw it as passed, not failed (#1068); the attempt row keeps its
// true state.
func allowedFailure(task apiclient.Task, index int, state string) bool {
	if state != "failed" || task.StepTotal <= 0 {
		return false
	}
	return index < task.CurrentStep || task.State == stateDone
}
