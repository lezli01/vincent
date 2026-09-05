package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// TestStepDetailsFieldsSurviveTheRealServer wires the Step Details tab to the
// real API handlers over httptest, which is what keeps the tab from reading a
// wire type the server does not actually write. The rest of the tab's tests
// build apiclient.StepRun values directly; this one is the only place the
// migration-0027 columns make the whole trip — store → handler → apiclient →
// the rendered pane — so a rename on either side fails here rather than
// silently rendering "not recorded" forever.
func TestStepDetailsFieldsSurviveTheRealServer(t *testing.T) {
	const token = "step-details-live-token"
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	p := &store.Project{Name: "live", Path: "/nowhere", DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task := &store.Task{
		ProjectID: p.ID, Title: "step details", WorkflowName: "adhoc", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "b", State: store.TaskRunning,
	}
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Two rows: one that recorded its input and one that never did. The
	// second is the pre-0027 shape — it is inserted and simply never handed
	// to RecordStepRunInput, which is exactly how an old row reads.
	recorded := &store.StepRun{
		TaskID: task.ID, StepIndex: 1, StepID: "implement", StepType: "agent",
		Attempt: 2, Iteration: 3, LoopTotal: 5, LoopItem: "api",
		State: store.StepRunning, Agent: "claude", Model: "opus", Effort: "high",
	}
	if err := st.CreateStepRun(ctx, recorded); err != nil {
		t.Fatalf("CreateStepRun(recorded): %v", err)
	}
	bare := &store.StepRun{
		TaskID: task.ID, StepIndex: 2, StepID: "verify", StepType: "agent",
		Attempt: 1, State: store.StepRunning,
	}
	if err := st.CreateStepRun(ctx, bare); err != nil {
		t.Fatalf("CreateStepRun(bare): %v", err)
	}

	prompt := "Implement OPS-42 in {{ .Fields.pkg }}."
	check := "go test ./..."
	guard := "true"
	forEach := `["api","web"]`
	in := store.StepRunInput{
		Prompt: &prompt, Check: &check, Guard: &guard, ForEach: &forEach,
		AgentSource: "task", ModelSource: "workflow", EffortSource: "adapter",
		PermissionMode: "restricted",
		TimeoutMS:      600_000, CheckTimeoutMS: 120_000,
		Shell: "/bin/sh", WorkDir: "/tmp/wt/7",
	}
	if err := st.RecordStepRunInput(ctx, recorded.ID, in); err != nil {
		t.Fatalf("RecordStepRunInput: %v", err)
	}

	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	detail, err := apiclient.New(ts.URL, token).GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(detail.Steps) != 2 {
		t.Fatalf("the server returned %d step runs, want 2", len(detail.Steps))
	}

	var got, old apiclient.StepRun
	for _, r := range detail.Steps {
		switch r.ID {
		case recorded.ID:
			got = r
		case bare.ID:
			old = r
		}
	}

	// The recorded row: every column this tab reads made the trip.
	for _, c := range []struct {
		name string
		got  *string
		want string
	}{
		{"rendered_prompt", got.RenderedPrompt, prompt},
		{"rendered_check", got.RenderedCheck, check},
		{"rendered_if", got.RenderedIf, guard},
		{"rendered_for_each", got.RenderedForEach, forEach},
		{"agent_source", got.AgentSource, "task"},
		{"model_source", got.ModelSource, "workflow"},
		{"effort_source", got.EffortSource, "adapter"},
		{"permission_mode", got.PermissionMode, "restricted"},
		{"shell", got.Shell, "/bin/sh"},
		{"work_dir", got.WorkDir, "/tmp/wt/7"},
	} {
		if c.got == nil {
			t.Errorf("%s came back nil, want %q", c.name, c.want)
			continue
		}
		if *c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, *c.got, c.want)
		}
	}
	if got.TimeoutMS != 600_000 {
		t.Errorf("timeout_ms = %d, want 600000", got.TimeoutMS)
	}
	if got.CheckTimeoutMS != 120_000 {
		t.Errorf("check_timeout_ms = %d, want 120000", got.CheckTimeoutMS)
	}
	// An agent step records no run script; nil here is the type having no
	// such input, not a lost column.
	if got.RenderedRun != nil {
		t.Errorf("rendered_run = %q on an agent step, want null", *got.RenderedRun)
	}

	// The row that never recorded: nil all the way through, which is what the
	// tab turns into "not recorded" rather than an empty prompt.
	for _, c := range []struct {
		name string
		got  *string
	}{
		{"rendered_prompt", old.RenderedPrompt},
		{"rendered_check", old.RenderedCheck},
		{"agent_source", old.AgentSource},
		{"shell", old.Shell},
	} {
		if c.got != nil {
			t.Errorf("%s = %q on an unrecorded row, want null", c.name, *c.got)
		}
	}
	if old.TimeoutMS != 0 {
		t.Errorf("timeout_ms = %d on an unrecorded row, want 0", old.TimeoutMS)
	}

	// And the tab renders what the server sent, on both rows.
	d := newTestDetail(t)
	d.taskID = task.ID
	d.applyLoaded(detailLoadedMsg{id: task.ID, task: detail})
	v := newTaskView(d)
	v.tab = taskTabStepDetails
	v.width, v.height = 140, 40

	d.selectedRun = recorded.ID
	body := ansi.Strip(v.renderStepDetails(140, 200))
	for _, want := range []string{prompt, check, "restricted", "/bin/sh", "/tmp/wt/7", "10m00s"} {
		if !strings.Contains(body, want) {
			t.Errorf("the tab did not render %q from the real server:\n%s", want, body)
		}
	}

	d.selectedRun = bare.ID
	if body := ansi.Strip(v.renderStepDetails(140, 200)); !strings.Contains(body, stepInputNotRecorded) {
		t.Errorf("an unrecorded row rendered no %q marker:\n%s", stepInputNotRecorded, body)
	}
}
