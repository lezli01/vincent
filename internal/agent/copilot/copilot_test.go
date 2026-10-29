package copilot

import (
	"context"
	"errors"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/mcp/mcptest"
)

var _ agent.Adapter = (*Adapter)(nil)

func fakeAdapter(t *testing.T) *Adapter {
	t.Helper()
	path := agenttest.BuildFakeAgent(t)
	return New(func() string { return path })
}

// startRun launches the fake agent with the given scenario and returns the
// handle. Scenario env rides RunSpec.Env so tests never mutate process env.
func startRun(t *testing.T, scenario string) agent.RunHandle {
	t.Helper()
	h, err := fakeAdapter(t).Start(t.Context(), agent.RunSpec{
		Prompt:         "test prompt for " + scenario,
		WorkDir:        t.TempDir(),
		PermissionMode: agent.FullAuto,
		Env:            append(os.Environ(), "FAKEAGENT_SCENARIO="+scenario),
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

// uuidRe is a version 4 UUID, lowercase, as copilot prints them.
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestName(t *testing.T) {
	if got := New(nil).Name(); got != "copilot" {
		t.Errorf("Name() = %q, want copilot", got)
	}
}

func TestBuildArgs(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	base := []string{
		"--output-format", "json", "--no-auto-update", "--session-id", id,
		"--allow-all-tools", "--allow-all-paths", "--allow-all-urls",
	}
	tests := []struct {
		name string
		spec agent.RunSpec
		want []string
	}{
		{
			// Decision 9: no --model when nothing resolved one — the CLI
			// picks `auto` itself.
			name: "no model and no effort pass nothing",
			spec: agent.RunSpec{PermissionMode: agent.FullAuto},
			want: base,
		},
		{
			name: "model passes through",
			spec: agent.RunSpec{Model: "gpt-6-luna"},
			want: append(slices.Clone(base), "--model", "gpt-6-luna"),
		},
		{
			name: "effort passes through",
			spec: agent.RunSpec{Effort: "high"},
			want: append(slices.Clone(base), "--reasoning-effort", "high"),
		},
		{
			name: "model and effort",
			spec: agent.RunSpec{Model: "gpt-6-luna", Effort: "low"},
			want: append(slices.Clone(base), "--model", "gpt-6-luna", "--reasoning-effort", "low"),
		},
		{
			// Task 138.8: inline --additional-mcp-config, and --allow-tool
			// naming the server so restricted mode (138.10) keeps it.
			name: "mcp server rides inline with its allow-tool",
			spec: agent.RunSpec{
				Model: "gpt-6-luna",
				MCP:   &agent.MCPServer{Name: "vincent", URL: "http://127.0.0.1:7777/mcp/step/9", Token: "s3cret-_A9"},
			},
			want: append(slices.Clone(base), "--model", "gpt-6-luna",
				"--additional-mcp-config",
				`{"mcpServers":{"vincent":{"type":"http","url":"http://127.0.0.1:7777/mcp/step/9",`+
					`"headers":{"Authorization":"Bearer s3cret-_A9"},"tools":["*"]}}}`,
				"--allow-tool=vincent"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildArgs(tt.spec, id)
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("buildArgs = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSessionIDIsAFreshUUID pins decision 3: the id is minted per run, so two
// runs never share a session copilot would then resume.
func TestSessionIDIsAFreshUUID(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		id := newSessionID()
		if !uuidRe.MatchString(id) {
			t.Fatalf("session id %q is not a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("session id %q minted twice", id)
		}
		seen[id] = true
	}
}

// TestInterimRefusals pins the two refusals from Start, each with its
// sentinel and no process spawned. Each is replaced by the item named in
// buildArgs (§9.9).
func TestInterimRefusals(t *testing.T) {
	tests := []struct {
		name string
		spec agent.RunSpec
		want error
	}{
		{
			name: "restricted",
			spec: agent.RunSpec{PermissionMode: agent.Restricted},
			want: agent.ErrRestrictedUnsupported,
		},
		{
			name: "resume",
			spec: agent.RunSpec{ResumeSessionID: "11111111-2222-4333-8444-555555555555"},
			want: agent.ErrResumeUnsupported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &agenttest.RecordingLauncher{}
			spec := tt.spec
			spec.Prompt = "should never run"
			spec.WorkDir = t.TempDir()
			spec.Launcher = rec
			h, err := fakeAdapter(t).Start(t.Context(), spec)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Start error = %v, want %v", err, tt.want)
			}
			if h != nil {
				t.Error("Start returned a handle; no process may be spawned")
			}
			if n := len(rec.Launches()); n != 0 {
				t.Errorf("launcher saw %d commands, want none", n)
			}
		})
	}
}

// TestStartMCPCallback is per-step wiring proven from a running process
// (task 138.8, after 057.9): the fake agent reads the endpoint and secret out
// of the --additional-mcp-config this adapter rendered — refusing it without
// --allow-tool, type http, a bearer header or a tools filter — and calls
// step_status over the real per-step endpoint.
func TestStartMCPCallback(t *testing.T) {
	srv := mcptest.New(t)
	h := startMCPCallback(t, fakeAdapter(t), srv, "copilot called back")
	checkMCPCallback(t, h, srv, "copilot called back")
}

// startMCPCallback starts the mcp-callback scenario wired to srv.
func startMCPCallback(t *testing.T, a *Adapter, srv *mcptest.Server, message string) agent.RunHandle {
	t.Helper()
	env := append(os.Environ(), "FAKEAGENT_SCENARIO=mcp-callback", "FAKEAGENT_MCP_STATUS="+message)
	env = append(env, srv.Env()...)
	h, err := a.Start(t.Context(), agent.RunSpec{
		Prompt:         "call back",
		WorkDir:        t.TempDir(),
		PermissionMode: agent.FullAuto,
		Env:            env,
		MCP:            srv.MCP,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return h
}

// checkMCPCallback waits for the run and asserts exactly one step_status
// carrying message reached the server.
func checkMCPCallback(t *testing.T, h agent.RunHandle, srv *mcptest.Server, message string) {
	t.Helper()
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 || res.IsError {
		t.Fatalf("got exit=%d isError=%v (%s), want clean success", res.ExitCode, res.IsError, res.ErrorMessage)
	}
	want := mcptest.StatusCall{TaskID: "42", StepID: mcptest.StepID, Message: message}
	if got := srv.StatusCalls(); len(got) != 1 || got[0] != want {
		t.Errorf("status calls = %+v, want exactly %+v", got, want)
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("FAKEAGENT_DIALECT", "copilot")
	// A token in the environment proves nothing — it may be a classic PAT
	// Copilot rejects — so LoggedIn stays nil with one set (task 138.6).
	t.Setenv("GH_TOKEN", "ghp_not-a-copilot-credential")
	av, err := fakeAdapter(t).Detect(t.Context())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !av.Found {
		t.Fatalf("Found = false (%s), want true", av.Error)
	}
	if av.Version != "1.0.105" {
		t.Errorf("Version = %q, want 1.0.105 (period and update hint stripped)", av.Version)
	}
	if av.VersionVerdict != agent.VersionTested {
		t.Errorf("VersionVerdict = %q, want tested", av.VersionVerdict)
	}
	if av.SupportsInput {
		t.Error("SupportsInput = true; copilot has no input channel (§9.9)")
	}
	if av.LoggedIn != nil {
		t.Errorf("LoggedIn = %v, want nil by design (decision 10), GH_TOKEN or not", *av.LoggedIn)
	}
	if av.TestedVersions != "1.0.105" {
		t.Errorf("TestedVersions = %q, want 1.0.105", av.TestedVersions)
	}
}

// TestParseVersionFixture parses the captured `--version` output, not the
// fake's rendition of it.
func TestParseVersionFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/version_1.0.105.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := parseVersion(string(b)); got != "1.0.105" {
		t.Errorf("parseVersion(version_1.0.105.txt) = %q, want 1.0.105", got)
	}
	// A reworded sentence is recorded verbatim, so it judges untested rather
	// than unknown.
	if got := parseVersion("copilot 2.0.0-beta\r\n"); got != "copilot 2.0.0-beta" {
		t.Errorf("parseVersion(reworded) = %q, want the line verbatim", got)
	}
	if got := parseVersion(""); got != "" {
		t.Errorf("parseVersion(empty) = %q, want empty", got)
	}
}

func TestDetectMissingBinary(t *testing.T) {
	av, err := New(func() string { return "/nonexistent/copilot-not-here" }).Detect(t.Context())
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
	if res.InputTokens != 0 || res.OutputTokens != 0 || res.CostUSD != nil {
		t.Errorf("tokens %d/%d cost %v, want unreported until task 138.7",
			res.InputTokens, res.OutputTokens, res.CostUSD)
	}
	if !uuidRe.MatchString(res.SessionID) {
		t.Errorf("SessionID = %q, want the generated UUID", res.SessionID)
	}
	if id, ok := flagAfter(h.Argv(), "--session-id"); !ok || id != res.SessionID {
		t.Errorf("SessionID = %q, argv --session-id = %q", res.SessionID, id)
	}
	counts := map[agent.EventType]int{}
	for _, ev := range events {
		counts[ev.Type]++
	}
	if counts[agent.EventOutput] < 2 || counts[agent.EventResult] != 1 || counts[agent.EventUsage] != 0 {
		t.Errorf("event mix %v, want outputs, exactly 1 result and no usage", counts)
	}
}

// flagAfter returns the value following flag in argv.
func flagAfter(argv []string, flag string) (string, bool) {
	i := slices.Index(argv, flag)
	if i < 0 || i+1 >= len(argv) {
		return "", false
	}
	return argv[i+1], true
}

func TestRunErrorEvent(t *testing.T) {
	h := startRun(t, "error-event")
	events := drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want nonzero")
	}
	if !res.IsError || !strings.Contains(res.ErrorMessage, "fake copilot failed on purpose") {
		t.Errorf("IsError=%v msg=%q, want the session.error surfaced", res.IsError, res.ErrorMessage)
	}
	if !slices.ContainsFunc(events, func(ev agent.Event) bool { return ev.Type == agent.EventError }) {
		t.Error("no error event for the session.error line")
	}
}

// TestRunNoResultCarriesStderr: an unavailable model exits 1 with the reason
// on stderr and no result line, so the stderr tail is the only diagnosis.
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
	if !strings.Contains(res.ErrorMessage, "is not available") {
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
	// Unlike cursor, copilot's result line carries the exit code itself, so
	// the stream does not claim a success the process denies.
	if !res.IsError {
		t.Error("IsError = false; the result line reported exitCode 3")
	}
}

// TestRunKillKeepsTheSessionID is the reason decision 3 mints the id: a run
// killed before its result line still records the session it ran in.
func TestRunKillKeepsTheSessionID(t *testing.T) {
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
	id, _ := flagAfter(h.Argv(), "--session-id")
	if res.SessionID == "" || res.SessionID != id {
		t.Errorf("SessionID = %q, want the generated --session-id %q", res.SessionID, id)
	}
}

// TestRunTerminateIsNotSuccess: copilot 1.0.105 traps SIGTERM, ends the
// stream `abort`, `result{exitCode:0}` and exits 0 (the fake's `sigterm`
// scenario). The whole path — exit 0, a result line present — must still
// report an error naming the abort. Windows has no SIGTERM to trap, so the
// fake ends that way unprompted there and Terminate is not sent.
func TestRunTerminateIsNotSuccess(t *testing.T) {
	h := startRun(t, "sigterm")
	for ev := range h.Events() {
		if ev.Type == agent.EventOutput {
			break
		}
	}
	if runtime.GOOS != "windows" {
		if err := h.Terminate(); err != nil {
			t.Fatalf("Terminate: %v", err)
		}
	}
	drain(t, h)
	res, _ := h.Wait()
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d; the case under test is a run that exits 0", res.ExitCode)
	}
	if !res.IsError || !strings.Contains(res.ErrorMessage, "user_initiated") {
		t.Errorf("terminated run = %+v, want an error naming the abort", res)
	}
}

// TestRunTerminateWithoutResultIsNotSuccess: a CLI that dies of the signal
// before any result line is not a success either.
func TestRunTerminateWithoutResultIsNotSuccess(t *testing.T) {
	h := startRun(t, "hang")
	for ev := range h.Events() {
		if ev.Type == agent.EventOutput {
			break
		}
	}
	if err := h.Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	drain(t, h)
	res, _ := h.Wait()
	if !res.IsError && res.ExitCode == 0 {
		t.Errorf("terminated run reported success: %+v", res)
	}
}

func TestRunContextCancelKills(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	h, err := fakeAdapter(t).Start(ctx, agent.RunSpec{
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
	if !res.IsError {
		t.Error("IsError = false after cancel")
	}
}

func TestRespondUnsupported(t *testing.T) {
	h := startRun(t, "success")
	if err := h.Respond(agent.InputResponse{}); err == nil {
		t.Error("Respond succeeded; copilot has no mid-run input")
	}
	drain(t, h)
	_, _ = h.Wait()
}

// TestAssemble covers what Wait makes of a run's leftovers, the legs a
// running fake cannot reach included.
func TestAssemble(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	ok := &agent.RunResult{SessionID: id, ResultText: "pong"}
	tests := []struct {
		name      string
		terminal  *agent.RunResult
		streamErr error
		wantErr   string // "" = success
		failure   agent.FailureKind
	}{
		{name: "agreeing result", terminal: ok},
		{name: "result without a session id", terminal: &agent.RunResult{ResultText: "pong"}},
		{name: "no result carries stderr", wantErr: "stream ended without a result event: boom"},
		{
			// session_id_unknown_1.0.105.jsonl shows copilot adopting the id
			// it was given; a result naming another one contradicts it.
			name:     "foreign session",
			terminal: &agent.RunResult{SessionID: "99999999-2222-4333-8444-555555555555"},
			wantErr:  "not the --session-id " + id,
		},
		{
			name:      "stream error outranks a clean result",
			terminal:  ok,
			streamErr: errors.New("token too long"),
			wantErr:   "agent stream capture failed: token too long",
			failure:   agent.FailureStreamError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := assemble(0, id, tt.terminal, tt.streamErr, "boom")
			if res.SessionID != id {
				t.Errorf("SessionID = %q, want the generated %q", res.SessionID, id)
			}
			if res.IsError != (tt.wantErr != "") || !strings.Contains(res.ErrorMessage, tt.wantErr) {
				t.Errorf("IsError=%v msg=%q, want error %q", res.IsError, res.ErrorMessage, tt.wantErr)
			}
			if tt.failure == "" && res.Failure != nil {
				t.Errorf("Failure = %+v, want nil", res.Failure)
			}
			if tt.failure != "" && (res.Failure == nil || res.Failure.Kind != tt.failure) {
				t.Errorf("Failure = %+v, want %q", res.Failure, tt.failure)
			}
		})
	}
}
