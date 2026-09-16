package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

func boolPtr(v bool) *bool { return &v }

// exitError produces a real *exec.ExitError: the parse matches the type, so a
// hand-built stand-in would prove nothing about the one the probe returns.
func exitError(t *testing.T) error {
	t.Helper()
	cmd := exec.Command(agenttest.BuildFakeAgent(t), "auth", "status", "--json")
	cmd.Env = append(os.Environ(), "FAKEAGENT_CLAUDE_AUTH_UNKNOWN=1")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("fake auth status err = %v, want an *exec.ExitError", err)
	}
	return err
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertLoggedIn(t *testing.T, got, want *bool) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Fatalf("logged_in = %v, want nil (unknown)", *got)
	case want != nil && got == nil:
		t.Fatalf("logged_in = nil, want %v", *want)
	case want != nil && *got != *want:
		t.Fatalf("logged_in = %v, want %v", *got, *want)
	}
}

// TestParseAuthStatus walks task 107 decision 1 against the captured 2.1.268
// answers: only a boolean `loggedIn` in a JSON object decides, whatever the
// exit code, and everything else is unknown. The exit-1-without-JSON leg is
// the one that separates claude's rule from codex's and cursor's — exit 1 is
// also every ordinary CLI failure, and reading it as "not authenticated" is a
// false accusation (T4.22).
func TestParseAuthStatus(t *testing.T) {
	exitErr := exitError(t)
	loggedIn := readFixture(t, "auth_status_logged_in_2.1.268.json")
	loggedOut := readFixture(t, "auth_status_logged_out_2.1.268.json")

	tests := []struct {
		name   string
		stdout []byte
		err    error
		want   *bool
	}{
		{name: "captured logged in, exit 0", stdout: loggedIn, want: boolPtr(true)},
		{name: "captured logged out, exit 1", stdout: loggedOut, err: exitErr, want: boolPtr(false)},
		{name: "exit 1 with empty stdout", err: exitErr},
		{name: "exit 1 with non-JSON stdout", stdout: []byte("Error: unknown option '--json'\n"), err: exitErr},
		{name: "exit 0 with JSON lacking loggedIn", stdout: []byte(`{"authMethod":"none"}`)},
		{name: "exit 0 with a non-boolean loggedIn", stdout: []byte(`{"loggedIn":"yes"}`)},
		{name: "exit 0 with a null loggedIn", stdout: []byte(`{"loggedIn":null}`)},
		{name: "exit 0 with non-JSON stdout", stdout: []byte("Logged in as fake@example.com\n")},
		{name: "exit 0 with a JSON array", stdout: []byte(`[true]`)},
		{
			name:   "timeout is never a verdict, even with a JSON answer",
			stdout: loggedOut,
			err:    fmt.Errorf("timed out after 20s: %w", context.DeadlineExceeded),
		},
		{
			name:   "timeout wrapping an exit error",
			stdout: loggedOut,
			err:    fmt.Errorf("%w: %w", exitErr, context.DeadlineExceeded),
		},
		{
			name:   "cancellation is never a verdict",
			stdout: loggedIn,
			err:    fmt.Errorf("probe canceled: %w", context.Canceled),
		},
		{name: "spawn failure", err: exec.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertLoggedIn(t, parseAuthStatus(tc.stdout, tc.err), tc.want)
		})
	}
}

// TestAuthStatusReadsOnlyLoggedIn pins decision 3's no-PII rule where it is
// cheapest to break: the parse struct. The captured answer carries an email,
// an organization and a subscription, and a second field on this struct is
// the first step to one of them being logged or sent over the API.
func TestAuthStatusReadsOnlyLoggedIn(t *testing.T) {
	typ := reflect.TypeFor[authStatus]()
	if typ.NumField() != 1 || typ.Field(0).Name != "LoggedIn" {
		t.Fatalf("authStatus has fields %v; it must decode loggedIn and nothing else", fieldNames(typ))
	}
	// And the committed captures carry placeholders, not the account they
	// were captured from.
	placeholders := map[string]string{
		"email":   "fake@example.com",
		"orgId":   "00000000-0000-0000-0000-000000000000",
		"orgName": "Example Organization",
	}
	for _, name := range []string{"auth_status_logged_in_2.1.268.json", "auth_status_logged_out_2.1.268.json"} {
		var fields map[string]any
		if err := json.Unmarshal(readFixture(t, name), &fields); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for key, want := range placeholders {
			if got, ok := fields[key]; ok && got != want {
				t.Errorf("%s: %s = %v, want the placeholder %q", name, key, got, want)
			}
		}
	}
}

func fieldNames(typ reflect.Type) []string {
	names := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		names = append(names, typ.Field(i).Name)
	}
	return names
}

// TestAuthStatusSupported pins decision 2's gate, [2.1.41, 3.0.0): the floor
// is the changelog's introduction of the subcommand, the ceiling the family
// supportsInput already trusts.
func TestAuthStatusSupported(t *testing.T) {
	tests := map[string]bool{
		"2.1.40":                false,
		"2.1.41":                true,
		"2.1.224":               true,
		"2.1.268":               true,
		"2.1.268 (Claude Code)": true,
		"2.2.0":                 true,
		"2.0.99":                false,
		"3.0.0":                 false,
		"1.0.0":                 false,
		"":                      false,
		"garbage":               false,
	}
	for version, want := range tests {
		if got := authStatusSupported(version); got != want {
			t.Errorf("authStatusSupported(%q) = %v, want %v", version, got, want)
		}
	}
}

// TestDetectLoggedIn drives the probe through Detect against the fake CLI,
// one leg per answer the parse has to tell apart.
func TestDetectLoggedIn(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want *bool
	}{
		{name: "logged in", want: boolPtr(true)},
		{name: "logged out", env: map[string]string{"FAKEAGENT_CLAUDE_LOGGED_OUT": "1"}, want: boolPtr(false)},
		{name: "exit 1 without JSON stays unknown", env: map[string]string{"FAKEAGENT_CLAUDE_AUTH_UNKNOWN": "1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			av, err := fakeAdapter(t).Detect(t.Context())
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if !av.Found {
				t.Fatalf("Found = false (%s); a logged-out CLI is still installed", av.Error)
			}
			assertLoggedIn(t, av.LoggedIn, tc.want)
		})
	}
}

// TestLoggedInTimeoutIsUnknown is the T4.22 leg: a probe that never answers
// is killed by its deadline — exit 1 on Windows — and must not read as "not
// authenticated".
func TestLoggedInTimeoutIsUnknown(t *testing.T) {
	t.Setenv("FAKEAGENT_CLAUDE_AUTH_HANG", "1")
	path := agenttest.BuildFakeAgent(t)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	assertLoggedIn(t, loggedIn(ctx, path, "2.1.268"), nil)
}

// TestDetectBelowAuthFloorSpawnsNothing proves the gate by what the fake CLI
// was asked, not by the nil it leads to: a pre-2.1.41 claude could take
// `auth status` for a prompt, so the only safe probe is none.
func TestDetectBelowAuthFloorSpawnsNothing(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv.txt")
	t.Setenv("FAKEAGENT_ARGV_FILE", argv)
	t.Setenv("FAKEAGENT_VERSION", "2.1.40")
	av, err := fakeAdapter(t).Detect(t.Context())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !av.Found {
		t.Fatalf("Found = false (%s)", av.Error)
	}
	assertLoggedIn(t, av.LoggedIn, nil)
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("argv log: %v; the version probe should have written it", err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")), "\n")
	if len(lines) != 1 || lines[0] != "--version" {
		t.Fatalf("fake CLI was invoked with %q; want only --version below the auth floor", lines)
	}

	// The same log shows the probe's argv above the floor, so the negative
	// above is not an artifact of a log that records nothing.
	if err := os.Remove(argv); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKEAGENT_VERSION", "2.1.268")
	if _, err := fakeAdapter(t).Detect(t.Context()); err != nil {
		t.Fatalf("Detect: %v", err)
	}
	b, err = os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "auth status --json") {
		t.Fatalf("fake CLI was invoked with %q; want an `auth status --json` probe", b)
	}
}
