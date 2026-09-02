package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

// The status line is the one vincent surface whose failure mode is somebody
// else's UI, so these tests are mostly about what does *not* happen: no
// stdout, no error, no missing chained output, whatever went wrong upstream.

// statusLineFixture is the shape Claude Code writes on stdin, with one
// `resets_at` in each of the two shapes it uses.
const statusLineFixture = `{
  "model": {"display_name": "Opus"},
  "rate_limits": {
    "five_hour": {"used_percentage": 28.5, "resets_at": "2026-09-02T14:20:00Z"},
    "seven_day": {"used_percentage": 61, "resets_at": 1772461200}
  }
}`

// runStatusLineCmd runs the subcommand the way Claude Code does and returns
// what reached stdout. The error is returned as-is: it must always be nil.
func runStatusLineCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newStatusLineCmd()
	var out, errOut strings.Builder
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

// noDaemon points the default dial at a data dir with no daemon in it, which
// is what "the daemon is down" looks like from here.
func noDaemon(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvConfigDir, t.TempDir())
}

// wrapArg builds the --wrap-b64 payload the TUI writes: RawURLEncoding of the
// original statusLine object's raw JSON bytes.
func wrapArg(t *testing.T, command string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"type": "command", "command": command})
	if err != nil {
		t.Fatalf("marshal statusLine: %v", err)
	}
	return "--wrap-b64=" + base64.RawURLEncoding.EncodeToString(raw)
}

// hostShellName names the shell chainStatusLine will pick on this platform.
// The pwsh spellings below work under Windows PowerShell too, which is what
// statusLineShell falls back to when pwsh is absent.
func hostShellName() string {
	if runtime.GOOS == "windows" {
		return "pwsh"
	}
	return "sh"
}

// shellCat is a command body copying stdin to stdout with nothing added — no
// trailing newline, no re-encoding — so a test can compare bytes exactly.
//
// pwsh gets the .NET spelling rather than `cat`, which is an alias for
// Get-Content and does not read stdin, and rather than `$input`, which
// re-emits the stream a line at a time with the platform newline.
func shellCat(shell string) string {
	if shell == "sh" {
		return "cat"
	}
	return "[Console]::Out.Write([Console]::In.ReadToEnd())"
}

// shellEcho is a command body printing s verbatim, again with nothing added.
// pwsh's `echo` is Write-Output, which appends a newline; [Console]::Out.Write
// does not. Neither body contains a space, so both survive Windows argv
// quoting unchanged.
func shellEcho(shell, s string) string {
	if shell == "sh" {
		return "printf %s " + s
	}
	return "[Console]::Out.Write('" + s + "')"
}

func TestStatusLineChainsStdinAndPrintsItsStdout(t *testing.T) {
	noDaemon(t)
	out, err := runStatusLineCmd(t, statusLineFixture, wrapArg(t, shellCat(hostShellName())))
	if err != nil {
		t.Fatalf("statusline returned %v, want nil", err)
	}
	// The chained command is handed the same bytes vincent was handed, and
	// its stdout is the whole of vincent's.
	if out != statusLineFixture {
		t.Errorf("stdout = %q, want the stdin verbatim %q", out, statusLineFixture)
	}
}

func TestStatusLineWithADownDaemonStillChains(t *testing.T) {
	noDaemon(t)
	out, err := runStatusLineCmd(t, statusLineFixture, wrapArg(t, shellEcho(hostShellName(), "ok")))
	if err != nil {
		t.Fatalf("statusline returned %v, want nil", err)
	}
	if out != "ok" {
		t.Errorf("stdout = %q, want %q", out, "ok")
	}
}

func TestStatusLineWithNothingChainedPrintsNothing(t *testing.T) {
	noDaemon(t)
	// No --wrap-b64 at all: there was no prior status line.
	out, err := runStatusLineCmd(t, statusLineFixture)
	if err != nil {
		t.Fatalf("statusline returned %v, want nil", err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

func TestStatusLineWithAnUnusableWrapPrintsNothing(t *testing.T) {
	noDaemon(t)
	for _, tc := range []struct{ name, arg string }{
		{"not base64", "--wrap-b64=not/valid+base64=="},
		{"not JSON", "--wrap-b64=" + base64.RawURLEncoding.EncodeToString([]byte("{"))},
		{"no command field", "--wrap-b64=" + base64.RawURLEncoding.EncodeToString([]byte(`{"type":"command"}`))},
		{"command is not a string", "--wrap-b64=" + base64.RawURLEncoding.EncodeToString([]byte(`{"command":42}`))},
		{"command is empty", wrapArg(t, "   ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runStatusLineCmd(t, statusLineFixture, tc.arg)
			if err != nil {
				t.Fatalf("statusline returned %v, want nil", err)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
		})
	}
}

// TestStatusLineRunsUnderEachShell holds §8.3's two shells to the same
// behaviour. Whichever one this host does not have is skipped; between the
// three CI platforms both legs run.
func TestStatusLineRunsUnderEachShell(t *testing.T) {
	noDaemon(t)
	for _, shell := range []string{"sh", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			spec, ok := lookupTestShell(shell)
			if !ok {
				t.Skipf("%s is not installed on this host", shell)
			}
			restore := statusLineShell
			statusLineShell = func() (statusLineShellSpec, error) { return spec, nil }
			t.Cleanup(func() { statusLineShell = restore })

			out, err := runStatusLineCmd(t, "hello", wrapArg(t, shellCat(shell)))
			if err != nil {
				t.Fatalf("statusline returned %v, want nil", err)
			}
			if out != "hello" {
				t.Errorf("stdout = %q, want %q", out, "hello")
			}
		})
	}
}

func lookupTestShell(shell string) (statusLineShellSpec, bool) {
	if shell == "sh" {
		if runtime.GOOS != "windows" {
			return statusLineShellSpec{path: "/bin/sh", args: []string{"-c"}}, true
		}
		path, err := exec.LookPath("sh") // Git Bash's, when it is on PATH
		if err != nil {
			return statusLineShellSpec{}, false
		}
		return statusLineShellSpec{path: path, args: []string{"-c"}}, true
	}
	path, err := exec.LookPath("pwsh")
	if err != nil {
		return statusLineShellSpec{}, false
	}
	return statusLineShellSpec{path: path, args: []string{"-NoProfile", "-Command"}}, true
}

// TestStatusLinePushesTheReading is the wire assertion: the right adapter, the
// two §9.6 windows, the reported source, and both `resets_at` shapes landing
// as RFC3339 on the wire.
func TestStatusLinePushesTheReading(t *testing.T) {
	noDaemon(t)
	type gotReq struct {
		path string
		body apiclient.AgentQuotaReport
	}
	reqs := make(chan gotReq, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body apiclient.AgentQuotaReport
		_ = json.NewDecoder(r.Body).Decode(&body)
		reqs <- gotReq{path: r.URL.Path, body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	dialTestServer(t, srv.URL)

	out, err := runStatusLineCmd(t, statusLineFixture)
	if err != nil {
		t.Fatalf("statusline returned %v, want nil", err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing with no wrapped command", out)
	}

	var req gotReq
	select {
	case req = <-reqs:
	case <-time.After(5 * time.Second):
		t.Fatal("no quota push arrived")
	}
	if req.path != "/v1/agents/claude/quota" {
		t.Errorf("path = %q, want the claude adapter's quota route", req.path)
	}
	if req.body.Source != apiclient.QuotaSourceClaudeStatusLine {
		t.Errorf("source = %q, want %q", req.body.Source, apiclient.QuotaSourceClaudeStatusLine)
	}
	if len(req.body.Windows) != 2 {
		t.Fatalf("windows = %+v, want the two claude windows", req.body.Windows)
	}
	five, seven := req.body.Windows[0], req.body.Windows[1]
	if five.Name != "five_hour" || five.Window != "5h" || five.UsedPercent != 28.5 {
		t.Errorf("five-hour window = %+v", five)
	}
	if seven.Name != "seven_day" || seven.Window != "7d" || seven.UsedPercent != 61 {
		t.Errorf("seven-day window = %+v", seven)
	}
	// A string resets_at is RFC3339; a numeric one is unix epoch seconds.
	if five.ResetsAt == nil || !five.ResetsAt.Equal(time.Date(2026, 9, 2, 14, 20, 0, 0, time.UTC)) {
		t.Errorf("five-hour resets_at = %v, want the RFC3339 value parsed", five.ResetsAt)
	}
	if seven.ResetsAt == nil || !seven.ResetsAt.Equal(time.Unix(1772461200, 0)) {
		t.Errorf("seven-day resets_at = %v, want the epoch seconds parsed", seven.ResetsAt)
	}
}

// TestStatusLineOmitsAnUnreadableReset proves the reading is not invented:
// a reset vincent cannot parse is left out, and the window still reports its
// percentage.
func TestStatusLineOmitsAnUnreadableReset(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"absent", `{"used_percentage": 4}`},
		{"null", `{"used_percentage": 4, "resets_at": null}`},
		{"not a timestamp", `{"used_percentage": 4, "resets_at": "in about an hour"}`},
		{"an object", `{"used_percentage": 4, "resets_at": {"seconds": 10}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, ok := statusLineReport([]byte(`{"rate_limits":{"five_hour":` + tc.raw + `}}`))
			if !ok {
				t.Fatal("no report, want the percentage reported without a reset")
			}
			if len(report.Windows) != 1 || report.Windows[0].UsedPercent != 4 {
				t.Fatalf("windows = %+v, want the one five-hour reading", report.Windows)
			}
			if report.Windows[0].ResetsAt != nil {
				t.Errorf("resets_at = %v, want it omitted", report.Windows[0].ResetsAt)
			}
		})
	}
}

// TestStatusLineReportsNothingWorthPushing covers the inputs that must not
// produce a push at all: a body that is not the object, and an object naming
// no window. Reporting an empty reading would claim claude's usage is
// unknown, which is a different statement from staying quiet.
func TestStatusLineReportsNothingWorthPushing(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"empty stdin", ``},
		{"not JSON", `not json at all`},
		{"no rate limits", `{"model":{"display_name":"Opus"}}`},
		{"a window with no percentage", `{"rate_limits":{"five_hour":{"resets_at":1772461200}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := statusLineReport([]byte(tc.in)); ok {
				t.Error("a report was produced, want none")
			}
		})
	}
}

// TestStatusLineSurvivesAWedgedDaemon holds the push to its one-second bound
// (Claude Code renders often) and proves the chained command runs anyway.
func TestStatusLineSurvivesAWedgedDaemon(t *testing.T) {
	noDaemon(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	dialTestServer(t, srv.URL)

	start := time.Now()
	out, err := runStatusLineCmd(t, statusLineFixture, wrapArg(t, shellEcho(hostShellName(), "ok")))
	if err != nil {
		t.Fatalf("statusline returned %v, want nil", err)
	}
	if out != "ok" {
		t.Errorf("stdout = %q, want the chained output %q", out, "ok")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("took %s; the push is meant to be bounded at %s", elapsed, statusLineTimeout)
	}
}

// TestStatusLineSurvivesARefusingDaemon covers the daemon that answers and
// says no — a 401 from a stale token is the realistic case.
func TestStatusLineSurvivesARefusingDaemon(t *testing.T) {
	noDaemon(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad token"}}`))
	}))
	t.Cleanup(srv.Close)
	dialTestServer(t, srv.URL)

	out, err := runStatusLineCmd(t, statusLineFixture, wrapArg(t, shellEcho(hostShellName(), "ok")))
	if err != nil {
		t.Fatalf("statusline returned %v, want nil", err)
	}
	if out != "ok" {
		t.Errorf("stdout = %q, want the chained output %q", out, "ok")
	}
}

// dialTestServer points the push at a server the test owns.
func dialTestServer(t *testing.T, baseURL string) {
	t.Helper()
	restore := statusLineDial
	statusLineDial = func() (*apiclient.Client, error) {
		return apiclient.New(baseURL, "test-token"), nil
	}
	t.Cleanup(func() { statusLineDial = restore })
}
