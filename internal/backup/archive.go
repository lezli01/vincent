package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"
)

// Archive entry names. These are the wire format between a backup written by
// one vincent and a restore run by another, so they are constants rather than
// literals spelled twice.
const (
	// ManifestEntry is written **first**, so a restore can read it after
	// decompressing one block rather than streaming a multi-gigabyte archive
	// to reach it. Nothing else depends on entry order.
	ManifestEntry = "manifest.json"
	// DatabaseEntry is the store.BackupTo output, not a copy of the live file.
	DatabaseEntry = "vincent.db"
	// TranscriptsPrefix and ConfigPrefix are the two directory trees.
	TranscriptsPrefix = "transcripts"
	ConfigPrefix      = "config"
)

// configFile is the one file taken from the config directory besides the
// workflows tree.
const configFile = "config.yaml"

// workflowsDir is the global workflow scope (§5.2), under both ConfigPrefix in
// the archive and the config directory on disk.
const workflowsDir = "workflows"

// timeFormat matches internal/store's: RFC3339 UTC with fixed-width
// nanoseconds. A manifest timestamp therefore compares and sorts exactly like
// one read out of the database it describes.
const timeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// entryMode / dirMode are what restore recreates. vincent stores no
// executables and no group-readable state, and a mode carried across from a
// POSIX host would mean nothing on Windows anyway — so the archive declares
// one owner-only mode rather than preserving whatever the source had.
const (
	entryMode = 0o600
	dirMode   = 0o700
)

// ErrDestinationExists is returned when the archive path is already taken.
// Overwriting is never right here: the file it would destroy is, by
// construction, somebody's backup.
var ErrDestinationExists = errors.New("destination already exists")

// Manifest is the archive's first entry: enough to decide whether this binary
// may restore it, without opening the database inside.
type Manifest struct {
	VincentVersion string `json:"vincent_version"`
	// SchemaVersion is the `schema_migrations` high-water mark of the
	// database in the archive. Migrations are up-only and append-only, so a
	// value above what the restoring binary embeds cannot be stepped back.
	SchemaVersion int `json:"schema_version"`
	// CreatedAt is RFC3339 UTC in store's fixed-width nanosecond format.
	CreatedAt string `json:"created_at"`
}

// FormatTime renders t the way the manifest and the API response both spell a
// timestamp, so the two can never disagree about one backup's creation time.
func FormatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

// Source is what Create reads.
type Source struct {
	// Database is the file to store as DatabaseEntry — a consistent copy
	// (store.BackupTo), never `{data_dir}/vincent.db` itself.
	Database string
	// DataDir supplies transcripts/. Its other contents are excluded (see the
	// package doc).
	DataDir string
	// ConfigDir supplies config.yaml and workflows/.
	ConfigDir string
	// Manifest is written verbatim as ManifestEntry.
	Manifest Manifest
}

// Result reports what Create wrote. The three sizes are separate because a
// vincent installation is megabytes of database beside gigabytes of
// transcripts, and a user looking at a surprising total wants to know which.
type Result struct {
	Path            string
	Bytes           int64
	DatabaseBytes   int64
	TranscriptBytes int64
}

// Create writes the archive at dst. dst must not exist.
//
// A failure part-way through removes the partial file: an archive that cannot
// be restored is worse than no archive, because it is the one a user reaches
// for on the day they need it.
func Create(dst string, src Source) (Result, error) {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, entryMode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Result{}, fmt.Errorf("%s: %w", dst, ErrDestinationExists)
		}
		return Result{}, fmt.Errorf("create %s: %w", dst, err)
	}
	res, err := writeArchive(f, src)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dst)
		return Result{}, err
	}
	info, err := os.Stat(dst)
	if err != nil {
		return Result{}, fmt.Errorf("stat %s: %w", dst, err)
	}
	res.Path = dst
	res.Bytes = info.Size()
	return res, nil
}

func writeArchive(w io.Writer, src Source) (Result, error) {
	var res Result
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)

	if err := writeManifest(tw, src.Manifest); err != nil {
		return res, err
	}
	dbBytes, err := writeRegular(tw, DatabaseEntry, src.Database)
	if err != nil {
		return res, err
	}
	res.DatabaseBytes = dbBytes

	if src.DataDir != "" {
		n, err := writeTree(tw, filepath.Join(src.DataDir, TranscriptsPrefix), TranscriptsPrefix)
		if err != nil {
			return res, err
		}
		res.TranscriptBytes = n
	}
	if src.ConfigDir != "" {
		// A config directory whose defaults have never been written is an
		// ordinary state, not a failure: the daemon rewrites them at start.
		_, err := writeRegular(tw, path.Join(ConfigPrefix, configFile), filepath.Join(src.ConfigDir, configFile))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, err
		}
		if _, err := writeTree(tw, filepath.Join(src.ConfigDir, workflowsDir), path.Join(ConfigPrefix, workflowsDir)); err != nil {
			return res, err
		}
	}

	if err := tw.Close(); err != nil {
		return res, fmt.Errorf("finish archive: %w", err)
	}
	if err := zw.Close(); err != nil {
		return res, fmt.Errorf("finish archive: %w", err)
	}
	return res, nil
}

func writeManifest(tw *tar.Writer, m Manifest) error {
	body, err := marshalManifest(m)
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     ManifestEntry,
		Size:     int64(len(body)),
		Mode:     entryMode,
		ModTime:  time.Now().UTC().Truncate(time.Second),
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s: %w", ManifestEntry, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("write %s: %w", ManifestEntry, err)
	}
	return nil
}

// writeRegular copies one file in under the given archive name. The
// fs.ErrNotExist from a missing source is returned unwrapped enough for
// errors.Is, so a caller can decide whether absence is a failure.
func writeRegular(tw *tar.Writer, name, src string) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", src, err)
	}
	if err := writeEntry(tw, name, info, f); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// writeTree adds root's contents under prefix, returning the bytes of file
// payload written. A missing root contributes nothing.
func writeTree(tw *tar.Writer, root, prefix string) (int64, error) {
	var bytes int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// The transcript pruner and a project delete both remove
			// directories under a data root while this walk is running
			// (§17, §10). A path that vanished mid-walk is not in the
			// backup and is not a failure either.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("walk %s: %w", p, err)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", p, err)
		}
		name := prefix
		if rel != "." {
			name = path.Join(prefix, filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("stat %s: %w", p, err)
		}
		if d.IsDir() {
			return writeDir(tw, name, info)
		}
		if !d.Type().IsRegular() {
			// vincent writes only directories and regular files under its
			// data roots; anything else was put there by something else and
			// is not restorable state.
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("open %s: %w", p, err)
		}
		defer func() { _ = f.Close() }()
		if err := writeEntry(tw, name, info, f); err != nil {
			return err
		}
		bytes += info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return bytes, nil
}

func writeDir(tw *tar.Writer, name string, info fs.FileInfo) error {
	hdr := &tar.Header{
		Typeflag: tar.TypeDir,
		Name:     name + "/",
		Mode:     dirMode,
		ModTime:  info.ModTime().UTC().Truncate(time.Second),
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func writeEntry(tw *tar.Writer, name string, info fs.FileInfo, r io.Reader) error {
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Size:     info.Size(),
		Mode:     entryMode,
		ModTime:  info.ModTime().UTC().Truncate(time.Second),
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := copyExactly(tw, r, info.Size()); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// copyExactly writes exactly size bytes from r, padding a short read.
//
// This is the live-data case, and it is not hypothetical: a transcript is
// being appended to while the archive is written, so the file is already
// longer than the size the header declared. archive/tar refuses both ends of
// that — ErrWriteTooLong on an overrun, a short-write error at the next
// header on an underrun — and the declared length is the only number that is
// true at both. The tail written after the stat is simply not in this backup.
//
// A short read means the file shrank instead (truncated, rotated, replaced).
// Padding keeps the archive well-formed; failing the whole backup because one
// transcript moved under the walk would not.
func copyExactly(w io.Writer, r io.Reader, size int64) error {
	n, err := io.CopyN(w, r, size)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n == size {
		return nil
	}
	if _, err := io.CopyN(w, zeroReader{}, size-n); err != nil {
		return err
	}
	return nil
}

// zeroReader is an endless run of NUL bytes, for copyExactly's padding.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
