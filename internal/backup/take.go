package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// StagingPrefix names the scratch directory Take creates beside the archive.
// It is exported because the scheduled timer sweeps leftovers carrying it
// (task 115): a daemon killed mid-run leaves one behind.
const StagingPrefix = ".vincent-backup-"

// Snapshot is what Take reads: Source, except that the database arrives as a
// function that writes a consistent copy rather than as a finished file.
type Snapshot struct {
	// CopyDatabase writes a consistent copy of the database to dst, which
	// does not exist yet — store.BackupTo in both real callers. It is a
	// function so this package stays a leaf and never imports store.
	CopyDatabase func(ctx context.Context, dst string) error
	DataDir      string
	ConfigDir    string
	Manifest     Manifest
	// Atomic writes the archive inside the staging directory and renames it
	// to dst only once it is complete, so no truncated file ever carries
	// dst's name. The timer needs that — its archive names are what it counts
	// and keeps (task 115 decision 3) — while a manual backup keeps writing
	// dst directly, with O_EXCL claiming the name before minutes of work.
	Atomic bool
}

// Take stages a database copy beside dst, then writes the archive at dst. It
// is the sequence `POST /v1/daemon/backup` and the scheduled timer share.
func Take(ctx context.Context, dst string, s Snapshot) (Result, error) {
	// VACUUM INTO refuses an existing path, so the copy is staged under a
	// name nothing else can hold — a fresh directory beside the destination,
	// which also means one RemoveAll cleans up on every exit path.
	staging, err := os.MkdirTemp(filepath.Dir(dst), StagingPrefix)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	dbCopy := filepath.Join(staging, DatabaseEntry)
	if err := s.CopyDatabase(ctx, dbCopy); err != nil {
		return Result{}, err
	}
	src := Source{
		Database:  dbCopy,
		DataDir:   s.DataDir,
		ConfigDir: s.ConfigDir,
		Manifest:  s.Manifest,
	}
	if !s.Atomic {
		return Create(dst, src)
	}

	tmp := filepath.Join(staging, filepath.Base(dst))
	res, err := Create(tmp, src)
	if err != nil {
		return Result{}, err
	}
	// Rename replaces an existing file on every platform, so the refusal to
	// overwrite a backup has to be asked for here. Create has already closed
	// its handle, which Windows needs before it will move the file.
	switch _, err := os.Lstat(dst); {
	case err == nil:
		return Result{}, fmt.Errorf("%s: %w", dst, ErrDestinationExists)
	case !errors.Is(err, fs.ErrNotExist):
		return Result{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return Result{}, fmt.Errorf("move archive into place: %w", err)
	}
	res.Path = dst
	return res, nil
}
