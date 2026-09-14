package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `vincent trigger` (task 096). The triggers themselves are authored in the
// TUI's triggers view, by hand under {config_dir}/triggers/, or staged by the
// create-trigger and update-triggers built-ins, and the reads are MCP tools.
// Here are the dry run a script wants (decision 29) and the three daemon-free
// verbs the built-ins install through (task 098 decision 3, trigger_author.go).

func newTriggerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trigger",
		Short: "Validate, list, install and dry-run event triggers",
		Long: "Event triggers turn outside events into vincent tasks (task 096).\n\n" +
			"They are created and edited in the TUI's triggers view, by hand under\n" +
			"{config_dir}/triggers/, or staged by the create-trigger and update-triggers\n" +
			"built-ins. validate, ls and apply need no daemon; apply installs a staged\n" +
			"proposal and refuses any change that arms a trigger. test is the dry run,\n" +
			"and asks the daemon.",
	}
	cmd.AddCommand(newTriggerValidateCmd(), newTriggerLsCmd(), newTriggerApplyCmd(), newTriggerTestCmd())
	return cmd
}

func newTriggerTestCmd() *cobra.Command {
	var eventFile string
	cmd := &cobra.Command{
		Use:   "test <id> --event FILE",
		Short: "Judge a sample event through a trigger's pipeline, writing nothing",
		Long: "Send one sample event to POST /v1/triggers/{id}/test and print what the\n" +
			"trigger would do with it: the match result, the if: verdict, the rendered\n" +
			"dedupe key and whether the ledger already holds it, and the request the\n" +
			"action would replay.\n\n" +
			"Nothing is written: no task, no ledger row, no cursor, no event. It works\n" +
			"while the trigger or triggers.enabled is off, since it fires nothing.\n\n" +
			"It needs the daemon, unlike `vincent workflow render`: whether an event\n" +
			"would be deduplicated is a question about the daemon's database.\n\n" +
			"Exit 0 when the event was judged, whatever the outcome; 1 when the\n" +
			"daemon refused the request or a template did not render (outcome error).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTriggerTest(cmd, args[0], eventFile)
		},
	}
	cmd.Flags().StringVar(&eventFile, "event", "", "JSON file holding one event object, or - for stdin")
	_ = cmd.MarkFlagRequired("event")
	jsonFlag(cmd)
	return cmd
}

func runTriggerTest(cmd *cobra.Command, id, eventFile string) error {
	// The fixture is refused before any request, so a typo in it is exit 1
	// whether or not a daemon is running.
	event, err := readEventFixture(cmd, eventFile)
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		return exitError{code: 1}
	}
	return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
		j, err := c.TestTrigger(ctx, id, event)
		if err != nil {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
			return exitError{code: 1}
		}
		if wantJSON(cmd) {
			if err := emitJSON(cmd.OutOrStdout(), j); err != nil {
				return err
			}
		} else if err := renderJudgement(cmd.OutOrStdout(), id, j); err != nil {
			return err
		}
		if j.Outcome == apiclient.TriggerError {
			return exitError{code: 1}
		}
		return nil
	})
}

// readEventFixture reads one JSON object. Numbers stay json.Number, so an
// issue id or a timestamp reaches the daemon as the literal the file holds
// rather than a float64 that may have rounded it.
func readEventFixture(cmd *cobra.Command, file string) (map[string]any, error) {
	text, err := flagText(cmd, "", file)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	var event map[string]any
	if err := dec.Decode(&event); err != nil {
		return nil, fmt.Errorf("--event %s: not a JSON object: %w", file, err)
	}
	if event == nil {
		return nil, fmt.Errorf("--event %s: not a JSON object", file)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("--event %s: one JSON object expected, found more after it", file)
	}
	return event, nil
}

// outcomeText says what a dry run's outcome means. `fired` in a dry run is
// "would be replayed": nothing was sent.
var outcomeText = map[string]string{
	apiclient.TriggerFired:       "the action would be replayed",
	apiclient.TriggerFiltered:    "match: or if: dropped the event",
	apiclient.TriggerDeduped:     "the ledger already holds this dedupe key",
	apiclient.TriggerRateLimited: "limits.max_per_hour is spent for the trailing hour",
	apiclient.TriggerRefused:     "the action has no target to act on",
	apiclient.TriggerError:       "a template did not render",
}

// renderJudgement prints one stage per line in pipeline order — match, if,
// dedupe, action — with "-" for a stage the event never reached.
func renderJudgement(w io.Writer, id string, j apiclient.TriggerJudgement) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "trigger %s, event %s\n", id, dash(j.EventID))

	match := "matched"
	if !j.Matched {
		match = "no match"
		if j.MatchMiss != "" {
			match += " (" + j.MatchMiss + ")"
		}
	}
	fmt.Fprintf(&b, "  match:    %s\n", match)

	guard := "-"
	switch {
	case j.If != nil:
		guard = strconv.FormatBool(*j.If)
		if j.IfRendered != "" && j.IfRendered != guard {
			guard += " (rendered " + strconv.Quote(j.IfRendered) + ")"
		}
	case j.IfRendered != "":
		guard = "rendered " + strconv.Quote(j.IfRendered)
	}
	fmt.Fprintf(&b, "  if:       %s\n", guard)

	dedupe := "-"
	if j.DedupeKey != "" {
		dedupe = j.DedupeKey + " (new)"
		if j.WouldDedupe {
			dedupe = j.DedupeKey + " (already delivered: would dedupe)"
		}
	}
	fmt.Fprintf(&b, "  dedupe:   %s\n", dedupe)

	if a := j.Action; a != nil {
		fmt.Fprintf(&b, "  action:   %s %s %s\n", a.Type, a.Method, a.Path)
		if a.Branch != "" {
			task := "-"
			if a.TaskID != nil {
				task = strconv.FormatInt(*a.TaskID, 10)
			}
			fmt.Fprintf(&b, "  target:   branch %s, task %s\n", a.Branch, task)
		}
		if len(a.Body) > 0 && string(a.Body) != "null" {
			var body bytes.Buffer
			if err := json.Indent(&body, a.Body, "            ", "  "); err != nil {
				body.Reset()
				body.Write(a.Body)
			}
			fmt.Fprintf(&b, "  body:     %s\n", body.String())
		}
	} else {
		fmt.Fprintf(&b, "  action:   -\n")
	}

	outcome := j.Outcome
	if text, ok := outcomeText[j.Outcome]; ok {
		outcome += ": " + text
	}
	fmt.Fprintf(&b, "  outcome:  %s\n", outcome)
	if j.Error != "" {
		fmt.Fprintf(&b, "  error:    %s\n", j.Error)
	}
	_, err := w.Write(b.Bytes())
	return err
}
