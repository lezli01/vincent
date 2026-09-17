package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrNoDatabase means the archive carries a manifest but no DatabaseEntry: a
// vincent backup with nothing to import a task from.
var ErrNoDatabase = errors.New("archive has no " + DatabaseEntry)

// TaskStage is what ExtractTask left in the staging directory.
type TaskStage struct {
	// Database is the staged copy of the archive's vincent.db.
	Database string
	// Transcripts is the staged `transcripts/{id}` directory, or "" when the
	// archive carried none for this task — a task whose transcripts were
	// pruned before the backup was taken still has rows worth importing.
	Transcripts string
	// TranscriptFiles and TranscriptBytes count the files staged under it.
	TranscriptFiles int
	TranscriptBytes int64
}

// ExtractTask stages the database and one task's transcript subtree out of
// archive into staging, in a single pass (task 117).
//
// It is Restore narrowed to one task, and it keeps every one of Restore's
// entry checks (030 decision 9): each entry — including the ones it then
// skips — goes through target, so an archive that Restore would refuse is
// refused here too. Nothing but the manifest's position is guaranteed about
// entry order, so the pass depends on none: the database may come before or
// after the transcripts.
//
// `transcripts/{id}0/` is not `transcripts/{id}/`: the match is on the whole
// path element, never a string prefix.
//
// staging must exist and be empty. What to do with the staged files is the
// caller's business; this package opens no database.
func ExtractTask(archive, staging string, taskID int64) (TaskStage, error) {
	var stage TaskStage
	// G304: the archive the API caller named, which the daemon has already
	// checked is an absolute path to a regular file (§16: the trust boundary
	// is the OS user).
	f, err := os.Open(archive) //nolint:gosec // G304: see above
	if err != nil {
		return stage, fmt.Errorf("open %s: %w", archive, err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return stage, fmt.Errorf("read %s: %w", archive, err)
	}
	defer func() { _ = zr.Close() }()

	// Config entries are validated against a root that is never written:
	// they are skipped, but an unsafe one must still be refused.
	dirs := Dirs{Data: staging, Config: filepath.Join(staging, ConfigPrefix)}
	taskPrefix := path.Join(TranscriptsPrefix, strconv.FormatInt(taskID, 10))
	sawDatabase := false

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stage, fmt.Errorf("read %s: %w", archive, err)
		}
		if path.Clean(hdr.Name) == ManifestEntry {
			continue
		}
		dest, isDir, err := target(hdr, dirs)
		if err != nil {
			return stage, err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		switch {
		case name == DatabaseEntry:
			if isDir {
				return stage, fmt.Errorf("%w: %s is a directory", ErrUnsafeEntry, hdr.Name)
			}
			if _, err := extractFile(dest, tr); err != nil {
				return stage, err
			}
			stage.Database = dest
			sawDatabase = true
		case name == taskPrefix || strings.HasPrefix(name, taskPrefix+"/"):
			stage.Transcripts = filepath.Join(staging, filepath.FromSlash(taskPrefix))
			if isDir {
				if err := os.MkdirAll(dest, dirMode); err != nil {
					return stage, fmt.Errorf("create %s: %w", dest, err)
				}
				continue
			}
			n, err := extractFile(dest, tr)
			if err != nil {
				return stage, err
			}
			stage.TranscriptFiles++
			stage.TranscriptBytes += n
		}
	}
	if !sawDatabase {
		return stage, fmt.Errorf("%s: %w", archive, ErrNoDatabase)
	}
	return stage, nil
}
