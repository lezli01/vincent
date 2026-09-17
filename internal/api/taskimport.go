package api

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/store"
)

// taskImportRequest is the body of POST /v1/tasks/import (task 117). The
// client resolves path to an absolute one before sending, for the reason
// backupRequest gives: the daemon's working directory is not the caller's.
type taskImportRequest struct {
	Path   string `json:"path"`
	TaskID int64  `json:"task_id"`
	// ProjectID re-homes the task; zero keeps the backed-up project, which
	// must be live under the same id and name (decision 7).
	ProjectID int64 `json:"project_id"`
}

// taskImportResponse reports what was imported. step_runs_renumbered is
// reported because it is the one thing about an import a user could not
// otherwise see: the attempts are all there either way (decision 3).
type taskImportResponse struct {
	TaskID              int64  `json:"task_id"`
	ProjectID           int64  `json:"project_id"`
	Title               string `json:"title"`
	StepRuns            int    `json:"step_runs"`
	StepRunsRenumbered  bool   `json:"step_runs_renumbered"`
	TranscriptFiles     int    `json:"transcript_files"`
	TranscriptBytes     int64  `json:"transcript_bytes"`
	ArchivedAt          string `json:"archived_at"`
	BackupSchemaVersion int    `json:"backup_schema_version"`
	BackupCreatedAt     string `json:"backup_created_at"`
}

// Reasons in `details` for the refusals that are not 409s. The 409s carry
// store.ImportRefused*.
const (
	importReasonSchemaTooNew    = "schema_too_new"
	importReasonTaskNotInBackup = "task_not_in_backup"
	importReasonProjectNotFound = "project_not_found"
)

// importStagingPrefix names the staging directory under {data_dir}: the same
// volume as the transcripts, so placing them is a rename rather than a copy.
const importStagingPrefix = ".vincent-import-"

// handleTaskImport serves POST /v1/tasks/import: one archived task out of a
// `vincent daemon backup` archive, back into the live installation (§13.2,
// task 117). It is the undo for DELETE /v1/tasks/{id}.
//
// It runs in the daemon, not the client, and that is decision 1: inserting
// rows means opening SQLite, and only the daemon does (§4). `daemon restore`
// is client-side only because it opens nothing.
//
// The order is what makes it atomic. The manifest is read first, so a schema
// this binary cannot migrate is refused before a multi-gigabyte extraction.
// One pass stages the database and the task's transcripts under the data dir —
// the same volume, so the final rename is atomic. The staged database is
// opened (and so migrated) as a store of its own, never the live file. Every
// refusal is then checked before anything is placed; the transcript directory
// is renamed in inside the import transaction and removed again if it does not
// commit. Staging is removed on every exit path.
func (s *Server) handleTaskImport(w http.ResponseWriter, r *http.Request) {
	var req taskImportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.deps.Store == nil {
		s.internalError(w, "task import", errors.New("no store is configured"))
		return
	}
	if s.deps.Dirs.Data == "" {
		s.internalError(w, "task import", errors.New("no data directory is configured"))
		return
	}
	archive, ok := importArchive(w, req.Path)
	if !ok {
		return
	}
	if req.TaskID <= 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "task_id must be a positive task id")
		return
	}
	if req.ProjectID < 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id must be a project id")
		return
	}

	manifest, err := backup.ReadManifest(archive)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("%s is not a vincent backup: %v", archive, err))
		return
	}
	if ceiling := store.NewestMigration(); manifest.SchemaVersion > ceiling {
		writeErrorDetails(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("%s holds schema version %d and this vincent embeds %d — "+
				"import it with the version that wrote it (%s)",
				archive, manifest.SchemaVersion, ceiling, manifest.VincentVersion),
			map[string]string{"reason": importReasonSchemaTooNew})
		return
	}

	staging, err := os.MkdirTemp(s.deps.Dirs.Data, importStagingPrefix)
	if err != nil {
		s.internalError(w, "task import", err)
		return
	}
	defer func() { _ = os.RemoveAll(staging) }()

	stage, err := backup.ExtractTask(archive, staging, req.TaskID)
	if err != nil {
		if archiveFault(err) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
			return
		}
		s.internalError(w, "task import", err)
		return
	}
	// Opening migrates an older schema forward — on the staged copy. This
	// handle belongs to the daemon and points at a different file, so the
	// one-writer invariant on the live store is untouched. Closed before the
	// staging directory is removed (defers run last-first), which Windows
	// requires.
	src, err := store.Open(stage.Database)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("the database in %s cannot be opened: %v", archive, err))
		return
	}
	defer func() { _ = src.Close() }()

	exp, err := src.ExportTask(r.Context(), req.TaskID)
	if errors.Is(err, store.ErrNotFound) {
		writeErrorDetails(w, http.StatusNotFound, CodeNotFound,
			fmt.Sprintf("task %d is not in %s", req.TaskID, archive),
			map[string]string{"reason": importReasonTaskNotInBackup})
		return
	}
	if err != nil {
		s.internalError(w, "task import", err)
		return
	}

	idText := strconv.FormatInt(req.TaskID, 10)
	liveDir := filepath.Join(s.deps.Dirs.Data, backup.TranscriptsPrefix, idText)
	for _, run := range exp.StepRuns {
		if p := run.String("transcript_path"); p != "" {
			run.Set("transcript_path", rewriteTranscriptPath(p, s.deps.Dirs.Data, req.TaskID))
		}
	}

	res, err := s.deps.Store.ImportTask(r.Context(), exp, store.ImportOptions{
		ProjectID: req.ProjectID,
		Place: func() (func(), error) {
			return placeTranscripts(stage.Transcripts, liveDir, req.TaskID)
		},
	})
	if !s.writeImportError(w, req, err) {
		return
	}
	writeJSON(w, http.StatusOK, taskImportResponse{
		TaskID:              res.TaskID,
		ProjectID:           res.ProjectID,
		Title:               res.Title,
		StepRuns:            res.StepRuns,
		StepRunsRenumbered:  res.StepRunsRenumbered,
		TranscriptFiles:     stage.TranscriptFiles,
		TranscriptBytes:     stage.TranscriptBytes,
		ArchivedAt:          backup.FormatTime(res.ArchivedAt),
		BackupSchemaVersion: manifest.SchemaVersion,
		BackupCreatedAt:     manifest.CreatedAt,
	})
}

// importArchive validates the archive path, answering the client itself when
// it is unusable. Every rejection is a 400: the request named a file the
// daemon will not read.
func importArchive(w http.ResponseWriter, raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "path is required")
		return "", false
	}
	if !filepath.IsAbs(raw) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"path must be absolute: the daemon resolves it against its own working directory, not yours")
		return "", false
	}
	archive := filepath.Clean(raw)
	fi, err := os.Stat(archive)
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("%s is not an existing regular file", archive))
		return "", false
	}
	return archive, true
}

// archiveFault reports whether an extraction error is the archive's — a file
// that is not gzip, not tar, truncated, unsafe, or missing its database —
// rather than the daemon's own disk.
func archiveFault(err error) bool {
	for _, target := range []error{
		backup.ErrUnsafeEntry, backup.ErrNoDatabase,
		gzip.ErrHeader, gzip.ErrChecksum, tar.ErrHeader, io.ErrUnexpectedEOF,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// placeTranscripts moves the staged transcript directory into place. A live
// directory already there is a refusal, whether or not the archive carries
// transcripts: it is a stray (`vincent gc` reports those), and §18 neither
// deletes nor merges one. The undo removes only what this rename put there.
func placeTranscripts(staged, liveDir string, taskID int64) (func(), error) {
	switch _, err := os.Lstat(liveDir); {
	case err == nil:
		return nil, &store.ImportRefusedError{
			ID: taskID, Reason: store.ImportRefusedTranscriptsPresent,
			Message: fmt.Sprintf("%s already exists; move it aside before importing task %d", liveDir, taskID),
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("stat %s: %w", liveDir, err)
	}
	if staged == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(liveDir), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(liveDir), err)
	}
	if err := os.Rename(staged, liveDir); err != nil {
		return nil, fmt.Errorf("place transcripts at %s: %w", liveDir, err)
	}
	return func() { _ = os.RemoveAll(liveDir) }, nil
}

// rewriteTranscriptPath points a backed-up `transcript_path` at this
// installation. The column is an absolute path into the *source* data dir,
// read as-is by the transcript route, so it is re-rooted at
// `{data_dir}/transcripts/{id}/` by finding that segment — with either
// separator, since the backup may come from Windows. A path with no such
// segment, or one whose remainder would climb out of the task's directory, is
// left as it was.
func rewriteTranscriptPath(p, dataDir string, taskID int64) string {
	id := strconv.FormatInt(taskID, 10)
	norm := "/" + strings.ReplaceAll(p, `\`, "/")
	seg := "/" + backup.TranscriptsPrefix + "/" + id + "/"
	i := strings.LastIndex(norm, seg)
	if i < 0 {
		return p
	}
	rest := norm[i+len(seg):]
	dir := filepath.Join(dataDir, backup.TranscriptsPrefix, id)
	out := filepath.Join(dir, filepath.FromSlash(rest))
	if rest == "" || out == dir || !pathUnder(dir, out) {
		return p
	}
	return out
}

// writeImportError maps an ImportTask failure onto §13.1 and reports whether
// the handler may carry on. A nil error is the only "carry on".
func (s *Server) writeImportError(w http.ResponseWriter, req taskImportRequest, err error) bool {
	if err == nil {
		return true
	}
	var refused *store.ImportRefusedError
	switch {
	case errors.As(err, &refused):
		details := map[string]string{"action": "import", "reason": refused.Reason}
		if refused.State != "" {
			details["state"] = refused.State
		}
		writeConflict(w, refused.Message, details)
	case errors.Is(err, store.ErrNotFound):
		writeErrorDetails(w, http.StatusNotFound, CodeNotFound,
			fmt.Sprintf("project %d not found", req.ProjectID),
			map[string]string{"reason": importReasonProjectNotFound})
	default:
		s.internalError(w, "task import", err)
	}
	return false
}

// writeErrorDetails is writeError with structured details, for the refusals a
// client branches on that are not 409s.
func writeErrorDetails(w http.ResponseWriter, status int, code, message string, details map[string]string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message, Details: details}})
}
