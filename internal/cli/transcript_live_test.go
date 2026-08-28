package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/worktree"
)

// `vincent task transcript` runs against the **real** API handlers over
// httptest, not a stub: the command's whole job is to render what the
// endpoint normalizes, and a hand-written stub would be the one place client
// and server wire types could drift apart unnoticed.

// The claude dialect, which the endpoint normalizes with the same parser that
// read it live. Assertions name the rendered result, not these lines.
const (
	lineOutput = `{"type":"assistant","message":{"content":[{"type":"text","text":"looking at the failing test"}]}}`
	lineTool   = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1",` +
		`"name":"Bash","input":{"command":"go test ./internal/store"}}]}}`
	lineToolResult = `{"type":"user","message":{"content":[{"type":"tool_result",` +
		`"tool_use_id":"t1","content":"ok"}]}}`
	lineUsage  = `{"type":"system","subtype":"usage","usage":{"input_tokens":10,"output_tokens":2}}`
	lineResult = `{"type":"result","subtype":"success","is_error":false,"result":"all green",` +
		`"usage":{"input_tokens":10,"output_tokens":2},"total_cost_usd":0.0125}`
	// vincent's own annotations pass through the normalizer untouched.
	lineCommandStarted = `{"type":"vincent.command_started","phase":"run","command":"go build ./..."}`
	lineStderr         = `{"type":"vincent.output","phase":"run","stream":"stderr","text":"vet: no files"}`
	lineInputRequest   = `{"type":"vincent.input_request","kind":"question","summary":"which branch?"}`
	// An annotation this binary does not name: it still says something
	// happened, and dropping it would lose an event rather than a detail.
	lineUnknownAnnotation = `{"type":"vincent.parked","reason":"quota"}`
	// Not JSON at all. The normalizer keeps it as agent.raw and the client's
	// decoder drops it — which is why --raw must not go through that decoder.
	lineGarbage = `not json at all`
)

// liveHarness is the daemon's own API server, its store, and a data dir the
// CLI's discovery finds — everything `vincent task transcript` talks to.
type liveHarness struct {
	st        *store.Store
	dataDir   string
	taskID    int64
	projectID int64
	// transcriptCalls counts requests to the transcript endpoint, which is
	// how "no transcript is decided client-side, before the request" is
	// asserted rather than assumed.
	transcriptCalls atomic.Int64
}

func newLiveHarness(t *testing.T) *liveHarness {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv(config.EnvDataDir, dataDir)
	t.Setenv(config.EnvConfigDir, t.TempDir())

	st, err := store.Open(filepath.Join(dataDir, "vincent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := t.Context()
	p := &store.Project{Name: "vincent", Path: "/nowhere", DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task := &store.Task{
		ProjectID: p.ID, Title: "read the transcript", WorkflowName: "adhoc",
		WorkflowSnapshot: "x", BaseBranch: "main", BranchName: "b", State: store.TaskRunning,
	}
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	h := &liveHarness{st: st, dataDir: dataDir, taskID: task.ID, projectID: p.ID}
	token, err := daemon.EnsureToken(dataDir)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	git := gitx.New()
	agents := agent.NewRegistry(claude.New(func() string { return "" }))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The runner is never started: nothing here admits a task, and the rows
	// under test are written directly.
	runner := taskrun.New(taskrun.Deps{
		Store: st, Config: config.Default,
		Worktrees: worktree.NewManager(git, dataDir),
		Agents:    agents, DataDir: dataDir, Logger: logger,
	})
	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: logger,
		Store: st, Broker: broker, Git: git, Runner: runner, WakeRunner: func() {},
		// The registry is what lets the endpoint normalize a recorded run
		// with the parser that read it live.
		Agents: agents,
	})
	handler := s.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/transcript") {
			h.transcriptCalls.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("server port: %v", err)
	}
	if err := daemon.WriteRuntimeInfo(dataDir, daemon.RuntimeInfo{
		Port: port, PID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("daemon.json: %v", err)
	}
	return h
}

// addRun creates one step run, writing its transcript file when lines are
// given. A run with no lines has no transcript at all — a manual gate.
func (h *liveHarness) addRun(
	t *testing.T, taskID int64, stepID string, state store.StepRunState, lines ...string,
) *store.StepRun {
	t.Helper()
	run := &store.StepRun{
		TaskID: taskID, StepIndex: 0, StepID: stepID, StepType: "agent",
		Attempt: 1, State: state, Agent: "claude",
	}
	if len(lines) > 0 {
		dir := filepath.Join(h.dataDir, "transcripts")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir transcripts: %v", err)
		}
		run.TranscriptPath = filepath.Join(dir, stepID+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".jsonl")
		if err := os.WriteFile(run.TranscriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
	} else {
		run.StepType = "manual"
	}
	if err := h.st.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return run
}

func (h *liveHarness) appendTranscript(t *testing.T, run *store.StepRun, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(run.TranscriptPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatalf("append transcript: %v", err)
	}
}

func (h *liveHarness) setRunState(t *testing.T, run *store.StepRun, state store.StepRunState) {
	t.Helper()
	run.State = state
	if err := h.st.UpdateStepRun(t.Context(), run); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
}

// The default rendering covers every record type the renderer branches on,
// including a `vincent.*` annotation it does not name — and drops the one it
// deliberately drops.
func TestTranscriptRendersTheRecordVocabulary(t *testing.T) {
	h := newLiveHarness(t)
	run := h.addRun(t, h.taskID, "implement", store.StepSucceeded,
		lineCommandStarted, lineOutput, lineTool, lineToolResult, lineStderr,
		lineInputRequest, lineUnknownAnnotation, lineUsage, lineResult)

	out, _, code := runCLI(t, "task", "transcript", strconv.FormatInt(h.taskID, 10),
		"--step", strconv.FormatInt(run.ID, 10))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, out)
	}
	for _, want := range []string{
		"$ go build ./...",
		"looking at the failing test",
		"> Bash go test ./internal/store",
		"[stderr] vet: no files",
		"? which branch?",
		"* parked",
		"= done ($0.0125)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// agent.usage is dropped: `vincent task show` already carries those
	// numbers, and repeating them mid-transcript is noise.
	if strings.Contains(out, "input_tokens") {
		t.Errorf("output rendered the usage record:\n%s", out)
	}
}

// --raw is the file's own bytes, including a line the normalizer's client-side
// decoder drops. Anything less would make the flag's promise false.
func TestTranscriptRawIsByteIdentical(t *testing.T) {
	h := newLiveHarness(t)
	run := h.addRun(t, h.taskID, "implement", store.StepSucceeded,
		lineOutput, lineGarbage, lineResult)

	out, _, code := runCLI(t, "task", "transcript", strconv.FormatInt(h.taskID, 10),
		"--step", strconv.FormatInt(run.ID, 10), "--raw")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want, err := os.ReadFile(run.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if out != string(want) {
		t.Errorf("--raw output = %q, want the file verbatim %q", out, want)
	}
}

// --json is vincent's own vocabulary as NDJSON — one record per line,
// decodable back into the client's type, annotations and all.
func TestTranscriptJSONIsNDJSON(t *testing.T) {
	h := newLiveHarness(t)
	run := h.addRun(t, h.taskID, "implement", store.StepSucceeded,
		lineOutput, lineStderr, lineUnknownAnnotation, lineResult)

	out, _, code := runCLI(t, "task", "transcript", strconv.FormatInt(h.taskID, 10),
		"--step", strconv.FormatInt(run.ID, 10), "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want one per record:\n%s", len(lines), out)
	}
	var types []string
	for _, line := range lines {
		var rec apiclient.TranscriptRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %q is not a TranscriptRecord: %v", line, err)
		}
		types = append(types, rec.Type)
	}
	want := []string{"agent.output", "vincent.output", "vincent.parked", "agent.result"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Errorf("types = %v, want %v", types, want)
	}
	// The annotation's own fields survive: the records are the endpoint's
	// lines, not a re-encoding of the struct that would drop what it does
	// not name.
	if !strings.Contains(out, `"reason":"quota"`) {
		t.Errorf("output dropped an annotation field:\n%s", out)
	}
}

// With --step omitted: the running attempt if there is one, else the newest
// by step_run id — asserted on a task whose step_index order disagrees with
// its id order, which is the case that decision is for (parallel steps and
// fan-out lanes).
func TestTranscriptDefaultsToRunningThenNewest(t *testing.T) {
	h := newLiveHarness(t)
	// Created first, so the lower id, but the later lane by step_index.
	later := h.addRun(t, h.taskID, "lane-b", store.StepRunning, lineOutput)
	later.StepIndex = 7
	if err := h.st.UpdateStepRun(t.Context(), later); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	earlier := h.addRun(t, h.taskID, "lane-a", store.StepSucceeded,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"lane a spoke"}]}}`)
	if earlier.ID <= later.ID {
		t.Fatalf("ids are not creation-ordered: %d, %d", earlier.ID, later.ID)
	}

	out, _, code := runCLI(t, "task", "transcript", strconv.FormatInt(h.taskID, 10))
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "looking at the failing test") {
		t.Errorf("default selection did not pick the running attempt:\n%s", out)
	}

	h.setRunState(t, later, store.StepSucceeded)
	out, _, code = runCLI(t, "task", "transcript", strconv.FormatInt(h.taskID, 10))
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "lane a spoke") {
		t.Errorf("with nothing running, selection did not pick the newest id:\n%s", out)
	}
}

// A step run id belonging to another task is refused, as the endpoint refuses
// it — the CLI holds the rows already, so it says so without asking.
func TestTranscriptRejectsAForeignStepRun(t *testing.T) {
	h := newLiveHarness(t)
	h.addRun(t, h.taskID, "implement", store.StepSucceeded, lineOutput)
	other := &store.Task{
		ProjectID: h.projectID, Title: "other", WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "b2", State: store.TaskRunning,
	}
	if err := h.st.CreateTask(t.Context(), other, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	foreign := h.addRun(t, other.ID, "elsewhere", store.StepSucceeded, lineOutput)

	out, errOut, code := runCLI(t, "task", "transcript", strconv.FormatInt(h.taskID, 10),
		"--step", strconv.FormatInt(foreign.ID, 10))
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "not found on task") {
		t.Errorf("stderr = %q, want it to name the task", errOut)
	}
}

// Two different 404s, told apart from the step rows rather than from the
// message: a run that never had a transcript is not a failure, a file that is
// gone is.
func TestTranscriptNoTranscriptVersusPrunedFile(t *testing.T) {
	h := newLiveHarness(t)
	gate := h.addRun(t, h.taskID, "approve", store.StepSucceeded)

	out, errOut, code := runCLI(t, "task", "transcript", strconv.FormatInt(h.taskID, 10),
		"--step", strconv.FormatInt(gate.ID, 10))
	if code != 0 {
		t.Errorf("exit on a manual gate = %d, want 0", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut, "has no transcript") {
		t.Errorf("stderr = %q, want it to say there is no transcript", errOut)
	}
	if n := h.transcriptCalls.Load(); n != 0 {
		t.Errorf("%d transcript requests; the answer was in the step rows already", n)
	}

	pruned := h.addRun(t, h.taskID, "implement", store.StepSucceeded, lineOutput)
	if err := os.Remove(pruned.TranscriptPath); err != nil {
		t.Fatalf("prune: %v", err)
	}
	_, errOut, code = runCLI(t, "task", "transcript", strconv.FormatInt(h.taskID, 10),
		"--step", strconv.FormatInt(pruned.ID, 10))
	if code != 1 {
		t.Errorf("exit on a pruned transcript = %d, want 1", code)
	}
	if !strings.Contains(errOut, "transcript_retention_days") {
		t.Errorf("stderr = %q, want it to name the retention setting", errOut)
	}
}

// Following resumes from X-Next-Offset: no record is printed twice across the
// seam, none is missed, and the follow ends when the attempt it is printing
// stops running rather than waiting for a later retry.
func TestTranscriptFollowResumesWithoutGapOrDuplicate(t *testing.T) {
	h := newLiveHarness(t)
	run := h.addRun(t, h.taskID, "implement", store.StepRunning, lineCommandStarted)
	c, err := apiclient.Discover(h.dataDir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	var out, errOut lockedBuffer
	p := &transcriptPrinter{out: &out, errOut: &errOut}
	next, err := p.print(t.Context(), c, h.taskID, run.ID,
		apiclient.TranscriptOptions{Tail: apiclient.DefaultTailBytes})
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.follow(ctx, c, h.taskID, run.ID, next, followPoll) }()

	h.appendTranscript(t, run, lineOutput)
	waitFor(t, func() bool { return strings.Contains(out.String(), "looking at the failing test") },
		"the first appended record")
	h.appendTranscript(t, run, lineTool)
	waitFor(t, func() bool { return strings.Contains(out.String(), "> Bash") },
		"the second appended record")

	// The attempt settles: the follow prints what it wrote on the way out and
	// then returns, rather than sitting on a task that is finished as far as
	// this run is concerned.
	h.appendTranscript(t, run, lineResult)
	h.setRunState(t, run, store.StepSucceeded)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not end when the run left running")
	}

	want := []string{
		"$ go build ./...",
		"looking at the failing test",
		"> Bash go test ./internal/store",
		"= done ($0.0125)",
	}
	if got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n"); strings.Join(got, "\n") !=
		strings.Join(want, "\n") {
		t.Errorf("followed output =\n%s\nwant each record exactly once, in order:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(errOut.String(), "is succeeded") {
		t.Errorf("follow ended without saying why: %q", errOut.String())
	}
}
