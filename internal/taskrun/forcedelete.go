package taskrun

import (
	"context"
	"fmt"
	"strconv"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// ForceDelete permanently removes a task from any state that holds no live
// process, with its fan-out subtree and the chats tied to it (task 153). It
// is the way out for a task whose archive or delete keeps failing on
// something a human cannot fix from vincent — a git error, a repository that
// moved, a worktree git no longer recognises.
//
// The rows go first (store.ForceDeleteTaskTree); everything else is cleaned
// afterwards and can only produce warnings, never an error: each task's
// container, its worktree (worktree.Discard: git with force, then the
// directory itself inside vincent's root), its transcripts and proposal
// staging, and the transcripts of the chats that went with it. A branch is
// never deleted — force is for getting rid of a row, and a branch carrying
// work is not something a broken repository should get to judge empty.
//
// A task the engine still holds a live run for is refused even if its row
// says otherwise: the actor is the sole writer of the row (CLAUDE.md
// ownership invariants), and deleting under it would race its next write.
func (r *Runner) ForceDelete(ctx context.Context, id int64) ([]string, error) {
	if r.Running(id) {
		return nil, &store.DeleteRefusedError{
			Kind: "task", ID: id, Reason: store.DeleteRefusedLive,
			Message: fmt.Sprintf("task %d still has a live run in the engine; cancel it first", id),
		}
	}
	// A linked chat's shell command runs in the worktree about to be
	// discarded and writes beside the transcripts about to be removed: it is
	// stopped first, as a chat's own force delete stops it (task 170).
	r.stopTreeShells(ctx, id)
	gone, err := r.deps.Store.ForceDeleteTaskTree(ctx, id)
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	projects := map[int64]string{}
	for _, task := range gone.Tasks {
		log := r.deps.Logger.With("task", task.ID)
		log.Warn("force delete: task removed", "state", task.State, "requested", id)
		r.removeTaskContainer(ctx, task, log)
		if task.WorktreePath != "" {
			path, ok := projects[task.ProjectID]
			if !ok {
				if p, err := r.deps.Store.GetProject(ctx, task.ProjectID); err == nil {
					path = p.Path
				}
				projects[task.ProjectID] = path
			}
			if err := r.deps.Worktrees.Discard(ctx, path, task.WorktreePath); err != nil {
				log.Warn("force delete: worktree left behind", "error", err)
				warnings = append(warnings, fmt.Sprintf("task %d: %v", task.ID, err))
			}
		}
		RemoveTranscriptDir(r.deps.DataDir, strconv.FormatInt(task.ID, 10), log)
		RemoveProposalDir(r.deps.DataDir, task.ID, log)
	}
	for _, chatID := range gone.ChatIDs {
		RemoveTranscriptDir(r.deps.DataDir, worktree.ChatOwner(chatID).Dir(), r.deps.Logger)
	}
	return warnings, nil
}
