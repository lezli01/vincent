package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// transcriptPollInterval paces `--follow`, on the TUI's cadence.
//
// Following polls the transcript endpoint rather than subscribing to §13.3's
// live output stream, and the reason is an ownership invariant rather than
// simplicity: live chunks are *dropped* for a slow subscriber because the
// transcript file is the durable copy, and a CLI writing into a slow pipe is
// exactly that subscriber. The stream would silently lose output in the case
// this command exists for. Polling also gives one code path for a live
// attempt and a finished one (task 047 decision 2).
const transcriptPollInterval = 2 * time.Second

// newTaskTranscriptCmd prints one attempt's transcript. Reading a transcript
// is not a §6 human action, so task 025 decision 12 — retry, repair, skip and
// approve stay TUI-and-API only — does not reach it: that decision is about
// writes.
func newTaskTranscriptCmd() *cobra.Command {
	var (
		stepRun int64
		follow  bool
		raw     bool
	)
	cmd := &cobra.Command{
		Use:   "transcript <id>",
		Short: "Print a step run's transcript",
		Long: "Print the complete record of what one attempt did (§17), rendered as text. " +
			"--step takes a step_run id — the RUN column `vincent task show` prints — " +
			"which is unambiguous across retries, where every attempt is its own run. " +
			"Omitted, it selects the running attempt if there is one and the newest " +
			"attempt otherwise.\n\n" +
			"--json emits the normalized records as NDJSON, in vincent's own vocabulary " +
			"including its `vincent.*` annotations; --raw streams the agent's untouched " +
			"dialect, byte for byte. Everything a human reads goes to stdout, including " +
			"a command step's stderr, which is tagged rather than split off: a " +
			"transcript is one interleaved stream and two file descriptors would " +
			"scramble the ordering that makes it readable.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("task id must be a number: %q", args[0])
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, err := c.GetTask(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				run, err := selectStepRun(t.Steps, id, stepRun)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
					return exitError{code: 1}
				}
				// Decided here rather than from the endpoint's answer: it
				// returns 404 for two different facts — a run that never had
				// a transcript, and a file that is gone — and the step rows
				// this command already holds tell the two apart without
				// matching on a message string (task 047 decision 6). A run
				// with no transcript is not an error: a manual gate records
				// nothing, and saying so is the whole answer.
				if deref(run.TranscriptPath) == "" {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
						"step run %d (%s, %s) has no transcript\n", run.ID, run.StepID, run.StepType)
					return nil
				}
				p := &transcriptPrinter{
					out:    cmd.OutOrStdout(),
					errOut: cmd.ErrOrStderr(),
					raw:    raw,
					json:   wantJSON(cmd),
				}
				opts := apiclient.TranscriptOptions{}
				if follow {
					// A follow opens on a tail: the point is what happens
					// next, and a long-running agent's file can be enormous.
					opts.Tail = apiclient.DefaultTailBytes
				}
				next, err := p.print(ctx, c, id, run.ID, opts)
				if err != nil {
					return transcriptError(p.errOut, err)
				}
				if !follow {
					return nil
				}
				return p.follow(ctx, c, id, run.ID, next, transcriptPollInterval)
			})
		},
	}
	cmd.Flags().Int64Var(&stepRun, "step", 0,
		"step_run id to print (default: the running attempt, else the newest)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false,
		"Keep printing as the attempt writes, until it is no longer running")
	cmd.Flags().BoolVar(&raw, "raw", false,
		"Emit the agent's own JSONL, byte for byte, instead of rendering it")
	jsonFlag(cmd)
	// --json is vincent's typed JSON on every other subcommand; a transcript
	// does not get to redefine it (task 047 decision 1).
	cmd.MarkFlagsMutuallyExclusive("json", "raw")
	return cmd
}

// selectStepRun resolves which attempt to print (task 047 decision 3).
//
// With no --step, the running attempt wins, because that is the one a person
// asking about a live task means. Otherwise the newest by step_run id: the
// rows arrive ordered by step_index, iteration, attempt, id, which stops
// being chronological the moment a task has parallel steps or fan-out lanes,
// while id is creation order and does not.
func selectStepRun(steps []apiclient.StepRun, taskID, want int64) (*apiclient.StepRun, error) {
	if want > 0 {
		for i := range steps {
			if steps[i].ID == want {
				return &steps[i], nil
			}
		}
		return nil, fmt.Errorf("step run %d not found on task %d", want, taskID)
	}
	var newest, running *apiclient.StepRun
	for i := range steps {
		s := &steps[i]
		if newest == nil || s.ID > newest.ID {
			newest = s
		}
		if s.State == "running" && (running == nil || s.ID > running.ID) {
			running = s
		}
	}
	switch {
	case running != nil:
		return running, nil
	case newest != nil:
		return newest, nil
	}
	return nil, fmt.Errorf("task %d has no step runs yet", taskID)
}

// transcriptError maps a failed fetch to an exit code. The 404 that can still
// arrive after the step row said there is a transcript means the file itself
// is gone — pruned by §10's retention or removed by hand — which is a real
// failure, unlike a run that never wrote one.
func transcriptError(errOut io.Writer, err error) error {
	var apiErr *apiclient.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		_, _ = fmt.Fprintln(errOut, "Error:", apiMessage(err)+
			" — it was pruned (config transcript_retention_days) or removed")
		return exitError{code: 1}
	}
	_, _ = fmt.Fprintln(errOut, "Error:", apiMessage(err))
	return exitError{code: 1}
}

// transcriptPrinter renders one attempt in whichever of the three forms was
// asked for, carrying the little state the rendering needs across fetches so
// a followed transcript reads the same as one printed in a single pass.
type transcriptPrinter struct {
	out    io.Writer
	errOut io.Writer
	raw    bool
	json   bool
	// sawOutput reports whether the agent has said anything yet, which is
	// what decides whether the terminal result record repeats it. A
	// subagent's prose does not count: it is not what the result repeats.
	sawOutput bool
	// rail is the subagent the last printed line belonged to, "" for the
	// main loop, and labels names each spawning call seen so far (task 109).
	// Both are carried across fetches for sawOutput's reason.
	rail   string
	labels map[string]string
	// skills is the fetch being printed's model skill loads, by the `Skill`
	// call each prints at (task 124 decision 38). It is per fetch: a load is
	// drawn on its call's line only when both are in the range that is being
	// printed, because by the next fetch that line has already gone out.
	skills map[string]apiclient.TranscriptRecord
	// called is every call id already printed on a `>` line, carried across
	// fetches. A model load whose call is in it prints nothing: either its
	// line was the call's, or a `-f` poll split the two and the call's line
	// went out as `> Skill <name>` — printing the load then would read as a
	// second call.
	called map[string]bool
}

// transcriptSource is the subject a printer reads: a task's step run or a
// chat's turn (task 103 decision 5). Both routes share one byte-range
// contract (§13.2), so everything past these three calls — the rendering, the
// resume offset, how a follow ends — is one code path, and the two commands
// cannot drift apart in how a transcript reads.
type transcriptSource interface {
	// normalized fetches a range as vincent's records.
	normalized(ctx context.Context, opts apiclient.TranscriptOptions) ([]apiclient.TranscriptRecord, int64, error)
	// raw fetches the same range as the agent's own bytes.
	raw(ctx context.Context, opts apiclient.TranscriptOptions) ([]byte, int64, error)
	// state reports whether the subject is still running and, for when it is
	// not, the line a follow ends on.
	state(ctx context.Context) (running bool, label string, err error)
}

// stepRunSource is one attempt of a task, for `vincent task transcript`.
type stepRunSource struct {
	c             *apiclient.Client
	taskID, runID int64
}

func (s stepRunSource) normalized(
	ctx context.Context, opts apiclient.TranscriptOptions,
) ([]apiclient.TranscriptRecord, int64, error) {
	return s.c.Transcript(ctx, s.taskID, s.runID, opts)
}

func (s stepRunSource) raw(ctx context.Context, opts apiclient.TranscriptOptions) ([]byte, int64, error) {
	return s.c.TranscriptRaw(ctx, s.taskID, s.runID, opts)
}

func (s stepRunSource) state(ctx context.Context) (bool, string, error) {
	t, err := s.c.GetTask(ctx, s.taskID)
	if err != nil {
		return false, "", err
	}
	for i := range t.Steps {
		if t.Steps[i].ID == s.runID {
			state := t.Steps[i].State
			return state == "running", fmt.Sprintf("step run %d is %s", s.runID, state), nil
		}
	}
	return false, "", fmt.Errorf("step run %d is no longer on task %d", s.runID, s.taskID)
}

// print fetches one range of a step run's transcript and writes it, returning
// the offset to resume from.
func (p *transcriptPrinter) print(
	ctx context.Context, c *apiclient.Client, taskID, runID int64, opts apiclient.TranscriptOptions,
) (int64, error) {
	return p.printFrom(ctx, stepRunSource{c: c, taskID: taskID, runID: runID}, opts)
}

// printFrom fetches one range of src and writes it, returning the offset to
// resume from.
func (p *transcriptPrinter) printFrom(
	ctx context.Context, src transcriptSource, opts apiclient.TranscriptOptions,
) (int64, error) {
	if p.raw {
		data, next, err := src.raw(ctx, opts)
		if err != nil {
			return 0, err
		}
		_, err = p.out.Write(data)
		return next, err
	}
	records, next, err := src.normalized(ctx, opts)
	if err != nil {
		return 0, err
	}
	p.skills = transcriptSkillsAtCalls(records)
	for _, rec := range records {
		if err := p.write(rec); err != nil {
			return next, err
		}
	}
	return next, nil
}

// write emits one record.
func (p *transcriptPrinter) write(rec apiclient.TranscriptRecord) error {
	if p.json {
		// The record's own line, not a re-encoding of the struct: the
		// annotations vincent writes carry fields TranscriptRecord does not
		// name, and re-encoding would drop exactly those.
		line := strings.TrimRight(string(rec.Raw), "\r\n")
		if line == "" {
			return nil
		}
		_, err := fmt.Fprintln(p.out, line)
		return err
	}
	p.learnLabel(rec)
	parent := rec.ParentCallID
	switch {
	case rec.Type == "agent.subagent_started" || rec.Type == "agent.subagent_progress":
		// Never a line (task 109): the start names the rail label, and
		// progress arrives after every few of the subagent's lines.
		return nil
	case rec.Type == "agent.subagent_finished":
		p.rail = ""
		_, err := fmt.Fprintln(p.out, renderTranscriptSubagentFinished(rec, p.label(rec.CallID)))
		return err
	case parent != "" && !transcriptChildShown(rec.Type):
		return nil
	case rec.Type == "agent.skill" && isModelSkillLoad(rec) && p.called[rec.CallID]:
		return nil
	}
	text, ok := renderTranscriptRecord(rec, p.sawOutput, p.skills)
	if rec.Type == "agent.output" && rec.Text != "" && parent == "" {
		p.sawOutput = true
	}
	if !ok {
		return nil
	}
	if rec.Type == "agent.tool_use" {
		p.noteCalls(rec.Tools)
	}
	if parent == "" {
		p.rail = ""
		_, err := fmt.Fprintln(p.out, text)
		return err
	}
	if p.rail != parent {
		p.rail = parent
		if _, err := fmt.Fprintln(p.out, transcriptRail+"-> "+p.label(parent)); err != nil {
			return err
		}
	}
	// Every line of a multi-line message carries the rail, or the prose
	// after its first line would read as the main loop's.
	_, err := fmt.Fprintln(p.out, transcriptRail+strings.ReplaceAll(text, "\n", "\n"+transcriptRail))
	return err
}

// transcriptRail is drawn in front of every line a subagent produced (task
// 109) — the pane's `┊ ` in ASCII, for the reason the markers are.
const transcriptRail = "| "

// transcriptChildShown is what this command prints of a subagent. It has no
// levels and prints the pane's `normal`, where a subagent — one level quieter
// — shows its prose, its tool calls and their outcomes, its skill loads, and
// its errors.
func transcriptChildShown(recType string) bool {
	switch recType {
	case "agent.output", "agent.tool_use", "agent.tool_result", "agent.skill", "agent.error":
		return true
	}
	return false
}

// noteCalls remembers the call ids a printed `>` line carried.
func (p *transcriptPrinter) noteCalls(tools []apiclient.TranscriptTool) {
	for _, t := range tools {
		if t.CallID == "" {
			continue
		}
		if p.called == nil {
			p.called = map[string]bool{}
		}
		p.called[t.CallID] = true
	}
}

// transcriptSkillsAtCalls maps a `Skill` call to the model's load that came
// from it, for every load in records whose call comes earlier in them — the
// pane's pairing (task 124.12), keyed on the call id and never on the tool's
// name.
func transcriptSkillsAtCalls(records []apiclient.TranscriptRecord) map[string]apiclient.TranscriptRecord {
	calls := map[string]bool{}
	out := map[string]apiclient.TranscriptRecord{}
	for _, rec := range records {
		switch rec.Type {
		case "agent.tool_use":
			for _, t := range rec.Tools {
				if t.CallID != "" {
					calls[t.CallID] = true
				}
			}
		case "agent.skill":
			if _, seen := out[rec.CallID]; isModelSkillLoad(rec) && calls[rec.CallID] && !seen {
				out[rec.CallID] = rec
			}
		}
	}
	return out
}

// isModelSkillLoad is a load the model asked for through a call, which is the
// only kind that has a call line to print on.
func isModelSkillLoad(rec apiclient.TranscriptRecord) bool {
	return rec.By == "agent" && rec.CallID != ""
}

// learnLabel records the name a spawning call's rail label shows: the
// description its subagent's start gave, else the call's own subject. No tool
// name is read.
func (p *transcriptPrinter) learnLabel(rec apiclient.TranscriptRecord) {
	if p.labels == nil {
		p.labels = map[string]string{}
	}
	switch rec.Type {
	case "agent.tool_use":
		for _, t := range rec.Tools {
			if _, named := p.labels[t.CallID]; t.CallID != "" && t.Summary != "" && !named {
				p.labels[t.CallID] = t.Summary
			}
		}
	case "agent.subagent_started":
		if rec.CallID != "" && rec.Description != "" {
			p.labels[rec.CallID] = rec.Description
		}
	}
}

func (p *transcriptPrinter) label(callID string) string {
	return firstNonEmpty(p.labels[callID], "subagent")
}

// renderTranscriptSubagentFinished renders how a subagent ended, on the rail
// it closes: its status, its name and its tally. `stopped` gets its own
// marker for the reason the pane gives it its own mark, and a status no
// capture has shown gets a neutral one (task 109).
func renderTranscriptSubagentFinished(rec apiclient.TranscriptRecord, label string) string {
	mark := "- "
	switch rec.Status {
	case "completed":
		mark = "= "
	case "failed":
		mark = "! "
	case "stopped":
		mark = "~ "
	}
	parts := []string{firstNonEmpty(rec.Status, "finished"), label}
	switch {
	case rec.ToolUses == 1:
		parts = append(parts, "1 tool use")
	case rec.ToolUses > 1:
		parts = append(parts, fmt.Sprintf("%d tool uses", rec.ToolUses))
	}
	if rec.DurationMS > 0 {
		parts = append(parts, formatTranscriptDuration(rec.DurationMS))
	}
	return transcriptRail + mark + strings.Join(parts, " - ")
}

// follow re-fetches a step run's transcript from the resume offset until the
// attempt stops running.
func (p *transcriptPrinter) follow(
	ctx context.Context, c *apiclient.Client, taskID, runID, offset int64, every time.Duration,
) error {
	return p.followFrom(ctx, stepRunSource{c: c, taskID: taskID, runID: runID}, offset, every)
}

// followFrom re-fetches src from the resume offset until it stops running.
// The state is read *before* each fetch so the last fetch happens after the
// subject settled: reading it after would leave whatever it wrote between the
// two calls unprinted.
// The poll interval is a parameter so a test can drive the loop faster than a
// human would wait; the commands always pass transcriptPollInterval.
func (p *transcriptPrinter) followFrom(
	ctx context.Context, src transcriptSource, offset int64, every time.Duration,
) error {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		running, label, err := src.state(ctx)
		if err != nil {
			return transcriptError(p.errOut, err)
		}
		next, err := p.printFrom(ctx, src, apiclient.TranscriptOptions{Offset: offset})
		if err != nil {
			return transcriptError(p.errOut, err)
		}
		offset = next
		// Only this subject: a later retry is a different step run and a
		// later send a different turn, and sitting here waiting for one would
		// make the command hang on work that is finished as far as the
		// printed transcript is concerned.
		if !running {
			_, _ = fmt.Fprintln(p.errOut, label)
			return nil
		}
	}
}

// renderTranscriptRecord maps one normalized record to a line of text,
// covering the vocabulary the TUI's output pane covers. It is deliberately
// not a reuse of that renderer: `detail.renderRecord` returns styled pane
// segments for a Bubble Tea layout, and sharing it would mean lifting the
// layout primitives into a package the CLI can import — a larger and worse
// change than one small renderer over the same public records. What holds
// the two together is the record vocabulary, which is where the contract
// already lives.
//
// The markers are ASCII, unlike the pane's glyphs: this output is piped, and
// a Windows console under an OEM code page renders `✓` as noise.
//
// A record with nothing a reader wants reports false. agent.usage is the
// point of that rule — `vincent task show` already carries those numbers.
//
// skills is transcriptSkillsAtCalls over the fetch: a call there prints as
// its skill load rather than as `Skill <name>` (task 124 decision 38).
func renderTranscriptRecord(
	rec apiclient.TranscriptRecord, sawOutput bool, skills map[string]apiclient.TranscriptRecord,
) (string, bool) {
	switch rec.Type {
	case "agent.run_header":
		return renderTranscriptRunHeader(rec)
	case "agent.output":
		return rec.Text, rec.Text != ""
	case "agent.tool_use":
		if len(rec.Tools) == 0 {
			return "", false
		}
		mark := "> "
		parts := make([]string, 0, len(rec.Tools))
		for _, tool := range rec.Tools {
			if load, ok := skills[tool.CallID]; ok && tool.CallID != "" {
				parts = append(parts, transcriptSkill(load))
				if load.Error != "" {
					mark = "! "
				}
				continue
			}
			parts = append(parts, strings.TrimSpace(tool.Name+" "+tool.Summary))
		}
		return mark + strings.Join(parts, ", "), true
	case "agent.skill":
		// A skill the human invoked, or a model's load whose call is not in
		// the range printed: its own line, where it arrived.
		if rec.Error != "" {
			return "! " + transcriptSkill(rec), true
		}
		return "> " + transcriptSkill(rec), true
	case "agent.conversation_reset":
		// The point at which the agent CLI threw away its own conversation
		// (task 124.20). The pane's words with the pane's `#`, in ASCII: the
		// run header's marker, because the frame of the conversation is what
		// changed. It prints at no level because this command has none, which
		// matches the pane showing it at all four.
		return "# conversation reset · the agent no longer sees the turns above", true
	case "agent.tool_result":
		if len(rec.Results) == 0 {
			return "", false
		}
		// One record can report several outcomes; the first owns the line,
		// as it does in the pane.
		res := rec.Results[0]
		mark := "< "
		if res.IsError {
			mark = "! "
		}
		// A result with no summary still says what it did — a subagent
		// launched into the background reports only its verb (task 109).
		return mark + strings.TrimSpace(res.Name+" "+firstNonEmpty(res.Summary, res.Verb)), true
	case "agent.plan":
		if len(rec.Items) == 0 {
			return "", false
		}
		parts := make([]string, 0, len(rec.Items))
		for _, item := range rec.Items {
			box := "[ ] "
			if item.Completed {
				box = "[x] "
			}
			parts = append(parts, box+item.Text)
		}
		return "# plan: " + strings.Join(parts, "; "), true
	case "agent.command_output":
		// Skipped, deliberately. The pane shows this at `verbose` only
		// (task 070 decision 2) and this command has no verbosity control,
		// so the alternative to skipping is showing every command's whole
		// output to every reader. `--raw` still carries it verbatim, and
		// `--json` as its normalized record, which is what a reader who
		// wants the body asks for.
		return "", false
	case "agent.patch":
		// Skipped for agent.command_output's reason (task 110): the pane
		// shows an edit's hunks at `verbose` only. The edit's `+N −M` delta
		// is its outcome and prints on the agent.tool_result line.
		return "", false
	case "agent.error":
		return "! " + firstNonEmpty(rec.Message, "agent error"), true
	case "agent.result":
		return renderTranscriptResult(rec, sawOutput), true
	case "command.output", "vincent.output":
		// Both streams land on stdout, tagged (task 047 decision 5): a
		// transcript is one interleaved record stream, and splitting it
		// across two file descriptors scrambles the ordering.
		if rec.Stream == "stderr" {
			return "[stderr] " + rec.Text, true
		}
		return rec.Text, true
	case "vincent.command_started":
		return "$ " + rawField(rec.Raw, "command"), true
	case "vincent.input_request":
		return "? " + firstNonEmpty(rec.Summary, rec.Kind, "input requested"), true
	case "vincent.input_response":
		return "? answered", true
	case "vincent.input_timeout", "vincent.input_protocol_error", "vincent.error":
		return "! " + firstNonEmpty(rec.Message, rawField(rec.Raw, "error"), rec.Type), true
	default:
		// An annotation this binary does not name still says that something
		// happened, and a transcript that silently omitted it would be
		// missing an event rather than a detail.
		if strings.HasPrefix(rec.Type, "vincent.") {
			return "* " + strings.TrimPrefix(rec.Type, "vincent."), true
		}
		return "", false
	}
}

// transcriptSkill is a skill load after its marker: `skill <name> <args>`,
// `(forked)` for one that ran as its own sub-run, or the refusal. The pane's
// words, so the two read alike; the lowercase `skill` is what tells it from
// the `Skill` tool's own call line. The skill's body is never here — no
// record carries it (task 124 decision 39).
func transcriptSkill(rec apiclient.TranscriptRecord) string {
	if rec.Error != "" {
		return "skill " + firstNonEmpty(rec.Name, "invocation") + " failed: " + rec.Error
	}
	parts := []string{"skill"}
	for _, part := range []string{rec.Name, rec.Args} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if rec.Forked {
		parts = append(parts, "(forked)")
	}
	return strings.Join(parts, " ")
}

// renderTranscriptRunHeader renders what the agent CLI announced before the
// run started — where it ran and the tools it was given (task 066) — as the
// pane's run header does, with an ASCII separator. A header carrying neither
// field has nothing to say, so it prints nothing.
func renderTranscriptRunHeader(rec apiclient.TranscriptRecord) (string, bool) {
	if rec.WorkDir == "" && len(rec.AvailableTools) == 0 {
		return "", false
	}
	line := "# " + firstNonEmpty(rec.WorkDir, "run started")
	if n := len(rec.AvailableTools); n > 0 {
		unit := "tools"
		if n == 1 {
			unit = "tool"
		}
		line += fmt.Sprintf(" - %d %s: %s", n, unit, strings.Join(rec.AvailableTools, ", "))
	}
	return line, true
}

// renderTranscriptResult renders the terminal record. On success it says the
// outcome and what the run reported about itself, and not the result text:
// every dialect's result text repeats assistant messages already printed —
// cursor's is the whole turn concatenated — so printing it again is the same
// words twice. The text is kept when nothing else rendered, which is what a
// codex turn with no agent_message looks like, and always on error, where it
// may be the only content there is.
func renderTranscriptResult(rec apiclient.TranscriptRecord, sawOutput bool) string {
	if rec.IsError {
		return "! " + firstNonEmpty(rec.Message, rec.ResultText, "run failed")
	}
	if !sawOutput {
		return "= " + firstNonEmpty(rec.ResultText, "run finished")
	}
	if details := transcriptResultDetails(rec); len(details) > 0 {
		return "= done (" + strings.Join(details, ", ") + ")"
	}
	return "= done"
}

// transcriptResultDetails is the result line's metadata at the level this
// command mirrors the output pane at, `normal` (spec §15, task 066): elapsed
// time, turns, an unusual stop or terminal reason, permission denials, cost.
// Each part appears only when the adapter reported it — zero is unreported —
// so a codex or cursor result stays `= done`. The API-time, cache and
// per-model breakdown is the pane's `verbose` tail, and is left to `--json`
// the way agent.command_output is left to it.
func transcriptResultDetails(rec apiclient.TranscriptRecord) []string {
	var parts []string
	if rec.DurationMS > 0 {
		parts = append(parts, formatTranscriptDuration(rec.DurationMS))
	}
	switch {
	case rec.NumTurns == 1:
		parts = append(parts, "1 turn")
	case rec.NumTurns > 1:
		parts = append(parts, fmt.Sprintf("%d turns", rec.NumTurns))
	}
	// Every successful claude run ends end_turn/completed; only an unusual
	// reason distinguishes "the model finished" from "it hit a limit".
	if rec.StopReason != "" && rec.StopReason != "end_turn" {
		parts = append(parts, "stop: "+rec.StopReason)
	}
	if rec.TerminalReason != "" && rec.TerminalReason != "completed" {
		parts = append(parts, rec.TerminalReason)
	}
	if n := len(rec.PermissionDenials); n > 0 {
		parts = append(parts, fmt.Sprintf("%d denied", n))
	}
	if rec.CostUSD != nil {
		parts = append(parts, fmt.Sprintf("$%.4f", *rec.CostUSD))
	}
	return parts
}

// formatTranscriptDuration spells an agent-reported duration the way the
// pane does: a decimal under a minute, because most agent runs are seconds
// long, and the board's compact form above that.
func formatTranscriptDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// rawField reads one string field of a record this struct does not name.
func rawField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(fields[key], &s) != nil {
		return ""
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
