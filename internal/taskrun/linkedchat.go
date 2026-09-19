package taskrun

// Chats linked to a task (task 119, spec §5.5, §6, §16).
//
// internal/taskrun owns everything a linked chat needs to know about its task
// — the context its first turn opens with, the launcher its turns run through,
// and what `cancel` does to a task the chat has locked — so internal/chatrun
// never reads a step ledger and never imports this package. The daemon injects
// the two directions: chatrun is handed ChatLauncher, and this runner is
// handed a ChatTurnStopper.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/container"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
)

// ChatTurnStopper stops a linked chat's live turn and waits for it to end. It
// is what `cancel` on a locked task calls before its one transaction closes
// the chat and aborts the task (task 119); internal/chatrun implements it.
type ChatTurnStopper interface {
	StopTurn(ctx context.Context, chatID int64)
}

// chatIntro opens a linked chat's context. It says what the agent is looking
// at and, above all, what it is not: a workflow run with authority over the
// task's state.
const chatIntro = "A human opened this conversation on a vincent task that has stopped. " +
	"You are working in the task's existing worktree, on its branch: the files are as the " +
	"task left them. The task stays exactly where it is while this conversation is open, " +
	"and a human decides what it does next.\n\n"

// ChatContext assembles the context a chat opened on task opens with (task
// 119): the task block every state gets, then what the state it stopped in
// adds — the bounded failure block for `blocked`, the gate for
// `awaiting_gate`, the last step's summary for `done` and `aborted`.
//
// It is a snapshot. The task cannot move while its chat is open, so what is
// assembled here is exactly the context at the first send.
func (r *Runner) ChatContext(ctx context.Context, task *store.Task) (string, error) {
	project, err := r.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		return "", err
	}
	wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		return "", fmt.Errorf("parse workflow snapshot: %w", err)
	}
	var sb strings.Builder
	sb.WriteString(chatIntro)
	target, hasTarget := r.repairTargetOf(task, wf)
	fields := task.Fields
	if hasTarget && target.followUp && task.PendingFollowUp != nil {
		fields = mergeFields(task.Fields, task.PendingFollowUp.Fields)
	}
	writeTaskBlock(&sb, task, project, wf.Name, fields)
	fmt.Fprintf(&sb, "<task-state>%s</task-state>\n\n", task.State)
	switch task.State {
	case store.TaskBlocked:
		if hasTarget {
			r.writeFailureBlock(ctx, &sb, task, project, target, task.BlockReason)
		} else if task.BlockReason != "" {
			fmt.Fprintf(&sb, "block reason: %s\n\n", task.BlockReason)
		}
	case store.TaskAwaitingGate:
		if hasTarget {
			fmt.Fprintf(&sb, "<gate index=%q id=%q>\n", fmt.Sprint(target.index+1), target.step.ID)
			if body, _ := r.renderBlockedStep(ctx, task, project, target); body != "" {
				sb.WriteString(strings.TrimSuffix(body, "\n") + "\n")
			}
			sb.WriteString("</gate>\n\n")
		}
	case store.TaskDone, store.TaskAborted:
		runs, err := r.deps.Store.ListStepRuns(ctx, task.ID)
		if err != nil {
			return "", err
		}
		if n := len(runs); n > 0 {
			last := runs[n-1]
			fmt.Fprintf(&sb, "<last-step id=%q state=%q>\n", last.StepID, last.State)
			if task.State == store.TaskAborted && last.FailureReason != "" {
				fmt.Fprintf(&sb, "abort reason: %s\n", last.FailureReason)
			}
			if last.ResultSummary != "" {
				sb.WriteString(strings.TrimSuffix(last.ResultSummary, "\n") + "\n")
			}
			sb.WriteString("</last-step>\n\n")
		}
	case store.TaskQueued, store.TaskRunning, store.TaskAwaitingInput, store.TaskAwaitingChildren,
		store.TaskPaused, store.TaskArchived:
		// A chat cannot be opened from these (§6); the task block is all.
	}
	return sb.String(), nil
}

// ErrTaskContainerMissing is a linked-chat turn on a task whose workflow runs
// in a container that no longer exists. The turn fails rather than running on
// the host: the operator chose to confine that worktree (task 119 decision 3).
var ErrTaskContainerMissing = errors.New("the task's container is not running")

// ChatLauncher is where a linked chat's turn runs (task 119 decision 3): in
// the task's container when its workflow runs in one, on the host otherwise.
// turnID keys the pid file 061 decision 9's kill reaches the process through.
func (r *Runner) ChatLauncher(ctx context.Context, taskID, turnID int64) (agent.Launcher, error) {
	tc, err := r.chatContainer(ctx, taskID)
	if err != nil || !tc.active() {
		return agent.HostLauncher{}, err
	}
	return &containerLauncher{
		tc: tc, key: chatExecKey(turnID), user: container.HostUser(), log: r.deps.Logger,
	}, nil
}

// StopChatOrphan is §12.4's container-aware kill for a linked-chat turn a
// previous daemon died under: TERM then KILL through the turn's pid file
// inside the task's container. A task with no container has nothing to do
// here — the host kill already ran — and reports false.
func (r *Runner) StopChatOrphan(ctx context.Context, taskID, turnID int64) bool {
	tc, err := r.chatContainer(ctx, taskID)
	if err != nil || !tc.active() {
		return false
	}
	stopInContainer(tc, chatExecKey(turnID), r.deps.Logger)
	return true
}

// ChatInContainer reports whether taskID's workflow runs in a container,
// from the task's workflow snapshot and container settings alone. It never
// calls the runtime, so a configured container that is gone still reports
// true (task 124 decision 56).
//
// It is what GET /v1/chats/{id}/skills asks before listing a directory (task
// 124.9, §13.2): a question a client can repeat at will must not spawn
// `docker inspect` each time, and a missing container is the turn's failure to
// report, through ChatLauncher, not this bit's.
func (r *Runner) ChatInContainer(ctx context.Context, taskID int64) (bool, error) {
	c, err := r.chatContainerSettings(ctx, taskID)
	if err != nil {
		return false, err
	}
	return c.Enabled(), nil
}

// chatContainerSettings resolves a task's container settings from its workflow
// snapshot. It is the one place both ChatLauncher and ChatInContainer read
// them from, so where a turn runs and where the skills route says it runs can
// never disagree.
func (r *Runner) chatContainerSettings(ctx context.Context, taskID int64) (config.Container, error) {
	task, err := r.deps.Store.GetTask(ctx, taskID)
	if err != nil {
		return config.Container{}, err
	}
	wf, _, err := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if err != nil {
		return config.Container{}, fmt.Errorf("parse workflow snapshot: %w", err)
	}
	return r.containerSettings(wf), nil
}

// chatContainer finds the container a task's workflow runs in, the way a later
// admission finds it again: by name. A zero value means the host.
func (r *Runner) chatContainer(ctx context.Context, taskID int64) (taskContainer, error) {
	c, err := r.chatContainerSettings(ctx, taskID)
	if err != nil {
		return taskContainer{}, err
	}
	if !c.Enabled() {
		return taskContainer{}, nil
	}
	rt := r.runtimeFor(c)
	id, err := rt.Lookup(ctx, container.Name(taskID))
	if err != nil || id == "" {
		return taskContainer{}, fmt.Errorf("task %d: %w", taskID, ErrTaskContainerMissing)
	}
	return taskContainer{id: id, settings: c, rt: rt}, nil
}

// chatExecKey names a chat turn's pid file. It cannot collide with a step's:
// the prefixes differ.
func chatExecKey(turnID int64) string { return "chat-" + strconv.FormatInt(turnID, 10) }

// cancelLocked is `cancel` on a task an open chat has locked (task 119): the
// chat's live turn is stopped first, then one transaction closes the chat and
// aborts the task. A crash between the two leaves an idle open chat on a task
// that is still locked, and the operator repeats the cancel.
func (r *Runner) cancelLocked(ctx context.Context, id, chatID int64) (*store.Task, error) {
	if r.deps.ChatTurns != nil {
		r.deps.ChatTurns.StopTurn(ctx, chatID)
	}
	return r.humanAction(ctx, id, taskstate.Cancel, store.TaskChange{CloseLinkedChats: true})
}
