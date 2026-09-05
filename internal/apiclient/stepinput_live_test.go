package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// The migration-0027 record over the wire, against the real handlers (issue
// #323): what an attempt was handed and the resolution behind it, carried
// from the row to the client's StepRun. This is where the server's
// stepRunResponse and the client's StepRun would drift apart if either side
// grew the fields alone — and the nil-versus-empty distinction is the part a
// round trip is needed to prove, because a details pane says the two
// differently.
func TestStepRunInputRecordOverTheWire(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	run := &store.StepRun{
		TaskID: h.taskID, StepIndex: 0, StepID: "implement", StepType: "command",
		Attempt: 1, State: store.StepRunning,
	}
	if err := h.st.CreateStepRun(t.Context(), run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	empty := ""
	rendered := "go test ./..."
	forEach := `["a","b"]`
	if err := h.st.RecordStepRunInput(t.Context(), run.ID, store.StepRunInput{
		Run: &rendered, Check: &empty, ForEach: &forEach,
		AgentSource: "task", ModelSource: "workflow", EffortSource: "adapter",
		PermissionMode: "restricted",
		TimeoutMS:      600_000, CheckTimeoutMS: 120_000,
		Shell: "/bin/sh", WorkDir: "/tmp/wt",
	}); err != nil {
		t.Fatalf("RecordStepRunInput: %v", err)
	}

	detail, err := c.GetTask(t.Context(), h.taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(detail.Steps) != 1 {
		t.Fatalf("steps = %d, want the one attempt", len(detail.Steps))
	}
	got := detail.Steps[0]

	if got.RenderedRun == nil || *got.RenderedRun != rendered {
		t.Errorf("RenderedRun = %v, want the recorded bytes", got.RenderedRun)
	}
	// A render that produced nothing must not arrive as "nothing was
	// recorded"; a null anywhere on this path collapses the two.
	if got.RenderedCheck == nil || *got.RenderedCheck != "" {
		t.Errorf("RenderedCheck = %v, want a non-nil empty render", got.RenderedCheck)
	}
	if got.RenderedPrompt != nil || got.RenderedIf != nil {
		t.Errorf("fields nothing recorded came back non-nil: prompt=%v if=%v",
			got.RenderedPrompt, got.RenderedIf)
	}
	if got.RenderedForEach == nil || *got.RenderedForEach != forEach {
		t.Errorf("RenderedForEach = %v, want the resolved list", got.RenderedForEach)
	}
	if got.InputTruncated {
		t.Error("InputTruncated on a record that lost nothing")
	}
	if got.AgentSource == nil || *got.AgentSource != "task" ||
		got.ModelSource == nil || *got.ModelSource != "workflow" ||
		got.EffortSource == nil || *got.EffortSource != "adapter" {
		t.Errorf("sources = %v/%v/%v, want task/workflow/adapter",
			got.AgentSource, got.ModelSource, got.EffortSource)
	}
	if got.PermissionMode == nil || *got.PermissionMode != "restricted" {
		t.Errorf("PermissionMode = %v, want restricted", got.PermissionMode)
	}
	if got.TimeoutMS != 600_000 || got.CheckTimeoutMS != 120_000 {
		t.Errorf("timeouts = %d/%d, want 600000/120000", got.TimeoutMS, got.CheckTimeoutMS)
	}
	if got.Shell == nil || *got.Shell != "/bin/sh" || got.WorkDir == nil || *got.WorkDir != "/tmp/wt" {
		t.Errorf("shell/work_dir = %v/%v, want /bin/sh and /tmp/wt", got.Shell, got.WorkDir)
	}
}
