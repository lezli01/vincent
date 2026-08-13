package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// placeholderTaskID stands in for the real task id when a branch name has to be
// checked for *legality* before the row that would supply the id exists.
//
// This is sound because ref legality does not depend on which digits the id
// renders to: every positive integer is a legal ref component everywhere in a
// name, so a template that produces an illegal name with this id produces one
// with every id, and vice versa. It lets an id-bearing template's syntax be
// rejected with a 400 at creation instead of surfacing as a blocked task later.
//
// Collision is the part that genuinely cannot be pre-checked on this path, and it
// is not pre-checked: the name a real id produces is a different string. That is
// the original §10 argument still holding — an id-bearing name is unique by
// construction — with the admission-time `branch_exists` check as the guard.
const placeholderTaskID = 1

// branchPreview is a resolved name and the level of the chain that produced it.
type branchPreview struct {
	Name string
	// Source is one of worktree.BranchSource*.
	Source string
	// NeedsID reports that the real name can only be produced after the insert,
	// so Name was rendered with placeholderTaskID and is a preview, not the name.
	NeedsID bool
}

// branchSpec assembles the naming chain for a task: the literal the caller
// supplied, this project's template, and the global one from config.yaml.
func (s *Server) branchSpec(literal string, p *store.Project) worktree.BranchSpec {
	return worktree.BranchSpec{
		Literal:         literal,
		ProjectTemplate: p.BranchTemplate,
		ConfigTemplate:  s.deps.Config().BranchTemplate,
	}
}

// branchContext builds the template context for a task that does not exist yet.
func branchContext(title, baseBranch string, fields map[string]string, p *store.Project) worktree.BranchContext {
	return worktree.NewBranchContext(title, baseBranch, fields, worktree.BranchProject{
		Name:          p.Name,
		Path:          p.Path,
		DefaultBranch: p.DefaultBranch,
	})
}

// resolveBranchPreview applies the chain without a task id. A template that needs
// one is re-rendered with placeholderTaskID so the result is still inspectable —
// legal-or-not for creation, human-readable for /v1/resolve — with NeedsID set.
//
// The returned error is a template failure the caller should report as a 400: a
// missing field, a syntax error, or a name that rendered empty.
func resolveBranchPreview(spec worktree.BranchSpec, bctx worktree.BranchContext) (branchPreview, error) {
	name, source, err := worktree.ResolveBranchName(spec, bctx)
	if err == nil {
		return branchPreview{Name: name, Source: source}, nil
	}
	if !errors.Is(err, worktree.ErrBranchNeedsID) {
		return branchPreview{}, err
	}
	name, source, err = worktree.ResolveBranchName(spec, bctx.WithID(placeholderTaskID))
	if err != nil {
		return branchPreview{}, err
	}
	return branchPreview{Name: name, Source: source, NeedsID: true}, nil
}

// checkBranchLegality rejects a name git itself would refuse, returning the
// message for a 400 (empty means legal). It applies on both resolution paths,
// because legality does not depend on the id — see placeholderTaskID.
func (s *Server) checkBranchLegality(ctx context.Context, branch string) string {
	if err := s.deps.Worktrees.ValidateBranchName(ctx, branch); err != nil {
		return fmt.Sprintf("branch name %q is not valid: %s", branch, gitRefRules)
	}
	return ""
}

// checkBranchCollision rejects a name an existing branch already blocks,
// returning the message for a 400 (empty means free). It only applies where the
// resolved name is final, since a placeholder-rendered preview would be checking
// the wrong string.
//
// It shells out to git, so like checkBranchLegality it must run *outside* any
// database transaction: SQLite has a single writer and gitx allows 30 seconds, so
// holding the write lock across it would stall every admission and step_run write
// in the daemon.
//
// It is a courtesy, not a guarantee — a branch can appear between this check and
// the worktree creation that follows, which is why admission still refuses a
// pre-existing branch and remains the authority.
func (s *Server) checkBranchCollision(ctx context.Context, repo, branch string) string {
	conflict, err := s.deps.Worktrees.BranchConflict(ctx, repo, branch)
	if err != nil {
		// A git failure here is not the caller's fault. Let the task be created
		// and let admission, the authority, report it.
		s.deps.Logger.Warn("branch conflict pre-check failed", "branch", branch, "error", err)
		return ""
	}
	switch conflict {
	case "":
		return ""
	case branch:
		return fmt.Sprintf("branch %q already exists in %s; vincent never reuses a branch", branch, repo)
	default:
		return fmt.Sprintf("branch %q cannot be created because %q exists: git stores refs as a "+
			"path hierarchy, so a branch and a branch under it cannot coexist", branch, conflict)
	}
}

// renameBranchForRetry applies a retry's branch_override, writing the response
// itself and reporting whether the retry should proceed.
//
// It runs *before* the retry, because the point is to re-admit onto the new name,
// and it insists the task is `blocked` first: renaming a task that then turns out
// not to be retryable would leave a rename nobody asked for. Runner.Retry remains
// the authority on the transition — this only declines to rename early.
func (s *Server) renameBranchForRetry(w http.ResponseWriter, r *http.Request, branch string) bool {
	ctx := r.Context()
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return false
	}
	task, err := s.deps.Store.GetTask(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("task %d not found", id))
		return false
	}
	if err != nil {
		s.internalError(w, "get task", err)
		return false
	}
	if task.State != store.TaskBlocked {
		writeConflict(w,
			fmt.Sprintf("branch_override needs a blocked task; task %d is %s", id, task.State),
			map[string]string{"state": string(task.State)})
		return false
	}
	project, err := s.deps.Store.GetProject(ctx, task.ProjectID)
	if err != nil {
		s.internalError(w, "get project", err)
		return false
	}
	if msg := s.checkBranchLegality(ctx, branch); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return false
	}
	if msg := s.checkBranchCollision(ctx, project.Path, branch); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return false
	}
	if err := s.deps.Store.SetTaskBranchName(ctx, id, task.ProjectID, branch); err != nil {
		var claimed *store.BranchClaimedError
		if errors.As(err, &claimed) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("branch %q is already claimed by task %d", claimed.Branch, claimed.TaskID))
			return false
		}
		s.internalError(w, "rename task branch", err)
		return false
	}
	return true
}

// previewBranch answers the branch half of POST /v1/resolve: the name this draft
// would get and the level that decided it (task 001, §13.2).
//
// A name that depends on the task id is rendered with a literal `<id>` marker
// rather than a guessed number. Predicting the next id would be wrong as soon as
// two drafts are open, and a preview that is confidently wrong is worse than one
// that admits which part it cannot know.
func (s *Server) previewBranch(ctx context.Context, projectID int64, req resolveRequest) (*resolvedBranch, error) {
	project, err := s.deps.Store.GetProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project %d: %w", projectID, err)
	}
	baseBranch := req.BaseBranch
	if strings.TrimSpace(baseBranch) == "" {
		baseBranch = project.DefaultBranch
	}
	spec := s.branchSpec(strings.TrimSpace(req.BranchName), project)
	bctx := branchContext(req.Title, baseBranch, req.Fields, project)

	name, source, err := worktree.ResolveBranchName(spec, bctx)
	if err == nil {
		return &resolvedBranch{Value: name, Source: source}, nil
	}
	if !errors.Is(err, worktree.ErrBranchNeedsID) {
		return nil, fmt.Errorf("branch name could not be resolved: %w", err)
	}
	// Render with the placeholder id, then put the marker back where the digits
	// landed. Substitution beats a second template context: the id can appear
	// anywhere in the name, more than once, and only the template knows where.
	name, source, err = worktree.ResolveBranchName(spec, bctx.WithID(placeholderTaskID))
	if err != nil {
		return nil, fmt.Errorf("branch name could not be resolved: %w", err)
	}
	marked := strings.ReplaceAll(name, strconv.Itoa(placeholderTaskID), "<id>")
	return &resolvedBranch{Value: marked, Source: source, Placeholder: true}, nil
}

// gitRefRules summarises what git rejects, so a 400 tells the user what to change
// instead of only that something was wrong.
const gitRefRules = `a branch name cannot contain "..", "~", "^", ":", "?", "*", "[", "\\", ` +
	`whitespace or control characters, cannot contain "@{" or "//", cannot begin with "-" ` +
	`or end with "." or ".lock", and cannot be "HEAD"`
