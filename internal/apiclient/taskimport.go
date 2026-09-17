package apiclient

import "context"

// TaskImportRequest is the body of POST /v1/tasks/import (task 117).
type TaskImportRequest struct {
	// Path is the archive `vincent daemon backup` wrote. It must be
	// absolute: the daemon resolves it against its own working directory.
	Path   string `json:"path"`
	TaskID int64  `json:"task_id"`
	// ProjectID re-homes the task into this live project. Zero keeps the
	// backed-up project, which must be live under the same id and name.
	ProjectID int64 `json:"project_id,omitempty"`
}

// TaskImportResult is what an import wrote.
type TaskImportResult struct {
	TaskID    int64  `json:"task_id"`
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	StepRuns  int    `json:"step_runs"`
	// StepRunsRenumbered is true when any backed-up step run id was already
	// taken here, in which case every one got a fresh id in its original
	// order. After undoing a delete on the same installation it is false.
	StepRunsRenumbered bool   `json:"step_runs_renumbered"`
	TranscriptFiles    int    `json:"transcript_files"`
	TranscriptBytes    int64  `json:"transcript_bytes"`
	ArchivedAt         string `json:"archived_at"`
	// BackupSchemaVersion and BackupCreatedAt come from the archive's
	// manifest, not from this installation.
	BackupSchemaVersion int    `json:"backup_schema_version"`
	BackupCreatedAt     string `json:"backup_created_at"`
}

// Import refusal reasons, as they arrive in Error.Details["reason"].
const (
	ImportRefusedTaskExists         = "task_exists"
	ImportRefusedNotArchived        = "not_archived"
	ImportRefusedProjectMismatch    = "project_mismatch"
	ImportRefusedParentMissing      = "parent_missing"
	ImportRefusedTranscriptsPresent = "transcripts_present"
	ImportTaskNotInBackup           = "task_not_in_backup"
	ImportProjectNotFound           = "project_not_found"
	ImportSchemaTooNew              = "schema_too_new"
)

// ImportTask brings one archived task out of a backup archive back into the
// live installation (POST /v1/tasks/import). The task keeps its id and comes
// back `archived`; a live task holding that id is a 409 `task_exists`.
//
// Unlike `daemon restore` this needs a running daemon: it writes rows, and
// only the daemon opens the database.
func (c *Client) ImportTask(ctx context.Context, req TaskImportRequest) (TaskImportResult, error) {
	var out TaskImportResult
	if err := c.post(ctx, "/v1/tasks/import", req, &out); err != nil {
		return TaskImportResult{}, err
	}
	return out, nil
}
