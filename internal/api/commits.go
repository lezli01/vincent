package api

// Listing the commits a task made on its branch (§13.2, issue #601).
//
// A commit list is a fact a client cannot compute without git, which is the
// case task 100 decision 2 leaves open: the TUI holds no repository, so the
// daemon has to be the one to walk the branch.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

// commitResponse is one entry of GET /v1/tasks/{id}/commits.
type commitResponse struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	// AuthorTime is the author date, RFC3339 in UTC.
	AuthorTime string `json:"author_time"`
	// LaneID and ChildTaskID are set only on a lane merge, and mean what
	// they mean on a `?by=lane` diff section (difflane.go).
	LaneID      string `json:"lane_id,omitempty"`
	ChildTaskID int64  `json:"child_task_id,omitempty"`
}

// handleTaskCommits implements GET /v1/tasks/{id}/commits: the task's own
// commits — its first-parent chain past its base — oldest first.
//
// Git runs in the **project repository** against `refs/heads/<branch>`, not
// in the worktree, so the list outlives the worktree archive removes (§3 row
// 17) — and the worktree path archive clears. For a live task the branch ref is the worktree's committed HEAD, so
// uncommitted edits are not in it — correct for a list of commits.
//
// The base is chosen as handleTaskDiff chooses it: base_sha when the task
// recorded one, base_branch only when it did not (task 056). A merge-base
// against the base branch's name would list commits a task cut from a fetched
// tip, a pull request or an existing branch (tasks 064, 125) never made. Both
// are named in full (task 008 decision 1) so a same-named tag cannot answer.
func (s *Server) handleTaskCommits(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	// The branch is the worktree's, made at admission, and a live task keeps
	// its worktree path; so a live task without one was never admitted. That
	// holds even for a branch a task adopted (task 125): it exists already,
	// but its base is not recorded until admission, so there is nothing yet to
	// measure it against. Archive clears the path, so an archived task is
	// judged by its branch alone.
	if t.BranchName == "" || (t.WorktreePath == "" && t.State != store.TaskArchived) {
		writeError(w, http.StatusConflict, CodeInvalidState, "task has no branch yet")
		return
	}
	ctx := r.Context()
	p, err := s.deps.Store.GetProject(ctx, t.ProjectID)
	if err != nil {
		s.internalError(w, "get project", err)
		return
	}
	branch := "refs/heads/" + t.BranchName
	// Whatever deleted the branch — archive's cleanup of a branch with no
	// commits (task 008) or a human — the answer is the same: the daemon does
	// not record which, and a client renders both as "commits unavailable".
	// An archived task that was never admitted lands here too; with the
	// worktree path cleared it is indistinguishable from the first case.
	if _, err := s.git(ctx, p.Path, "rev-parse", "--verify", "--quiet", branch+"^{commit}"); err != nil {
		writeError(w, http.StatusConflict, CodeInvalidState,
			fmt.Sprintf("branch %q no longer exists", t.BranchName))
		return
	}
	base := "refs/heads/" + t.BaseBranch
	if t.BaseSHA != "" {
		base = t.BaseSHA
	}
	mergeBase, err := s.git(ctx, p.Path, "merge-base", base, branch)
	if err != nil {
		writeError(w, http.StatusConflict, CodeInvalidState,
			fmt.Sprintf("cannot compute merge-base with %q: %v", base, err))
		return
	}
	out, err := s.taskCommits(ctx, p.Path, mergeBase+".."+branch)
	if err != nil {
		writeError(w, http.StatusConflict, CodeInvalidState, fmt.Sprintf("git log failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// taskCommits walks revRange's first-parent chain into the wire shape. It is
// never nil: a branch with nothing past its base is `[]`, not `null`.
func (s *Server) taskCommits(ctx context.Context, dir, revRange string) ([]commitResponse, error) {
	log, err := s.firstParentLog(ctx, dir, revRange)
	if err != nil {
		return nil, err
	}
	out := make([]commitResponse, 0, len(log))
	for _, rec := range log {
		c := commitResponse{SHA: rec.SHA, Subject: rec.Subject}
		if secs, pErr := strconv.ParseInt(rec.AuthorTime, 10, 64); pErr == nil {
			c.AuthorTime = time.Unix(secs, 0).UTC().Format(time.RFC3339)
		}
		if laneID, childID, isLane := parseLaneMerge(rec); isLane {
			c.LaneID, c.ChildTaskID = laneID, childID
		}
		out = append(out, c)
	}
	return out, nil
}
