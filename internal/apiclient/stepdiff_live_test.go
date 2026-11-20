package apiclient_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestDiffStepAgainstRealHandlers is the drift guard for the per-step diff
// (task 164.6): the step-run fields the range is read from, DiffStep's query,
// and the typed 409 for a run with no range all round-trip through the real
// handlers.
func TestDiffStepAgainstRealHandlers(t *testing.T) {
	h := newHarness(t)
	repo := testrepo.Init(t, "main")
	start := strings.TrimSpace(testrepo.Run(t, repo, "rev-parse", "HEAD"))
	testrepo.WriteFile(t, repo, "step.txt", "this step's work\n")
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "step")
	end := strings.TrimSpace(testrepo.Run(t, repo, "rev-parse", "HEAD"))
	testrepo.WriteFile(t, repo, "later.txt", "after the step\n")
	testrepo.Run(t, repo, "add", ".")
	id := h.taskInWorktree(t, repo)

	ctx := context.Background()
	now := time.Now()
	ranged := &store.StepRun{
		TaskID: id, StepID: "build", StepType: "command", Attempt: 1,
		State: store.StepSucceeded, HeadStart: &start, HeadEnd: &end, FinishedAt: &now,
	}
	bare := &store.StepRun{
		TaskID: id, StepID: "build", StepType: "command", Attempt: 2,
		State: store.StepSucceeded, FinishedAt: &now,
	}
	for _, r := range []*store.StepRun{ranged, bare} {
		if err := h.st.CreateStepRun(t.Context(), r); err != nil {
			t.Fatalf("CreateStepRun: %v", err)
		}
	}

	detail, err := h.client().GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(detail.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(detail.Steps))
	}
	for _, s := range detail.Steps {
		switch s.ID {
		case ranged.ID:
			if s.HeadStart == nil || *s.HeadStart != start || s.HeadEnd == nil || *s.HeadEnd != end {
				t.Errorf("ranged run = %v..%v, want %s..%s", s.HeadStart, s.HeadEnd, start, end)
			}
		case bare.ID:
			if s.HeadStart != nil || s.HeadEnd != nil {
				t.Errorf("bare run = %v..%v, want both nil", s.HeadStart, s.HeadEnd)
			}
		}
	}

	diff, err := h.client().DiffStep(ctx, id, ranged.ID)
	if err != nil {
		t.Fatalf("DiffStep: %v", err)
	}
	if !strings.Contains(diff, "step.txt") || strings.Contains(diff, "later.txt") {
		t.Errorf("DiffStep is not the run's range:\n%s", diff)
	}

	_, err = h.client().DiffStep(ctx, id, bare.ID)
	if !apiclient.StepRangeUnrecorded(err) {
		t.Errorf("DiffStep on a run with no range: err = %v, want step_range_unrecorded", err)
	}
}
