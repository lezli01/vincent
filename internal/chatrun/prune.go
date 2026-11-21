package chatrun

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/gitx"
)

// chatsRefRoot is the namespace every chat's checkpoint refs live under.
const chatsRefRoot = "refs/vincent/chats/"

// PruneCheckpointRefs deletes chat chatID's checkpoint and rewound refs from
// the project repository at repoPath (task 167.8, decision 12). The objects
// they held become unreachable and go with git's own gc; nothing here prunes
// objects. A chat with no refs is not an error.
func (r *Runner) PruneCheckpointRefs(ctx context.Context, repoPath string, chatID int64) error {
	qctx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	if _, err := gitx.New().DeleteRefs(qctx, repoPath, CheckpointRefPrefix(chatID)); err != nil {
		return fmt.Errorf("prune chat %d checkpoint refs: %w", chatID, err)
	}
	return nil
}

// SweepCheckpointRefs deletes, in every project repository, the refs of
// chats that no longer have a row (decision 12): a permanent delete that
// died before its prune, or one from a daemon older than pruning. An
// archived or handed-off chat still has its row, so its refs stay — a hand-
// off keeps them by design. It returns how many chats' refs it removed. A
// project whose repository cannot be read is logged and skipped: one missing
// clone must not stop the sweep of the others.
func (r *Runner) SweepCheckpointRefs(ctx context.Context) (int, error) {
	projects, err := r.deps.Store.ListProjects(ctx)
	if err != nil {
		return 0, fmt.Errorf("sweep checkpoint refs: %w", err)
	}
	git := gitx.New()
	removed := 0
	for i := range projects {
		repo := projects[i].Path
		ids, err := chatRefIDs(ctx, git, repo)
		if err != nil {
			r.deps.Logger.Warn("sweep checkpoint refs: list refs", "project", projects[i].ID, "error", err)
			continue
		}
		if len(ids) == 0 {
			continue
		}
		exists, err := r.deps.Store.ExistingChatIDs(ctx, ids)
		if err != nil {
			return removed, fmt.Errorf("sweep checkpoint refs: %w", err)
		}
		for _, id := range ids {
			if exists[id] {
				continue
			}
			if err := r.PruneCheckpointRefs(ctx, repo, id); err != nil {
				r.deps.Logger.Warn("sweep checkpoint refs: delete", "project", projects[i].ID, "chat", id, "error", err)
				continue
			}
			removed++
		}
	}
	return removed, nil
}

// chatRefIDs lists the chat ids repo holds refs under chatsRefRoot for.
func chatRefIDs(ctx context.Context, git *gitx.Git, repo string) ([]int64, error) {
	qctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	out, err := git.Run(qctx, repo, "for-each-ref", "--format=%(refname)", chatsRefRoot)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var ids []int64
	for _, ref := range strings.Fields(out) {
		idText, _, ok := strings.Cut(strings.TrimPrefix(ref, chatsRefRoot), "/")
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}
