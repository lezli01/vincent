package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestCrashRecoveryE2E is T2.8's done criterion: kill the daemon hard in the
// middle of an agent step, restart it, and assert full recovery — the
// orphaned process is gone, the task re-queued at the same step without
// consuming a retry, and the re-run completes. Assertions converge on
// outcomes, so the platform difference (Windows' Job object reaps the tree
// when the daemon dies; POSIX leaves an orphan for recovery to kill) needs
// no branches.
func TestCrashRecoveryE2E(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	cfg := fmt.Sprintf("listen: \"127.0.0.1:0\"\nagents:\n  claude:\n    path: %q\n", fake)
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	repo := testrepo.Init(t, "main")

	// Daemon #1: the agent hangs until killed, so the crash lands mid-step.
	first := startDaemonProcess(t, dataDir, cfgDir, "hang")
	c := waitDaemonAPI(t, dataDir, first)

	var project struct {
		ID int64 `json:"id"`
	}
	c.post(t, "/v1/projects", map[string]any{"path": repo}, http.StatusCreated, &project)
	var task struct {
		ID int64 `json:"id"`
	}
	c.post(t, "/v1/tasks", map[string]any{
		"project_id": project.ID, "title": "survive a crash",
	}, http.StatusCreated, &task)

	pid := waitJournaledPID(t, c, task.ID)

	// Hard kill: SIGKILL / TerminateProcess. No goodbye, no cleanup.
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill daemon: %v", err)
	}
	<-first.exited

	// Daemon #2: recovery runs before it serves; the re-run must succeed.
	second := startDaemonProcess(t, dataDir, cfgDir, "success")
	c2 := waitDaemonAPI(t, dataDir, second)

	// The journaled process is gone, whichever mechanism reaped it.
	gone := time.Now().Add(15 * time.Second)
	for {
		if _, err := procx.StartTime(pid); errors.Is(err, procx.ErrProcessGone) {
			break
		}
		if time.Now().After(gone) {
			t.Fatalf("journaled process %d still alive after recovery", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Full recovery: re-queued at the same step, re-run to done. The ad-hoc
	// workflow allows no retries, so completing at all proves the
	// interruption consumed none (§7.2).
	deadline := time.Now().Add(90 * time.Second)
	for {
		var got struct {
			State       string `json:"state"`
			BlockReason string `json:"block_reason"`
		}
		c2.get(t, fmt.Sprintf("/v1/tasks/%d", task.ID), &got)
		if got.State == "done" {
			break
		}
		if got.State == "blocked" || got.State == "aborted" {
			t.Fatalf("task ended %s (%s), want done", got.State, got.BlockReason)
		}
		if time.Now().After(deadline) {
			t.Fatalf("task stuck in %s, want done", got.State)
		}
		time.Sleep(200 * time.Millisecond)
	}

	var steps []struct {
		StepIndex     int    `json:"step_index"`
		Attempt       int    `json:"attempt"`
		State         string `json:"state"`
		FailureReason string `json:"failure_reason"`
	}
	c2.get(t, fmt.Sprintf("/v1/tasks/%d/steps", task.ID), &steps)
	var interrupted, succeeded bool
	for _, s := range steps {
		if s.StepIndex != 0 {
			continue
		}
		if s.State == "interrupted" {
			interrupted = true
		}
		if s.State == "succeeded" {
			succeeded = true
		}
	}
	if !interrupted || !succeeded {
		t.Errorf("step 0 attempts = %+v, want an interrupted one and a succeeded one", steps)
	}

	// Daemon #2 goes down gracefully.
	c2.post(t, "/v1/daemon/stop", nil, http.StatusAccepted, nil)
	waitExit(t, second)
}

// daemonProc is a spawned daemon child plus the one goroutine that owns its
// exec.Cmd.Wait. Wait may be called exactly once, and these tests need the
// child's fate from several places at once — a kill that waits for the exit,
// a startup poll that has to notice a corpse, a cleanup that reaps whatever
// is left — so the reaper is centralized here and nothing else calls Wait.
type daemonProc struct {
	cmd    *exec.Cmd
	exited chan struct{} // closed once the child has been reaped
	err    error         // Wait's result; read only after exited is closed
}

// status describes the child for a failure message.
func (p *daemonProc) status() string {
	select {
	case <-p.exited:
		if p.err != nil {
			return "child exited: " + p.err.Error()
		}
		return "child exited: status 0"
	default:
		return fmt.Sprintf("child still running (pid %d)", p.cmd.Process.Pid)
	}
}

// startDaemonProcess runs `vincent daemon` (foreground) as a real child
// process with the fake agent's scenario in its environment, so a
// Process.Kill is a genuine daemon crash.
func startDaemonProcess(t *testing.T, dataDir, cfgDir, scenario string) *daemonProc {
	t.Helper()
	cmd := exec.Command(vincentBin, "daemon")
	cmd.Env = append(hermeticEnv(),
		config.EnvDataDir+"="+dataDir,
		config.EnvConfigDir+"="+cfgDir,
		"FAKEAGENT_SCENARIO="+scenario,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	p := &daemonProc{cmd: cmd, exited: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.exited
	})
	return p
}

// daemonExitTimeout bounds a graceful shutdown in the e2e tests: §12.4's 15 s
// process grace plus the HTTP drain has to fit inside it.
const daemonExitTimeout = 30 * time.Second

func waitExit(t *testing.T, p *daemonProc) {
	t.Helper()
	select {
	case <-p.exited:
	case <-time.After(daemonExitTimeout):
		_ = p.cmd.Process.Kill()
		t.Fatalf("daemon did not exit within %s", daemonExitTimeout)
	}
}

// apiClient is a minimal authorized client against one daemon instance.
type apiClient struct {
	base  string
	token string
}

// daemonAPIBudget is how long waitDaemonAPI gives a freshly spawned daemon to
// serve its API. It is not a claim about the daemon's startup cost: it is wall
// clock on a shared runner, and the load it competes with varies by platform
// and by build. `go test -race ./...` reaches internal/cli after internal/api
// and internal/taskrun have run in the same job, and on Windows the fixed 30 s
// this replaced turned out to be roughly the observed cost rather than a
// margin (issue #340). So scale it, and take the multipliers from the build
// rather than guessing at run time.
//
// This is a test-harness budget only. `daemon start`'s own startTimeout is a
// phase 1 decision about the user-facing command and is deliberately untouched.
func daemonAPIBudget() time.Duration {
	budget := 30 * time.Second
	if raceEnabled {
		budget *= 2
	}
	if runtime.GOOS == "windows" {
		budget *= 2
	}
	return budget
}

// waitDaemonAPI polls until the (re)started daemon serves its API and
// returns a client for it. A stale daemon.json from a crashed predecessor
// fails the health check and keeps the poll going.
//
// A daemon that died during startup will never become healthy, so the poll
// watches the child as well and gives up the moment it exits — otherwise a
// broken daemon costs the whole (now larger) budget on every run. Either way
// the failure carries the evidence: the budget actually spent, the child's
// fate, and the tail of daemon.log, which is where the daemon writes its
// reason. `daemon start` attaches the same log tail to its own timeout.
func waitDaemonAPI(t *testing.T, dataDir string, p *daemonProc) *apiClient {
	t.Helper()
	budget := daemonAPIBudget()
	start := time.Now()
	deadline := start.Add(budget)
	for {
		ri, err := daemon.ReadRuntimeInfo(dataDir)
		if err == nil {
			if _, err := daemon.CheckHealth(t.Context(), ri.Port); err == nil {
				token, err := daemon.ReadToken(dataDir)
				if err != nil {
					t.Fatalf("read token: %v", err)
				}
				return &apiClient{base: fmt.Sprintf("http://127.0.0.1:%d", ri.Port), token: token}
			}
		}
		select {
		case <-p.exited:
			t.Fatalf("daemon exited after %s without serving its API (%s)\n--- daemon.log tail ---\n%s",
				time.Since(start).Round(time.Millisecond), p.status(), daemon.LogTail(dataDir, 20))
		default:
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("daemon did not become healthy within %s (%s)\n--- daemon.log tail ---\n%s",
		budget, p.status(), daemon.LogTail(dataDir, 20))
	return nil
}

func (c *apiClient) do(t *testing.T, method, path string, body any, wantStatus int, out any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, c.base+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, wantStatus, buf.String())
	}
	if out != nil {
		if err := json.Unmarshal(buf.Bytes(), out); err != nil {
			t.Fatalf("%s %s body: %v\n%s", method, path, err, buf.String())
		}
	}
}

func (c *apiClient) post(t *testing.T, path string, body any, wantStatus int, out any) {
	t.Helper()
	c.do(t, http.MethodPost, path, body, wantStatus, out)
}

func (c *apiClient) get(t *testing.T, path string, out any) {
	t.Helper()
	c.do(t, http.MethodGet, path, nil, http.StatusOK, out)
}

// waitJournaledPID polls the task's step runs until the running attempt has
// journaled its process id — the moment a crash becomes interesting.
func waitJournaledPID(t *testing.T, c *apiClient, taskID int64) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var steps []struct {
			State string `json:"state"`
			PID   *int   `json:"pid"`
		}
		c.get(t, fmt.Sprintf("/v1/tasks/%d/steps", taskID), &steps)
		for _, s := range steps {
			if s.State == "running" && s.PID != nil {
				return *s.PID
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no running step journaled a PID within 60s")
	return 0
}
