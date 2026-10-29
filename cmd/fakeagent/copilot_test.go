package main_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// copilotArgs is the full-auto argv task 138's adapter passes, minus the
// prompt, which never rides on argv.
var copilotArgs = []string{
	"-p", "--no-auto-update", "--output-format", "json",
	"--allow-all-tools", "--allow-all-paths", "--allow-all-urls",
}

// runCopilot runs the fake with prompt on stdin, returning stdout, stderr and
// the exit code. A non-zero exit is a scenario's outcome here, not a failure.
func runCopilot(t *testing.T, bin, prompt string, args []string, env ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(cmd.Environ(), env...)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatalf("run fakeagent %v: %v", args, err)
	}
	return out.String(), errOut.String(), code
}

// copilotLines parses every stdout line as JSON, failing on any that is not.
func copilotLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("stdout line is not JSON: %v\n%s", err, line)
		}
		lines = append(lines, v)
	}
	return lines
}

func keys(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// fixtureResult is the terminal line of simple_1.0.105.jsonl, whose key set
// every completed fake run must reproduce.
func fixtureResult(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "agent", "copilot", "testdata", "simple_1.0.105.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := copilotLines(t, string(b))
	return lines[len(lines)-1]
}

// TestCopilotScenarios runs every scenario the dialect implements and holds
// its stdout to the fixtures: each line parses, and a run that completes ends
// in a result line with the captured key set — top level, usage and
// codeChanges alike — while every event before it carries the envelope.
func TestCopilotScenarios(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	want := fixtureResult(t)
	wantUsage := want["usage"].(map[string]any)
	wantChanges := wantUsage["codeChanges"].(map[string]any)

	tests := []struct {
		scenario string
		code     int
		result   bool
	}{
		{"success", 0, true},
		{"error-event", 1, true},
		{"nonzero-exit", 3, true},
		{"big-usage", 0, true},
		{"usage-limit", 1, true},
		{"report-env", 0, true},
		{"echo-prompt", 0, true},
		{"limit-prose", 0, true},
		{"no-result", 1, false},
		{"unauthenticated", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.scenario, func(t *testing.T) {
			t.Parallel()
			out, _, code := runCopilot(t, bin, "do a thing", copilotArgs,
				"FAKEAGENT_SCENARIO="+tt.scenario, "FAKEAGENT_PROMPT_FILE="+filepath.Join(t.TempDir(), "p"))
			if code != tt.code {
				t.Errorf("exit = %d, want %d", code, tt.code)
			}
			lines := copilotLines(t, out)
			var last map[string]any
			if len(lines) > 0 {
				last = lines[len(lines)-1]
			}
			if !tt.result {
				for _, l := range lines {
					if l["type"] == "result" {
						t.Errorf("a %s run wrote a result line: %v", tt.scenario, l)
					}
				}
				return
			}
			if last == nil || last["type"] != "result" {
				t.Fatalf("run does not end in a result line:\n%s", out)
			}
			if got, w := keys(last), keys(want); !reflect.DeepEqual(got, w) {
				t.Errorf("result keys = %v, want %v", got, w)
			}
			usage, _ := last["usage"].(map[string]any)
			if got, w := keys(usage), keys(wantUsage); !reflect.DeepEqual(got, w) {
				t.Errorf("usage keys = %v, want %v", got, w)
			}
			changes, _ := usage["codeChanges"].(map[string]any)
			if got, w := keys(changes), keys(wantChanges); !reflect.DeepEqual(got, w) {
				t.Errorf("codeChanges keys = %v, want %v", got, w)
			}
			if int(last["exitCode"].(float64)) != tt.code {
				t.Errorf("result exitCode = %v, want %d", last["exitCode"], tt.code)
			}
			for _, l := range lines[:len(lines)-1] {
				for _, k := range []string{"type", "data", "id", "timestamp", "parentId"} {
					if _, ok := l[k]; !ok {
						t.Errorf("event lacks %q: %v", k, l)
					}
				}
			}
		})
	}
}

// TestCopilotUnauthenticated pins the logged-out run to the capture byte for
// byte: stderr only, nothing on stdout, exit 1.
func TestCopilotUnauthenticated(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	want, err := os.ReadFile(filepath.Join("..", "..", "internal", "agent", "copilot", "testdata", "logged_out_1.0.105.txt"))
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runCopilot(t, bin, "do a thing", copilotArgs, "FAKEAGENT_SCENARIO=unauthenticated")
	if code != 1 || out != "" {
		t.Errorf("exit = %d, stdout = %q; want 1 and nothing", code, out)
	}
	if stderr != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("stderr =\n%q\nwant\n%q", stderr, want)
	}
}

// TestCopilotScenarioOverride mirrors the codex and cursor knobs.
func TestCopilotScenarioOverride(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	env := []string{"FAKEAGENT_SCENARIO=big-usage", "FAKEAGENT_SCENARIO_COPILOT=success"}

	copilot := runAgent(t, bin, copilotArgs, env...)
	if strings.Contains(copilot, "burning tokens") {
		t.Errorf("copilot ran FAKEAGENT_SCENARIO, not its own override:\n%s", copilot)
	}
	if !strings.Contains(copilot, "done: ") {
		t.Errorf("copilot did not run the overriding scenario:\n%s", copilot)
	}

	claude := runAgent(t, bin, []string{"-p", "--output-format", "stream-json"}, env...)
	if !strings.Contains(claude, "burning tokens") {
		t.Errorf("claude dialect lost FAKEAGENT_SCENARIO to the copilot override:\n%s", claude)
	}

	shared := runAgent(t, bin, copilotArgs, "FAKEAGENT_SCENARIO=big-usage")
	if !strings.Contains(shared, "burning tokens") {
		t.Errorf("copilot ignored FAKEAGENT_SCENARIO with no override set:\n%s", shared)
	}
}

// TestCopilotVersion pins --version to version_1.0.105.txt's two lines.
func TestCopilotVersion(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	want, err := os.ReadFile(filepath.Join("..", "..", "internal", "agent", "copilot", "testdata", "version_1.0.105.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := runAgent(t, bin, []string{"--version"}, "FAKEAGENT_DIALECT=copilot")
	if norm := strings.ReplaceAll(got, "\r\n", "\n"); norm != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("--version = %q, want %q", got, want)
	}
	got = runAgent(t, bin, []string{"--version"}, "FAKEAGENT_DIALECT=copilot", "FAKEAGENT_VERSION=9.9.9")
	if norm := strings.ReplaceAll(got, "\r\n", "\n"); norm != "GitHub Copilot CLI 9.9.9.\nRun 'copilot update' to check for updates.\n" {
		t.Errorf("--version with FAKEAGENT_VERSION = %q", got)
	}
}

// TestCopilotPromptOnStdin pins decision 2: the prompt is stdin's alone, so a
// prompt that reads like a flag or subcommand changes neither the dialect,
// nor the session id, nor whether a version is printed.
func TestCopilotPromptOnStdin(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	const id = "11111111-2222-4333-8444-555555555555"
	args := append(slices.Clone(copilotArgs), "--session-id", id)

	for _, prompt := range []string{"resume", "-v", "--trust"} {
		t.Run(prompt, func(t *testing.T) {
			t.Parallel()
			out, _, code := runCopilot(t, bin, prompt, args, "FAKEAGENT_SESSION_DIR="+t.TempDir())
			if code != 0 {
				t.Fatalf("exit = %d:\n%s", code, out)
			}
			if strings.Contains(out, "GitHub Copilot CLI") || strings.Contains(out, "Claude Code fake") {
				t.Errorf("a prompt of %q printed a version:\n%s", prompt, out)
			}
			lines := copilotLines(t, out)
			if lines[0]["type"] == "system" || lines[0]["type"] == "thinking" {
				t.Errorf("a prompt of %q left the copilot dialect:\n%s", prompt, out)
			}
			var user string
			for _, l := range lines {
				if l["type"] == "user.message" {
					user, _ = l["data"].(map[string]any)["content"].(string)
				}
			}
			if user != prompt {
				t.Errorf("user.message content = %q, want %q", user, prompt)
			}
			last := lines[len(lines)-1]
			if last["type"] != "result" || last["sessionId"] != id {
				t.Errorf("result sessionId = %v, want %s", last["sessionId"], id)
			}
		})
	}
}

// TestCopilotUsageCheckpoint holds the fake's usage checkpoint to
// simple_1.0.105.jsonl's: a root line between turn_end and idle, whose every
// key the capture also carries, with the capture's token split and per-model
// usage keys exactly — so the adapter reads the fake's tokens off the same
// fields it reads a real run's (task 138.7).
func TestCopilotUsageCheckpoint(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "agent", "copilot", "testdata", "simple_1.0.105.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := checkpointLine(t, copilotLines(t, string(b)))
	wantData := want["data"].(map[string]any)
	wantSnap := wantData["accountingSnapshot"].(map[string]any)

	for _, tt := range []struct {
		scenario string
		in, out  float64
	}{
		{"success", 100, 42},
		{"big-usage", 2_500_000, 1_200_000},
		{"error-event", 100, 42},
	} {
		t.Run(tt.scenario, func(t *testing.T) {
			t.Parallel()
			stdout, _, _ := runCopilot(t, bin, "do a thing", copilotArgs,
				"FAKEAGENT_SCENARIO="+tt.scenario, "FAKEAGENT_PROMPT_FILE="+filepath.Join(t.TempDir(), "p"))
			got := checkpointLine(t, copilotLines(t, stdout))
			if _, ok := got["agentId"]; ok {
				t.Errorf("checkpoint carries an agentId; the run's usage is the root's: %v", got)
			}
			data := got["data"].(map[string]any)
			subset(t, "data", keys(data), keys(wantData))
			snap, _ := data["accountingSnapshot"].(map[string]any)
			subset(t, "accountingSnapshot", keys(snap), keys(wantSnap))
			details, _ := snap["tokenDetails"].(map[string]any)
			if g, w := keys(details), keys(wantSnap["tokenDetails"].(map[string]any)); !reflect.DeepEqual(g, w) {
				t.Errorf("tokenDetails keys = %v, want %v", g, w)
			}
			in, _ := details["input"].(map[string]any)
			out, _ := details["output"].(map[string]any)
			if in["tokenCount"] != tt.in || out["tokenCount"] != tt.out {
				t.Errorf("tokens = %v in, %v out; want %v and %v", in["tokenCount"], out["tokenCount"], tt.in, tt.out)
			}
			wantModel := firstValue(t, wantSnap["modelMetrics"].(map[string]any))
			model := firstValue(t, snap["modelMetrics"].(map[string]any))
			if g, w := keys(model["usage"].(map[string]any)), keys(wantModel["usage"].(map[string]any)); !reflect.DeepEqual(g, w) {
				t.Errorf("modelMetrics usage keys = %v, want %v", g, w)
			}
		})
	}
}

// checkpointLine returns a stream's one session.usage_checkpoint, failing
// unless it sits where the captures put it: right after turn_end, right
// before idle.
func checkpointLine(t *testing.T, lines []map[string]any) map[string]any {
	t.Helper()
	at := -1
	for i, l := range lines {
		if l["type"] == "session.usage_checkpoint" {
			if at >= 0 {
				t.Fatalf("more than one usage checkpoint")
			}
			at = i
		}
	}
	if at <= 0 || at+1 >= len(lines) {
		t.Fatalf("no usage checkpoint inside the stream (at %d of %d lines)", at, len(lines))
	}
	if lines[at-1]["type"] != "assistant.turn_end" || lines[at+1]["type"] != "assistant.idle" {
		t.Errorf("checkpoint sits between %v and %v, want assistant.turn_end and assistant.idle",
			lines[at-1]["type"], lines[at+1]["type"])
	}
	return lines[at]
}

func subset(t *testing.T, what string, got, of []string) {
	t.Helper()
	for _, k := range got {
		if !slices.Contains(of, k) {
			t.Errorf("%s key %q is not in the capture's %v", what, k, of)
		}
	}
}

func firstValue(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	if len(m) != 1 {
		t.Fatalf("want one model, got %v", keys(m))
	}
	for _, v := range m {
		return v.(map[string]any)
	}
	return nil
}

// TestCopilotResume is the copilot dialect's memory (task 138.11): a fresh
// run adopts an unknown --session-id, a `--resume=` of it recalls the first
// prompt and reports a usage checkpoint that grew, and a `--resume=` of an id
// nobody wrote is resume_unknown_1.0.105.txt's refusal — stderr only, exit 1.
func TestCopilotResume(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	store := "FAKEAGENT_SESSION_DIR=" + t.TempDir()
	const id = "6f1c2a8e-3b4d-4e5f-8a9b-0c1d2e3f4a5b"

	first, _, code := runCopilot(t, bin, "remember the codeword", append(slices.Clone(copilotArgs), "--session-id", id), store)
	if code != 0 {
		t.Fatalf("first run exit = %d", code)
	}
	if got := copilotResultSession(t, first); got != id {
		t.Errorf("first run's session = %q, want the adopted --session-id %q", got, id)
	}

	second, _, code := runCopilot(t, bin, "what was it?", append(slices.Clone(copilotArgs), "--resume="+id), store)
	if code != 0 {
		t.Fatalf("resumed run exit = %d", code)
	}
	if !strings.Contains(second, "recalled: remember the codeword") {
		t.Errorf("resumed run does not recall the first prompt:\n%s", second)
	}
	if got := copilotResultSession(t, second); got != id {
		t.Errorf("resumed run's session = %q, want %q", got, id)
	}
	details := checkpointLine(t, copilotLines(t, second))["data"].(map[string]any)["accountingSnapshot"].(map[string]any)["tokenDetails"].(map[string]any)
	if in := details["input"].(map[string]any)["tokenCount"]; in != float64(200) {
		t.Errorf("resumed checkpoint input = %v, want the session's total 200", in)
	}

	want, err := os.ReadFile(filepath.Join("..", "..", "internal", "agent", "copilot", "testdata", "resume_unknown_1.0.105.txt"))
	if err != nil {
		t.Fatal(err)
	}
	const unknown = "5fb73928-d652-46ae-b08b-9548a753cfd6"
	out, stderr, code := runCopilot(t, bin, "hello?", append(slices.Clone(copilotArgs), "--resume="+unknown), store)
	if code != 1 || out != "" {
		t.Errorf("unknown resume: exit = %d, stdout = %q; want 1 and nothing", code, out)
	}
	if stderr != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("stderr =\n%q\nwant\n%q", stderr, want)
	}
}

// copilotResultSession is the sessionId on a stream's result line.
func copilotResultSession(t *testing.T, out string) string {
	t.Helper()
	lines := copilotLines(t, out)
	last := lines[len(lines)-1]
	if last["type"] != "result" {
		t.Fatalf("last line is %v, not the result", last["type"])
	}
	id, _ := last["sessionId"].(string)
	return id
}
