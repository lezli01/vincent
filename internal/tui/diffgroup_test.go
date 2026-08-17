package tui

import (
	"strings"
	"testing"
)

// A diff with one of everything git writes differently: a plain edit, a name
// with a space in it (git appends a tab to the ± markers and leaves the
// `diff --git` line ambiguous), a new file, a deletion, a rename, and a binary
// file it describes instead of diffing.
const shapesDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,4 +1,4 @@
 package main
-func old() {}
+func new() {}
 // trailing context
diff --git a/foo bar.txt b/foo bar.txt
index 422c2b7..0f7bc76 100644
--- a/foo bar.txt
+++ b/foo bar.txt
@@ -1,2 +1,2 @@
 a
-b
+c
diff --git a/added.go b/added.go
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/added.go
@@ -0,0 +1,2 @@
+package added
+// two lines
diff --git a/gone.go b/gone.go
deleted file mode 100644
index 4444444..0000000
--- a/gone.go
+++ /dev/null
@@ -1,1 +0,0 @@
-package gone
diff --git a/old.txt b/new.txt
similarity index 100%
rename from old.txt
rename to new.txt
diff --git a/bin.dat b/bin.dat
index 366fd40..f68ed80 100644
Binary files a/bin.dat and b/bin.dat differ
`

// TestParseDiffFilesReadsPathsAndCounts is the whole contract of the grouping:
// one section per file, named the way a reader names it, with the ± counts the
// folded header has to answer with.
func TestParseDiffFilesReadsPathsAndCounts(t *testing.T) {
	lead, files := parseDiffFiles(splitDiff(shapesDiff))
	if len(lead) != 0 {
		t.Errorf("git diff wrote no preamble, parse found %q", lead)
	}
	want := []struct {
		path           string
		added, removed int
		binary         bool
	}{
		{path: "main.go", added: 1, removed: 1},
		// The space is why the ± markers are authoritative: `a/foo bar.txt
		// b/foo bar.txt` cannot be split on a delimiter a path may contain.
		{path: "foo bar.txt", added: 1, removed: 1},
		{path: "added.go", added: 2},
		// A deletion's `+++` is /dev/null, so the old side is the only name.
		{path: "gone.go", removed: 1},
		{path: "old.txt → new.txt"},
		{path: "bin.dat", binary: true},
	}
	if len(files) != len(want) {
		var got []string
		for _, f := range files {
			got = append(got, f.path)
		}
		t.Fatalf("parsed %d files (%v), want %d", len(files), got, len(want))
	}
	for i, w := range want {
		f := files[i]
		if f.path != w.path {
			t.Errorf("file %d: path = %q, want %q", i, f.path, w.path)
		}
		if f.added != w.added || f.removed != w.removed {
			t.Errorf("file %d (%s): +%d -%d, want +%d -%d", i, f.path, f.added, f.removed, w.added, w.removed)
		}
		if f.binary != w.binary {
			t.Errorf("file %d (%s): binary = %v, want %v", i, f.path, f.binary, w.binary)
		}
	}
}

// TestDiffFileMarkersAreNotCountedAsChanges is the mistake a naive prefix
// count makes: `+++ b/x` and `--- a/x` start with the same characters content
// does, and counting them turns every file into one added and one removed line
// that is not there.
func TestDiffFileMarkersAreNotCountedAsChanges(t *testing.T) {
	_, files := parseDiffFiles(splitDiff(`diff --git a/only.go b/only.go
index 1111111..2222222 100644
--- a/only.go
+++ b/only.go
@@ -1,2 +1,2 @@
 context
`))
	if len(files) != 1 {
		t.Fatalf("parsed %d files, want 1", len(files))
	}
	if files[0].added != 0 || files[0].removed != 0 {
		t.Errorf("a file with no ± content lines counted +%d -%d", files[0].added, files[0].removed)
	}
}

// TestDiffFileBodyDropsOnlyRepetition: the header row names the file, so the
// four lines that repeat it are dropped — and everything that states a fact of
// its own stays, because a body is the last place those facts can be read.
func TestDiffFileBodyDropsOnlyRepetition(t *testing.T) {
	_, files := parseDiffFiles(splitDiff(shapesDiff))
	byPath := map[string]diffFile{}
	for _, f := range files {
		byPath[f.path] = f
	}
	for _, tc := range []struct {
		path string
		gone []string
		kept []string
	}{
		{
			path: "main.go",
			gone: []string{"diff --git", "index 1111111", "--- a/main.go", "+++ b/main.go"},
			kept: []string{"@@ -1,4 +1,4 @@", "-func old() {}", "+func new() {}"},
		},
		{path: "added.go", gone: []string{"+++ b/added.go"}, kept: []string{"new file mode 100644"}},
		{path: "gone.go", gone: []string{"--- a/gone.go"}, kept: []string{"deleted file mode 100644"}},
		{path: "old.txt → new.txt", kept: []string{"rename from old.txt", "rename to new.txt"}},
		{path: "bin.dat", kept: []string{"Binary files a/bin.dat and b/bin.dat differ"}},
	} {
		body := strings.Join(byPath[tc.path].body, "\n")
		for _, g := range tc.gone {
			if strings.Contains(body, g) {
				t.Errorf("%s: body repeats %q, which the header row already says:\n%s", tc.path, g, body)
			}
		}
		for _, k := range tc.kept {
			if !strings.Contains(body, k) {
				t.Errorf("%s: body lost %q:\n%s", tc.path, k, body)
			}
		}
	}
}

// TestParseDiffFilesKeepsWhatPrecedesTheFirstFile: a pane that swallowed
// anything ahead of the first marker would be lying about the change, and a
// patch with no marker at all still has to render.
func TestParseDiffFilesKeepsWhatPrecedesTheFirstFile(t *testing.T) {
	lead, files := parseDiffFiles(splitDiff("a preamble line\ndiff --git a/x b/x\n@@ -0,0 +1 @@\n+x\n"))
	if len(lead) != 1 || lead[0] != "a preamble line" {
		t.Errorf("lead = %q, want the preamble line", lead)
	}
	if len(files) != 1 || files[0].path != "x" {
		t.Errorf("files = %+v, want the one file after it", files)
	}

	lead, files = parseDiffFiles(splitDiff("@@ -1 +1 @@\n-a\n+b\n"))
	if len(files) != 0 || len(lead) != 3 {
		t.Errorf("a diff with no file marker: %d files, %d lead lines; want 0 and 3", len(files), len(lead))
	}
}
