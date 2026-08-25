package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// seed lays out a data dir and a config dir the way a real installation looks,
// plus a standalone file standing in for the store.BackupTo output.
func seed(t *testing.T) (dbCopy string, dirs Dirs) {
	t.Helper()
	staging, dataDir, cfgDir := t.TempDir(), t.TempDir(), t.TempDir()

	dbCopy = filepath.Join(staging, DatabaseEntry)
	write(t, dbCopy, "SQLite format 3\x00pretend-database")

	write(t, filepath.Join(dataDir, TranscriptsPrefix, "1", "0-1.jsonl"), `{"type":"start"}`+"\n")
	write(t, filepath.Join(dataDir, TranscriptsPrefix, "12", "3-2.jsonl"), `{"type":"result"}`+"\n")
	write(t, filepath.Join(cfgDir, configFile), "listen: \"127.0.0.1:0\"\n")
	write(t, filepath.Join(cfgDir, workflowsDir, "review.yaml"), "name: review\nsteps: []\n")

	// Excluded by construction — the assertions below prove they stay out.
	write(t, filepath.Join(dataDir, "token"), "secret")
	write(t, filepath.Join(dataDir, "daemon.json"), "{}")
	write(t, filepath.Join(dataDir, "tui.json"), "{}")
	write(t, filepath.Join(dataDir, "logs", "daemon.log"), "log line")
	write(t, filepath.Join(dataDir, "worktrees", "1", "README.md"), "# work")
	write(t, filepath.Join(dataDir, DatabaseEntry+"-wal"), "wal")

	return dbCopy, Dirs{Data: dataDir, Config: cfgDir}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func create(t *testing.T, dst, dbCopy string, dirs Dirs) Result {
	t.Helper()
	res, err := Create(dst, Source{
		Database:  dbCopy,
		DataDir:   dirs.Data,
		ConfigDir: dirs.Config,
		Manifest: Manifest{
			VincentVersion: "v9.9.9",
			SchemaVersion:  17,
			CreatedAt:      FormatTime(time.Now()),
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return res
}

// entries lists every archive entry name, in order.
func entries(t *testing.T, archive string) []string {
	t.Helper()
	var names []string
	eachEntry(t, archive, func(hdr *tar.Header, _ io.Reader) {
		names = append(names, hdr.Name)
	})
	return names
}

func eachEntry(t *testing.T, archive string, fn func(*tar.Header, io.Reader)) {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open %s: %v", archive, err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip %s: %v", archive, err)
	}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("read %s: %v", archive, err)
		}
		fn(hdr, tr)
	}
}

// TestCreateLayout pins the archive's contents: the four things that are in
// it, the running-identity files that are not, and forward-slashed names on
// every platform — an archive taken on Windows has to restore on Linux.
func TestCreateLayout(t *testing.T) {
	dbCopy, dirs := seed(t)
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")
	res := create(t, dst, dbCopy, dirs)

	names := entries(t, dst)
	if len(names) == 0 || names[0] != ManifestEntry {
		// Restore reads the manifest by decompressing one block; that is only
		// cheap while it is first.
		t.Fatalf("first entry = %q, want %s", names, ManifestEntry)
	}
	for _, want := range []string{
		DatabaseEntry,
		"transcripts/1/0-1.jsonl",
		"transcripts/12/3-2.jsonl",
		"config/config.yaml",
		"config/workflows/review.yaml",
	} {
		if !slices.Contains(names, want) {
			t.Errorf("archive is missing %q; has %v", want, names)
		}
	}
	for _, name := range names {
		if strings.ContainsRune(name, '\\') {
			t.Errorf("entry %q is not forward-slashed", name)
		}
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			t.Errorf("entry %q is not relative", name)
		}
		for _, excluded := range []string{
			"token", "daemon.json", "daemon.lock", "tui.json", "logs/", "worktrees/",
			DatabaseEntry + "-wal", DatabaseEntry + "-shm",
		} {
			if name == excluded || strings.HasPrefix(name, excluded) {
				t.Errorf("archive carries excluded entry %q", name)
			}
		}
	}

	if res.DatabaseBytes == 0 || res.TranscriptBytes == 0 || res.Bytes == 0 {
		t.Errorf("Result = %+v, want non-zero sizes", res)
	}
	if res.Path != dst {
		t.Errorf("Result.Path = %q, want %q", res.Path, dst)
	}
}

func TestCreateRefusesAnExistingDestination(t *testing.T) {
	dbCopy, dirs := seed(t)
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")
	write(t, dst, "somebody else's backup")

	_, err := Create(dst, Source{Database: dbCopy, DataDir: dirs.Data, ConfigDir: dirs.Config})
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("Create over an existing file = %v, want ErrDestinationExists", err)
	}
	body, err := os.ReadFile(dst)
	if err != nil || string(body) != "somebody else's backup" {
		t.Fatalf("the existing file was touched: %q, %v", body, err)
	}
}

func TestCreateRemovesAPartialArchive(t *testing.T) {
	dirs := Dirs{Data: t.TempDir(), Config: t.TempDir()}
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")

	// A database copy that is not there fails the archive part-way, after the
	// manifest entry is already written.
	_, err := Create(dst, Source{
		Database: filepath.Join(t.TempDir(), "missing.db"), DataDir: dirs.Data, ConfigDir: dirs.Config,
	})
	if err == nil {
		t.Fatal("Create with no database = nil, want an error")
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a partial archive was left at %s (%v)", dst, statErr)
	}
}

func TestCreateToleratesAMissingConfigFile(t *testing.T) {
	dbCopy, dirs := seed(t)
	if err := os.Remove(filepath.Join(dirs.Config, configFile)); err != nil {
		t.Fatalf("remove config.yaml: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dirs.Config, workflowsDir)); err != nil {
		t.Fatalf("remove workflows: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")
	names := entries(t, func() string { create(t, dst, dbCopy, dirs); return dst }())
	if slices.Contains(names, "config/config.yaml") {
		t.Errorf("archive carries a config.yaml that does not exist: %v", names)
	}
	if !slices.Contains(names, DatabaseEntry) {
		t.Errorf("archive lost the database: %v", names)
	}
}

// TestCopyExactlyHonorsTheDeclaredSize is the growing-file defect in
// isolation. archive/tar rejects both an overrun and an underrun, and a
// transcript is appended to while the walk runs, so neither branch is
// hypothetical.
func TestCopyExactlyHonorsTheDeclaredSize(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		size int64
	}{
		{"file grew after the stat", "0123456789abcdef", 10},
		{"file shrank after the stat", "0123", 10},
		{"exact", "0123456789", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := copyExactly(&buf, strings.NewReader(tc.body), tc.size); err != nil {
				t.Fatalf("copyExactly: %v", err)
			}
			if int64(buf.Len()) != tc.size {
				t.Fatalf("wrote %d bytes, want exactly %d", buf.Len(), tc.size)
			}
		})
	}
}

// TestCreateWhileTranscriptsAreAppended is the same defect end to end: the
// only place it shows up is a busy machine, so the test makes one. The
// assertion is that the archive is well-formed — every entry's declared size
// matches its payload, which is what tar's own reader checks.
func TestCreateWhileTranscriptsAreAppended(t *testing.T) {
	dbCopy, dirs := seed(t)
	transcripts := filepath.Join(dirs.Data, TranscriptsPrefix)

	var files []string
	for i := range 40 {
		p := filepath.Join(transcripts, "9", string(rune('a'+i%26))+string(rune('a'+i/26))+".jsonl")
		write(t, p, strings.Repeat(`{"type":"output"}`+"\n", 64))
		files = append(files, p)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		line := []byte(strings.Repeat(`{"type":"output","text":"appended"}`+"\n", 16))
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, p := range files {
				f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					return
				}
				_, _ = f.Write(line)
				_ = f.Close()
			}
		}
	}()

	dst := filepath.Join(t.TempDir(), "backup.tar.gz")
	res := create(t, dst, dbCopy, dirs)
	close(stop)
	wg.Wait()

	// tar's reader validates every declared size against the bytes that
	// follow it, so draining the archive is the well-formedness assertion.
	var seen int
	eachEntry(t, dst, func(hdr *tar.Header, r io.Reader) {
		n, err := io.Copy(io.Discard, r)
		if err != nil {
			t.Fatalf("entry %s: %v", hdr.Name, err)
		}
		if hdr.Typeflag == tar.TypeReg && n != hdr.Size {
			t.Fatalf("entry %s declared %d bytes and carries %d", hdr.Name, hdr.Size, n)
		}
		seen++
	})
	if seen < len(files) {
		t.Errorf("archive has %d entries, want at least the %d transcripts", seen, len(files))
	}
	if res.TranscriptBytes == 0 {
		t.Error("Result.TranscriptBytes = 0")
	}
}

func TestRoundTripIntoCleanDirectories(t *testing.T) {
	dbCopy, dirs := seed(t)
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")
	create(t, dst, dbCopy, dirs)

	into := Dirs{Data: t.TempDir(), Config: t.TempDir()}
	rep, err := Restore(dst, into, false)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rep.Manifest.SchemaVersion != 17 || rep.Manifest.VincentVersion != "v9.9.9" {
		t.Errorf("manifest = %+v, want the values Create was given", rep.Manifest)
	}
	if len(rep.Displaced) != 0 {
		t.Errorf("Displaced = %v on a clean restore", rep.Displaced)
	}

	for _, pair := range [][2]string{
		{dbCopy, filepath.Join(into.Data, DatabaseEntry)},
		{
			filepath.Join(dirs.Data, TranscriptsPrefix, "1", "0-1.jsonl"),
			filepath.Join(into.Data, TranscriptsPrefix, "1", "0-1.jsonl"),
		},
		{filepath.Join(dirs.Config, configFile), filepath.Join(into.Config, configFile)},
		{
			filepath.Join(dirs.Config, workflowsDir, "review.yaml"),
			filepath.Join(into.Config, workflowsDir, "review.yaml"),
		},
	} {
		want, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatalf("read source %s: %v", pair[0], err)
		}
		got, err := os.ReadFile(pair[1])
		if err != nil {
			t.Fatalf("read restored %s: %v", pair[1], err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s restored as %q, want %q", pair[1], got, want)
		}
	}
	// The excluded files stay excluded on the way back too.
	for _, gone := range []string{"token", "daemon.json", "tui.json", DatabaseEntry + "-wal"} {
		if _, err := os.Stat(filepath.Join(into.Data, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("restore wrote %s, which is not in a backup", gone)
		}
	}
}

func TestRestoreRefusesOccupiedState(t *testing.T) {
	dbCopy, dirs := seed(t)
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")
	create(t, dst, dbCopy, dirs)

	for _, occupant := range []string{
		DatabaseEntry,
		// A leftover write-ahead log is as dangerous as the database itself:
		// restoring a fresh vincent.db beside a stale -wal is how a good
		// backup becomes a corrupt database at first open.
		DatabaseEntry + "-wal",
		DatabaseEntry + "-shm",
	} {
		t.Run(occupant, func(t *testing.T) {
			into := Dirs{Data: t.TempDir(), Config: t.TempDir()}
			write(t, filepath.Join(into.Data, occupant), "existing")
			if _, err := Restore(dst, into, false); !errors.Is(err, ErrOccupied) {
				t.Fatalf("Restore over %s = %v, want ErrOccupied", occupant, err)
			}
			if _, err := os.Stat(filepath.Join(into.Data, TranscriptsPrefix)); !errors.Is(err, os.ErrNotExist) {
				t.Error("a refused restore still wrote transcripts")
			}
		})
	}

	t.Run("config.yaml", func(t *testing.T) {
		into := Dirs{Data: t.TempDir(), Config: t.TempDir()}
		write(t, filepath.Join(into.Config, configFile), "mine")
		if _, err := Restore(dst, into, false); !errors.Is(err, ErrOccupied) {
			t.Fatalf("Restore over config.yaml = %v, want ErrOccupied", err)
		}
	})
}

// TestRestoreForceMovesAsideAndDeletesNothing is the §18 posture applied to
// the one command whose job is to overwrite. The assertion is on the *bytes*
// at the displaced path, not on a name existing: a rename that clobbered the
// old file would satisfy the weaker check.
func TestRestoreForceMovesAsideAndDeletesNothing(t *testing.T) {
	dbCopy, dirs := seed(t)
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")
	create(t, dst, dbCopy, dirs)

	into := Dirs{Data: t.TempDir(), Config: t.TempDir()}
	write(t, filepath.Join(into.Data, DatabaseEntry), "the database that was here")
	write(t, filepath.Join(into.Data, DatabaseEntry+"-wal"), "the wal that was here")
	write(t, filepath.Join(into.Data, TranscriptsPrefix, "77", "0-1.jsonl"), "an older transcript")
	write(t, filepath.Join(into.Config, configFile), "the config that was here")
	write(t, filepath.Join(into.Config, workflowsDir, "mine.yaml"), "name: mine")

	rep, err := Restore(dst, into, true)
	if err != nil {
		t.Fatalf("Restore --force: %v", err)
	}
	if len(rep.Displaced) != 5 {
		t.Fatalf("Displaced = %+v, want all five occupied paths", rep.Displaced)
	}
	want := map[string]string{
		filepath.Join(into.Data, DatabaseEntry):                        "the database that was here",
		filepath.Join(into.Data, DatabaseEntry+"-wal"):                 "the wal that was here",
		filepath.Join(into.Config, configFile):                         "the config that was here",
		filepath.Join(into.Data, TranscriptsPrefix, "77", "0-1.jsonl"): "an older transcript",
		filepath.Join(into.Config, workflowsDir, "mine.yaml"):          "name: mine",
	}
	for _, d := range rep.Displaced {
		if !strings.Contains(d.To, ".bak-") {
			t.Errorf("displaced %s to %s, want a .bak- name", d.From, d.To)
		}
		// A displaced directory keeps its contents; a displaced file keeps
		// its bytes. Either way the old state is readable.
		probe := d.To
		if rel, err := filepath.Rel(d.From, findKey(want, d.From)); err == nil && rel != "." {
			probe = filepath.Join(d.To, rel)
		}
		body, err := os.ReadFile(probe)
		if err != nil {
			t.Errorf("displaced state at %s is unreadable: %v", probe, err)
			continue
		}
		if got := string(body); got != want[findKey(want, d.From)] {
			t.Errorf("displaced %s reads %q, want the pre-restore bytes", probe, got)
		}
	}
	// And the restore itself landed.
	body, err := os.ReadFile(filepath.Join(into.Data, DatabaseEntry))
	if err != nil || !strings.Contains(string(body), "pretend-database") {
		t.Fatalf("restored database = %q, %v", body, err)
	}
}

// findKey returns the want-map key that from covers: the path itself for a
// displaced file, or the file inside it for a displaced directory.
func findKey(want map[string]string, from string) string {
	for k := range want {
		if k == from || strings.HasPrefix(k, from+string(filepath.Separator)) {
			return k
		}
	}
	return from
}

func TestMoveAsideNeverOverwritesAnEarlierBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "vincent.db")
	write(t, p, "first")
	first, err := moveAside(p, "20260825T120000Z")
	if err != nil {
		t.Fatalf("moveAside: %v", err)
	}
	write(t, p, "second")
	second, err := moveAside(p, "20260825T120000Z")
	if err != nil {
		t.Fatalf("moveAside again: %v", err)
	}
	if first == second {
		t.Fatalf("two displacements in one second collided on %s", first)
	}
	for path, want := range map[string]string{first: "first", second: "second"} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Errorf("%s = %q (%v), want %q", path, body, err, want)
		}
	}
}

// tarball builds an archive by hand, so a restore can be shown what a hostile
// or corrupted file looks like. Create can never emit these.
func tarball(t *testing.T, headers []*tar.Header, bodies []string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for i, hdr := range headers {
		body := ""
		if i < len(bodies) {
			body = bodies[i]
		}
		hdr.Size = int64(len(body))
		if hdr.Typeflag == tar.TypeDir || hdr.Typeflag == tar.TypeSymlink {
			hdr.Size = 0
			body = ""
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", hdr.Name, err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatalf("write body %s: %v", hdr.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	p := filepath.Join(t.TempDir(), "hostile.tar.gz")
	write(t, p, buf.String())
	return p
}

func manifestHeader() *tar.Header {
	return &tar.Header{Typeflag: tar.TypeReg, Name: ManifestEntry, Mode: entryMode, Format: tar.FormatPAX}
}

// TestRestoreRejectsUnsafeEntries: an archive is untrusted input even when
// this package wrote it. Nothing may land outside the two destinations.
func TestRestoreRejectsUnsafeEntries(t *testing.T) {
	manifest := `{"vincent_version":"v9.9.9","schema_version":1,"created_at":"x"}`
	for _, tc := range []struct {
		name string
		hdr  *tar.Header
	}{
		{"parent escape", &tar.Header{Typeflag: tar.TypeReg, Name: "../evil", Format: tar.FormatPAX}},
		{"nested escape", &tar.Header{Typeflag: tar.TypeReg, Name: "transcripts/../../evil", Format: tar.FormatPAX}},
		{"absolute", &tar.Header{Typeflag: tar.TypeReg, Name: "/etc/evil", Format: tar.FormatPAX}},
		{"backslash", &tar.Header{Typeflag: tar.TypeReg, Name: `transcripts\..\..\evil`, Format: tar.FormatPAX}},
		{"symlink", &tar.Header{
			Typeflag: tar.TypeSymlink, Name: "transcripts/link", Linkname: "/etc/passwd", Format: tar.FormatPAX,
		}},
		{"unknown entry", &tar.Header{Typeflag: tar.TypeReg, Name: "worktrees/1/file", Format: tar.FormatPAX}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := tarball(t, []*tar.Header{manifestHeader(), tc.hdr}, []string{manifest, "pwned"})
			outside := t.TempDir()
			into := Dirs{Data: filepath.Join(outside, "data"), Config: filepath.Join(outside, "config")}

			_, err := Restore(archive, into, false)
			if !errors.Is(err, ErrUnsafeEntry) {
				t.Fatalf("Restore(%s) = %v, want ErrUnsafeEntry", tc.name, err)
			}
			var strays []string
			_ = filepath.WalkDir(outside, func(p string, _ os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if strings.Contains(filepath.Base(p), "evil") || strings.Contains(filepath.Base(p), "link") {
					strays = append(strays, p)
				}
				return nil
			})
			if len(strays) > 0 {
				t.Errorf("a rejected entry still wrote %v", strays)
			}
		})
	}
}

func TestReadManifestRejectsWhatIsNotABackup(t *testing.T) {
	t.Run("not gzip", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "plain.tar.gz")
		write(t, p, "this is not a gzip stream")
		if _, err := ReadManifest(p); err == nil {
			t.Fatal("ReadManifest on a plain file = nil, want an error")
		}
	})
	t.Run("no manifest", func(t *testing.T) {
		archive := tarball(t,
			[]*tar.Header{{Typeflag: tar.TypeReg, Name: DatabaseEntry, Format: tar.FormatPAX}},
			[]string{"db"})
		if _, err := ReadManifest(archive); !errors.Is(err, ErrNoManifest) {
			t.Fatalf("ReadManifest without a manifest = %v, want ErrNoManifest", err)
		}
	})
}

func TestFormatTimeMatchesTheStoreFormat(t *testing.T) {
	// Fixed-width nanoseconds, UTC: a manifest timestamp has to compare and
	// sort the same way as one read out of the database it describes (§14).
	got := FormatTime(time.Date(2026, 8, 25, 12, 0, 0, 0, time.FixedZone("x", 3600)))
	if got != "2026-08-25T11:00:00.000000000Z" {
		t.Fatalf("FormatTime = %q", got)
	}
}
