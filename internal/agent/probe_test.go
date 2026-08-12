package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// probeHelperEnv makes this test binary stand in for an agent CLI: the probe
// paths need a real subprocess that exits how the test says, and the three
// shipped adapters' own fakeagent cannot be used from this package (it imports
// it back). The mode travels in the environment because Probe deliberately
// spawns with the daemon's own — an agent CLI reads its credentials from there.
const probeHelperEnv = "VINCENT_PROBE_HELPER_MODE"

// TestProbeHelperProcess is not a test. It is the subprocess the tests below
// spawn, and it exits before the testing package can print PASS so that the
// captured stdout is exactly what the mode wrote.
func TestProbeHelperProcess(t *testing.T) {
	switch os.Getenv(probeHelperEnv) {
	case "":
		t.Skip("helper process: only meaningful when spawned by a probe test")
	case "version":
		_, _ = fmt.Fprintln(os.Stdout, "helper-cli 1.2.3")
	case "fail":
		// A blank line, the sentence that matters, then the usage banner a
		// real CLI pads its failures with.
		fmt.Fprint(os.Stderr, "\nnot logged in: run `helper login`\nusage: helper [flags]\n")
		os.Exit(3)
	case "hang":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

// probeSelf runs this test binary as the probe subprocess in the given mode.
func probeSelf(t *testing.T, mode string, timeout time.Duration) (stdout, stderr []byte, err error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	t.Setenv(probeHelperEnv, mode)
	return Probe(t.Context(), timeout, exe, "-test.run=^TestProbeHelperProcess$")
}

func TestProbeReturnsOutput(t *testing.T) {
	stdout, _, err := probeSelf(t, "version", 30*time.Second)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got := strings.TrimSpace(string(stdout)); got != "helper-cli 1.2.3" {
		t.Errorf("stdout = %q, want the version line alone", got)
	}
}

// TestProbeFailureCarriesStderr pins the diagnosis half of T4.22: .Output()
// hides the child's stderr inside an *exec.ExitError nobody unwraps, so a
// failing probe used to report nothing but "exit status 3".
func TestProbeFailureCarriesStderr(t *testing.T) {
	_, stderr, err := probeSelf(t, "fail", 30*time.Second)
	if err == nil {
		t.Fatal("Probe: no error for a nonzero exit")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("error %v does not unwrap to *exec.ExitError; callers read the exit status", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "not logged in: run `helper login`") {
		t.Errorf("error = %q, want the first meaningful stderr line", msg)
	}
	if msg := err.Error(); strings.Contains(msg, "usage:") {
		t.Errorf("error = %q, want one line rather than the whole banner", msg)
	}
	if !strings.Contains(string(stderr), "usage:") {
		t.Error("stderr does not carry the full output the caller may want to parse")
	}
}

// TestProbeTimeoutIsNotAnExitStatus pins the finding T4.22 came from: on
// Windows a context deadline is enforced with TerminateProcess(pid, 1), so a
// probe killed by its own bound reports "exit status 1" — exactly what a
// genuine failure reports. A cold `codex --version` was recorded as a broken
// CLI on that basis.
func TestProbeTimeoutIsNotAnExitStatus(t *testing.T) {
	start := time.Now()
	_, _, err := probeSelf(t, "hang", 300*time.Millisecond)
	if err == nil {
		t.Fatal("Probe: no error for a probe that never finished")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not wrap context.DeadlineExceeded; a caller cannot tell it from a refusal", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "timed out after 300ms") {
		t.Errorf("error = %q, want the bound it exceeded named", msg)
	}
	// The subprocess sleeps for a minute; the deadline, not the child, ends it.
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("Probe took %s, want the deadline to have killed the child", elapsed)
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "whitespace only", in: " \n\t\n", want: ""},
		{name: "skips leading blanks", in: "\n\n  real error \nsecond\n", want: "real error"},
		{name: "crlf", in: "first\r\nsecond\r\n", want: "first"},
		{
			name: "truncates by runes",
			in:   strings.Repeat("é", stderrLimit+10),
			want: strings.Repeat("é", stderrLimit) + "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstLine([]byte(tt.in)); got != tt.want {
				t.Errorf("firstLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
