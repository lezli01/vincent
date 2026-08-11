package cursor

import (
	"context"
	"errors"
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
			name: "full auto forces and defaults the model",
			spec: agent.RunSpec{PermissionMode: agent.FullAuto},
			want: []string{
				"-p", "--output-format", "stream-json", "--trust",
				"--force", "--model", "auto",
			},
		},
		{
			name: "restricted maps to the sandbox and still trusts the worktree",
			spec: agent.RunSpec{PermissionMode: agent.Restricted},
			want: []string{
				"-p", "--output-format", "stream-json", "--trust",
				"--sandbox", "enabled", "--model", "auto",
			},
		},
		{
			name: "model passes through",
			spec: agent.RunSpec{Model: "claude-sonnet-5-thinking-high"},
			want: []string{
				"-p", "--output-format", "stream-json", "--trust",
				"--force", "--model", "claude-sonnet-5-thinking-high",
			},
		},
		{
			// §9.7: cursor has no effort flag — reasoning depth lives in the
			// model id. An effort that survived §8.2 must be dropped, not
			// improvised into a bracket override.
			name: "effort is dropped, never composed into the model",
			spec: agent.RunSpec{Model: "gpt-5.5-high", Effort: "xhigh"},
			want: []string{
				"-p", "--output-format", "stream-json", "--trust",
				"--force", "--model", "gpt-5.5-high",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withSandbox(t, true)
			got, err := buildArgs(tt.spec)
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("buildArgs = %q, want %q", got, tt.want)
			}
		})
	}
}

// withSandbox pins the platform capability for one test, so both legs of the
// restricted-mode decision are covered on every OS CI runs.
func withSandbox(t *testing.T, available bool) {
	t.Helper()
	prev := sandboxAvailable
	sandboxAvailable = available
	t.Cleanup(func() { sandboxAvailable = prev })
}

// TestRestrictedWithoutSandboxRefuses is the §9.7 safety rule. Cursor's
// sandbox requires macOS or Linux; where it is unavailable a restricted step
// must fail rather than run, because the only other option is executing
// full-auto a step that explicitly asked to be restricted.
func TestRestrictedWithoutSandboxRefuses(t *testing.T) {
	withSandbox(t, false)
	args, err := buildArgs(agent.RunSpec{PermissionMode: agent.Restricted})
	if !errors.Is(err, agent.ErrRestrictedUnsupported) {
		t.Fatalf("buildArgs error = %v, want agent.ErrRestrictedUnsupported", err)
	}
	if args != nil {
		t.Errorf("args = %v, want none alongside the refusal", args)
	}
	// Full-auto is unaffected: the platform limit is about restricting, not
	// about running.
	if _, err := buildArgs(agent.RunSpec{PermissionMode: agent.FullAuto}); err != nil {
		t.Errorf("full-auto refused on a sandboxless platform: %v", err)
	}
}

// TestStartRefusesRestrictedWithoutSandbox proves the refusal reaches the
// caller as a failed start, never as a silently-downgraded run.
func TestStartRefusesRestrictedWithoutSandbox(t *testing.T) {
	withSandbox(t, false)
	a := fakeAdapter(t)
	h, err := a.Start(t.Context(), agent.RunSpec{
		Prompt:         "should never run",
		WorkDir:        t.TempDir(),
		PermissionMode: agent.Restricted,
	})
	if !errors.Is(err, agent.ErrRestrictedUnsupported) {
		t.Fatalf("Start error = %v, want agent.ErrRestrictedUnsupported", err)
	}
	if h != nil {
		t.Error("Start returned a handle; no process may be spawned")
	}
}

// TestBuildArgsNeverPassesCursorWorktree guards §9.7's ownership rule:
// worktrees belong to vincent (§10), so cursor's own worktree flags must
// never appear no matter what the spec carries.
func TestBuildArgsNeverPassesCursorWorktree(t *testing.T) {
	built, err := buildArgs(agent.RunSpec{Model: "auto"})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	args := strings.Join(built, " ")
	for _, banned := range []string{"--worktree", "--worktree-base", "--resume", "--continue"} {
		if strings.Contains(args, banned) {
			t.Errorf("buildArgs contains %q; vincent owns worktrees and sessions (§9.7, §10)", banned)
		}
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("FAKEAGENT_DIALECT", "cursor")
	av, err := fakeAdapter(t).Detect(t.Context())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !av.Found {
		t.Fatalf("Found = false (%s), want true", av.Error)
	}
	// Recorded verbatim: calver plus a sha, never reduced to semver (§9.7).
	if av.Version != "2026.08.04-fake000" {
		t.Errorf("Version = %q, want the verbatim calver+sha", av.Version)
	}
	if av.SupportsInput {
		t.Error("SupportsInput = true; cursor-agent has no control channel (§9.7)")
	}
	if av.LoggedIn == nil || !*av.LoggedIn {
		t.Errorf("LoggedIn = %v, want a definite true — cursor can answer this (§9.5)", av.LoggedIn)
	}
}

// TestDetectLoggedOut pins the negative leg of the §9.5 probe: an installed,
// authenticated-looking CLI that will fail every run must be distinguishable
// from a healthy one.
func TestDetectLoggedOut(t *testing.T) {
	t.Setenv("FAKEAGENT_DIALECT", "cursor")
	t.Setenv("FAKEAGENT_CURSOR_LOGGED_OUT", "1")
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

func TestDetectMissingBinary(t *testing.T) {
	a := New(func() string { return "/nonexistent/cursor-agent-not-here" })
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
		t.Errorf("tokens = %d/%d, want 100/42 (camelCase usage keys, §9.7)", res.InputTokens, res.OutputTokens)
	}
	if res.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil (cursor reports no cost, §9.7)", *res.CostUSD)
	}
	counts := map[agent.EventType]int{}
	for _, ev := range events {
		counts[ev.Type]++
	}
	if counts[agent.EventOutput] == 0 || counts[agent.EventToolUse] != 1 ||
		counts[agent.EventResult] != 1 {
		t.Errorf("event mix %v, want output + exactly 1 tool_use (started only) + 1 result", counts)
	}
	// system, user, thinking×2, tool_call/completed and the fake_marker are
	// all transcripted-but-unnormalized (§9.7).
	if counts[agent.EventUnknown] < 5 {
		t.Errorf("unknown events = %d, want the system/user/thinking/completed/marker lines preserved",
			counts[agent.EventUnknown])
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
		t.Errorf("ExitCode = 0, want nonzero")
	}
	if !res.IsError || !strings.Contains(res.ErrorMessage, "fake cursor failed on purpose") {
		t.Errorf("IsError=%v msg=%q, want the error result surfaced", res.IsError, res.ErrorMessage)
	}
}

// TestRunNoResultCarriesStderr is the everyday cursor failure, not a corner:
// an invalid model id exits nonzero with the reason on stderr and no result
// event at all, so the stderr tail is the only diagnosis available (§9.7).
func TestRunNoResultCarriesStderr(t *testing.T) {
	h := startRun(t, "no-result")
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !res.IsError {
		t.Fatal("IsError = false; a stream with no result event is a failure")
	}
	if !strings.Contains(res.ErrorMessage, "stream ended without a result event") {
		t.Errorf("ErrorMessage = %q, want the no-result diagnosis", res.ErrorMessage)
	}
	if !strings.Contains(res.ErrorMessage, "Model name is not valid") {
		t.Errorf("ErrorMessage = %q, want the stderr tail appended", res.ErrorMessage)
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

func TestRespondUnsupported(t *testing.T) {
	h := startRun(t, "success")
	if err := h.Respond(agent.InputResponse{}); err == nil {
		t.Error("Respond succeeded; cursor-agent -p is non-interactive")
	}
	drain(t, h)
	_, _ = h.Wait()
}
