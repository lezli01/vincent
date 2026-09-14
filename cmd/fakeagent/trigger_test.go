package main_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// runMode runs fakeagent with args, stdin and env, and returns stdout and the
// exit code. Unlike runAgent it does not fail on a non-zero exit, because
// FAKEAGENT_TRIGGER_EXIT's whole point is one.
func runMode(t *testing.T, bin string, args []string, stdin string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	// The cursor the daemon would hand over is set explicitly by each test, so
	// a developer's own environment cannot leak one in.
	cmd.Env = append(cmd.Environ(), "VINCENT_TRIGGER_CURSOR=")
	cmd.Env = append(cmd.Env, env...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), 0
	case errors.As(err, &exitErr):
		return stdout.String(), exitErr.ExitCode()
	default:
		t.Fatalf("run fakeagent %v: %v", args, err)
		return "", 0
	}
}

// TestTriggerPollPrintsEventsAndCursor pins the m16 gate's poll command: the
// events file verbatim, then the cursor line, with the cursor it was handed
// recorded where the gate can read it back.
func TestTriggerPollPrintsEventsAndCursor(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	dir := t.TempDir()
	events := filepath.Join(dir, "events.ndjson")
	// The last line has no newline: the gate appends with printf, and a poll
	// must not glue the cursor line onto it.
	if err := os.WriteFile(events, []byte("{\"id\":\"e1\"}\n{\"id\":\"e2\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen := filepath.Join(dir, "seen.log")

	out, code := runMode(t, bin, []string{"trigger-poll", events}, "",
		"FAKEAGENT_TRIGGER_CURSOR=c-9", "FAKEAGENT_TRIGGER_SEEN="+seen, "VINCENT_TRIGGER_CURSOR=c-8")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	want := "{\"id\":\"e1\"}\n{\"id\":\"e2\"}\n{\"cursor\":\"c-9\"}\n"
	if out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\"c-8\"\n" {
		t.Errorf("seen log = %q, want the handed-over cursor as one JSON line", got)
	}

	// A second, cursor-less invocation appends rather than overwrites, and
	// records the empty cursor an unseeded trigger is handed.
	if _, code := runMode(t, bin, []string{"trigger-poll", events}, "", "FAKEAGENT_TRIGGER_SEEN="+seen); code != 0 {
		t.Fatalf("second poll exit %d", code)
	}
	if got, _ := os.ReadFile(seen); string(got) != "\"c-8\"\n\"\"\n" {
		t.Errorf("seen log after two polls = %q", got)
	}
}

// TestTriggerPollSources covers where the events come from: argv beats the
// environment, and a missing file is a quiet source rather than a failure.
func TestTriggerPollSources(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	dir := t.TempDir()
	fromEnv := filepath.Join(dir, "env.ndjson")
	fromArgv := filepath.Join(dir, "argv.ndjson")
	for path, body := range map[string]string{fromEnv: "{\"id\":\"env\"}\n", fromArgv: "{\"id\":\"argv\"}\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name string
		args []string
		env  []string
		want string
	}{
		{"env", []string{"trigger-poll"}, []string{"FAKEAGENT_TRIGGER_EVENTS=" + fromEnv}, "{\"id\":\"env\"}\n"},
		{"argv wins", []string{"trigger-poll", fromArgv}, []string{"FAKEAGENT_TRIGGER_EVENTS=" + fromEnv}, "{\"id\":\"argv\"}\n"},
		{"missing file", []string{"trigger-poll", filepath.Join(dir, "absent.ndjson")}, nil, ""},
		{"nothing named", []string{"trigger-poll"}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, code := runMode(t, bin, tc.args, "", tc.env...)
			if code != 0 {
				t.Fatalf("exit %d, want 0", code)
			}
			if out != tc.want {
				t.Errorf("stdout = %q, want %q", out, tc.want)
			}
		})
	}
}

// TestTriggerPollExit pins FAKEAGENT_TRIGGER_EXIT: the events are still
// printed, so a daemon that parsed them despite the status would be caught.
func TestTriggerPollExit(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	events := filepath.Join(t.TempDir(), "events.ndjson")
	if err := os.WriteFile(events, []byte("{\"id\":\"e1\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runMode(t, bin, []string{"trigger-poll", events}, "", "FAKEAGENT_TRIGGER_EXIT=4")
	if code != 4 {
		t.Errorf("exit %d, want 4", code)
	}
	if !strings.Contains(out, "\"e1\"") {
		t.Errorf("a failing poll printed no events: %q", out)
	}
	if _, code := runMode(t, bin, []string{"trigger-poll", events}, "", "FAKEAGENT_TRIGGER_EXIT=nope"); code != 0 {
		t.Errorf("an unparseable exit status exited %d, want 0", code)
	}
}

// TestHMACSignsLikeGitHub checks the signer against the example GitHub
// documents for validating webhook deliveries, so the gate's signature is the
// scheme's and not merely the daemon's own idea of it.
func TestHMACSignsLikeGitHub(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)

	out, code := runMode(t, bin, []string{"hmac", "GATE_SECRET"}, "Hello, World!",
		"GATE_SECRET=It's a Secret to Everybody")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	const want = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if got := strings.TrimSpace(out); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no variable named", []string{"hmac"}},
		{"variable unset", []string{"hmac", "FAKEAGENT_TEST_SURELY_UNSET"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, code := runMode(t, bin, tc.args, "body"); code != 2 {
				t.Errorf("exit %d, want 2: a gate must not sign with an empty secret", code)
			}
		})
	}
}
