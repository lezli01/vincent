package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// manifestMaxBytes bounds the manifest read. It is three short fields; an
// entry claiming megabytes is a malformed or hostile archive, not a manifest.
const manifestMaxBytes = 1 << 20

var (
	// ErrNoManifest means the file is not a vincent backup: it decompressed
	// and parsed as a tar, but carries no manifest.
	ErrNoManifest = errors.New("archive has no " + ManifestEntry)
	// ErrOccupied means the destination already holds state a restore would
	// overwrite. The caller decides whether --force applies.
	ErrOccupied = errors.New("destination already holds vincent state")
	// ErrUnsafeEntry means an entry names a path outside the destination, or
	// something that is not a plain file or directory. An archive is
	// untrusted input: it may have been edited, corrupted, or handed over.
	ErrUnsafeEntry = errors.New("archive entry is not safe to extract")
)

// Dirs are the two directories a restore writes into (§12.2).
type Dirs struct {
	Data   string
	Config string
}

// Report is what a restore did.
type Report struct {
	Manifest Manifest
	Files    int
	Bytes    int64
	// Displaced are the paths --force moved aside, newest name first written.
	// Nothing is ever deleted, so this list is the record of where the
	// previous installation's state went (§18).
	Displaced []Displaced
}

// Displaced is one path a --force restore renamed rather than overwrote.
type Displaced struct {
	From string
	To   string
}

// ReadManifest reads just the manifest out of an archive. Create writes it
// first, so this decompresses one block rather than the whole file — which is
// what makes a schema check affordable before a multi-gigabyte extraction.
func ReadManifest(archive string) (Manifest, error) {
	f, err := os.Open(archive)
	if err != nil {
		return Manifest{}, fmt.Errorf("open %s: %w", archive, err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", archive, err)
	}
	defer func() { _ = zr.Close() }()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return Manifest{}, fmt.Errorf("%s: %w", archive, ErrNoManifest)
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("read %s: %w", archive, err)
		}
		if path.Clean(hdr.Name) != ManifestEntry {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, manifestMaxBytes))
		if err != nil {
			return Manifest{}, fmt.Errorf("read %s from %s: %w", ManifestEntry, archive, err)
		}
		var m Manifest
		if err := json.Unmarshal(body, &m); err != nil {
			return Manifest{}, fmt.Errorf("parse %s from %s: %w", ManifestEntry, archive, err)
		}
		return m, nil
	}
}

func marshalManifest(m Manifest) ([]byte, error) {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", ManifestEntry, err)
	}
	return append(body, '\n'), nil
}

// Occupied lists the paths under dirs that a restore would overwrite, in the
// order a report should name them. An empty result means a clean restore.
//
// The write-ahead log and shared-memory files are in the set with the database
// they belong to, and that is not tidiness: leaving a `vincent.db-wal` from
// the old installation beside a freshly restored `vincent.db` is how a good
// backup turns into a corrupt database at first open.
func Occupied(dirs Dirs) []string {
	var candidates []string
	if dirs.Data != "" {
		candidates = append(candidates,
			filepath.Join(dirs.Data, DatabaseEntry),
			filepath.Join(dirs.Data, DatabaseEntry+"-wal"),
			filepath.Join(dirs.Data, DatabaseEntry+"-shm"),
			filepath.Join(dirs.Data, TranscriptsPrefix))
	}
	if dirs.Config != "" {
		candidates = append(candidates,
			filepath.Join(dirs.Config, configFile),
			filepath.Join(dirs.Config, workflowsDir))
	}
	var out []string
	for _, p := range candidates {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// Restore unpacks archive into dirs.
//
// It refuses when the destination already holds vincent state, unless force,
// in which case each occupied path is **moved aside** as `<name>.bak-<ts>`
// and nothing is deleted — §18's posture, applied to the one command whose
// whole job is to overwrite. Whether the daemon is down is the caller's
// precondition to check: this package opens no lock file and no database.
func Restore(archive string, dirs Dirs, force bool) (Report, error) {
	var rep Report
	m, err := ReadManifest(archive)
	if err != nil {
		return rep, err
	}
	rep.Manifest = m

	if occupied := Occupied(dirs); len(occupied) > 0 {
		if !force {
			return rep, fmt.Errorf("%w: %s", ErrOccupied, strings.Join(occupied, ", "))
		}
		stamp := time.Now().UTC().Format("20060102T150405Z")
		for _, p := range occupied {
			to, err := moveAside(p, stamp)
			if err != nil {
				return rep, err
			}
			rep.Displaced = append(rep.Displaced, Displaced{From: p, To: to})
		}
	}

	f, err := os.Open(archive)
	if err != nil {
		return rep, fmt.Errorf("open %s: %w", archive, err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return rep, fmt.Errorf("read %s: %w", archive, err)
	}
	defer func() { _ = zr.Close() }()

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return rep, fmt.Errorf("read %s: %w", archive, err)
		}
		name := path.Clean(hdr.Name)
		if name == ManifestEntry {
			continue
		}
		dest, isDir, err := target(hdr, dirs)
		if err != nil {
			return rep, err
		}
		if isDir {
			if err := os.MkdirAll(dest, dirMode); err != nil {
				return rep, fmt.Errorf("create %s: %w", dest, err)
			}
			continue
		}
		n, err := extractFile(dest, tr)
		if err != nil {
			return rep, err
		}
		rep.Files++
		rep.Bytes += n
	}
	return rep, nil
}

// target maps an archive entry onto a destination path, refusing anything
// that would escape the two roots or that is not a plain file or directory.
//
// The check is done twice on purpose: once on the entry name, which catches a
// hand-crafted `../evil`, and once on the joined result, which catches
// whatever a platform's path rules make of a name the first pass thought was
// fine.
func target(hdr *tar.Header, dirs Dirs) (dest string, isDir bool, err error) {
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeDir:
	default:
		// Symlinks, hard links and device nodes: vincent writes none of
		// them, and a link is exactly how an archive reaches outside the
		// directory it was told to write to.
		return "", false, fmt.Errorf("%w: %s (type %q)", ErrUnsafeEntry, hdr.Name, hdr.Typeflag)
	}
	name := hdr.Name
	if strings.ContainsRune(name, '\\') {
		// tar names are forward-slashed by definition; a backslash is a
		// Windows separator smuggled into a name, where filepath.Join on
		// that platform would treat it as one.
		return "", false, fmt.Errorf("%w: %s", ErrUnsafeEntry, hdr.Name)
	}
	name = strings.TrimSuffix(name, "/")
	if name == "" || path.IsAbs(name) || path.Clean(name) != name {
		return "", false, fmt.Errorf("%w: %s", ErrUnsafeEntry, hdr.Name)
	}
	for _, elem := range strings.Split(name, "/") {
		if elem == ".." || elem == "." || elem == "" {
			return "", false, fmt.Errorf("%w: %s", ErrUnsafeEntry, hdr.Name)
		}
	}

	var root, rel string
	switch {
	case name == DatabaseEntry:
		root, rel = dirs.Data, DatabaseEntry
	case name == TranscriptsPrefix || strings.HasPrefix(name, TranscriptsPrefix+"/"):
		root, rel = dirs.Data, name
	case name == ConfigPrefix:
		// The container itself; the config directory already exists.
		root, rel = dirs.Config, "."
	case strings.HasPrefix(name, ConfigPrefix+"/"):
		root, rel = dirs.Config, strings.TrimPrefix(name, ConfigPrefix+"/")
	default:
		// Not a layout this binary knows. Refused rather than skipped: a
		// newer vincent that adds an entry produces an archive this one
		// cannot restore faithfully, and saying so beats restoring most of
		// it in silence.
		return "", false, fmt.Errorf("%w: unexpected entry %s", ErrUnsafeEntry, hdr.Name)
	}
	dest = filepath.Join(root, filepath.FromSlash(rel))
	if !within(root, dest) {
		return "", false, fmt.Errorf("%w: %s", ErrUnsafeEntry, hdr.Name)
	}
	return dest, hdr.Typeflag == tar.TypeDir, nil
}

// within reports whether p is root itself or sits under it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func extractFile(dest string, r io.Reader) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), dirMode); err != nil {
		return 0, fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, entryMode)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", dest, err)
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return 0, fmt.Errorf("write %s: %w", dest, copyErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("write %s: %w", dest, closeErr)
	}
	return n, nil
}

// moveAside renames p to `<p>.bak-<stamp>`, adding a counter when that name is
// taken. os.Rename onto an existing path replaces it, and replacing a
// displaced file would delete state — which is the one thing restore promises
// never to do — so two restores in the same second must not collide.
func moveAside(p, stamp string) (string, error) {
	base := p + ".bak-" + stamp
	candidate := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			break
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	if err := os.Rename(p, candidate); err != nil {
		return "", fmt.Errorf("move %s aside: %w", p, err)
	}
	return candidate, nil
}
