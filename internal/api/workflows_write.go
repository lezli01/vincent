package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lezli01/vincent/internal/workflow"
)

// The write half of §13.2 (task 065): POST /v1/workflows creates a workflow
// file in a chosen scope, PATCH /v1/workflows applies edit operations to one,
// and GET /v1/workflows/schema serves the §8.2 descriptor a client builds its
// forms from.
//
// The daemon owns the file end to end. A client sends a scope, a name and
// operations — never YAML — so the original bytes never leave the daemon and
// there is nothing for a round trip to flatten (task 065 decisions 1 and 2).
// The registry's own watch reloads what was written, exactly as it reloads a
// change made in $EDITOR; nothing here reaches into the registry to install a
// parsed workflow.
//
// Both write routes are excluded from MCP (decision 5, under task 057
// decision 4's wording): an agent must not reconfigure the daemon supervising
// it, and a workflow file is what that daemon runs. The schema route is an
// ordinary tool.

// workflowCreateRequest is the POST /v1/workflows body.
type workflowCreateRequest struct {
	Scope     string `json:"scope"`
	ProjectID int64  `json:"project_id"`
	Name      string `json:"name"`
	// From forks an existing registry entry: its bytes are copied verbatim,
	// comments included. A fork keeps the source's own `name:`, because
	// keeping it is what makes the copy shadow the original under §5.2 — so
	// Name addresses the *file*, and the two differ only for a fork.
	From string `json:"from,omitempty"`
	// FromProjectID scopes the lookup of From, defaulting to ProjectID.
	FromProjectID *int64 `json:"from_project_id,omitempty"`
}

// workflowWriteResponse is what both writes answer with: where the file
// landed and the version token the next PATCH must carry.
type workflowWriteResponse struct {
	Name    string `json:"name"`
	Scope   string `json:"scope"`
	File    string `json:"file"`
	Version string `json:"version"`
	// Errors and Warnings are the parse verdict on what was just written.
	// A create from the skeleton is valid; a PATCH that would not parse never
	// reaches the disk, so these are informational rather than a refusal.
	Errors   []workflow.Error `json:"errors"`
	Warnings []workflow.Error `json:"warnings"`
}

// handleWorkflowSchema serves GET /v1/workflows/schema — §8.2 as data, so a
// client renders forms from the same table Parse validates against instead of
// re-deriving it (task 065 decision 3; PR L's finding about drift).
func (s *Server) handleWorkflowSchema(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, workflow.SchemaDescriptor())
}

// handleWorkflowCreate serves POST /v1/workflows. It writes the §8 skeleton
// with its name: rewritten, or a fork source's bytes verbatim, so no YAML
// travels on the wire in either direction.
func (s *Server) handleWorkflowCreate(w http.ResponseWriter, r *http.Request) {
	var req workflowCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "name is required")
		return
	}
	scope := workflow.Scope(req.Scope)
	if scope == workflow.ScopeProject && req.ProjectID < 1 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"project_id is required for a project-scoped workflow")
		return
	}
	if req.ProjectID != 0 && !s.projectExists(w, r, req.ProjectID) {
		return
	}

	src, declared, ok := s.createSource(w, r, req)
	if !ok {
		return
	}

	s.workflowMu.Lock()
	defer s.workflowMu.Unlock()

	path, err := s.deps.Workflows.Destination(scope, req.ProjectID, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
		return
	}
	if _, err := os.Stat(path); err == nil {
		writeConflict(w, fmt.Sprintf("%s already exists", filepath.Base(path)),
			map[string]string{"file": path})
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.internalError(w, "workflows: stat destination", err)
		return
	}
	// A second file declaring a name the target scope already declares is the
	// §5.2 duplicate it would become — the registry would list one of them as
	// an error rather than run it. Refusing it here is the same judgement,
	// made before the file exists.
	if other, dup := s.duplicateInScope(scope, req.ProjectID, declared, path); dup {
		writeConflict(w, fmt.Sprintf("%s already declares the name %q in this scope",
			filepath.Base(other), declared), map[string]string{"file": other, "name": declared})
		return
	}
	if err := workflow.WriteFile(path, src); err != nil {
		s.internalError(w, "workflows: write", err)
		return
	}
	s.reloadScope(scope, req.ProjectID)
	writeJSON(w, http.StatusCreated, s.writeResponse(declared, scope, path, src))
}

// createSource returns the bytes a create writes and the name they declare:
// the §8 skeleton renamed, or a fork source copied verbatim.
func (s *Server) createSource(w http.ResponseWriter, r *http.Request, req workflowCreateRequest) ([]byte, string, bool) {
	if req.From == "" {
		src, err := workflow.SetName([]byte(workflow.SkeletonSource), req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
			return nil, "", false
		}
		return src, req.Name, true
	}
	fromProject := req.ProjectID
	if req.FromProjectID != nil {
		fromProject = *req.FromProjectID
		if fromProject != 0 && !s.projectExists(w, r, fromProject) {
			return nil, "", false
		}
	}
	entry, found := s.deps.Workflows.Lookup(fromProject, req.From)
	if !found {
		writeError(w, http.StatusNotFound, CodeNotFound, "no workflow named "+req.From)
		return nil, "", false
	}
	src, err := entrySource(entry)
	if err != nil {
		s.internalError(w, "workflows: read fork source", err)
		return nil, "", false
	}
	// A fork keeps the source's `name:`. That is not an oversight: shadowing
	// under §5.2 is by name, so renaming the copy would produce a second
	// workflow beside the original rather than one in front of it.
	name := workflow.DeclaredName(src)
	if name == "" {
		name = req.From
	}
	return src, name, true
}

// entrySource reads the bytes behind a registry entry, including a built-in's
// compiled-in source — a fork of a built-in is the only way to change one.
func entrySource(e workflow.Entry) ([]byte, error) {
	if e.File == "" {
		src, ok := workflow.BuiltinSource(e.Name)
		if !ok {
			return nil, fmt.Errorf("workflow %s has no source", e.Name)
		}
		return src, nil
	}
	return os.ReadFile(e.File)
}

// duplicateInScope reports another file in the same scope already declaring
// name, skipping the destination itself.
func (s *Server) duplicateInScope(scope workflow.Scope, projectID int64, name, dest string) (string, bool) {
	for _, e := range s.deps.Workflows.List(projectID) {
		if e.Scope != scope || e.File == "" || e.File == dest {
			continue
		}
		if e.ProjectID != projectID {
			continue
		}
		if e.Name == name {
			return e.File, true
		}
	}
	return "", false
}

// workflowPatchRequest is the PATCH /v1/workflows body.
type workflowPatchRequest struct {
	// Version is the token the read handed back. An empty one is refused
	// rather than treated as "force": a client that never read the file has
	// no edit to apply to it.
	Version string        `json:"version"`
	Ops     []workflow.Op `json:"ops"`
}

// handleWorkflowPatch serves PATCH /v1/workflows?name=&project_id=.
//
// The order is task 060's, extended by the precondition: read the file fresh,
// compare the version, apply the operations to its bytes, parse the
// candidate, refuse the request without touching the disk if it does not
// hold, then write atomically. A rejected patch leaves the file byte-identical.
func (s *Server) handleWorkflowPatch(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDParam(s, w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "name is required")
		return
	}
	var req workflowPatchRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if req.Version == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "version is required")
		return
	}
	if len(req.Ops) == 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "ops must not be empty")
		return
	}

	s.workflowMu.Lock()
	defer s.workflowMu.Unlock()

	entry, found := s.deps.Workflows.Lookup(projectID, name)
	if !found {
		writeError(w, http.StatusNotFound, CodeNotFound, "no workflow named "+name)
		return
	}
	if entry.File == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"built-in workflows have no file to edit; fork "+name+" into a project or global scope first")
		return
	}
	src, err := os.ReadFile(entry.File)
	if err != nil {
		s.internalError(w, "workflows: read", err)
		return
	}
	current, err := workflow.Version(entry.File)
	if err != nil {
		s.internalError(w, "workflows: version", err)
		return
	}
	if current != req.Version {
		// The second writer is not the same human (task 065 decision 4): the
		// create-workflow built-in writes this directory from an agent run,
		// and $EDITOR is one key away in the same view.
		writeConflict(w, filepath.Base(entry.File)+" changed on disk since it was read",
			map[string]string{"version": current, "file": entry.File})
		return
	}
	next, err := workflow.Edit(src, req.Ops)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
		return
	}
	if len(next) > workflow.MaxSourceBytes {
		writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
			fmt.Sprintf("a workflow source must be at most %d bytes (got %d)",
				workflow.MaxSourceBytes, len(next)))
		return
	}
	wf, warns, err := workflow.Parse(next, s.deps.Workflows.Options())
	if err != nil {
		var errs workflow.Errors
		if !errors.As(err, &errs) {
			errs = workflow.Errors{{Message: err.Error()}}
		}
		// Nothing is written: the file on disk is byte-identical to what it
		// was before the request, which is task 060's pattern.
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorDetail{
			Code:    CodeValidationFailed,
			Message: errs.Error(),
			Details: map[string]string{"file": entry.File, "errors": errs.Error()},
		}})
		return
	}
	// A patch that renames the workflow into a name its own scope already
	// declares is the same §5.2 duplicate a create is refused for.
	if wf.Name != entry.Name {
		if other, dup := s.duplicateInScope(entry.Scope, entry.ProjectID, wf.Name, entry.File); dup {
			writeConflict(w, fmt.Sprintf("%s already declares the name %q in this scope",
				filepath.Base(other), wf.Name), map[string]string{"file": other, "name": wf.Name})
			return
		}
	}
	if err := workflow.WriteFile(entry.File, next); err != nil {
		s.internalError(w, "workflows: write", err)
		return
	}
	s.reloadScope(entry.Scope, entry.ProjectID)
	resp := s.writeResponse(wf.Name, entry.Scope, entry.File, next)
	resp.Warnings = warns
	writeJSON(w, http.StatusOK, resp)
}

// writeResponse builds the answer both writes share. The version is computed
// from the file that is now on disk, so the client's next PATCH carries a
// token that matches what it would read.
func (s *Server) writeResponse(name string, scope workflow.Scope, path string, src []byte) workflowWriteResponse {
	out := workflowWriteResponse{
		Name:     name,
		Scope:    string(scope),
		File:     path,
		Errors:   []workflow.Error{},
		Warnings: []workflow.Error{},
	}
	if v, err := workflow.Version(path); err == nil {
		out.Version = v
	} else {
		out.Version = workflow.VersionOf(0, src)
	}
	if _, warns, err := workflow.Parse(src, s.deps.Workflows.Options()); err != nil {
		var errs workflow.Errors
		if errors.As(err, &errs) {
			out.Errors = errs
		} else {
			out.Errors = workflow.Errors{{Message: err.Error()}}
		}
	} else if len(warns) > 0 {
		out.Warnings = warns
	}
	return out
}

// reloadScope re-reads the scope a write landed in, so a GET issued the
// instant the write returns sees it — the fsnotify watcher's later fire
// re-reads identical bytes and is a no-op. It is the same "put it into force
// before answering" contract PATCH /v1/config has (task 060 decision 5).
func (s *Server) reloadScope(scope workflow.Scope, projectID int64) {
	if scope == workflow.ScopeProject {
		s.deps.Workflows.ReloadProject(projectID)
		return
	}
	s.deps.Workflows.ReloadGlobal()
}

// projectExists writes the §13.1 envelope and reports false for an id the
// store does not know.
func (s *Server) projectExists(w http.ResponseWriter, r *http.Request, id int64) bool {
	if _, err := s.deps.Store.GetProject(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("project %d not found", id))
			return false
		}
		s.internalError(w, "workflows: get project", err)
		return false
	}
	return true
}
