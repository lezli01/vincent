package tui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/apiclient"
)

// editRecords serves the recorded claude edits through the real transcript
// handler, so the delta and the patch the pane renders are what the daemon
// sends and what apiclient decodes (task 110).
func editRecords(t *testing.T) []apiclient.TranscriptRecord {
	t.Helper()
	return fixtureRecords(t, claude.New(func() string { return "" }),
		filepath.Join("..", "agent", "claude", "testdata", "stream_edit_2.1.268.jsonl"))
}

// TestEditDeltaAndPatchByLevel: an edit's outcome is its `+N −M` delta
// wherever outcomes render, and its hunks appear at verbose only.
func TestEditDeltaAndPatchByLevel(t *testing.T) {
	records := editRecords(t)
	var patches int
	for _, rec := range records {
		if rec.Type == "agent.patch" {
			patches++
			if rec.Patch == "" || rec.CallID == "" {
				t.Errorf("patch record decoded without its body or call: %+v", rec)
			}
		}
	}
	if patches != 4 {
		t.Fatalf("agent.patch records = %d, want 4", patches)
	}

	d := newTestDetail(t)
	d.width = 80
	d.records = records

	d.level.set(levelQuiet)
	if got := strings.Join(plainLines(d.outputLines()), "\n"); strings.Contains(got, "+2 −1") || strings.Contains(got, "@@") {
		t.Errorf("quiet rendered an edit's outcome or patch:\n%s", got)
	}
	for _, level := range []outputLevel{levelCompact, levelNormal, levelVerbose} {
		d.level.set(level)
		got := plainLines(d.outputLines())
		for _, want := range []string{"    ✓ +2 −1", "    ✓ +12 −6", "    ✓ +2 −2", "    ✓ updated · +1 −1"} {
			if !slices.Contains(got, want) {
				t.Errorf("%s is missing the outcome line %q:\n%s", level, want, strings.Join(got, "\n"))
			}
		}
		hasPatch := slices.Contains(got, "  @@ -590,7 +590,8 @@")
		if hasPatch != (level == levelVerbose) {
			t.Errorf("%s patch shown = %v, want it at verbose only:\n%s", level, hasPatch, strings.Join(got, "\n"))
		}
	}

	d.level.set(levelVerbose)
	got := strings.Join(plainLines(d.outputLines()), "\n")
	for _, want := range []string{
		"    ✓ +2 −1\n  @@ -590,7 +590,8 @@\n",
		"\n  -// fsnotify watcher's later fire re-reads identical bytes and is a no-op.\n",
		"\n  +// fsnotify watcher reads the file under the applier's lock, so its fire\n",
		"\n  @@ -338,11 +343,12 @@\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose is missing %q:\n%s", want, got)
		}
	}
}

// TestCompactDoesNotGrowForEdits is task 066 decision 1 held for task 110: the
// delta replaces the outcome's prose rather than adding a line, and the patch
// adds none below verbose.
func TestCompactDoesNotGrowForEdits(t *testing.T) {
	records := editRecords(t)
	var withoutPatches []apiclient.TranscriptRecord
	for _, rec := range records {
		if rec.Type != "agent.patch" {
			withoutPatches = append(withoutPatches, rec)
		}
	}
	for _, level := range []outputLevel{levelQuiet, levelCompact, levelNormal} {
		with := outputLines(records, level, 80, lineOpts{expandKey: "v"})
		without := outputLines(withoutPatches, level, 80, lineOpts{expandKey: "v"})
		if len(with) != len(without) {
			t.Errorf("%s = %d lines with patches, %d without", level, len(with), len(without))
		}
	}
	// Every delta is one row at compact; the prose it replaced wrapped at 80.
	compact := plainLines(outputLines(records, levelCompact, 80, lineOpts{expandKey: "v"}))
	for i, line := range compact {
		if strings.HasPrefix(line, "    ✓ +") && i+1 < len(compact) && strings.HasPrefix(compact[i+1], "     ") {
			t.Errorf("delta %q wrapped onto a second row", line)
		}
	}
}

// TestNestedPatchNeverRenders: a subagent's record renders one level quieter
// than the main loop's, and agent.patch is verbose-only, so a nested one has
// no level to render at (task 109 decision 2).
func TestNestedPatchNeverRenders(t *testing.T) {
	records := []apiclient.TranscriptRecord{{
		Type: "agent.patch", ParentCallID: "toolu_parent", CallID: "toolu_1",
		Patch: "@@ -1,1 +1,1 @@\n-old\n+new",
	}}
	for _, level := range []outputLevel{levelQuiet, levelCompact, levelNormal, levelVerbose} {
		if got := plainLines(outputLines(records, level, 80, lineOpts{expandKey: "v"})); slices.ContainsFunc(got, func(l string) bool {
			return strings.Contains(l, "@@") || strings.Contains(l, "+new")
		}) {
			t.Errorf("%s rendered a nested patch: %q", level, got)
		}
	}
}

// TestPatchStylingAndTruncation: additions and removals take the Diff tab's
// colors, a long line wraps at the pane's width without losing a character,
// and a cut patch says so.
func TestPatchStylingAndTruncation(t *testing.T) {
	long := "+" + strings.Repeat("abcdefghij", 20)
	records := []apiclient.TranscriptRecord{{
		Type: "agent.patch", CallID: "toolu_1", Truncated: true,
		Patch: "@@ -1,2 +1,2 @@\n-    old line\n" + long + "\n     context",
	}}
	styled := outputLines(records, levelVerbose, 80, lineOpts{expandKey: "v"})
	plain := plainLines(styled)

	for i, line := range plain {
		if n := cols(line); n > 80 {
			t.Errorf("line %d is %d cells, wider than the pane: %q", i, n, line)
		}
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("line %d lost the pane's gutter: %q", i, line)
		}
	}
	if plain[1] != "  -    old line" {
		t.Errorf("removal = %q, want its indentation kept", plain[1])
	}
	if !strings.Contains(styled[1], styleDiffDel.Render("-    old line")) {
		t.Errorf("removal is not styled as the Diff tab styles one: %q", styled[1])
	}
	if !strings.Contains(styled[2], styleDiffAdd.Render(strings.TrimPrefix(plain[2], "  "))) {
		t.Errorf("addition is not styled as the Diff tab styles one: %q", styled[2])
	}
	var rejoined strings.Builder
	for _, line := range plain[2 : len(plain)-2] {
		rejoined.WriteString(strings.TrimPrefix(line, "  "))
	}
	if rejoined.String() != long {
		t.Errorf("long line was clipped:\n%s\nwant\n%s", rejoined.String(), long)
	}
	if got := plain[len(plain)-2]; got != "       context" {
		t.Errorf("context line = %q", got)
	}
	if got := plain[len(plain)-1]; got != "  … patch truncated" {
		t.Errorf("last line = %q, want the truncation stated", got)
	}
}
