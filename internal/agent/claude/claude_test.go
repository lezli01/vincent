package claude

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

func fakeAdapter(t *testing.T) *Adapter {
	t.Helper()
	path := agenttest.BuildFakeAgent(t)
	return New(func() string { return path })
}

// startRun launches the fake agent with the given scenario and returns the
// handle. Scenario env rides RunSpec.Env so tests never mutate process env.
func startRun(t *testing.T, scenario string, extraEnv ...string) agent.RunHandle {
	t.Helper()
	a := fakeAdapter(t)
	env := append(os.Environ(), "FAKEAGENT_SCENARIO="+scenario)
	env = append(env, extraEnv...)
	h, err := a.Start(t.Context(), agent.RunSpec{
		Prompt:         "test prompt for " + scenario,
		WorkDir:        t.TempDir(),
		PermissionMode: agent.FullAuto,
		Env:            env,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return h
}

// drain consumes all events and returns them; every event must carry Raw.
func drain(t *testing.T, h agent.RunHandle) []agent.Event {
	t.Helper()
	var events []agent.Event
	for ev := range h.Events() {
		if len(ev.Raw) == 0 {
			t.Errorf("event %q has empty Raw; transcripts would lose it", ev.Type)
		}
		events = append(events, ev)
	}
	return events
}

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name string
		spec agent.RunSpec
		want []string
	}{
		{
			name: "full auto defaults",
			spec: agent.RunSpec{PermissionMode: agent.FullAuto},
			want: []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"},
		},
		{
			name: "restricted maps to the curated allowlist",
			spec: agent.RunSpec{PermissionMode: agent.Restricted},
			want: []string{"-p", "--output-format", "stream-json", "--verbose", "--allowedTools", restrictedTools},
		},
		{
			name: "model and effort pass through",
			spec: agent.RunSpec{Model: "opus", Effort: "max"},
			want: []string{
				"-p", "--output-format", "stream-json", "--verbose",
				"--dangerously-skip-permissions", "--model", "opus", "--effort", "max",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildArgs(tt.spec)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("buildArgs = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetect(t *testing.T) {
	av, err := fakeAdapter(t).Detect(t.Context())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !av.Found {
		t.Fatalf("Found = false (%s), want true", av.Error)
	}
	if av.Version != "2.1.224" {
		t.Errorf("Version = %q, want 2.1.224", av.Version)
	}
	if av.SupportsInput {
		t.Error("SupportsInput = true; must be false until T2.12")
	}
	if av.LoggedIn != nil {
		t.Error("LoggedIn must stay nil (unknown) in v1")
	}
}

func TestDetectMissingBinary(t *testing.T) {
	a := New(func() string { return "/nonexistent/claude-not-here" })
	av, err := a.Detect(t.Context())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if av.Found {
		t.Error("Found = true for a missing binary")
	}
	if av.Error == "" {
		t.Error("Error is empty; want the resolution failure")
	}
}

func TestRunSuccess(t *testing.T) {
	h := startRun(t, "success")
	events := drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 || res.IsError {
		t.Fatalf("got exit=%d isError=%v (%s), want clean success", res.ExitCode, res.IsError, res.ErrorMessage)
	}
	if !strings.Contains(res.ResultText, "test prompt for success") {
		t.Errorf("ResultText %q does not echo the stdin prompt", res.ResultText)
	}
	if res.InputTokens != 100 || res.OutputTokens != 42 {
		t.Errorf("tokens = %d/%d, want 100/42", res.InputTokens, res.OutputTokens)
	}
	if res.CostUSD == nil || *res.CostUSD != 0.0123 {
		t.Errorf("CostUSD = %v, want 0.0123", res.CostUSD)
	}
	counts := map[agent.EventType]int{}
	for _, ev := range events {
		counts[ev.Type]++
	}
	if counts[agent.EventOutput] == 0 || counts[agent.EventToolUse] == 0 ||
		counts[agent.EventResult] != 1 || counts[agent.EventUnknown] < 2 {
		t.Errorf("event mix %v, want output+tool_use+1 result+2 unknown (init, fake_marker)", counts)
	}
}

func TestRunErrorEvent(t *testing.T) {
	h := startRun(t, "error-event")
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (failure is the error event)", res.ExitCode)
	}
	if !res.IsError || !strings.Contains(res.ErrorMessage, "fake agent failed on purpose") {
		t.Errorf("IsError=%v msg=%q, want the error event surfaced", res.IsError, res.ErrorMessage)
	}
}

func TestRunNonzeroExit(t *testing.T) {
	h := startRun(t, "nonzero-exit")
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.IsError {
		t.Errorf("IsError = true; the stream reported success — exit-code judgment is the runner's (§7.1)")
	}
}

func TestRunKill(t *testing.T) {
	h := startRun(t, "hang")
	// Wait for output proving the process is up, then kill it.
	for ev := range h.Events() {
		if ev.Type == agent.EventOutput {
			break
		}
	}
	if err := h.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	drain(t, h)
	res, _ := h.Wait()
	if res.ExitCode == 0 {
		t.Error("ExitCode = 0 after kill")
	}
	if !res.IsError {
		t.Error("IsError = false after kill; no result event ever arrived")
	}
}

func TestRunContextCancelKills(t *testing.T) {
	a := fakeAdapter(t)
	ctx, cancel := context.WithCancel(t.Context())
	h, err := a.Start(ctx, agent.RunSpec{
		Prompt:  "hang please",
		WorkDir: t.TempDir(),
		Env:     append(os.Environ(), "FAKEAGENT_SCENARIO=hang"),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	drain(t, h)
	res, _ := h.Wait()
	if res.ExitCode == 0 {
		t.Error("ExitCode = 0; context cancel must kill the run")
	}
}

func TestRunTreeKillReapsChildren(t *testing.T) {
	h := startRun(t, "hang", "FAKEAGENT_SPAWN_CHILD=1")
	childPID := 0
	for ev := range h.Events() {
		var line struct {
			Type string `json:"type"`
			PID  int    `json:"pid"`
		}
		if json.Unmarshal(ev.Raw, &line) == nil && line.Type == "fakeagent.child" {
			childPID = line.PID
			break
		}
	}
	if childPID == 0 {
		t.Fatal("fakeagent never reported a child pid")
	}
	if !processAlive(childPID) {
		t.Fatalf("child %d is not alive before the kill", childPID)
	}
	if err := h.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	drain(t, h)
	_, _ = h.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(childPID) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived the tree kill", childPID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// processAlive reports whether pid names a live process. On Windows a
// successful handle open means alive; on POSIX signal 0 probes existence.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		_ = p.Release()
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func TestRespondUnsupported(t *testing.T) {
	h := startRun(t, "success")
	if err := h.Respond(agent.InputResponse{}); err == nil {
		t.Error("Respond succeeded; claude must reject input until T2.12")
	}
	drain(t, h)
	_, _ = h.Wait()
}
