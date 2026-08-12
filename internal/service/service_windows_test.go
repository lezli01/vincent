//go:build windows

package service

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
)

const testSID = "S-1-5-21-1111111111-2222222222-3333333333-1001"

// taskDoc is the part of the definition worth asserting on. Decoding rather
// than substring-matching is deliberate twice over: schtasks rejects a
// malformed or out-of-schema definition with one message that names neither
// the element nor the reason, and every value is XML-escaped on the way in, so
// only a round trip proves what the scheduler will actually read.
type taskDoc struct {
	XMLName  xml.Name `xml:"Task"`
	Triggers struct {
		Logon struct {
			Enabled string `xml:"Enabled"`
			UserID  string `xml:"UserId"`
		} `xml:"LogonTrigger"`
	} `xml:"Triggers"`
	Principals struct {
		Principal struct {
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principal"`
	} `xml:"Principals"`
	Settings struct {
		MultipleInstancesPolicy    string `xml:"MultipleInstancesPolicy"`
		DisallowStartIfOnBatteries string `xml:"DisallowStartIfOnBatteries"`
		StopIfGoingOnBatteries     string `xml:"StopIfGoingOnBatteries"`
		ExecutionTimeLimit         string `xml:"ExecutionTimeLimit"`
		IdleSettings               struct {
			StopOnIdleEnd string `xml:"StopOnIdleEnd"`
		} `xml:"IdleSettings"`
		RestartOnFailure struct {
			Interval string `xml:"Interval"`
			Count    int    `xml:"Count"`
		} `xml:"RestartOnFailure"`
	} `xml:"Settings"`
	Actions struct {
		Exec struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

func parseTask(t *testing.T, doc string) taskDoc {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	// The declaration says UTF-16 because that is the encoding writeTaskXML
	// hands schtasks; in memory this is still a Go string, so the reader
	// passes the bytes through unchanged.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	var got taskDoc
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("task definition is not well-formed XML: %v\n%s", err, doc)
	}
	return got
}

// TestRenderTaskRunsAsTheInvokingUser is the whole point of T4.17. A Windows
// Service defaults to LocalSystem, which resolved LOCALAPPDATA to the SYSTEM
// profile — a data dir no TUI would ever probe — and ran §16's full-auto
// agents with SYSTEM's privileges instead of the user's.
func TestRenderTaskRunsAsTheInvokingUser(t *testing.T) {
	got := parseTask(t, renderTask(Options{
		Exe:  `C:\Users\u\bin\vincent.exe`,
		Dirs: config.Dirs{Config: `C:\Users\u\AppData\Roaming\vincent`, Data: `C:\Users\u\AppData\Local\vincent`},
	}, testSID))

	p := got.Principals.Principal
	if p.UserID != testSID {
		t.Errorf("principal = %q, want the invoking user %q", p.UserID, testSID)
	}
	if p.LogonType != "InteractiveToken" {
		t.Errorf("logon type = %q, want InteractiveToken (no stored password, the user's own session)", p.LogonType)
	}
	if p.RunLevel != "LeastPrivilege" {
		t.Errorf("run level = %q, want LeastPrivilege", p.RunLevel)
	}
	// The trigger is scoped to the same account: an unscoped LogonTrigger
	// fires for every user who logs on to the machine.
	if got.Triggers.Logon.UserID != testSID {
		t.Errorf("logon trigger user = %q, want %q", got.Triggers.Logon.UserID, testSID)
	}
	if got.Triggers.Logon.Enabled != "true" {
		t.Errorf("logon trigger is not enabled:\n%+v", got.Triggers)
	}
}

// TestRenderTaskSurvivesTheSchedulerDefaults: three Task Scheduler defaults
// each stop a long-running daemon on their own — a P3D run limit, refusing to
// start on battery, and stopping when the machine stops being idle.
func TestRenderTaskSurvivesTheSchedulerDefaults(t *testing.T) {
	s := parseTask(t, renderTask(Options{
		Exe:  `C:\bin\vincent.exe`,
		Dirs: config.Dirs{Config: `C:\cfg`, Data: `C:\data`},
	}, testSID)).Settings

	if s.ExecutionTimeLimit != "PT0S" {
		t.Errorf("execution time limit = %q, want PT0S; the default P3D kills the daemon after three days", s.ExecutionTimeLimit)
	}
	if s.DisallowStartIfOnBatteries != "false" || s.StopIfGoingOnBatteries != "false" {
		t.Errorf("battery settings = %q/%q, want false/false; both default to true and stop a laptop's daemon",
			s.DisallowStartIfOnBatteries, s.StopIfGoingOnBatteries)
	}
	if s.IdleSettings.StopOnIdleEnd != "false" {
		t.Errorf("StopOnIdleEnd = %q, want false; the default stops the task when the user comes back", s.IdleSettings.StopOnIdleEnd)
	}
	if s.MultipleInstancesPolicy != "IgnoreNew" {
		t.Errorf("multiple-instances policy = %q, want IgnoreNew", s.MultipleInstancesPolicy)
	}
	// Restart must be conditional on a failure: Task Scheduler restarts on a
	// nonzero exit only, which is what leaves a daemon that exited 0 because
	// `vincent daemon stop` asked it to actually stopped. This is the same
	// reasoning behind systemd's Restart=on-failure and launchd's
	// KeepAlive/SuccessfulExit=false.
	if s.RestartOnFailure.Count == 0 || s.RestartOnFailure.Interval == "" {
		t.Errorf("task does not restart on failure: %+v", s.RestartOnFailure)
	}
}

// TestRenderTaskPinsTheDirs is the decision that makes an installed task use
// the same database as the CLI that installed it. A task's Exec action has no
// environment, so the dirs travel as arguments where the plist and the unit
// use VINCENT_CONFIG_DIR/VINCENT_DATA_DIR.
func TestRenderTaskPinsTheDirs(t *testing.T) {
	got := parseTask(t, renderTask(Options{
		Exe:  `C:\bin\vincent.exe`,
		Dirs: config.Dirs{Config: `C:\cfg`, Data: `C:\data`},
	}, testSID))

	if want := `C:\bin\vincent.exe`; got.Actions.Exec.Command != want {
		t.Errorf("command = %q, want %q", got.Actions.Exec.Command, want)
	}
	want := `daemon --hide-console --config-dir "C:\cfg" --data-dir "C:\data"`
	if got.Actions.Exec.Arguments != want {
		t.Errorf("arguments = %q, want %q", got.Actions.Exec.Arguments, want)
	}
}

// TestRenderTaskHidesTheConsole guards T4.20's fix. The action runs on the
// user's desktop under an InteractiveToken principal, and nothing in a task
// definition suppresses a console-subsystem process's window — `<Hidden>` hides
// the task from Task Scheduler's list, not the window from the desktop. Without
// the flag every logon left a terminal sitting there whose close button stopped
// the daemon.
func TestRenderTaskHidesTheConsole(t *testing.T) {
	got := parseTask(t, renderTask(Options{
		Exe:  `C:\bin\vincent.exe`,
		Dirs: config.Dirs{Config: `C:\cfg`, Data: `C:\data`},
	}, testSID))

	if !strings.Contains(got.Actions.Exec.Arguments, "--hide-console") {
		t.Errorf("arguments = %q, want --hide-console: the task's window is a kill switch without it",
			got.Actions.Exec.Arguments)
	}
	// The daemon must stay the task's *own* process, not a launcher that spawns
	// it and exits, or RestartOnFailure supervises the wrong process and the
	// registration reports Ready while the daemon runs.
	if !strings.HasPrefix(got.Actions.Exec.Arguments, "daemon --") {
		t.Errorf("arguments = %q, want the foreground `daemon` subcommand", got.Actions.Exec.Arguments)
	}
}

// TestRenderTaskEscapes: a `&` in a profile path is not exotic, and an
// unescaped one makes the scheduler reject the whole definition. Decoding is
// what proves the escaping is reversible rather than merely present.
func TestRenderTaskEscapes(t *testing.T) {
	exe := `C:\Program Files\R&D\vincent.exe`
	got := parseTask(t, renderTask(Options{
		Exe:  exe,
		Dirs: config.Dirs{Config: `C:\a&b\cfg`, Data: `C:\data`},
	}, testSID))

	if got.Actions.Exec.Command != exe {
		t.Errorf("command = %q, want %q", got.Actions.Exec.Command, exe)
	}
	if want := `daemon --hide-console --config-dir "C:\a&b\cfg" --data-dir "C:\data"`; got.Actions.Exec.Arguments != want {
		t.Errorf("arguments = %q, want %q", got.Actions.Exec.Arguments, want)
	}
}

// TestQuoteArg pins the CommandLineToArgvW rules the daemon's own flag parser
// applies to <Arguments>. A data dir ending in a separator is the case that
// matters: unescaped, its backslash escapes the closing quote and the rest of
// the argument list collapses into one string.
func TestQuoteArg(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`C:\data`, `"C:\data"`},
		{`C:\data\`, `"C:\data\\"`},
		{`C:\my data\`, `"C:\my data\\"`},
		{`C:\a\\`, `"C:\a\\\\"`},
		{`C:\q"x`, `"C:\q\"x"`},
		{``, `""`},
	} {
		if got := quoteArg(tc.in); got != tc.want {
			t.Errorf("quoteArg(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestUTF16LE: schtasks rejects a non-ASCII UTF-8 definition with a message
// mentioning neither the encoding nor the offending value, and a Windows
// profile path carries whatever characters the account name has.
func TestUTF16LE(t *testing.T) {
	got := utf16LE("aé")
	want := []byte{0xFF, 0xFE, 'a', 0x00, 0xE9, 0x00}
	if string(got) != string(want) {
		t.Errorf("utf16LE = % x, want % x", got, want)
	}
}
