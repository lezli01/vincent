package codex

import (
	"context"
	"os"
	"strings"
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
func startRun(t *testing.T, scenario string) agent.RunHandle {
	t.Helper()
	a := fakeAdapter(t)
	env := append(os.Environ(), "FAKEAGENT_SCENARIO="+scenario)
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
			want: []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name: "restricted maps to the workspace-write sandbox",
			spec: agent.RunSpec{PermissionMode: agent.Restricted},
			want: []string{"exec", "--json", "--sandbox", "workspace-write"},
		},
		{
			name: "model and effort pass through",
			spec: agent.RunSpec{Model: "gpt-5.6-sol", Effort: "xhigh"},
			want: []string{
				"exec", "--json", "--dangerously-bypass-approvals-and-sandbox",
				"-m", "gpt-5.6-sol", "-c", "model_reasoning_effort=xhigh",
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
	t.Setenv("FAKEAGENT_DIALECT", "codex")
	av, err := fakeAdapter(t).Detect(t.Context())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !av.Found {
		t.Fatalf("Found = false (%s), want true", av.Error)
	}
	if av.Version != "0.142.5" {
		t.Errorf("Version = %q, want 0.142.5", av.Version)
	}
	if av.SupportsInput {
		t.Error("SupportsInput = true; codex exec is permanently non-interactive (§9.3)")
	}
	// task 005: codex gained `login status`, so the §9.5 field is a definite
	// boolean here now. It rides Detect rather than doctor, which is what
	// makes it reach /v1/agents, /v1/info and the new-task form too.
	if av.LoggedIn == nil || !*av.LoggedIn {
		t.Errorf("LoggedIn = %v, want a definite true — codex can answer this (§9.5)", av.LoggedIn)
	}
}

// TestDetectLoggedOut pins the negative leg: an installed CLI whose session
// has expired must be distinguishable from a healthy one, because otherwise
// it is invisible until a task has burned its retry budget (§18).
func TestDetectLoggedOut(t *testing.T) {
	t.Setenv("FAKEAGENT_DIALECT", "codex")
	t.Setenv("FAKEAGENT_CODEX_LOGGED_OUT", "1")
	av, err := fakeAdapter(t).Detect(t.Context())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !av.Found {
		t.Fatalf("Found = false (%s); logged out is still installed", av.Error)
	}
	if av.LoggedIn == nil || *av.LoggedIn {
		t.Errorf("LoggedIn = %v, want a definite false", av.LoggedIn)
	}
}

// TestLoggedInParseLayers walks every leg of the §9.5 probe. The layering is
// the contract — non-zero exit is false, an explicit negative is false, an
// explicit positive is true, anything else is unknown — and the timeout leg
// is the T4.22 regression, not an edge case: on Windows a probe killed by its
// deadline exits 1, so an exit-status reading would accuse a logged-in
// account on the one morning the machine was slow.
func TestLoggedInParseLayers(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		timeout time.Duration
		want    *bool
	}{
		{name: "explicit positive", want: boolPtr(true)},
		{name: "explicit negative and non-zero exit", env: map[string]string{"FAKEAGENT_CODEX_LOGGED_OUT": "1"}, want: boolPtr(false)},
		{name: "unrecognized output stays unknown", env: map[string]string{"FAKEAGENT_CODEX_LOGIN_UNKNOWN": "1"}, want: nil},
		{
			name:    "timeout is never a verdict",
			env:     map[string]string{"FAKEAGENT_CODEX_LOGIN_HANG": "1"},
			timeout: 200 * time.Millisecond,
			want:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			path := agenttest.BuildFakeAgent(t)
			a := New(func() string { return path })
			ctx := t.Context()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			got := a.loggedIn(ctx, path)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("loggedIn = %v, want nil (unknown)", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("loggedIn = nil, want %v", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("loggedIn = %v, want %v", *got, *tc.want)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }

func TestDetectMissingBinary(t *testing.T) {
	a := New(func() string { return "/nonexistent/codex-not-here" })
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
	if av.LoggedIn != nil {
		t.Error("LoggedIn is set for an unresolvable binary; want nil (unknown)")
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
	if res.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil (codex reports no cost, §9.3)", *res.CostUSD)
	}
	counts := map[agent.EventType]int{}
	for _, ev := range events {
		counts[ev.Type]++
	}
	if counts[agent.EventOutput] == 0 || counts[agent.EventToolUse] == 0 ||
		counts[agent.EventResult] != 1 || counts[agent.EventUnknown] < 3 {
		t.Errorf("event mix %v, want output+tool_use+1 result+unknowns (thread/turn.started, fake_marker)", counts)
	}
}

func TestRunErrorEvent(t *testing.T) {
	h := startRun(t, "error-event")
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want nonzero (real codex exits 1 on turn.failed)")
	}
	if !res.IsError || !strings.Contains(res.ErrorMessage, "fake codex failed on purpose") {
		t.Errorf("IsError=%v msg=%q, want the turn.failed surfaced", res.IsError, res.ErrorMessage)
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
		t.Error("IsError = false after kill; no terminal turn event ever arrived")
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

func TestRespondUnsupported(t *testing.T) {
	h := startRun(t, "success")
	if err := h.Respond(agent.InputResponse{}); err == nil {
		t.Error("Respond succeeded; codex exec is permanently non-interactive")
	}
	drain(t, h)
	_, _ = h.Wait()
}
