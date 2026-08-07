// Package worktree manages per-task git worktrees and vincent/{id}-{slug}
// branches: create, remove, prune, and dirty detection (spec §10, §5.3; T1.6).
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/gitx"
)

// Reason values carried by *Error (spec §18). The same snake_case strings
// later become task block_reason values — one vocabulary end to end
// (T1.5/T1.6 decision).
const (
	ReasonProjectPathMissing   = "project_path_missing"
	ReasonBaseBranchMissing    = "base_branch_missing"
	ReasonBranchExists         = "branch_exists"
	ReasonWorktreeDirty        = "worktree_dirty"
	ReasonWorktreeMissing      = "worktree_missing"
	ReasonWorktreePathOccupied = "worktree_path_occupied"
	ReasonGitError             = "git_error"
)

// Error is a typed worktree-management failure.
type Error struct {
	Reason  string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Reason, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Reason, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// ReasonOf returns the taxonomy reason of err, or "" when err is not a
// worktree error.
func ReasonOf(err error) string {
	var we *Error
	if errors.As(err, &we) {
		return we.Reason
	}
	return ""
}

// Slug reduces title to the §5.3 branch-slug form: lowercase, runs of
// characters outside [a-z0-9] collapsed to single dashes, trimmed, at most
// 40 characters with no trailing dash.
func Slug(title string) string {
	var b strings.Builder
	prevDash := true // suppress a leading dash
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// BranchName returns vincent/{id}-{slug}, or vincent/{id} when the title
// sanitizes to an empty slug (phase 1 decision).
func BranchName(taskID int64, title string) string {
	s := Slug(title)
	if s == "" {
		return "vincent/" + strconv.FormatInt(taskID, 10)
	}
	return fmt.Sprintf("vincent/%d-%s", taskID, s)
}

// Manager creates and removes per-task worktrees under
// {data_dir}/worktrees/{task_id} (spec §10).
type Manager struct {
	git  *gitx.Git
	root string
}

// NewManager returns a Manager storing worktrees under dataDir.
func NewManager(git *gitx.Git, dataDir string) *Manager {
	return &Manager{git: git, root: filepath.Join(dataDir, "worktrees")}
}

// Path returns the worktree location for a task.
func (m *Manager) Path(taskID int64) string {
	return filepath.Join(m.root, strconv.FormatInt(taskID, 10))
}

// Create adds branch (from base) and a worktree for it, returning the
// worktree path. It prunes stale worktree registrations first and never
// deletes an existing directory (prune-then-fail decision).
func (m *Manager) Create(ctx context.Context, projectPath string, taskID int64, branch, base string) (string, error) {
	if _, err := os.Stat(projectPath); err != nil {
		return "", &Error{
			Reason:  ReasonProjectPathMissing,
			Message: fmt.Sprintf("project path %s does not exist", projectPath), Err: err,
		}
	}
	if !m.localBranchExists(ctx, projectPath, base) {
		return "", &Error{
			Reason:  ReasonBaseBranchMissing,
			Message: fmt.Sprintf("base branch %q does not exist in %s", base, projectPath),
		}
	}
	if m.localBranchExists(ctx, projectPath, branch) {
		return "", &Error{
			Reason:  ReasonBranchExists,
			Message: fmt.Sprintf("branch %q already exists in %s (never reused)", branch, projectPath),
		}
	}
	if err := m.prune(ctx, projectPath); err != nil {
		return "", err
	}
	target := m.Path(taskID)
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		return "", &Error{
			Reason:  ReasonWorktreePathOccupied,
			Message: fmt.Sprintf("worktree path %s already exists and is not empty; remove it manually", target),
		}
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return "", &Error{Reason: ReasonGitError, Message: "create worktrees dir", Err: err}
	}
	addCtx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	if _, err := m.git.Run(addCtx, projectPath, "worktree", "add", target, "-b", branch, base); err != nil {
		return "", &Error{Reason: ReasonGitError, Message: "git worktree add failed", Err: err}
	}
	return target, nil
}

// IsDirty reports whether the worktree has any local changes; untracked
// files count (T1.5/T1.6 decision, matching git worktree remove's own rule).
func (m *Manager) IsDirty(ctx context.Context, worktreePath string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	out, err := m.git.Run(ctx, worktreePath, "status", "--porcelain")
	if err != nil {
		return false, &Error{Reason: ReasonGitError, Message: "git status failed", Err: err}
	}
	return out != "", nil
}

// Remove deletes the worktree and prunes bookkeeping. It is idempotent: a
// worktree directory that is already gone prunes and succeeds. Without force
// a dirty worktree (untracked included) is refused with ReasonWorktreeDirty.
// With force, when git itself cannot remove (project path gone, locked
// worktree), a directory under the manager's root is removed directly. The
// branch is never deleted (spec §10).
func (m *Manager) Remove(ctx context.Context, projectPath, worktreePath string, force bool) error {
	projectMissing := false
	if _, err := os.Stat(projectPath); err != nil {
		projectMissing = true
	}
	if _, err := os.Stat(worktreePath); err != nil {
		if !projectMissing {
			return m.prune(ctx, projectPath)
		}
		return nil
	}
	if projectMissing {
		if !force {
			return &Error{
				Reason:  ReasonProjectPathMissing,
				Message: fmt.Sprintf("project path %s does not exist", projectPath),
			}
		}
		return m.removeDirect(worktreePath)
	}
	if !force {
		dirty, err := m.IsDirty(ctx, worktreePath)
		if err != nil {
			return err
		}
		if dirty {
			return &Error{
				Reason:  ReasonWorktreeDirty,
				Message: fmt.Sprintf("worktree %s has local changes (untracked included); confirm with force", worktreePath),
			}
		}
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)
	rmCtx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	if _, err := m.git.Run(rmCtx, projectPath, args...); err != nil {
		if force {
			if derr := m.removeDirect(worktreePath); derr == nil {
				return m.prune(ctx, projectPath)
			}
		}
		return &Error{Reason: ReasonGitError, Message: "git worktree remove failed", Err: err}
	}
	return m.prune(ctx, projectPath)
}

// removeDirect deletes a worktree directory without git, but only inside the
// manager's own root — nothing outside vincent's namespace is ever removed.
func (m *Manager) removeDirect(worktreePath string) error {
	rel, err := filepath.Rel(m.root, worktreePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return &Error{
			Reason:  ReasonGitError,
			Message: fmt.Sprintf("refusing to delete %s: outside the worktrees root %s", worktreePath, m.root),
		}
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		return &Error{Reason: ReasonGitError, Message: "remove worktree dir", Err: err}
	}
	return nil
}

func (m *Manager) prune(ctx context.Context, projectPath string) error {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	if _, err := m.git.Run(ctx, projectPath, "worktree", "prune"); err != nil {
		return &Error{Reason: ReasonGitError, Message: "git worktree prune failed", Err: err}
	}
	return nil
}

func (m *Manager) localBranchExists(ctx context.Context, repo, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	_, err := m.git.Run(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}
