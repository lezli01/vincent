package workflow

import (
	"sort"
	"strings"
	"testing"
)

// fixture is deliberately awkward: a header comment, an inline trailing
// comment, blank lines between blocks, a `|` block scalar, a nested parallel
// group and a fan_out with lanes and a merge. Every fidelity assertion below
// edits one region of it and compares the rest byte for byte.
const fixture = `# review — the team's own review pass.
# Keep the comments: they are why anyone can read this file.
name: review
description: Review a change end to end

fields:
  - name: scope
    type: enum
    values: [api, tui]
    default: api

defaults:
  agent: claude          # the one everybody has installed

steps:
  - id: plan
    type: agent
    prompt: |
      Plan the work.
      Do not write code yet.

  # The build and the tests have no reason to wait for each other.
  - id: checks
    type: parallel
    steps:
      - id: build
        type: command
        run: go build ./...
      - id: test
        type: command
        run: go test ./...

  - id: spread
    type: fan_out
    lanes:
      - id: one
        workflow: fix
    merge:
      on_conflict: skip
`

// applyOrFail edits the fixture and fails the test on an error, so each case
// below is one assertion about the bytes rather than three lines of plumbing.
func applyOrFail(t *testing.T, ops ...Op) string {
	t.Helper()
	out, err := Edit([]byte(fixture), ops)
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	return string(out)
}

// assertUntouched compares before and after line by line, ignoring the lines
// the test says it changed. It is the byte-identity guarantee of task 065
// decision 1 expressed as an assertion.
func assertUntouched(t *testing.T, before, after string, changed ...string) {
	t.Helper()
	skip := map[string]bool{}
	for _, s := range changed {
		skip[s] = true
	}
	b := filterLines(strings.Split(before, "\n"), skip)
	a := filterLines(strings.Split(after, "\n"), skip)
	if len(b) != len(a) {
		t.Fatalf("line count changed outside the edit: %d then %d\n--- after ---\n%s", len(b), len(a), after)
	}
	for i := range b {
		if b[i] != a[i] {
			t.Fatalf("line %d changed outside the edit:\n  before %q\n  after  %q", i, b[i], a[i])
		}
	}
}

// assertSameLines checks that the two documents hold exactly the same lines,
// which is what a reorder must leave behind.
func assertSameLines(t *testing.T, before, after string) {
	t.Helper()
	b := append([]string(nil), strings.Split(before, "\n")...)
	a := append([]string(nil), strings.Split(after, "\n")...)
	sort.Strings(b)
	sort.Strings(a)
	if len(b) != len(a) {
		t.Fatalf("line count changed: %d then %d\n%s", len(b), len(a), after)
	}
	for i := range b {
		if b[i] != a[i] {
			t.Fatalf("line %q became %q", b[i], a[i])
		}
	}
}

func filterLines(lines []string, skip map[string]bool) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if skip[l] {
			continue
		}
		out = append(out, l)
	}
	return out
}

func TestEditSetScalarLeavesEverythingElseIdentical(t *testing.T) {
	got := applyOrFail(t, Op{Kind: OpSet, Path: "steps[1].steps[1].run", Value: "go test -race ./..."})
	if !strings.Contains(got, "        run: go test -race ./...") {
		t.Fatalf("run not rewritten:\n%s", got)
	}
	assertUntouched(t, fixture, got, "        run: go test ./...", "        run: go test -race ./...")
}

func TestEditKeepsTrailingComment(t *testing.T) {
	got := applyOrFail(t, Op{Kind: OpSet, Path: "defaults.agent", Value: "codex"})
	// The comment keeps the column it sat in, not its distance from the old
	// value: config.Apply's setValue does the same, and a file whose comments
	// line up is one somebody lined up.
	if !strings.Contains(got, "  agent: codex           # the one everybody has installed") {
		t.Fatalf("trailing comment lost or moved:\n%s", got)
	}
}

func TestEditBlockScalarReplacesOnlyItsBody(t *testing.T) {
	got := applyOrFail(t, Op{
		Kind: OpSet, Path: "steps[0].prompt", Block: true,
		Value: "Plan it.\nThen stop.",
	})
	if !strings.Contains(got, "    prompt: |\n      Plan it.\n      Then stop.\n") {
		t.Fatalf("block scalar not rewritten:\n%s", got)
	}
	if strings.Contains(got, "Do not write code yet.") {
		t.Fatalf("old block body survived:\n%s", got)
	}
	assertUntouched(t, fixture, got,
		"      Plan the work.", "      Do not write code yet.",
		"      Plan it.", "      Then stop.")
}

func TestEditInsertStep(t *testing.T) {
	got := applyOrFail(t, Op{
		Kind: OpInsert, Path: "steps[1]",
		Item: []OpField{
			{Key: "id", Value: "lint"},
			{Key: "type", Value: "command"},
			{Key: "run", Value: "go run mage.go lint"},
		},
	})
	want := "  - id: lint\n    type: command\n    run: go run mage.go lint\n"
	if !strings.Contains(got, want) {
		t.Fatalf("step not inserted:\n%s", got)
	}
	// The new step lands before the comment that introduces `checks`, which
	// is the point of excluding a following comment block from an entry's
	// extent.
	if strings.Index(got, "- id: lint") > strings.Index(got, "# The build and the tests") {
		t.Fatalf("insert landed after the next step's comment:\n%s", got)
	}
	assertUntouched(t, fixture, got,
		"  - id: lint", "    type: command", "    run: go run mage.go lint")
}

func TestEditInsertLaneAndSetMerge(t *testing.T) {
	got := applyOrFail(t,
		Op{Kind: OpInsert, Path: "steps[2].lanes[1]", Item: []OpField{
			{Key: "id", Value: "two"},
			{Key: "workflow", Value: "test"},
		}},
		Op{Kind: OpSet, Path: "steps[2].merge.on_conflict", Value: "agent"},
	)
	if !strings.Contains(got, "      - id: two\n        workflow: test\n") {
		t.Fatalf("lane not inserted:\n%s", got)
	}
	if !strings.Contains(got, "      on_conflict: agent") {
		t.Fatalf("merge not rewritten:\n%s", got)
	}
	assertUntouched(t, fixture, got,
		"      - id: two", "        workflow: test",
		"      on_conflict: skip", "      on_conflict: agent")
}

func TestEditRemoveStep(t *testing.T) {
	got := applyOrFail(t, Op{Kind: OpRemove, Path: "steps[1].steps[0]"})
	if strings.Contains(got, "id: build") {
		t.Fatalf("step not removed:\n%s", got)
	}
	assertUntouched(t, fixture, got,
		"      - id: build", "        type: command", "        run: go build ./...")
}

func TestEditMoveStep(t *testing.T) {
	got := applyOrFail(t, Op{Kind: OpMove, Path: "steps[1].steps[1]", To: 0})
	if strings.Index(got, "id: test") > strings.Index(got, "id: build") {
		t.Fatalf("steps not reordered:\n%s", got)
	}
	// A move rewrites no line, it only changes where they sit, so the
	// assertion is on the multiset rather than on the sequence.
	assertSameLines(t, fixture, got)
}

func TestEditCRLFSurvives(t *testing.T) {
	src := strings.ReplaceAll(fixture, "\n", "\r\n")
	out, err := Edit([]byte(src), []Op{{Kind: OpSet, Path: "description", Value: RenderScalar("Review it")}})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got := string(out)
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Fatalf("mixed line endings in the result")
	}
	assertUntouched(t, src, got,
		"description: Review a change end to end\r", "description: \"Review it\"\r")
}

func TestEditCreatesNestedBlock(t *testing.T) {
	got := applyOrFail(t,
		Op{Kind: OpSet, Path: "steps[2].merge.agent.id", Value: "resolve"},
		Op{Kind: OpSet, Path: "steps[2].merge.agent.type", Value: "agent"},
	)
	if !strings.Contains(got, "      agent:\n        id: resolve\n        type: agent") {
		t.Fatalf("nested block not created:\n%s", got)
	}
}

func TestEditRejectsBadPathsAndIndices(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   Op
	}{
		{"unknown op", Op{Kind: "delete", Path: "name"}},
		{"empty path", Op{Kind: OpSet, Path: ""}},
		{"malformed", Op{Kind: OpSet, Path: "steps[a]"}},
		{"out of range", Op{Kind: OpInsert, Path: "steps[9]", Item: []OpField{{Key: "id", Value: "x"}}}},
		{"remove missing", Op{Kind: OpRemove, Path: "steps[0].nope"}},
		{"move out of range", Op{Kind: OpMove, Path: "steps[0]", To: 12}},
		{"insert without index", Op{Kind: OpInsert, Path: "steps", Item: []OpField{{Key: "id", Value: "x"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Edit([]byte(fixture), []Op{tc.op}); err == nil {
				t.Fatalf("expected an error")
			}
		})
	}
}

func TestFileNameRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../escape", "a/b", `a\b`, "", ".", "..", "Upper"} {
		if _, err := FileName(name); err == nil {
			t.Fatalf("FileName(%q) accepted", name)
		}
	}
	got, err := FileName("review.v2")
	if err != nil || got != "review.v2.yaml" {
		t.Fatalf("FileName: %q %v", got, err)
	}
}
