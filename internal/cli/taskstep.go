package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `vincent task show <id> --step RUN` (task 101): what one attempt was
// actually given, as text. It is the TUI's Step Details tab read from a shell
// — the same four sections in the same order, and the same rule for when a
// field appears — but it is new code rather than the tab's lipgloss panes,
// and it is ASCII throughout (task 047 decision 7). `--json` is the API's
// StepRun, unchanged, so there is no second shape to keep in sync.

// stepNotRecorded is what a nil recorded field says: the record does not
// exist for this attempt. Printing an empty body instead would claim the step
// was handed nothing, which task 088 insists is a different fact.
const stepNotRecorded = "not recorded (this attempt predates the record)"

// stepRenderedEmpty is a recorded render that genuinely came out empty.
const stepRenderedEmpty = "(rendered empty)"

// stepFactWidth is the label column of a section's facts, wide enough for
// the longest label ("edited before retry").
const stepFactWidth = 20

// findStepRun is the attempt with this step_run id among the task's own runs.
// A fan-out lane's runs belong to the lane task (task 088 decision 9), so
// they are not found on the parent.
func findStepRun(t apiclient.TaskDetail, runID int64) (apiclient.StepRun, bool) {
	for _, s := range t.Steps {
		if s.ID == runID {
			return s, true
		}
	}
	return apiclient.StepRun{}, false
}

// printStepRun is `task show --step`: the one attempt, or exit 1 when the id
// is not one of this task's runs.
func printStepRun(out, errOut io.Writer, t apiclient.TaskDetail, runID int64, asJSON bool) error {
	run, ok := findStepRun(t, runID)
	if !ok {
		_, _ = fmt.Fprintf(errOut, "Error: task %d has no step run %d\n", t.ID, runID)
		return exitError{code: 1}
	}
	if asJSON {
		return emitJSON(out, run)
	}
	_, err := io.WriteString(out, renderStepRun(run, stepDefFor(t.WorkflowSteps, run), t.LaneID, time.Now()))
	return err
}

// stepDefFor is this attempt's step in the task's workflow snapshot, where the
// §7.9 include chain lives — the TUI's stepDefFor, over the same field. The id
// is tried first: a `parallel` group's sub-steps share their group's index
// (task 014), so the index alone would answer with the group.
func stepDefFor(steps []apiclient.WorkflowStep, run apiclient.StepRun) apiclient.WorkflowStep {
	if run.StepID != "" {
		for _, step := range steps {
			if step.ID == run.StepID {
				return step
			}
		}
	}
	for _, step := range steps {
		if step.Index == run.StepIndex {
			return step
		}
	}
	return apiclient.WorkflowStep{}
}

// renderStepRun is the whole text view of one attempt. lane is the task's own
// fan-out lane id, nil outside one.
func renderStepRun(run apiclient.StepRun, def apiclient.WorkflowStep, lane *string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "run %d  step %s  attempt %d  state %s\n",
		run.ID, dash(run.StepID), run.Attempt, run.State)
	writeStepSection(&b, "input", stepInputLines(run))
	writeStepSection(&b, "resolution", stepFactLines([][2]string{
		{"agent", stepSourced(run.Agent, run.AgentSource)},
		{"model", stepSourced(run.Model, run.ModelSource)},
		{"effort", stepSourced(run.Effort, run.EffortSource)},
		{"permission mode", stepRecorded(run.PermissionMode)},
		{"timeout", stepRecordedMS(run.TimeoutMS)},
		{"check timeout", stepRecordedMS(run.CheckTimeoutMS)},
		{"shell", stepRecorded(run.Shell)},
		{"working dir", stepRecorded(run.WorkDir)},
		{"resolved from", stepResolvedFrom(def)},
	}))
	writeStepSection(&b, "control flow", stepFactLines(stepControlFlowFacts(run, lane)))
	writeStepSection(&b, "outcome", stepFactLines(stepOutcomeFacts(run, now)))
	return b.String()
}

func writeStepSection(b *strings.Builder, title string, lines []string) {
	b.WriteString("\n" + title + ":\n")
	for _, line := range lines {
		b.WriteString(line + "\n")
	}
}

// stepInputLines is what the adapter or the shell was handed. An agent step
// always has a prompt entry and a command step a run entry, because the type
// guarantees one and its absence is a missing record; `check:` is a field
// either *may* carry (§8.2), so it appears only when it was recorded.
func stepInputLines(run apiclient.StepRun) []string {
	var out []string
	if run.InputTruncated {
		// Said out loud rather than elided: the record has a size ceiling,
		// and showing a prefix as though it were the whole is what task 088
		// refused.
		out = append(out, "  the record was cut at its 64 KiB ceiling; what follows is a prefix of what the step got")
	}
	if run.RenderedPrompt != nil || run.StepType == "agent" {
		out = append(out, "  rendered prompt:")
		out = append(out, stepBody(run.RenderedPrompt, true)...)
	}
	if run.RenderedRun != nil || run.StepType == "command" {
		out = append(out, "  rendered run:")
		out = append(out, stepBody(run.RenderedRun, false)...)
	}
	if run.RenderedCheck != nil {
		out = append(out, "  rendered check:")
		out = append(out, stepBody(run.RenderedCheck, false)...)
	}
	out = append(out, "  result summary:")
	if strings.TrimSpace(run.ResultSummary) == "" {
		return append(out, "    none")
	}
	return append(out, indentLines(run.ResultSummary)...)
}

// stepBody prints one recorded input in full. markTrailer is set for an agent
// step's prompt, where the daemon appends the previous attempt's failure on a
// retry (task 088 decision 3): the join is marked so a reader can tell what
// the workflow wrote from what vincent added.
func stepBody(value *string, markTrailer bool) []string {
	switch {
	case value == nil:
		return []string{"    " + stepNotRecorded}
	case *value == "":
		return []string{"    " + stepRenderedEmpty}
	case !markTrailer:
		return indentLines(*value)
	}
	body, trailer := apiclient.SplitFailureTrailer(*value)
	out := indentLines(body)
	if trailer == "" {
		return out
	}
	out = append(out, "    --- appended by vincent: the previous attempt's failure ---")
	return append(out, indentLines(trailer)...)
}

// indentLines indents every line of a body under its entry. Blank lines stay
// blank rather than carrying trailing whitespace; the exact bytes are
// `--json`'s job.
func indentLines(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			line = "    " + line
		}
		lines[i] = line
	}
	return lines
}

// stepFactLines lays out label/value pairs, with a multi-line value's
// continuation lines under the value column.
func stepFactLines(facts [][2]string) []string {
	out := make([]string, 0, len(facts))
	pad := strings.Repeat(" ", 2+stepFactWidth+1)
	for _, f := range facts {
		for i, line := range strings.Split(f[1], "\n") {
			if i == 0 {
				out = append(out, fmt.Sprintf("  %-*s %s", stepFactWidth, f[0], line))
				continue
			}
			out = append(out, pad+line)
		}
	}
	return out
}

// stepSourced is a resolved value with the level that supplied it (§8.6).
// The value without its level is still worth showing: an older row has the
// one and not the other.
func stepSourced(value, source *string) string {
	if value == nil || *value == "" {
		return stepNotRecorded
	}
	if source == nil || *source == "" {
		return *value + " (level not recorded)"
	}
	return *value + " (from the " + *source + ")"
}

func stepRecorded(value *string) string {
	if value == nil || *value == "" {
		return stepNotRecorded
	}
	return *value
}

func stepRecordedMS(ms int64) string {
	if ms <= 0 {
		return stepNotRecorded
	}
	return stepDuration(time.Duration(ms) * time.Millisecond)
}

// stepDuration is a duration without Go's trailing zero units: "30m", not
// "30m0s". Under a second it keeps milliseconds, so a quick command step does
// not read as having taken no time at all.
func stepDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	s := d.Round(time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// stepResolvedFrom is the §7.9 include chain off the task's snapshot. An
// empty chain is a step the task's own workflow wrote.
func stepResolvedFrom(def apiclient.WorkflowStep) string {
	if len(def.ResolvedFrom) == 0 {
		return "this task's own workflow"
	}
	return strings.Join(def.ResolvedFrom, " -> ")
}

// stepControlFlowFacts is what decided that this attempt ran, and on what.
func stepControlFlowFacts(run apiclient.StepRun, lane *string) [][2]string {
	var facts [][2]string
	// A guard is re-evaluated every time it is reached and never sticky
	// (§7.7), so this is what it rendered to on this attempt. A step with no
	// `if:` has no record missing, so it is simply not asked about.
	if run.RenderedIf != nil {
		facts = append(facts, [2]string{"if: rendered to", valueOrRenderedEmpty(*run.RenderedIf)})
	}
	iteration := "not inside a loop"
	switch {
	case run.Iteration > 0 && run.LoopTotal > 0:
		iteration = fmt.Sprintf("%d of %d", run.Iteration, run.LoopTotal)
	case run.Iteration > 0:
		iteration = fmt.Sprintf("%d (total not recorded)", run.Iteration)
	}
	facts = append(facts, [2]string{"iteration", iteration})
	if run.LoopItem != nil {
		facts = append(facts, [2]string{"for_each item", valueOrRenderedEmpty(*run.LoopItem)})
	}
	if run.RenderedForEach != nil {
		facts = append(facts, [2]string{"for_each list", stepForEachValue(*run.RenderedForEach)})
	}
	if lane != nil && *lane != "" {
		facts = append(facts, [2]string{"fan-out lane", *lane})
	}
	return facts
}

func valueOrRenderedEmpty(s string) string {
	if s == "" {
		return stepRenderedEmpty
	}
	return s
}

// stepForEachValue prints the resolved list as its items. Bytes that do not
// parse are shown raw rather than swallowed: the record is evidence.
func stepForEachValue(raw string) string {
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return raw
	}
	if len(items) == 0 {
		return "resolved to no items"
	}
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = strconv.Itoa(i+1) + ". " + item
	}
	return strings.Join(lines, "\n")
}

// stepOutcomeFacts is what the attempt cost and what became of it.
func stepOutcomeFacts(run apiclient.StepRun, now time.Time) [][2]string {
	tokens := "not reported"
	if run.InputTokens != nil || run.OutputTokens != nil {
		var in, out int64
		if run.InputTokens != nil {
			in = *run.InputTokens
		}
		if run.OutputTokens != nil {
			out = *run.OutputTokens
		}
		tokens = fmt.Sprintf("%d in / %d out", in, out)
	}
	// nil is "nothing reported one", which is not the same as free.
	cost := "not reported"
	if run.CostUSD != nil {
		cost = fmt.Sprintf("$%.4f", *run.CostUSD)
	}
	active := "not started"
	if d, ok := run.Duration(now); ok {
		active = stepDuration(d)
	}
	wait := "none"
	if run.InputWaitMS > 0 {
		wait = stepDuration(time.Duration(run.InputWaitMS) * time.Millisecond)
	}
	return [][2]string{
		{"tokens", tokens},
		{"cost", cost},
		{"active duration", active},
		{"waiting on a human", wait},
		{"exit code", stepExitCode(run.ExitCode)},
		{"check exit code", stepExitCode(run.CheckExitCode)},
		{"failure reason", stepOptional(run.FailureReason, "none")},
		{"skip reason", stepOptional(run.SkipReason, "none")},
		{"edited before retry", stepOverrideValue(run)},
		{"transcript", stepOptional(run.TranscriptPath, "none - this step produced no transcript")},
	}
}

func stepExitCode(code *int) string {
	if code == nil {
		return "none"
	}
	return strconv.Itoa(*code)
}

func stepOptional(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}

// stepOverrideValue is the §6 edit+retry badge: a human replaced the step's
// text before this attempt ran, which is why the rendered input may not match
// the workflow snapshot at all.
func stepOverrideValue(run apiclient.StepRun) string {
	switch {
	case run.PromptOverride && run.RunOverride:
		return "yes - the prompt and the command"
	case run.PromptOverride:
		return "yes - the prompt"
	case run.RunOverride:
		return "yes - the command"
	}
	return "no"
}
