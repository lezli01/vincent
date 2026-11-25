package tui

import (
	"context"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// #1155, against the real handlers: the list row carries the agents a
// task's attempts ran on and its agent override, and the board's `/` filter
// finds a task by either. This is where client and server would drift if
// only one side grew `agents` or the client dropped `agent_override`.
func TestBoardFilterMatchesAgentsFromTheDaemon(t *testing.T) {
	h := newBoardLiveHarness(t)
	ctx := context.Background()

	ran := h.createTask(t, "ran on cursor")
	if err := h.st.CreateStepRun(ctx, &store.StepRun{
		TaskID: ran.ID, StepIndex: 0, StepID: "one", StepType: "agent",
		Attempt: 1, State: store.StepRunning, Agent: "cursor",
	}); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	pinned := &store.Task{
		ProjectID: h.projectID, Title: "pinned to codex", WorkflowName: "three",
		WorkflowSnapshot: threeStepWorkflow, BaseBranch: "main",
		State: store.TaskQueued, AgentOverride: "codex",
	}
	if err := h.st.CreateTask(ctx, pinned, func(id int64) (string, error) {
		return worktree.BranchName(id, pinned.Title), nil
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	h.createTask(t, "plain task")
	// A committed transition after the step row exists, so the board's
	// refresh is sure to read it.
	if _, _, err := h.st.TransitionTask(
		ctx, ran.ID, store.TaskQueued, store.TaskRunning, store.TaskChange{},
	); err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}

	b := h.m.views[viewHome].(*shell).board
	h.p.until(20*time.Second, "the agents to arrive on the list rows", func() bool {
		var sawAgents, sawOverride bool
		for _, task := range b.tasks {
			switch task.ID {
			case ran.ID:
				sawAgents = len(task.Agents) == 1 && task.Agents[0] == "cursor"
			case pinned.ID:
				sawOverride = task.AgentOverride != nil && *task.AgentOverride == "codex"
			}
		}
		return sawAgents && sawOverride
	})

	for q, want := range map[string]int64{"cursor": ran.ID, "codex": pinned.ID} {
		b.filter.SetValue(q)
		if got := b.shownTasks(); len(got) != 1 || got[0].ID != want {
			t.Errorf("filter %q shows %d tasks, want only #%d", q, len(got), want)
		}
	}
}
