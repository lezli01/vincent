package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// projectResponse is the JSON shape of a project (spec §5.1). Optional fields
// render as null: default_workflow null = none, max_parallel_tasks null =
// unlimited.
type projectResponse struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Path             string  `json:"path"`
	DefaultBranch    string  `json:"default_branch"`
	DefaultWorkflow  *string `json:"default_workflow"`
	MaxParallelTasks *int    `json:"max_parallel_tasks"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

func toProjectResponse(p *store.Project) projectResponse {
	r := projectResponse{
		ID:               p.ID,
		Name:             p.Name,
		Path:             p.Path,
		DefaultBranch:    p.DefaultBranch,
		MaxParallelTasks: p.MaxParallelTasks,
		CreatedAt:        p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.DefaultWorkflow != "" {
		w := p.DefaultWorkflow
		r.DefaultWorkflow = &w
	}
	return r
}

func (s *Server) handleProjectList(w http.ResponseWriter, r *http.Request) {
	projects, err := s.deps.Store.ListProjects(r.Context())
	if err != nil {
		s.internalError(w, "list projects", err)
		return
	}
	out := make([]projectResponse, 0, len(projects))
	for i := range projects {
		out = append(out, toProjectResponse(&projects[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

type projectCreateRequest struct {
	Path             string  `json:"path"`
	Name             *string `json:"name"`
	DefaultBranch    *string `json:"default_branch"`
	DefaultWorkflow  *string `json:"default_workflow"`
	MaxParallelTasks *int    `json:"max_parallel_tasks"`
}

func (s *Server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	var req projectCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	path, msg := s.validateRepoPath(ctx, req.Path)
	if msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	existing, err := s.deps.Store.ListProjects(ctx)
	if err != nil {
		s.internalError(w, "list projects", err)
		return
	}
	if dup := findSameRepo(existing, path, 0); dup != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("repository %s is already registered as project %q (id %d)", path, dup.Name, dup.ID))
		return
	}

	name := filepath.Base(path)
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if msg := validateName(existing, name, 0); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}

	var branch string
	if req.DefaultBranch != nil {
		branch = *req.DefaultBranch
		if msg := s.validateLocalBranch(ctx, path, branch); msg != "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
			return
		}
	} else {
		branch, err = s.detectDefaultBranch(ctx, path)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
			return
		}
	}

	p := store.Project{Name: name, Path: path, DefaultBranch: branch}
	if req.DefaultWorkflow != nil {
		p.DefaultWorkflow = *req.DefaultWorkflow
	}
	if req.MaxParallelTasks != nil {
		if msg := validateMaxParallel(*req.MaxParallelTasks); msg != "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
			return
		}
		p.MaxParallelTasks = req.MaxParallelTasks
	}
	if err := s.deps.Store.CreateProject(ctx, &p); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: projects.name") {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("project name %q is already in use; pass a different name", name))
			return
		}
		s.internalError(w, "create project", err)
		return
	}
	s.projectsChanged()
	writeJSON(w, http.StatusCreated, toProjectResponse(&p))
}

func (s *Server) handleProjectGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toProjectResponse(p))
}

// projectPatchRequest distinguishes absent, null, and set fields: absent =
// unchanged, null = clear where the field is optional (T1.5 decision).
type projectPatchRequest struct {
	Name             opt[string] `json:"name"`
	Path             opt[string] `json:"path"`
	DefaultBranch    opt[string] `json:"default_branch"`
	DefaultWorkflow  opt[string] `json:"default_workflow"`
	MaxParallelTasks opt[int]    `json:"max_parallel_tasks"`
}

func (s *Server) handleProjectPatch(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	var req projectPatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	for field, o := range map[string]interface{ isNull() bool }{
		"name": req.Name, "path": req.Path, "default_branch": req.DefaultBranch,
	} {
		if o.isNull() {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, field+" cannot be null")
			return
		}
	}

	existing, err := s.deps.Store.ListProjects(ctx)
	if err != nil {
		s.internalError(w, "list projects", err)
		return
	}
	if req.Name.set {
		name := strings.TrimSpace(req.Name.val)
		if msg := validateName(existing, name, p.ID); msg != "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
			return
		}
		p.Name = name
	}
	branchChecked := false
	if req.Path.set {
		path, msg := s.validateRepoPath(ctx, req.Path.val)
		if msg != "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
			return
		}
		if dup := findSameRepo(existing, path, p.ID); dup != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("repository %s is already registered as project %q (id %d)", path, dup.Name, dup.ID))
			return
		}
		p.Path = path
		branchChecked = true // repoint must re-prove the branch invariant
	}
	if req.DefaultBranch.set {
		p.DefaultBranch = req.DefaultBranch.val
		branchChecked = true
	}
	if branchChecked {
		if msg := s.validateLocalBranch(ctx, p.Path, p.DefaultBranch); msg != "" {
			if req.Path.set && !req.DefaultBranch.set {
				msg += " (repointing to a repo without the stored default branch; pass default_branch in the same request)"
			}
			writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
			return
		}
	}
	if req.DefaultWorkflow.set {
		if req.DefaultWorkflow.null {
			p.DefaultWorkflow = ""
		} else {
			p.DefaultWorkflow = req.DefaultWorkflow.val
		}
	}
	if req.MaxParallelTasks.set {
		if req.MaxParallelTasks.null {
			p.MaxParallelTasks = nil
		} else {
			if msg := validateMaxParallel(req.MaxParallelTasks.val); msg != "" {
				writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
				return
			}
			v := req.MaxParallelTasks.val
			p.MaxParallelTasks = &v
		}
	}
	if err := s.deps.Store.UpdateProject(ctx, p); err != nil {
		s.internalError(w, "update project", err)
		return
	}
	s.projectsChanged()
	writeJSON(w, http.StatusOK, toProjectResponse(p))
}

// projectsChanged tells the workflow registry to follow the current set of
// project scopes (§5.2). It is a no-op when no callback is wired.
func (s *Server) projectsChanged() {
	if s.deps.OnProjectsChanged != nil {
		s.deps.OnProjectsChanged()
	}
}

func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	force := hasForce(r)
	// ArchivedAll is explicit: this handler classifies archived vs not
	// itself, and must not silently inherit the list default (which excludes
	// archives for the board's benefit, §13.2).
	tasks, err := s.deps.Store.ListTasks(ctx,
		store.TaskFilter{ProjectID: p.ID, Archived: store.ArchivedAll})
	if err != nil {
		s.internalError(w, "list tasks", err)
		return
	}
	var nonArchived []store.Task
	for _, t := range tasks {
		if t.State != store.TaskArchived {
			nonArchived = append(nonArchived, t)
		}
	}
	if len(nonArchived) > 0 && !force {
		writeError(w, http.StatusConflict, CodeInvalidState,
			fmt.Sprintf("project has %d non-archived task(s); pass ?force to archive them and delete anyway", len(nonArchived)))
		return
	}
	for _, t := range nonArchived {
		if t.State == store.TaskRunning {
			writeError(w, http.StatusConflict, CodeInvalidState,
				fmt.Sprintf("task %d is running; cancel it before deleting the project", t.ID))
			return
		}
	}
	// force is the dirty-worktree confirmation (T1.5 decision); rows are
	// deleted regardless, so worktree removal is best-effort.
	for _, t := range nonArchived {
		if t.WorktreePath == "" {
			continue
		}
		if err := s.deps.Worktrees.Remove(ctx, p.Path, t.WorktreePath, true); err != nil {
			s.deps.Logger.Warn("force delete: worktree removal failed",
				"task", t.ID, "worktree", t.WorktreePath, "error", err)
		}
	}
	if err := s.deps.Store.DeleteProjectCascade(ctx, p.ID); err != nil {
		s.internalError(w, "delete project", err)
		return
	}
	s.projectsChanged()
	w.WriteHeader(http.StatusNoContent)
}

// projectFromPath resolves the {id} path segment to a project, writing the
// 404/400 response itself when it cannot.
func (s *Server) projectFromPath(w http.ResponseWriter, r *http.Request) (*store.Project, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "project ids are integers")
		return nil, false
	}
	p, err := s.deps.Store.GetProject(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("project %d not found", id))
		return nil, false
	}
	if err != nil {
		s.internalError(w, "get project", err)
		return nil, false
	}
	return p, true
}

// validateRepoPath enforces the strict-toplevel registration rules (T1.5
// decision): absolute existing directory, a non-bare git repository, not a
// subdirectory, not a linked worktree. Returns the cleaned path or a
// validation message.
func (s *Server) validateRepoPath(ctx context.Context, raw string) (path, msg string) {
	if strings.TrimSpace(raw) == "" {
		return "", "path is required"
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Sprintf("path %q must be absolute (the daemon does not share your working directory)", raw)
	}
	path = filepath.Clean(raw)
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Sprintf("path %s does not exist", path)
	}
	if !fi.IsDir() {
		return "", fmt.Sprintf("path %s is not a directory", path)
	}
	bare, err := s.git(ctx, path, "rev-parse", "--is-bare-repository")
	if err != nil {
		return "", fmt.Sprintf("path %s is not a git repository", path)
	}
	if bare == "true" {
		return "", fmt.Sprintf("path %s is a bare repository; register a checkout with a working tree", path)
	}
	toplevel, err := s.git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Sprintf("path %s is not a git repository", path)
	}
	toplevel = filepath.FromSlash(toplevel)
	if !sameDir(path, toplevel) {
		return "", fmt.Sprintf("path %s is inside a repository whose toplevel is %s; register the toplevel", path, toplevel)
	}
	gitDir, err1 := s.git(ctx, path, "rev-parse", "--absolute-git-dir")
	commonDir, err2 := s.git(ctx, path, "rev-parse", "--git-common-dir")
	if err1 == nil && err2 == nil {
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(path, commonDir)
		}
		if !sameDir(filepath.FromSlash(gitDir), filepath.FromSlash(commonDir)) {
			return "", fmt.Sprintf("path %s is a linked git worktree of %s; register the main checkout",
				path, filepath.Dir(filepath.FromSlash(commonDir)))
		}
	}
	return path, ""
}

// detectDefaultBranch walks the phase 1 detection chain — origin/HEAD →
// main → master → current HEAD branch — requiring every candidate to resolve
// to a local branch (T1.5 decision).
func (s *Server) detectDefaultBranch(ctx context.Context, repo string) (string, error) {
	if ref, err := s.git(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if name, ok := strings.CutPrefix(ref, "refs/remotes/origin/"); ok && s.localBranchExists(ctx, repo, name) {
			return name, nil
		}
	}
	for _, name := range []string{"main", "master"} {
		if s.localBranchExists(ctx, repo, name) {
			return name, nil
		}
	}
	if ref, err := s.git(ctx, repo, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok && s.localBranchExists(ctx, repo, name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("could not detect a default branch in %s (no origin/HEAD, main, or master; HEAD detached or unborn); pass default_branch explicitly", repo)
}

func (s *Server) validateLocalBranch(ctx context.Context, repo, name string) string {
	if strings.TrimSpace(name) == "" {
		return "default_branch cannot be empty"
	}
	if !s.localBranchExists(ctx, repo, name) {
		return fmt.Sprintf("default_branch %q does not resolve to a local branch in %s", name, repo)
	}
	return ""
}

func (s *Server) localBranchExists(ctx context.Context, repo, name string) bool {
	_, err := s.git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

func (s *Server) git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	return s.deps.Git.Run(ctx, dir, args...)
}

func validateName(existing []store.Project, name string, selfID int64) string {
	if name == "" {
		return "name cannot be empty"
	}
	for _, p := range existing {
		if p.ID != selfID && p.Name == name {
			return fmt.Sprintf("project name %q is already in use; pass a different name", name)
		}
	}
	return ""
}

func validateMaxParallel(v int) string {
	if v < 1 {
		return "max_parallel_tasks must be at least 1 (null = unlimited)"
	}
	return ""
}

// findSameRepo returns the first project (excluding selfID) whose path names
// the same directory as path — identity, not string, comparison, so symlink
// and case aliases are caught (T1.5 decision).
func findSameRepo(existing []store.Project, path string, selfID int64) *store.Project {
	for i := range existing {
		if existing[i].ID != selfID && sameDir(existing[i].Path, path) {
			return &existing[i]
		}
	}
	return nil
}

// sameDir reports whether a and b name the same directory on disk. A path
// that no longer exists matches nothing.
func sameDir(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

func hasForce(r *http.Request) bool {
	if !r.URL.Query().Has("force") {
		return false
	}
	v := r.URL.Query().Get("force")
	return v != "false" && v != "0"
}

// opt is a JSON field that distinguishes absent (set=false), null, and a
// concrete value.
type opt[T any] struct {
	set  bool
	null bool
	val  T
}

func (o opt[T]) isNull() bool { return o.set && o.null }

// UnmarshalJSON is only invoked for fields present in the document, which is
// what makes absent-vs-null distinguishable.
func (o *opt[T]) UnmarshalJSON(b []byte) error {
	o.set = true
	if string(b) == "null" {
		o.null = true
		return nil
	}
	if err := json.Unmarshal(b, &o.val); err != nil {
		return err
	}
	return nil
}

// decodeJSON decodes the request body strictly (unknown fields rejected),
// writing the error response itself on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			writeError(w, http.StatusBadRequest, CodeInvalidJSON, "request body is not valid JSON")
			return false
		}
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "invalid request body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.deps.Logger.Error(what, "error", err)
	writeError(w, http.StatusInternalServerError, CodeInternal, what+" failed")
}
