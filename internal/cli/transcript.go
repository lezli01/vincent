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
	// what decides whether the terminal result record repeats it.
	sawOutput bool
}

// print fetches one range and writes it, returning the offset to resume from.
func (p *transcriptPrinter) print(
	ctx context.Context, c *apiclient.Client, taskID, runID int64, opts apiclient.TranscriptOptions,
) (int64, error) {
	if p.raw {
		data, next, err := c.TranscriptRaw(ctx, taskID, runID, opts)
		if err != nil {
			return 0, err
		}
		_, err = p.out.Write(data)
		return next, err
	}
	records, next, err := c.Transcript(ctx, taskID, runID, opts)
	if err != nil {
		return 0, err
	}
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
	text, ok := renderTranscriptRecord(rec, p.sawOutput)
	if rec.Type == "agent.output" && rec.Text != "" {
		p.sawOutput = true
	}
	if !ok {
		return nil
	}
	_, err := fmt.Fprintln(p.out, text)
	return err
}

// follow re-fetches from the resume offset until the attempt stops running.
// The state is read *before* each fetch so the last fetch happens after the
// run settled: reading it after would leave whatever the step wrote between
// the two calls unprinted.
// The poll interval is a parameter so a test can drive the loop faster than a
// human would wait; the command always passes transcriptPollInterval.
func (p *transcriptPrinter) follow(
	ctx context.Context, c *apiclient.Client, taskID, runID, offset int64, every time.Duration,
) error {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		state, err := stepRunState(ctx, c, taskID, runID)
		if err != nil {
			return transcriptError(p.errOut, err)
		}
		next, err := p.print(ctx, c, taskID, runID, apiclient.TranscriptOptions{Offset: offset})
		if err != nil {
			return transcriptError(p.errOut, err)
		}
		offset = next
		// Only this attempt: a later retry is a different step run, and
		// sitting here waiting for one would make the command hang on a task
		// that is finished as far as the printed run is concerned.
		if state != "running" {
			_, _ = fmt.Fprintf(p.errOut, "step run %d is %s\n", runID, state)
			return nil
		}
	}
}

func stepRunState(ctx context.Context, c *apiclient.Client, taskID, runID int64) (string, error) {
	t, err := c.GetTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	for i := range t.Steps {
		if t.Steps[i].ID == runID {
			return t.Steps[i].State, nil
		}
	}
	return "", fmt.Errorf("step run %d is no longer on task %d", runID, taskID)
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
func renderTranscriptRecord(rec apiclient.TranscriptRecord, sawOutput bool) (string, bool) {
	switch rec.Type {
	case "agent.output":
		return rec.Text, rec.Text != ""
	case "agent.tool_use":
		if len(rec.Tools) == 0 {
			return "", false
		}
		parts := make([]string, 0, len(rec.Tools))
		for _, tool := range rec.Tools {
			parts = append(parts, strings.TrimSpace(tool.Name+" "+tool.Summary))
		}
		return "> " + strings.Join(parts, ", "), true
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
		return mark + strings.TrimSpace(res.Name+" "+res.Summary), true
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

// renderTranscriptResult renders the terminal record. On success it says the
// outcome and nothing else: every dialect's result text repeats assistant
// messages already printed — cursor's is the whole turn concatenated — so
// printing it again is the same words twice. The text is kept when nothing
// else rendered, which is what a codex turn with no agent_message looks like,
// and always on error, where it may be the only content there is.
func renderTranscriptResult(rec apiclient.TranscriptRecord, sawOutput bool) string {
	if rec.IsError {
		return "! " + firstNonEmpty(rec.Message, rec.ResultText, "run failed")
	}
	if !sawOutput {
		return "= " + firstNonEmpty(rec.ResultText, "run finished")
	}
	if rec.CostUSD != nil {
		return fmt.Sprintf("= done ($%.4f)", *rec.CostUSD)
	}
	return "= done"
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
