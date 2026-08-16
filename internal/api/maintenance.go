package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/lezli01/vincent/internal/taskrun"
)

// orphanResponse is one entry under a data root that no task row claims
// (§10, §18). `skip` and `error` are separate on purpose: the first is gc
// declining to act, the second is gc having tried and failed.
type orphanResponse struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	// TaskID is the id the directory is named after, or null when its name
	// is not an id. Informational — a claim, not a name, is what decides.
	TaskID  *int64 `json:"task_id"`
	Bytes   int64  `json:"bytes"`
	Skip    string `json:"skip_reason,omitempty"`
	Error   string `json:"error,omitempty"`
	Removed bool   `json:"removed"`
}

// mismatchResponse is a task row whose worktree_path names a directory that
// is gone (§18's `worktree_missing` shape). Report-only: gc modifies no row.
type mismatchResponse struct {
	TaskID int64  `json:"task_id"`
	Path   string `json:"path"`
	State  string `json:"state"`
}

// orphanReport is the body of both maintenance endpoints. One shape for the
// list and the reclaim, so a `--dry-run` and a real run are compared field by
// field rather than read as two different reports.
type orphanReport struct {
	Orphans        []orphanResponse   `json:"orphans"`
	Mismatches     []mismatchResponse `json:"mismatches"`
	Bytes          int64              `json:"bytes"`
	Reclaimed      int                `json:"reclaimed"`
	ReclaimedBytes int64              `json:"reclaimed_bytes"`
	DryRun         bool               `json:"dry_run"`
	Force          bool               `json:"force"`
}

// gcRequest is the body of POST /v1/maintenance/gc. Both fields default
// false, so a bodyless POST is "remove the clean orphans", which is what
// `vincent gc` sends.
type gcRequest struct {
	Force  bool `json:"force"`
	DryRun bool `json:"dry_run"`
}

// handleOrphans serves GET /v1/maintenance/orphans: what gc would consider,
// with sizes, removing nothing.
func (s *Server) handleOrphans(w http.ResponseWriter, r *http.Request) {
	s.serveReport(w, r, func(ctx context.Context) (taskrun.Report, error) {
		return s.deps.Reclaimer.Scan(ctx)
	})
}

// handleGC serves POST /v1/maintenance/gc: the same scan, followed by the
// removal of everything eligible. The daemon owns the filesystem, so the
// client sends two booleans and renders what comes back (§4).
func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	var req gcRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	s.serveReport(w, r, func(ctx context.Context) (taskrun.Report, error) {
		return s.deps.Reclaimer.Reclaim(ctx, req.Force, req.DryRun)
	})
}

func (s *Server) serveReport(
	w http.ResponseWriter, r *http.Request, run func(context.Context) (taskrun.Report, error),
) {
	if s.deps.Reclaimer == nil {
		s.internalError(w, "reclaim", errors.New("no reclaimer is configured"))
		return
	}
	rep, err := run(r.Context())
	if err != nil {
		s.internalError(w, "reclaim", err)
		return
	}
	writeJSON(w, http.StatusOK, toOrphanReport(rep))
}

func toOrphanReport(rep taskrun.Report) orphanReport {
	out := orphanReport{
		Orphans:        make([]orphanResponse, 0, len(rep.Orphans)),
		Mismatches:     make([]mismatchResponse, 0, len(rep.Mismatches)),
		Bytes:          rep.Bytes,
		Reclaimed:      rep.Reclaimed,
		ReclaimedBytes: rep.ReclaimedBytes,
		DryRun:         rep.DryRun,
		Force:          rep.Force,
	}
	for _, o := range rep.Orphans {
		entry := orphanResponse{
			Path:    o.Path,
			Kind:    o.Kind,
			Bytes:   o.Bytes,
			Skip:    o.Skip,
			Error:   o.Error,
			Removed: o.Removed,
		}
		if o.TaskID != 0 {
			id := o.TaskID
			entry.TaskID = &id
		}
		out.Orphans = append(out.Orphans, entry)
	}
	for _, m := range rep.Mismatches {
		out.Mismatches = append(out.Mismatches, mismatchResponse{
			TaskID: m.TaskID, Path: m.Path, State: m.State,
		})
	}
	return out
}
