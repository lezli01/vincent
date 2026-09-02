package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

// The claude push (§9.6, task 082). Claude Code has no usage endpoint to
// poll — that half of §9.2 stands — but it has a push: it runs whatever
// `statusLine.command` names on every render and hands that process a JSON
// object on stdin carrying `.rate_limits.five_hour.used_percentage`,
// `.rate_limits.seven_day.used_percentage` and each window's `resets_at`.
// Wiring `vincent statusline` in as that command is how the numbers reach the
// daemon.
//
// The alternative — reading the OAuth token out of `~/.claude/.credentials.json`
// and calling Anthropic's usage endpoint — was rejected: that is state-file
// parsing, which the v0 T1.7 decision forbids and §9.5 records as still
// standing.
//
// It is a vincent subcommand rather than the shell script this could have
// been for three reasons: it works on Windows, it needs neither bash nor jq
// installed, and the daemon's bearer token stays out of a file on disk — it
// is discovered the way every other subcommand discovers it.
const (
	// statusLineAdapter is the adapter a status-line reading belongs to.
	// It is fixed, not a flag: Claude Code is the only program that invokes
	// this command.
	statusLineAdapter = "claude"

	// statusLineTimeout bounds the push. Claude Code calls this on every
	// render, so a daemon that is wedged must cost the status line a beat,
	// not a hang.
	statusLineTimeout = time.Second
)

// newStatusLineCmd is `vincent statusline` (§12.1, task 082).
//
// Nothing it does may break the user's status line. A daemon that is down, an
// auth failure, a push that times out, a stdin body that is not JSON — every
// one of them still runs the chained command, still prints its output and
// still exits 0. RunE therefore returns nil unconditionally, and the only
// bytes ever written to stdout are the chained command's.
func newStatusLineCmd() *cobra.Command {
	var wrapB64 string
	cmd := &cobra.Command{
		Use:   "statusline",
		Short: "Report claude's usage windows from Claude Code's status line",
		Long: "Read Claude Code's status-line JSON on stdin, push the usage windows it " +
			"carries to the daemon (§9.6), and print the wrapped status line, if any.\n\n" +
			"This is not run by hand. Claude Code invokes it, once per render, as the " +
			"`statusLine.command` in ~/.claude/settings.json — the daemon view installs it " +
			"there. Failure is silent by construction: whatever goes wrong, the wrapped " +
			"command still runs and its stdout is still what you see.",
		// Extra arguments are tolerated rather than refused. This argv is
		// written into a settings file that outlives the binary that wrote
		// it, and a status line that starts printing usage text because a
		// future flag was left behind is exactly the failure this command
		// exists to avoid.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runStatusLine(cmd, wrapB64)
			return nil
		},
	}
	cmd.Flags().StringVar(&wrapB64, "wrap-b64", "",
		"base64 (RawURLEncoding) of the statusLine object vincent replaced, whose "+
			"`command` is run and printed")
	return cmd
}

// runStatusLine does the three things the command is: read stdin whole, push
// what it found, chain. It reports nothing — every step is best-effort.
func runStatusLine(cmd *cobra.Command, wrapB64 string) {
	// The raw bytes are kept because the chained command must be handed the
	// same stdin it would have been handed had vincent not been spliced in
	// front of it; parsing is a side errand.
	stdin, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		stdin = nil
	}
	if report, ok := statusLineReport(stdin); ok {
		pushStatusLineQuota(cmd.Context(), report)
	}
	chainStatusLine(cmd, wrapB64, stdin)
}

// statusLineInput is the part of Claude Code's status-line object vincent
// reads. Everything else in it — model, workspace, transcript path — belongs
// to whoever was drawing the status line before.
type statusLineInput struct {
	RateLimits struct {
		FiveHour statusLineWindow `json:"five_hour"`
		SevenDay statusLineWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

// statusLineWindow is one usage window as Claude Code spells it.
type statusLineWindow struct {
	// UsedPercentage is a pointer so that an absent window is distinguishable
	// from one sitting at 0%: the first must not be reported at all, the
	// second is a real reading.
	UsedPercentage *float64 `json:"used_percentage"`
	// ResetsAt is left raw because the shape is not fixed: a JSON number is
	// unix epoch seconds and a JSON string is RFC3339. Neither is guessed at
	// from the other.
	ResetsAt json.RawMessage `json:"resets_at"`
}

// statusLineReport maps the stdin object onto §9.6's push. The second result
// is false when there is nothing worth pushing — unparseable input, or an
// object naming no window — because an empty report would tell the daemon
// claude's usage is unknown, which is a different claim from silence.
func statusLineReport(stdin []byte) (apiclient.AgentQuotaReport, bool) {
	var in statusLineInput
	if err := json.Unmarshal(stdin, &in); err != nil {
		return apiclient.AgentQuotaReport{}, false
	}
	report := apiclient.AgentQuotaReport{Source: apiclient.QuotaSourceClaudeStatusLine}
	// The names are §9.6's, the labels are what the board shows.
	for _, w := range []struct {
		name, label string
		src         statusLineWindow
	}{
		{"five_hour", "5h", in.RateLimits.FiveHour},
		{"seven_day", "7d", in.RateLimits.SevenDay},
	} {
		if w.src.UsedPercentage == nil {
			continue
		}
		win := apiclient.AgentQuotaReportWindow{
			Name:        w.name,
			UsedPercent: *w.src.UsedPercentage,
			Window:      w.label,
		}
		if at, ok := parseStatusLineReset(w.src.ResetsAt); ok {
			win.ResetsAt = &at
		}
		report.Windows = append(report.Windows, win)
	}
	return report, len(report.Windows) > 0
}

// parseStatusLineReset reads a `resets_at` in either shape Claude Code writes:
// a JSON number is unix epoch seconds, a JSON string is RFC3339. Anything else
// — absent, null, a string in some other layout — is reported as absent rather
// than turned into a reset time nobody named. A fabricated reset is worse than
// none: the board would count down to it.
func parseStatusLineReset(raw json.RawMessage) (time.Time, bool) {
	// `null` is checked by hand: unmarshalling it into anything succeeds and
	// leaves the destination untouched, which would report the epoch as a
	// reset time.
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, false
	}
	var secs float64
	if err := json.Unmarshal(raw, &secs); err == nil {
		return time.Unix(int64(secs), 0).UTC(), true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// statusLineDial resolves the daemon the reading is pushed to. It is a
// variable so a test can point it at a server it controls, and it deliberately
// does not go through client(): that one reports unreachability on stderr and
// probes /health first, and here an unreachable daemon is a non-event that
// must cost neither a message nor a second round trip.
var statusLineDial = func() (*apiclient.Client, error) {
	dirs, err := config.ResolveDirs()
	if err != nil {
		return nil, err
	}
	return apiclient.Discover(dirs.Data)
}

// pushStatusLineQuota posts the reading and swallows whatever happens.
func pushStatusLineQuota(ctx context.Context, report apiclient.AgentQuotaReport) {
	c, err := statusLineDial()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, statusLineTimeout)
	defer cancel()
	_ = c.ReportAgentQuota(ctx, statusLineAdapter, report)
}

// statusLineWrapped is the object vincent displaced in settings.json. Only
// `command` is read; the rest is carried so that uninstalling can put it back.
type statusLineWrapped struct {
	Command string `json:"command"`
}

// wrappedStatusLineCommand decodes --wrap-b64 into the command to chain.
//
// The payload is base64.RawURLEncoding of the original `statusLine` object's
// raw JSON bytes — raw URL encoding because the result must be safe unquoted
// in both /bin/sh and pwsh. A payload that does not decode, does not parse, or
// names no string `command` means there is nothing to chain, which is the same
// answer as no flag at all: never an error, never a byte on stdout.
func wrappedStatusLineCommand(wrapB64 string) string {
	if wrapB64 == "" {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(wrapB64)
	if err != nil {
		return ""
	}
	var w statusLineWrapped
	if err := json.Unmarshal(raw, &w); err != nil {
		return ""
	}
	return strings.TrimSpace(w.Command)
}

// chainStatusLine runs the wrapped command with the stdin vincent was given
// and copies its stdout through. With nothing chained it prints nothing.
func chainStatusLine(cmd *cobra.Command, wrapB64 string, stdin []byte) {
	command := wrappedStatusLineCommand(wrapB64)
	if command == "" {
		return
	}
	sh, err := statusLineShell()
	if err != nil {
		return
	}
	// The command string is handed to a shell rather than tokenized here: it
	// was written by a human into settings.json and may be a pipeline, and
	// §8.3's rule is that a command string is the platform shell's to parse.
	argv := append(append([]string{}, sh.args...), command)
	//nolint:gosec // G204 is the point: running the user's own status-line command.
	child := exec.CommandContext(cmd.Context(), sh.path, argv...)
	child.Stdin = bytes.NewReader(stdin)
	child.Stdout = cmd.OutOrStdout()
	// stderr is forwarded rather than dropped so a broken wrapped command can
	// be diagnosed. Claude Code renders stdout only, so this cannot reach the
	// status line.
	child.Stderr = cmd.ErrOrStderr()
	// A wrapped command that fails still printed whatever it printed, and
	// that is the status line. Its exit code is not vincent's to pass on.
	_ = child.Run()
}

// statusLineShellSpec is the resolved shell plus the flags that make it run
// one command string.
type statusLineShellSpec struct {
	path string
	args []string
}

// statusLineShell resolves §8.3's platform shell — /bin/sh on POSIX, pwsh on
// Windows falling back to the Windows PowerShell that ships with the OS.
//
// It is resolved here rather than borrowed from internal/taskrun because the
// dependency runs cli → tui/daemon; the CLI does not reach into the engine.
// It is a variable so a test can pin the other platform's shell on a host
// that has both.
var statusLineShell = func() (statusLineShellSpec, error) {
	if runtime.GOOS != "windows" {
		return statusLineShellSpec{path: "/bin/sh", args: []string{"-c"}}, nil
	}
	if path, err := exec.LookPath("pwsh"); err == nil {
		return statusLineShellSpec{path: path, args: []string{"-NoProfile", "-Command"}}, nil
	}
	path, err := exec.LookPath("powershell")
	if err != nil {
		return statusLineShellSpec{}, fmt.Errorf("neither pwsh nor powershell found on PATH")
	}
	return statusLineShellSpec{path: path, args: []string{"-NoProfile", "-Command"}}, nil
}
