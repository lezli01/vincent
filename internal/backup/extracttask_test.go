package backup

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func regular(name string) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: entryMode, Format: tar.FormatPAX}
}

func directory(name string) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: 0o700, Format: tar.FormatPAX}
}

// stagedFiles lists every regular file under root, slash-separated and sorted.
func stagedFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	slices.Sort(out)
	return out
}

// TestExtractTaskStagesExactlyOneTask: the database and task 2's subtree, and
// nothing of task 20's — a prefix of "2" but a different task — or of the
// config half. The entries arrive in both orders, because only the manifest's
// position is guaranteed.
func TestExtractTaskStagesExactlyOneTask(t *testing.T) {
	manifest := `{"vincent_version":"v9.9.9","schema_version":1,"created_at":"x"}`
	type entry struct {
		hdr  *tar.Header
		body string
	}
	rest := []entry{
		{directory("transcripts/"), ""},
		{directory("transcripts/2/"), ""},
		{regular("transcripts/2/0-1.jsonl"), "first attempt"},
		{regular("transcripts/2/1-2.jsonl"), "second"},
		{regular("transcripts/20/0-1.jsonl"), "another task"},
		{regular("transcripts/2.jsonl"), "not a directory of task 2"},
		{regular(DatabaseEntry), "SQLite format 3"},
		{regular("config/config.yaml"), "listen: x"},
	}
	for _, order := range []string{"database last", "database first"} {
		t.Run(order, func(t *testing.T) {
			entries := slices.Clone(rest)
			if order == "database first" {
				slices.Reverse(entries)
			}
			headers := []*tar.Header{manifestHeader()}
			bodies := []string{manifest}
			for _, e := range entries {
				h := *e.hdr
				headers = append(headers, &h)
				bodies = append(bodies, e.body)
			}
			archive := tarball(t, headers, bodies)
			staging := t.TempDir()

			stage, err := ExtractTask(archive, staging, 2)
			if err != nil {
				t.Fatalf("ExtractTask: %v", err)
			}
			want := []string{"transcripts/2/0-1.jsonl", "transcripts/2/1-2.jsonl", DatabaseEntry}
			slices.Sort(want)
			if got := stagedFiles(t, staging); !slices.Equal(got, want) {
				t.Errorf("staged %v, want %v", got, want)
			}
			if stage.Database != filepath.Join(staging, DatabaseEntry) {
				t.Errorf("Database = %q", stage.Database)
			}
			if stage.Transcripts != filepath.Join(staging, "transcripts", "2") {
				t.Errorf("Transcripts = %q", stage.Transcripts)
			}
			if stage.TranscriptFiles != 2 || stage.TranscriptBytes != int64(len("first attempt")+len("second")) {
				t.Errorf("counted %d file(s), %d byte(s)", stage.TranscriptFiles, stage.TranscriptBytes)
			}
		})
	}
}

func TestExtractTaskWithoutTranscriptsOrDatabase(t *testing.T) {
	manifest := `{"vincent_version":"v9.9.9","schema_version":1,"created_at":"x"}`
	t.Run("pruned transcripts", func(t *testing.T) {
		archive := tarball(t,
			[]*tar.Header{manifestHeader(), regular(DatabaseEntry), regular("transcripts/3/0-1.jsonl")},
			[]string{manifest, "db", "other task"})
		stage, err := ExtractTask(archive, t.TempDir(), 2)
		if err != nil {
			t.Fatalf("ExtractTask: %v", err)
		}
		if stage.Transcripts != "" || stage.TranscriptFiles != 0 {
			t.Errorf("stage = %+v, want no transcripts", stage)
		}
	})
	t.Run("no database", func(t *testing.T) {
		archive := tarball(t,
			[]*tar.Header{manifestHeader(), regular("transcripts/2/0-1.jsonl")},
			[]string{manifest, "x"})
		if _, err := ExtractTask(archive, t.TempDir(), 2); !errors.Is(err, ErrNoDatabase) {
			t.Fatalf("ExtractTask = %v, want ErrNoDatabase", err)
		}
	})
}

// TestExtractTaskRejectsUnsafeEntries is 030's hostile-archive set, applied to
// the narrowed extraction: an entry it would skip is still checked.
func TestExtractTaskRejectsUnsafeEntries(t *testing.T) {
	manifest := `{"vincent_version":"v9.9.9","schema_version":1,"created_at":"x"}`
	for _, tc := range []struct {
		name string
		hdr  *tar.Header
	}{
		{"parent escape", regular("../evil")},
		{"nested escape", regular("transcripts/2/../../evil")},
		{"absolute", regular("/etc/evil")},
		{"backslash", regular(`transcripts\2\..\..\evil`)},
		{"symlink", &tar.Header{
			Typeflag: tar.TypeSymlink, Name: "transcripts/2/link", Linkname: "/etc/passwd", Format: tar.FormatPAX,
		}},
		{"unknown entry", regular("worktrees/2/file")},
		{"unsafe entry of another task", regular("transcripts/9/../../../evil")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := tarball(t,
				[]*tar.Header{manifestHeader(), regular(DatabaseEntry), tc.hdr},
				[]string{manifest, "db", "pwned"})
			outside := t.TempDir()
			staging := filepath.Join(outside, "staging")
			if err := os.Mkdir(staging, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := ExtractTask(archive, staging, 2); !errors.Is(err, ErrUnsafeEntry) {
				t.Fatalf("ExtractTask(%s) = %v, want ErrUnsafeEntry", tc.name, err)
			}
			_ = filepath.WalkDir(outside, func(p string, _ os.DirEntry, err error) error {
				if err == nil && (strings.Contains(filepath.Base(p), "evil") || filepath.Base(p) == "link") {
					t.Errorf("a rejected entry still wrote %s", p)
				}
				return nil
			})
		})
	}
}
