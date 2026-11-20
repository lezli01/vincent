package chatrun

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// CheckpointRefPrefix is the ref namespace a chat's checkpoints live under
// (task 167, spec §5.5, §10). The refs are in the project repository's shared
// namespace, so a worktree's checkpoints are visible from the main checkout
// and survive the worktree's removal.
func CheckpointRefPrefix(chatID int64) string {
	return fmt.Sprintf("refs/vincent/chats/%d/", chatID)
}

// CheckpointRef is the ref holding turn seq's checkpoint.
func CheckpointRef(chatID int64, seq int) string {
	return fmt.Sprintf("%sturn-%d", CheckpointRefPrefix(chatID), seq)
}

// checkpoint snapshots workDir before turn's agent starts (task 167.2):
// the snapshot commit is written to CheckpointRef and, with the HEAD it is
// parented on, onto the turn row — both before adapter.Start, so a crash
// after the spawn finds them committed and recovery (§12.4) needs nothing
// new. A drained queued turn (task 156) comes through runTurn too and is
// checkpointed like any other.
//
// An error fails the turn `checkpoint_failed`; it never runs unprotected.
// Files git could not read are not an error (review F1): they are left out
// of the checkpoint, logged, and returned for the turn's transcript, so one
// unreadable file never stops a chat from running at all.
func (r *Runner) checkpoint(
	ctx context.Context, chat *store.Chat, turn *store.ChatTurn, workDir string,
) (skipped []string, err error) {
	if r.beforeCheckpoint != nil {
		r.beforeCheckpoint(ctx, chat.ID)
	}
	git := gitx.New()
	snapCtx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	snap, err := git.Snapshot(snapCtx, workDir, r.checkpointScratch(),
		fmt.Sprintf("vincent: chat %d turn %d checkpoint", chat.ID, turn.Seq))
	if err != nil {
		return nil, err
	}
	if err := git.UpdateRef(snapCtx, workDir, CheckpointRef(chat.ID, turn.Seq), snap.Commit); err != nil {
		return nil, fmt.Errorf("checkpoint: write ref: %w", err)
	}
	turn.CheckpointSHA, turn.HeadBefore = snap.Commit, snap.Head
	if err := r.deps.Store.UpdateChatTurn(ctx, turn); err != nil {
		turn.CheckpointSHA, turn.HeadBefore = "", ""
		return nil, fmt.Errorf("checkpoint: record: %w", err)
	}
	if len(snap.Skipped) > 0 {
		r.deps.Logger.Warn("chat turn checkpoint skipped unreadable files",
			"chat", chat.ID, "turn", turn.Seq, "paths", snap.Skipped)
	}
	return snap.Skipped, nil
}

// recordHeadAfter notes workDir's HEAD once the turn's process is gone, for
// the shared-worktree rewind rule (task 167 decision 3). It is best effort:
// the turn's own outcome does not depend on it, and an unreadable HEAD is
// recorded as none, which that rule reads as "cannot prove", never as a match.
func (r *Runner) recordHeadAfter(turn *store.ChatTurn, workDir string) {
	ctx, cancel := context.WithTimeout(r.persist, gitx.QueryTimeout)
	defer cancel()
	head, err := gitx.New().Run(ctx, workDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		r.deps.Logger.Warn("chat turn: read HEAD after turn", "chat", turn.ChatID, "turn", turn.Seq, "error", err)
		return
	}
	turn.HeadAfter = head
}

// checkpointScratch is where a snapshot's temporary index lives: under the
// data directory, never the worktree, so the agent and `git status` never see
// it.
func (r *Runner) checkpointScratch() string {
	return CheckpointScratch(r.deps.DataDir)
}

// CheckpointScratch is that directory for a daemon whose data directory is
// dataDir. It is exported for the chat diff (task 168), whose working-tree
// snapshot is the tree half of a checkpoint and keeps its temporary index in
// the same place.
func CheckpointScratch(dataDir string) string {
	return filepath.Join(dataDir, "tmp", "checkpoints")
}
