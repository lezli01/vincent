package cli

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newAgentsCmd is `vincent agents` (§12.1, task 104): a thin client of
// `GET /v1/agents` (§9.6, §13.2), so a script and a human at a shell can read
// what the TUI's daemon view shows without opening it.
//
// Its exit code is 0 whenever the daemon answered. A missing, logged-out,
// untested or quota-spent adapter is the normal state of a healthy machine
// somewhere, and an exit code that fires on the normal state is no use in a
// script — task 041 decision 4 and task 006 decision 7, applied to a sibling
// of `vincent doctor`.
func newAgentsCmd() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Show each agent CLI's version, build verdict, login and quota",
		Long: "List every agent adapter the daemon knows, in its registration order: the\n" +
			"installed version, whether that build is tested, the login state, and the\n" +
			"usage window. NOTES carries bad news only — a missing CLI, no mid-run input,\n" +
			"no restricted mode on this host, an option probe that fell back to the\n" +
			"curated catalog.\n\n" +
			"QUOTA is the one block the daemon serves: a reading a source reported, or\n" +
			"failing that the last usage-limit stop it watched. `→` is a reset the CLI\n" +
			"stated, `≈` one vincent estimated. `unknown` means neither exists.\n\n" +
			"The answer comes from the daemon's catalog cache; --refresh re-probes every\n" +
			"adapter first. Exits 0 whenever the daemon answered, whatever the adapters'\n" +
			"health; --json carries every field, including the ones the table leaves out.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				agents, err := c.ListAgents(ctx, refresh)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if agents == nil {
					agents = apiclient.Agents{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), agents)
				}
				return table(cmd.OutOrStdout(), agentsHeader, agentsRows(agents, time.Now()))
			})
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false,
		"Re-probe every agent CLI instead of answering from the daemon's cache")
	jsonFlag(cmd)
	return cmd
}

var agentsHeader = []string{"AGENT", "VERSION", "BUILD", "LOGIN", "QUOTA", "NOTES"}

func agentsRows(agents apiclient.Agents, now time.Time) [][]string {
	rows := make([][]string, 0, len(agents))
	for _, a := range agents {
		rows = append(rows, agentRow(a, now))
	}
	return rows
}

// agentRow is one adapter's table row. Four facts are columns and every other
// facet is a trailing note, printed only when it is bad news (task 104
// decision 3): a column per facet squeezes the quota cell out of a terminal.
func agentRow(a apiclient.Agent, now time.Time) []string {
	return []string{
		a.Name,
		dash(a.Version),
		// Empty is no judgement — nothing installed to judge, or a daemon that
		// predates the field — and must not read as a verdict.
		dash(a.VersionVerdict),
		agentLoginCell(a),
		agentQuotaCell(a.Quota, now),
		strings.Join(agentNotes(a), "; "),
	}
}

// agentLoginCell uses doctor's words so the two commands never describe one
// login two ways. An adapter that is not installed has no login to report,
// and "unknown" there would suggest there is something to find out.
func agentLoginCell(a apiclient.Agent) string {
	if !a.Available {
		return "-"
	}
	return loggedInWord(a.LoggedIn)
}

// agentNotes renders the facets that trail a row, the way doctorAgentVerdicts
// trails doctor's. Good news adds nothing: a note per adapter saying all is
// well is what makes the one saying otherwise hard to see.
func agentNotes(a apiclient.Agent) []string {
	var notes []string
	if !a.Available {
		note := "not found"
		if a.Error != "" {
			note += ": " + a.Error
		}
		notes = append(notes, note)
	}
	if a.CannotTakeInput() {
		notes = append(notes, "no mid-run input")
	}
	if a.CannotRestrict() {
		notes = append(notes, "no restricted mode on "+runtime.GOOS)
	}
	if a.ProbeError != nil {
		notes = append(notes, "option probe failed (curated catalog)")
	}
	return notes
}

// agentQuotaCell renders the quota block exactly as the daemon merged it
// (task 082: a reading wins, an observation is the fallback). Nothing is
// merged here; the source is what tells a reader which of the two it is.
//
// Times are full local RFC3339, not the TUI's `15:04`: a 7d window's reset is
// days away, and a bare clock would not say which day (task 101's hold row
// made the same choice).
func agentQuotaCell(q *apiclient.AgentQuota, now time.Time) string {
	switch {
	case q == nil:
		// Never "ok" and never empty: no record is not the same as fine
		// (task 026 decision 5).
		return "unknown"
	case len(q.Windows) > 0:
		return agentQuotaReading(q)
	case q.SpentAt(now):
		if reset := agentQuotaReset(q.ResetsAt, q.ResetsAtReported); reset != "" {
			return "spent " + reset
		}
		return "spent"
	default:
		return "ok · last spent " + agentLocalTime(q.ObservedAt)
	}
}

// agentQuotaReading renders a reported reading: where it came from, every
// window it named, and when it was taken. The reading time is part of the
// fact — a status line may not have pushed for an hour, and a percentage with
// no timestamp reads as live.
func agentQuotaReading(q *apiclient.AgentQuota) string {
	parts := make([]string, 0, len(q.Windows)+2)
	parts = append(parts, agentQuotaSource(q.Source))
	for _, w := range q.Windows {
		part := agentQuotaWindowLabel(w) + " " + agentQuotaPercent(w.UsedPercent)
		if reset := agentQuotaReset(w.ResetsAt, w.ResetsAtReported); reset != "" {
			part += " " + reset
		}
		parts = append(parts, part)
	}
	if !q.ObservedAt.IsZero() {
		parts = append(parts, "read "+agentLocalTime(q.ObservedAt))
	}
	return strings.Join(parts, " · ")
}

// agentQuotaReset renders a reset with its provenance: `→` for a time the CLI
// stated, `≈` for one vincent estimated. Showing an estimate as a stated fact
// is what task 026 decision 2 forbids. A window that named no reset renders as
// nothing, never as the zero time.
func agentQuotaReset(at time.Time, reported bool) string {
	if at.IsZero() {
		return ""
	}
	arrow := "≈ "
	if reported {
		arrow = "→ "
	}
	return arrow + agentLocalTime(at)
}

// agentQuotaWindowLabel prefers the source's human label ("5h") and falls back
// to its wire name, because a percentage attached to nothing is not a reading.
func agentQuotaWindowLabel(w apiclient.AgentQuotaWindow) string {
	if w.Window != "" {
		return w.Window
	}
	return w.Name
}

// agentQuotaPercent is one decimal, trimmed: "28%", "28.5%" — not a precision
// the source never had.
func agentQuotaPercent(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) + "%"
}

// agentQuotaSource turns a wire source into prose. An unrecognised one prints
// as it arrived: inventing prose for a source this build has never heard of
// would hide it.
func agentQuotaSource(source string) string {
	switch source {
	case apiclient.QuotaSourceCodexAppServer:
		return "codex app-server"
	case apiclient.QuotaSourceClaudeStatusLine:
		return "claude status line"
	default:
		return source
	}
}

func agentLocalTime(t time.Time) string { return t.Local().Format(time.RFC3339) }
