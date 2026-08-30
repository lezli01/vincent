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
	"sync"

	"github.com/lezli01/vincent/internal/gitx"
)

// Reason values carried by *Error (spec §18). The same snake_case strings
// later become task block_reason values — one vocabulary end to end
// (T1.5/T1.6 decision).
const (
	ReasonProjectPathMissing   = "project_path_missing"
	ReasonBaseBranchMissing    = "base_branch_missing"
	ReasonBranchExists         = "branch_exists"
	ReasonBranchNameInvalid    = "branch_name_invalid"
	ReasonWorktreeDirty        = "worktree_dirty"
	ReasonWorktreeMissing      = "worktree_missing"
	ReasonWorktreePathOccupied = "worktree_path_occupied"
	ReasonGitError             = "git_error"
)

// ReasonDirtyUnknown is gc's answer when git cannot say whether a directory
// holds uncommitted work (task 005) — the *common* case for an orphan, whose
// `.git` file points into a repo that has been deleted or pruned, so
// `git status --porcelain` fails outright.
//
// It is deliberately distinct from ReasonWorktreeDirty: "git says you have
// local changes" and "nobody can tell what is in here" are different facts,
// and only the second one is about a missing repo. It is also the one reason
// in this file that never becomes a task `block_reason` — it is a gc *skip*
// reason, which is why it carries no `worktree_` prefix.
const ReasonDirtyUnknown = "dirty_unknown"

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

// OwnerKind says which entity a worktree belongs to (spec §10, task 063).
type OwnerKind string

// Worktree owner kinds.
const (
	OwnerTask OwnerKind = "task"
	OwnerChat OwnerKind = "chat"
)

// Owner identifies the entity a worktree and its directory belong to. It
// replaced a bare `taskID int64` when chats arrived (task 063 decision 9):
// chats live under the same root, so `{root}/{id}` would have put chat 7 and
// task 7 in one directory.
//
// The directory name stays informational — gc's rule is that the claim
// decides, not the name — but two owners resolving to one path is not a
// naming preference, it is a collision.
type Owner struct {
	Kind OwnerKind
	ID   int64
}

// TaskOwner is the worktree owner for a task. Its directory name is the bare
// id, unchanged from before Owner existed, so no installed worktree moves.
func TaskOwner(id int64) Owner { return Owner{Kind: OwnerTask, ID: id} }

// ChatOwner is the worktree owner for a chat (§5.5).
func ChatOwner(id int64) Owner { return Owner{Kind: OwnerChat, ID: id} }

// Dir is the owner's directory name directly under the worktree root.
func (o Owner) Dir() string {
	if o.Kind == OwnerChat {
		return "chat-" + strconv.FormatInt(o.ID, 10)
	}
	return strconv.FormatInt(o.ID, 10)
}

// Manager creates and removes per-owner worktrees under
// {data_dir}/worktrees/{owner} (spec §10).
type Manager struct {
	git  *gitx.Git
	root string

	// claims guards the window in which a directory exists on disk but no
	// task row claims it yet (task 005). gc defines an orphan by claim, not
	// by name, so that window is exactly when a live task's worktree would
	// look reclaimable. Creation and archival take it shared; the reclaim
	// scan takes it exclusively. An mtime/age heuristic was rejected: a
	// timing guess has no place in the package whose subject is ownership.
	claims sync.RWMutex

	// repos serializes the operations that mutate one project repository's
	// shared `.git/worktrees` bookkeeping — create (its prune *and* its
	// `git worktree add`) and Remove — one mutex per project path (#126).
	//
	// git does not lock that state for us. `git worktree add` creates
	// `.git/worktrees/{id}` and only *then* writes the `locked` file that
	// would fend off a peer's prune, and it enumerates the whole directory
	// before it starts, so it equally dies reading a peer entry that already
	// has a `gitdir` but not yet a `commondir`. Two admissions into one
	// project — routine for a fan_out step (§7.6), whose lanes all live in
	// the parent's repository — then fail each other with git_error (§18)
	// for tasks that had nothing wrong with them.
	//
	// The daemon is the only thing that runs git here (ownership invariant),
	// so a process-local lock covers it. It is deliberately not m.claims:
	// that lock is task 005's, about gc versus create/claim, and taking it
	// exclusively would serialize creation daemon-wide across unrelated
	// projects. Ordering is claims-then-repo everywhere; nothing takes them
	// in the other order.
	reposMu sync.Mutex
	repos   map[string]*sync.Mutex
}

// NewManager returns a Manager storing worktrees under dataDir.
func NewManager(git *gitx.Git, dataDir string) *Manager {
	return &Manager{
		git:   git,
		root:  filepath.Join(dataDir, "worktrees"),
		repos: make(map[string]*sync.Mutex),
	}
}

// lockRepo blocks until nothing else in this process is mutating
// projectPath's `.git/worktrees`, and returns the release func.
//
// Entries are never dropped: there is one per project the daemon has touched,
// which is bounded by the project list, and a mutex nobody holds is two words.
func (m *Manager) lockRepo(projectPath string) func() {
	key := filepath.Clean(projectPath)
	m.reposMu.Lock()
	repo, ok := m.repos[key]
	if !ok {
		repo = new(sync.Mutex)
		m.repos[key] = repo
	}
	m.reposMu.Unlock()
	repo.Lock()
	return repo.Unlock
}

// Root is the directory every worktree lives under, and the only directory
// Reclaim will delete inside (spec §10).
func (m *Manager) Root() string { return m.root }

// Path returns the worktree location for an owner.
func (m *Manager) Path(o Owner) string {
	return filepath.Join(m.root, o.Dir())
}

// Created is what one worktree creation produced.
type Created struct {
	// Path is the worktree directory.
	Path string
	// BaseSHA is the commit the task branch starts at, set only when a fetch
	// resolved one (§5.3, task 056). Empty means the branch was cut from the
	// local base and `base_branch` still names the fork point.
	BaseSHA string
	// Fetch says what the base-branch fetch did; the zero value means none
	// was asked for.
	Fetch FetchOutcome
}

// Create adds branch (from base) and a worktree for it, returning the
// worktree path. It prunes stale worktree registrations first and never
// deletes an existing directory (prune-then-fail decision).
//
// fetch refreshes base from its configured upstream first and starts the
// branch at the fetched commit (§10, task 056); see fetchBase. A fetch that
// cannot happen or does not work never fails the creation.
//
// Callers that persist the result afterwards must use CreateAndClaim instead,
// so the create-then-claim window is closed against a concurrent gc scan —
// and because the base SHA the fetch resolved and the outcome worth logging
// are things only a caller that records the creation has any use for. This
// one deliberately drops both.
func (m *Manager) Create(
	ctx context.Context, projectPath string, owner Owner, branch, base string, fetch bool,
) (string, error) {
	m.claims.RLock()
	defer m.claims.RUnlock()
	c, err := m.create(ctx, projectPath, owner, branch, base, fetch)
	return c.Path, err
}

// CreateAndClaim creates the worktree and, still holding the claim lock,
// hands the path to claim — which is where the caller records it against the
// task row (task 005). A gc scan cannot run between the two, so a worktree is
// never unclaimed and reclaimable at the same instant.
//
// A claim that fails is reported, not undone: the directory is real and the
// engine's own error path decides what happens to the task. The next scan
// will see it as an orphan, which is precisely the crash case gc exists for.
func (m *Manager) CreateAndClaim(
	ctx context.Context, projectPath string, owner Owner, branch, base string, fetch bool,
	claim func(c Created) error,
) (Created, error) {
	m.claims.RLock()
	defer m.claims.RUnlock()
	c, err := m.create(ctx, projectPath, owner, branch, base, fetch)
	if err != nil {
		return c, err
	}
	if claim != nil {
		if err := claim(c); err != nil {
			return c, err
		}
	}
	return c, nil
}

// RemoveAndRelease removes the worktree and then clears the claim, both under
// the claim lock — the mirror of CreateAndClaim. Archive is the caller: a
// scan slipping between the removal and the cleared `worktree_path` would
// report the row as a reverse mismatch it never was (task 005).
func (m *Manager) RemoveAndRelease(
	ctx context.Context, projectPath, worktreePath string, force bool, release func() error,
) error {
	m.claims.RLock()
	defer m.claims.RUnlock()
	if err := m.Remove(ctx, projectPath, worktreePath, force); err != nil {
		return err
	}
	return release()
}

// WithReclaimLock runs fn with no create-or-claim in flight anywhere in the
// daemon. gc holds it across scan **and** removal, so the claim set it
// classifies against cannot move underneath it (task 005).
func (m *Manager) WithReclaimLock(fn func() error) error {
	m.claims.Lock()
	defer m.claims.Unlock()
	return fn()
}

func (m *Manager) create(
	ctx context.Context, projectPath string, owner Owner, branch, base string, fetch bool,
) (Created, error) {
	// Whole of create, not just the prune: the add is as unsafe against a
	// peer add as it is against a peer prune (#126, see Manager.repos). The
	// fetch and the FETCH_HEAD read that follows it are inside the same lock,
	// so no peer admission into this repository can land between them.
	unlock := m.lockRepo(projectPath)
	defer unlock()

	if _, err := os.Stat(projectPath); err != nil {
		return Created{}, &Error{
			Reason:  ReasonProjectPathMissing,
			Message: fmt.Sprintf("project path %s does not exist", projectPath), Err: err,
		}
	}
	// The local base is still required, and task creation is still offline
	// (§10 fail-fast): a base that exists only on the remote is not a case
	// this serves, and POST /v1/tasks rejects it before a task row exists.
	if !m.localBranchExists(ctx, projectPath, base) {
		return Created{}, &Error{
			Reason:  ReasonBaseBranchMissing,
			Message: fmt.Sprintf("base branch %q does not exist in %s", base, projectPath),
		}
	}
	if m.localBranchExists(ctx, projectPath, branch) {
		return Created{}, &Error{
			Reason:  ReasonBranchExists,
			Message: fmt.Sprintf("branch %q already exists in %s (never reused)", branch, projectPath),
		}
	}
	if err := m.prune(ctx, projectPath); err != nil {
		return Created{}, err
	}
	target := m.Path(owner)
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		return Created{}, &Error{
			Reason:  ReasonWorktreePathOccupied,
			Message: fmt.Sprintf("worktree path %s already exists and is not empty; remove it manually", target),
		}
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return Created{}, &Error{Reason: ReasonGitError, Message: "create worktrees dir", Err: err}
	}
	out := Created{Path: target, Fetch: FetchOutcome{Result: FetchDisabled}}
	start := base
	if fetch {
		sha, fo := m.fetchBase(ctx, projectPath, base)
		out.Fetch = fo
		if sha != "" {
			out.BaseSHA, start = sha, sha
		}
	}
	addCtx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	// --no-track unconditionally, including when start is a local branch
	// name. Under `branch.autoSetupMerge` git copies the start point's
	// upstream onto the new branch, and a task branch that carries one is a
	// live hazard: archive's remote leg pushes `--delete {remote} {merge-ref}`
	// (§10, task 008), so an inherited `refs/heads/master` would delete the
	// project's default branch on the forge, and a fan_out child would fetch
	// that upstream and silently unmake §7.6's inheritance. Starting from a
	// resolved SHA already avoids it; this is the belt behind the braces, and
	// it also covers `autoSetupMerge = always` on a local base, which was
	// always able to set one.
	if _, err := m.git.Run(addCtx, projectPath,
		"worktree", "add", target, "-b", branch, "--no-track", start); err != nil {
		return Created{}, &Error{Reason: ReasonGitError, Message: "git worktree add failed", Err: err}
	}
	return out, nil
}

// ValidateBranchName reports whether branch is a legal branch name, returning a
// ReasonBranchNameInvalid error when it is not (task 001). Rejection is loud
// rather than sanitized: quietly rewriting a name the user typed is the same
// dishonesty as faking a capability an adapter lacks.
func (m *Manager) ValidateBranchName(ctx context.Context, branch string) error {
	if err := m.git.CheckRefFormat(ctx, branch); err != nil {
		return &Error{
			Reason:  ReasonBranchNameInvalid,
			Message: fmt.Sprintf("%q is not a valid git branch name", branch),
			Err:     err,
		}
	}
	return nil
}

// BranchConflict returns the name of an existing branch that would prevent
// creating branch, or "" when the name is free.
//
// An exact-match check is not enough, because git stores refs as a path
// hierarchy: `feat/foo` cannot be created while `feat/foo/bar` exists, and
// `feat/foo/bar` cannot be created while `feat/foo` does. In the first case
// `git rev-parse --verify refs/heads/feat/foo` reports *not found*, so an
// exact-only pre-flight passes and `git worktree add` then fails with a
// directory/file-conflict message that maps to no named reason. Unreachable while
// every name is `vincent/{id}-{slug}` — one slash, and the id makes it unique —
// and reachable as soon as the user chooses the name (task 001).
//
// It is a pre-flight courtesy, not a guarantee: a branch can appear between this
// check and the worktree creation that follows, which is why Create still refuses
// a pre-existing branch and remains the authority.
func (m *Manager) BranchConflict(ctx context.Context, repo, branch string) (string, error) {
	if m.localBranchExists(ctx, repo, branch) {
		return branch, nil
	}
	// Refs *under* the name. The trailing slash in the pattern is what keeps this
	// from also matching the name itself.
	listCtx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	out, err := m.git.Run(listCtx, repo, "for-each-ref", "--count=1",
		"--format=%(refname:short)", "refs/heads/"+branch+"/")
	if err != nil {
		return "", &Error{Reason: ReasonGitError, Message: "git for-each-ref failed", Err: err}
	}
	if out != "" {
		return out, nil
	}
	// A prefix of the name that is itself a branch.
	for i := range len(branch) {
		if branch[i] != '/' {
			continue
		}
		if prefix := branch[:i]; m.localBranchExists(ctx, repo, prefix) {
			return prefix, nil
		}
	}
	return "", nil
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
// worktree), a directory under the manager's root is removed directly.
//
// Remove never touches the branch. That is a statement about this function,
// not the standing rule it used to restate: since task 008 archive may delete a
// branch that carries no commits past its base, through DeleteEmptyBranch,
// after this returns (spec §10). Ordering matters — the branch is checked out
// in the worktree until the worktree is gone.
func (m *Manager) Remove(ctx context.Context, projectPath, worktreePath string, force bool) error {
	// Same repository bookkeeping as create, from the other side: an archive
	// prunes the project repo while an admission may be adding to it (#126).
	unlock := m.lockRepo(projectPath)
	defer unlock()

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

// Reclaim deletes a directory under the manager's root without consulting
// git at all — gc's removal path (task 005), and doctor's (task 006). An
// orphan has, by definition, no task row and often no reachable repo, so
// `git worktree remove` has nothing to work from; the containment check is
// what keeps that safe.
//
// It is removeDirect exported unchanged: one containment rule, one
// implementation, so a path outside {data_dir}/worktrees is refused here for
// the same reason it is refused during a forced archive.
//
// What that costs is a stale registration left in whichever repo the worktree
// came from — `git worktree prune` there clears it, and doctor says so rather
// than reaching into a repository it was not asked to touch.
func (m *Manager) Reclaim(path string) error { return m.removeDirect(path) }

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
