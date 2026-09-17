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
	"github.com/lezli01/vincent/internal/workflow"
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
      {{.Task.BranchName}} onto {{.Task.BaseBranch}} in {{.Project.Name}} #{{.Project.ID}}
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
		"vincent/7-ship", "onto release", "in live #" + strconv.FormatInt(project.ID, 10),
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

// TestRenderProjectResolvesDerivedFanOut is issue #370's `--project` path end
// to end: a file includes a registry workflow whose fan-out derives its lanes,
// the callee comes back through the real GET /v1/workflows/definition, and its
// `lane:` template must survive the trip back to the parser's model — or the
// lane's step, and the template's own id, never render at all.
func TestRenderProjectResolvesDerivedFanOut(t *testing.T) {
	dataDir, projectID := serveRegistryWorkflow(t, "derive", `name: derive
steps:
  - id: plan
    type: command
    run: "plan {{ .Task.Title }}"
  - id: build
    type: fan_out
    max_lanes: 8
    schedule: eager
    for_each: '{{ .Steps.plan.Result }}'
    lane:
      id: '{{ .Item.id }}'
      needs: '{{ .Item.needs }}'
      steps:
        - id: implement
          type: command
          run: "make {{ .Task.Title }}"
`)

	file := filepath.Join(t.TempDir(), "wf.yaml")
	body := `name: outer
steps:
  - id: derived
    type: include
    workflow: derive
`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	out, code := runWorkflowInDataDir(t, dataDir,
		"render", file, "--project", strconv.FormatInt(projectID, 10), "--json")
	if code != 0 {
		t.Fatalf("render --project exit code = %d, want 0: %s", code, out)
	}
	var got renderResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json is not JSON: %v (%s)", err, out)
	}

	outputs := map[string]string{}
	var derivedLane []string
	for _, s := range got.Steps {
		for _, f := range s.Fields {
			outputs[s.ID+" "+f.Field] = f.Output
		}
		if s.ID == "implement" {
			derivedLane = s.DerivedLane
		}
	}
	if want := "make " + workflow.SentinelTitle; outputs["implement run"] != want {
		t.Errorf("implement run = %q, want %q — the callee's derived lane step did not render: %+v",
			outputs["implement run"], want, got.Steps)
	}
	if outputs["build lane.id"] != workflow.SentinelItem("id") {
		t.Errorf("lane.id = %q, want %q: %+v", outputs["build lane.id"], workflow.SentinelItem("id"), got.Steps)
	}
	if strings.Join(derivedLane, ",") != "{{ .Steps.plan.Result }}" {
		t.Errorf("implement's derived_lane = %q, want the for_each it expands over", derivedLane)
	}
}

// TestRenderProjectDrawsResolvedLaneDAG is issue #407's `--project` path: a
// callee whose declared lanes carry `needs:` comes back through the real GET
// /v1/workflows/definition, and after the include is spliced the fan-out draws
// the same graph a local file would — which only holds while the definition
// DTO carries a lane's `needs` and the step's `schedule`.
func TestRenderProjectDrawsResolvedLaneDAG(t *testing.T) {
	dataDir, projectID := serveRegistryWorkflow(t, "dag", `name: dag
steps:
  - id: spread
    type: fan_out
    schedule: eager
    lanes:
      - {id: api, steps: [{id: api_impl, type: command, run: api}]}
      - {id: db, steps: [{id: db_migrate, type: command, run: db}]}
      - {id: wire, needs: [api, db], steps: [{id: wire_up, type: command, run: wire}]}
`)

	file := filepath.Join(t.TempDir(), "wf.yaml")
	if err := os.WriteFile(file, []byte("name: outer\nsteps:\n  - {id: shared, type: include, workflow: dag}\n"), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	out, code := runWorkflowInDataDir(t, dataDir, "render", file, "--project", strconv.FormatInt(projectID, 10))
	if code != 0 {
		t.Fatalf("render --project exit code = %d, want 0: %s", code, out)
	}
	want := "spread (fan_out)\n" +
		"  schedule: eager\n" +
		"  lanes:\n" +
		"    wave 1: api, db\n" +
		"    wave 2: wire (needs api, db)\n"
	if !strings.Contains(out, want) {
		t.Errorf("the resolved fan-out does not draw its graph; want\n%s\nin\n%s", want, out)
	}
}

// serveRegistryWorkflow starts the real API handlers over httptest with one
// project and a global registry holding callee under name, and publishes the
// daemon.json a client discovers. It returns that data dir and the project id.
func serveRegistryWorkflow(t *testing.T, name, callee string) (dataDir string, projectID int64) {
	t.Helper()
	dataDir = t.TempDir()
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

	project := &store.Project{Name: "live", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(context.Background(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	globalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(globalDir, name+".yaml"), []byte(callee), 0o600); err != nil {
		t.Fatalf("write callee: %v", err)
	}
	reg := workflow.NewRegistry(globalDir, workflow.Options{}, nil)
	reg.Reload()
	if e, ok := reg.Lookup(project.ID, name); !ok || len(e.Errors) > 0 {
		t.Fatalf("callee did not load cleanly: ok=%v %+v", ok, e.Errors)
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
		Workflows:   reg,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	publishDaemon(t, dataDir, ts.URL)
	return dataDir, project.ID
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
