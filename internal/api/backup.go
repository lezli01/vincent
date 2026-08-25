package api

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/version"
)

// backupRequest is the body of POST /v1/daemon/backup. One field: where to
// write. The client resolves it to an absolute path before sending, because
// the daemon's working directory is not the caller's and a relative path
// would land somewhere neither of them chose.
type backupRequest struct {
	Path string `json:"path"`
}

// backupResponse reports what was written. The three sizes are separate
// because a vincent installation is a database of megabytes beside
// transcripts of gigabytes (§12.3's per-run cap alone is 512 MB), and a user
// surprised by the total wants to know which half it came from.
type backupResponse struct {
	Path            string `json:"path"`
	Bytes           int64  `json:"bytes"`
	DatabaseBytes   int64  `json:"database_bytes"`
	TranscriptBytes int64  `json:"transcript_bytes"`
	SchemaVersion   int    `json:"schema_version"`
	CreatedAt       string `json:"created_at"`
}

// handleBackup serves POST /v1/daemon/backup (task 029).
//
// The daemon assembles the whole archive — the database copy *and* the two
// directory trees — rather than copying the database and letting the client
// tar the rest. That keeps exactly one process walking daemon-owned state, and
// leaves the staging copy with an unambiguous owner. It is also why backup
// needs a running daemon at all: only the daemon opens SQLite (§4), so only
// the daemon can take a consistent copy while work is in flight.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	var req backupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.deps.Store == nil {
		s.internalError(w, "backup", errors.New("no store is configured"))
		return
	}
	if s.deps.Dirs.Data == "" || s.deps.Dirs.Config == "" {
		s.internalError(w, "backup", errors.New("no data directory is configured"))
		return
	}
	dst, ok := s.backupDestination(w, req.Path)
	if !ok {
		return
	}

	schemaVersion, err := s.deps.Store.SchemaVersion(r.Context())
	if err != nil {
		s.internalError(w, "backup", err)
		return
	}

	// VACUUM INTO refuses an existing path, so the copy is staged under a
	// name nothing else can hold — a fresh directory beside the destination,
	// which also means one RemoveAll cleans up on every exit path.
	staging, err := os.MkdirTemp(filepath.Dir(dst), ".vincent-backup-")
	if err != nil {
		s.internalError(w, "backup", err)
		return
	}
	defer func() { _ = os.RemoveAll(staging) }()

	dbCopy := filepath.Join(staging, backup.DatabaseEntry)
	if err := s.deps.Store.BackupTo(r.Context(), dbCopy); err != nil {
		s.internalError(w, "backup", err)
		return
	}

	created := backup.FormatTime(time.Now())
	res, err := backup.Create(dst, backup.Source{
		Database:  dbCopy,
		DataDir:   s.deps.Dirs.Data,
		ConfigDir: s.deps.Dirs.Config,
		Manifest: backup.Manifest{
			VincentVersion: version.Version(),
			SchemaVersion:  schemaVersion,
			CreatedAt:      created,
		},
	})
	if err != nil {
		if errors.Is(err, backup.ErrDestinationExists) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
			return
		}
		s.internalError(w, "backup", err)
		return
	}

	writeJSON(w, http.StatusOK, backupResponse{
		Path:            res.Path,
		Bytes:           res.Bytes,
		DatabaseBytes:   res.DatabaseBytes,
		TranscriptBytes: res.TranscriptBytes,
		SchemaVersion:   schemaVersion,
		CreatedAt:       created,
	})
}

// backupDestination validates the requested path, answering the client itself
// when it is unusable. Every rejection here is a 400: the request named a
// place the daemon will not write, which is the caller's to fix.
func (s *Server) backupDestination(w http.ResponseWriter, raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "path is required")
		return "", false
	}
	if !filepath.IsAbs(raw) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"path must be absolute: the daemon resolves it against its own working directory, not yours")
		return "", false
	}
	dst := filepath.Clean(raw)

	// A destination inside the transcript tree would be walked by the archive
	// it is being written into — the file feeding itself.
	transcripts := filepath.Join(s.deps.Dirs.Data, backup.TranscriptsPrefix)
	if pathUnder(transcripts, dst) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("path is inside %s, which the backup itself reads", transcripts))
		return "", false
	}

	switch _, err := os.Lstat(dst); {
	case err == nil:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("%s: %s — vincent never overwrites a backup", dst, backup.ErrDestinationExists))
		return "", false
	case !errors.Is(err, fs.ErrNotExist):
		s.internalError(w, "backup", err)
		return "", false
	}
	if fi, err := os.Stat(filepath.Dir(dst)); err != nil || !fi.IsDir() {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("%s is not an existing directory", filepath.Dir(dst)))
		return "", false
	}
	return dst, true
}

// pathUnder reports whether p sits at or under root.
func pathUnder(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
