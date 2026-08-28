package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// TestRenderTaskAgainstRealServer binds `--task` against the **real** API
// handlers over httptest. That is what keeps the four fields task 044 put
// back on apiclient.TaskDetail — base_branch and the three overrides — from
// drifting from the server DTO that has always served them: a rename on
// either side makes the assertions below read a zero value.
func TestRenderTaskAgainstRealServer(t *testing.T) {
	dataDir := t.TempDir()
	token, err := daemon.EnsureToken(dataDir)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	project := &store.Project{Name: "live", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task := &store.Task{
		ProjectID: project.ID, Title: "Ship the thing", Description: "the description",
		Fields:       map[string]string{"ticket": "ABC-1"},
		WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "release", BranchName: "vincent/7-ship",
		AgentOverride: "codex", ModelOverride: "gpt-5", EffortOverride: "high",
		State: store.TaskQueued,
	}
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	srv := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	publishDaemon(t, dataDir, ts.URL)

	file := filepath.Join(t.TempDir(), "wf.yaml")
	body := `name: demo
steps:
  - id: plan
    type: agent
    prompt: |
      {{.Task.Title}} / {{.Task.Description}} / {{.Task.Fields.ticket}}
      {{.Task.BranchName}} onto {{.Task.BaseBranch}} in {{.Project.Name}}
`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	out, code := runWorkflowInDataDir(t, dataDir,
		"render", file, "--task", strconv.FormatInt(task.ID, 10), "--json")
	if code != 0 {
		t.Fatalf("render --task exit code = %d, want 0: %s", code, out)
	}
	var got renderResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json is not JSON: %v (%s)", err, out)
	}
	if len(got.Steps) != 1 || len(got.Steps[0].Fields) != 1 {
		t.Fatalf("unexpected result: %+v", got)
	}
	rendered := got.Steps[0].Fields[0].Output
	for _, want := range []string{
		"Ship the thing", "the description", "ABC-1",
		"vincent/7-ship", "onto release", "in live",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered prompt is missing %q:\n%s", want, rendered)
		}
	}

	// The task's own §8.6 level-2 override is what the step resolves through.
	sel := got.Steps[0].Selection
	if sel == nil {
		t.Fatal("agent step carries no selection")
	}
	if sel.Agent.Value != "codex" || sel.Agent.Source != "task" {
		t.Errorf("agent = %+v, want codex from the task override", sel.Agent)
	}
	if sel.Model.Value != "gpt-5" || sel.Model.Source != "task" {
		t.Errorf("model = %+v, want gpt-5 from the task override", sel.Model)
	}
	if sel.Effort.Value != "high" || sel.Effort.Source != "task" {
		t.Errorf("effort = %+v, want high from the task override", sel.Effort)
	}
}

// publishDaemon writes the daemon.json a client discovers, pointing at an
// httptest server. Only on-disk discovery is faked; the handlers are real.
func publishDaemon(t *testing.T, dataDir, serverURL string) {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("test server port: %v", err)
	}
	if err := daemon.WriteRuntimeInfo(dataDir, daemon.RuntimeInfo{
		Port: port, PID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("daemon.json: %v", err)
	}
}

// runWorkflowInDataDir is runWorkflowCLI against a data dir the caller owns,
// so the published daemon.json is the one the command discovers.
func runWorkflowInDataDir(t *testing.T, dataDir string, args ...string) (string, int) {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv(config.EnvDataDir, dataDir)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SilenceErrors = true
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"workflow"}, args...))
	code := asExitCode(root.ExecuteContext(context.Background()))
	return buf.String(), code
}
