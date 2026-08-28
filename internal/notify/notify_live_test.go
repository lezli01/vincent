package notify

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
)

// TestLiveNotificationFromRealTransition is the acceptance sentence the issue
// wrote, assembled from the real parts: a real SQLite store, the real
// post-commit broker, the daemon's own hook wiring, and a real child process
// — with no client attached to anything.
//
// It is a *live_test.go for the same reason the TUI's are: the pieces this
// feature spans (the store's event hook, the broker's fan-out contract, the
// state-change payload's field names) are exactly the ones a unit test with a
// hand-built event cannot keep honest.
func TestLiveNotificationFromRealTransition(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	project := &store.Project{Name: "vincent", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	snapshot := "name: review\nsteps:\n" +
		"  - {id: plan, type: manual, instructions: plan it}\n" +
		"  - {id: ship, type: manual, instructions: ship it}\n"
	task := &store.Task{
		ProjectID: project.ID, Title: "Ship the notify hook",
		WorkflowName: "review", WorkflowSnapshot: snapshot,
		BranchName: "vincent/1-ship", State: store.TaskRunning,
	}
	if err := st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	cfg := config.Default()
	cfg.Notify = config.Notify{
		On:      []taskstate.State{taskstate.Blocked},
		Command: helperArgv(t, "capture", dir),
	}
	logs := &logCapture{}

	// The daemon's wiring, verbatim in shape: the store's single event hook
	// publishes to the broker, and the notifier is one of the broker's
	// internal subscribers (internal/daemon/daemon.go).
	broker := events.New()
	notifier := New(Deps{
		Store:  st,
		Config: func() config.Config { return cfg },
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		StepCount: func(s string) int {
			wf, _, perr := workflow.Parse([]byte(s), workflow.Options{})
			if perr != nil {
				return 0
			}
			return len(wf.Steps)
		},
	})
	notifier.Start(t.Context())
	t.Cleanup(notifier.Stop)
	broker.OnEvent(notifier.OnEvent)
	st.SetEventHook(broker.Publish)
	t.Cleanup(broker.Close)

	reason := "step_failed"
	if _, _, err := st.TransitionTask(t.Context(), task.ID,
		store.TaskRunning, store.TaskBlocked,
		store.TaskChange{BlockReason: &reason},
	); err != nil {
		t.Fatalf("block: %v", err)
	}

	waitFor(t, "the notifier child to run off a real transition", func() bool {
		return len(helperFiles(t, dir)) == 2
	})

	var env Envelope
	for _, body := range helperFiles(t, dir) {
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			if uerr := json.Unmarshal([]byte(body), &env); uerr != nil {
				t.Fatalf("stdin is not the documented envelope: %v (%q)", uerr, body)
			}
		}
	}
	if env.TaskID != task.ID || env.Title != "Ship the notify hook" {
		t.Errorf("envelope names the wrong task: %+v", env)
	}
	if env.From != "running" || env.To != "blocked" {
		t.Errorf("from/to = %q/%q, want running/blocked", env.From, env.To)
	}
	if env.BlockReason != reason {
		t.Errorf("block_reason = %q, want %q", env.BlockReason, reason)
	}
	if env.Project != "vincent" || env.Workflow != "review" {
		t.Errorf("project/workflow = %q/%q, want vincent/review", env.Project, env.Workflow)
	}
	// The honest n for this run comes from the task's own snapshot.
	if env.StepsTotal != 2 {
		t.Errorf("steps_total = %d, want 2 from the workflow snapshot", env.StepsTotal)
	}
	if env.EventID == 0 || env.TS == "" {
		t.Errorf("envelope carries no event cursor: %+v", env)
	}
	if env.Type != store.EventTaskStateChanged {
		t.Errorf("type = %q; no event type is introduced for this (§13.3)", env.Type)
	}
}
