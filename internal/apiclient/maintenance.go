package apiclient

import "context"

// Orphan kinds as the daemon names them (§10, §17).
const (
	OrphanWorktree   = "worktree"
	OrphanTranscript = "transcript"
)

// SkipNotADirectory is the one skip reason a client renders differently: it is
// the only one `--force` never clears, because vincent creates nothing but
// directories under a data root and will not delete what it did not create.
const SkipNotADirectory = "not_a_directory"

// Orphan is one directory under a vincent data root that no task row claims.
//
// SkipReason and Error are different answers: the first is gc declining to
// remove it (`worktree_dirty`, `dirty_unknown`, `not_a_directory`), the
// second is gc having tried and failed — a file locked by another process, a
// permissions problem. Only the second means the leak is still there after a
// `--force` run.
type Orphan struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	// TaskID is the id the directory is named after, nil when its name is
	// not an id at all.
	TaskID     *int64 `json:"task_id"`
	Bytes      int64  `json:"bytes"`
	SkipReason string `json:"skip_reason,omitempty"`
	Error      string `json:"error,omitempty"`
	Removed    bool   `json:"removed"`
}

// Mismatch is a task row whose worktree_path names a directory that is gone
// (§18's `worktree_missing`). Report-only — gc modifies no row.
type Mismatch struct {
	TaskID int64  `json:"task_id"`
	Path   string `json:"path"`
	State  string `json:"state"`
}

// OrphanReport is the body of both maintenance endpoints. A dry run and a
// real run return the same shape, so the two are compared field by field.
type OrphanReport struct {
	Orphans        []Orphan   `json:"orphans"`
	Mismatches     []Mismatch `json:"mismatches"`
	Bytes          int64      `json:"bytes"`
	Reclaimed      int        `json:"reclaimed"`
	ReclaimedBytes int64      `json:"reclaimed_bytes"`
	DryRun         bool       `json:"dry_run"`
	Force          bool       `json:"force"`
}

// Orphans lists what gc would consider, with sizes, removing nothing
// (GET /v1/maintenance/orphans).
func (c *Client) Orphans(ctx context.Context) (OrphanReport, error) {
	var out OrphanReport
	if err := c.get(ctx, "/v1/maintenance/orphans", &out); err != nil {
		return OrphanReport{}, err
	}
	return out, nil
}

// GC reclaims orphaned worktree and transcript directories
// (POST /v1/maintenance/gc). force removes a worktree git calls dirty, or
// cannot judge; dryRun returns the identical report and removes nothing.
func (c *Client) GC(ctx context.Context, force, dryRun bool) (OrphanReport, error) {
	var out OrphanReport
	body := gcRequest{Force: force, DryRun: dryRun}
	if err := c.post(ctx, "/v1/maintenance/gc", body, &out); err != nil {
		return OrphanReport{}, err
	}
	return out, nil
}

type gcRequest struct {
	Force  bool `json:"force"`
	DryRun bool `json:"dry_run"`
}
