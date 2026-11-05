package api

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/issuearchive"
	"github.com/lezli01/vincent/internal/pathx"
	"github.com/lezli01/vincent/internal/store"
)

// fillIssueArchive fills doctor's issue_archive group (task 143.4, #929):
// the settled roots of closed issues the auto-archive has not archived, each
// with every reason that holds it now, and the issue-chat main-worktree
// directories closed issues left behind.
//
// It only reads. Nothing here archives, forces or deletes: the unattended
// path never deletes (§10, §18), and forcing past a dirty worktree is a
// human's call. The row is derived from current rows and directories on
// every request, so it cannot go stale — which is why no event records a
// skipped auto-archive (task 143 decision 9).
func (s *Server) fillIssueArchive(ctx context.Context, rep *doctor.Report) {
	if s.deps.Store == nil {
		return
	}
	ia := &rep.IssueArchive
	ia.Known = true
	ia.Enabled = s.deps.Config().ArchiveTasksOnIssueClose
	if !ia.Enabled {
		return
	}
	held, err := s.heldIssueTasks(ctx)
	if err != nil {
		ia.Error = err.Error()
		return
	}
	ia.Held = held
	dirs, err := s.leftoverIssueChatDirs(ctx)
	if err != nil {
		ia.Error = err.Error()
		return
	}
	ia.ChatDirs = dirs
}

// heldIssueTasks computes every candidate's reasons. Task-level holds come
// first, for every candidate, because the review-F3 rule needs them all:
// a main task waits on any held side task or merge-back of its issue,
// whatever order the two are listed in.
func (s *Server) heldIssueTasks(ctx context.Context) ([]doctor.HeldTask, error) {
	cands, err := s.deps.Store.ClosedIssueArchiveCandidates(ctx)
	if err != nil {
		return nil, err
	}
	projects := map[int64]string{}
	taskHolds := make([][]string, len(cands))
	sideHolds := map[int64]*issuearchive.SideWorkHold{}
	for i := range cands {
		c := &cands[i]
		holds, err := s.taskArchiveHolds(ctx, c, projects)
		if err != nil {
			return nil, err
		}
		taskHolds[i] = holds
		h := sideHolds[c.IssueID]
		if h == nil {
			h = &issuearchive.SideWorkHold{}
			sideHolds[c.IssueID] = h
		}
		if len(holds) > 0 {
			h.Refuse(issuearchive.IsIssueMain(c.Role, c.WorkflowName))
		}
	}
	out := make([]doctor.HeldTask, 0, len(cands))
	for i := range cands {
		c := &cands[i]
		reasons := taskHolds[i]
		if sideHolds[c.IssueID].Holds(issuearchive.IsIssueMain(c.Role, c.WorkflowName)) {
			reasons = append(reasons, doctor.HoldHeldForSideWork)
		}
		if !c.Quiescent {
			reasons = append(reasons, doctor.HoldIssueNotQuiescent)
		}
		if c.HandoffClaimed {
			reasons = append(reasons, doctor.HoldHandoffClaimed)
		}
		if len(reasons) == 0 {
			reasons = []string{doctor.HoldAwaitingRetry}
		}
		out = append(out, doctor.HeldTask{
			TaskID: c.TaskID, IssueID: c.IssueID, Title: c.Title,
			State: string(c.State), Role: c.Role, Reasons: reasons,
		})
	}
	return out, nil
}

// taskArchiveHolds is what Runner.Archive would refuse the task for now:
// dirty (or its project path gone), locked by a chat, or an unfinished lane,
// in HoldReasons order. Each
// asks the definition the archive itself consults, over the subtree the
// cascade walks: the root and every unarchived lane.
func (s *Server) taskArchiveHolds(ctx context.Context, c *store.IssueArchiveCandidate, projects map[int64]string) ([]string, error) {
	var holds []string
	projectPath, ok := projects[c.ProjectID]
	if !ok {
		p, err := s.deps.Store.GetProject(ctx, c.ProjectID)
		if err != nil {
			return nil, err
		}
		projectPath = p.Path
		projects[c.ProjectID] = projectPath
	}
	paths := []string{c.WorktreePath}
	lanes, err := s.deps.Store.UnarchivedLaneWorktrees(ctx, c.TaskID)
	if err != nil {
		return nil, err
	}
	paths = append(paths, lanes...)
	if hold := s.worktreeHold(ctx, projectPath, paths); hold != "" {
		holds = append(holds, hold)
	}
	chatID, err := s.deps.Store.SubtreeOpenLinkedChatID(ctx, c.TaskID)
	if err != nil {
		return nil, err
	}
	if chatID != 0 {
		holds = append(holds, doctor.HoldLockedByChat)
	}
	unfinished, err := s.deps.Store.NonTerminalDescendants(ctx, c.TaskID)
	if err != nil {
		return nil, err
	}
	if len(unfinished) > 0 {
		holds = append(holds, doctor.HoldUnfinishedLane)
	}
	return holds, nil
}

// worktreeHold is the refusal worktree.Manager.Remove would make without
// force for any of paths, in Remove's own order: a missing directory is
// never refused, and neither is the project's own checkout, which archive
// never removes (task 125); an existing worktree of a project whose path is
// gone is project_path_missing, decided before git runs (review F2) —
// cleaning the worktree cannot help there, restoring the path or forcing
// can; otherwise a dirty one is dirty. A worktree git cannot judge is
// reported dirty, because the unattended archive refuses it the same way.
// Each IsDirty is bounded by gitx.QueryTimeout.
func (s *Server) worktreeHold(ctx context.Context, projectPath string, paths []string) string {
	if s.deps.Worktrees == nil {
		return ""
	}
	var projectMissing *bool
	for _, path := range paths {
		if path == "" || pathx.SameDir(path, projectPath) {
			continue
		}
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if projectMissing == nil {
			_, err := os.Stat(projectPath)
			missing := err != nil
			projectMissing = &missing
		}
		if *projectMissing {
			return doctor.HoldProjectPathMissing
		}
		if dirty, err := s.deps.Worktrees.IsDirty(ctx, path); err != nil || dirty {
			return doctor.HoldDirty
		}
	}
	return ""
}

// leftoverIssueChatDirs lists, per closed issue, the released issue chat
// still naming the issue's main-worktree directory (task 143 decision 11),
// when that directory is still on disk.
func (s *Server) leftoverIssueChatDirs(ctx context.Context) ([]doctor.LeftoverChatDir, error) {
	ids, err := s.deps.Store.ClosedIssueIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := []doctor.LeftoverChatDir{}
	for _, id := range ids {
		c, err := s.deps.Store.IssueMainWorktreeChatHolder(ctx, id)
		if err != nil {
			return nil, err
		}
		if c == nil {
			continue
		}
		if fi, err := os.Stat(c.WorktreePath); err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, doctor.LeftoverChatDir{IssueID: id, ChatID: c.ID, Path: c.WorktreePath})
	}
	return out, nil
}
