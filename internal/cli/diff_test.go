package cli

import (
	"bytes"
	"strings"
	"testing"
)

// The colour renderer, forced on. Each case is one diff and the exact bytes it
// must produce; the cases are the lines a prefix-only classifier gets wrong.
func TestDiffPainter(t *testing.T) {
	bold := func(s string) string { return sgrBold + s + sgrReset + "\n" }
	cyan := func(s string) string { return sgrCyan + s + sgrReset + "\n" }
	green := func(s string) string { return sgrGreen + s + sgrReset + "\n" }
	red := func(s string) string { return sgrRed + s + sgrReset + "\n" }
	plain := func(s string) string { return s + "\n" }

	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "a modified file",
			in: []string{
				"diff --git a/x.go b/x.go", "index 1..2 100644", "--- a/x.go", "+++ b/x.go",
				"@@ -1,2 +1,2 @@", " keep", "-old", "+new",
			},
			want: []string{
				bold("diff --git a/x.go b/x.go"), bold("index 1..2 100644"), bold("--- a/x.go"),
				bold("+++ b/x.go"), cyan("@@ -1,2 +1,2 @@"), plain(" keep"), red("-old"), green("+new"),
			},
		},
		{
			// Inside a hunk `---` and `+++` are a removed `--` line and an
			// added `++` line, not file markers.
			name: "content that looks like a marker",
			in: []string{
				"diff --git a/q.sql b/q.sql", "--- a/q.sql", "+++ b/q.sql",
				"@@ -1,2 +1,2 @@", "--- a comment", "+++ counter", `\ No newline at end of file`,
			},
			want: []string{
				bold("diff --git a/q.sql b/q.sql"), bold("--- a/q.sql"), bold("+++ b/q.sql"),
				cyan("@@ -1,2 +1,2 @@"), red("--- a comment"), green("+++ counter"),
				plain(`\ No newline at end of file`),
			},
		},
		{
			// The next file's header ends the hunk before it.
			name: "a second file after a hunk",
			in: []string{
				"diff --git a/a b/a", "@@ -1 +1 @@", "-a", "diff --git a/run.sh b/run.sh",
				"old mode 100644", "new mode 100755", "diff --git a/b.png b/b.png",
				"Binary files a/b.png and b/b.png differ",
			},
			want: []string{
				bold("diff --git a/a b/a"), cyan("@@ -1 +1 @@"), red("-a"), bold("diff --git a/run.sh b/run.sh"),
				bold("old mode 100644"), bold("new mode 100755"), bold("diff --git a/b.png b/b.png"),
				bold("Binary files a/b.png and b/b.png differ"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := diffPainter{w: &buf}
			if err := eachDiffLine(strings.NewReader(strings.Join(tc.in, "\n")+"\n"), p.line); err != nil {
				t.Fatal(err)
			}
			if want := strings.Join(tc.want, ""); buf.String() != want {
				t.Errorf("painted =\n%q\nwant\n%q", buf.String(), want)
			}
		})
	}
}

// A buffer is never a terminal, so no command writing into one colours —
// which is what makes every command test a test of the piped bytes.
func TestDiffColorIsOffWhenNotATerminal(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	if _, color := diffColor(&bytes.Buffer{}); color {
		t.Error("diffColor coloured a non-terminal writer")
	}
}

func TestDiffStatScanner(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []fileStat
	}{
		{
			name: "counts inside hunks only",
			in: []string{
				"diff --git a/q.sql b/q.sql", "index 1..2 100644", "--- a/q.sql", "+++ b/q.sql",
				"@@ -1,3 +1,3 @@", " keep", "--- a comment", "+++ counter", "+new",
				"@@ -9 +9 @@", "-x",
			},
			want: []fileStat{{Path: "q.sql", Added: 2, Removed: 2}},
		},
		{
			name: "binary, new and deleted files",
			in: []string{
				"diff --git a/b.bin b/b.bin", "new file mode 100644", "index 0..1",
				"Binary files /dev/null and b/b.bin differ",
				"diff --git a/gone.txt b/gone.txt", "deleted file mode 100644", "index 1..0",
				"--- a/gone.txt", "+++ /dev/null", "@@ -1 +0,0 @@", "-bye",
				"diff --git a/new.txt b/new.txt", "new file mode 100644", "--- /dev/null",
				"+++ b/new.txt", "@@ -0,0 +1 @@", "+hi",
			},
			want: []fileStat{
				{Path: "b.bin", Binary: true},
				{Path: "gone.txt", Removed: 1},
				{Path: "new.txt", Added: 1},
			},
		},
		{
			name: "a rename, with and without edits",
			in: []string{
				"diff --git a/old.txt b/new.txt", "similarity index 100%", "rename from old.txt",
				"rename to new.txt",
				"diff --git a/a b/dir/b", "similarity index 90%", "rename from a", "rename to dir/b",
				"--- a/a", "+++ b/dir/b", "@@ -1 +1 @@", "-1", "+one",
			},
			want: []fileStat{{Path: "new.txt"}, {Path: "dir/b", Added: 1, Removed: 1}},
		},
		{
			name: "a mode change and a name with a space",
			in: []string{
				"diff --git a/run.sh b/run.sh", "old mode 100644", "new mode 100755",
				"diff --git a/my file.txt b/my file.txt", "--- a/my file.txt\t", "+++ b/my file.txt\t",
				"@@ -1 +1 @@", "-a", "+b",
			},
			want: []fileStat{{Path: "run.sh"}, {Path: "my file.txt", Added: 1, Removed: 1}},
		},
		{
			// The `--by lane` remainder joins the parent's commits to its
			// uncommitted work, so one file can arrive as two entries.
			name: "a path twice is one row",
			in: []string{
				"diff --git a/own.txt b/own.txt", "--- /dev/null", "+++ b/own.txt", "@@ -0,0 +1 @@", "+own",
				"diff --git a/x b/x", "--- a/x", "+++ b/x", "@@ -1 +1 @@", "-a", "+b",
				"diff --git a/own.txt b/own.txt", "--- a/own.txt", "+++ b/own.txt", "@@ -1 +1,2 @@",
				" own", "+more",
			},
			want: []fileStat{{Path: "own.txt", Added: 2}, {Path: "x", Added: 1, Removed: 1}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sc diffStatScanner
			if err := eachDiffLine(strings.NewReader(strings.Join(tc.in, "\n")+"\n"), sc.line); err != nil {
				t.Fatal(err)
			}
			got := sc.finish()
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("row %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}

	var sc diffStatScanner
	if got := sc.finish(); got == nil || len(got) != 0 {
		t.Errorf("an empty diff = %#v, want an empty non-nil slice", got)
	}
}

// A line longer than bufio.Scanner's 64 KiB token limit is one line, whole.
func TestEachDiffLineReadsLongLines(t *testing.T) {
	long := "+" + strings.Repeat("x", 200_000)
	var got []string
	in := "diff --git a/m.js b/m.js\n@@ -0,0 +1 @@\n" + long + "\n"
	if err := eachDiffLine(strings.NewReader(in), func(l string) { got = append(got, l) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2] != long {
		t.Fatalf("got %d lines; the long line did not survive whole", len(got))
	}
}
