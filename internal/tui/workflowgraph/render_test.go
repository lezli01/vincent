package workflowgraph

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Golden files are the artifact a reviewer reads when a layout changes: the
// diff shows the picture moving. They are paired with the invariant
// assertions in layout_test.go, so refreshing a golden cannot quietly bless a
// topology error (decision 21).

var update = flag.Bool("update", false, "rewrite the golden renders in testdata")

func TestGoldenRenders(t *testing.T) {
	for name, wf := range corpus() {
		t.Run(name, func(t *testing.T) {
			d := Build(wf)
			s := Layout(d, DefaultOptions())
			got := strings.Join(Render(d, s, ViewState{}, Theme{}), "\n") + "\n"
			compareGolden(t, name+".txt", got)
		})
	}
}

// The selected node is drawn with a heavier border, which is a change of
// glyphs and never of geometry: a selection that moved the picture would make
// keyboard navigation unusable.
func TestGoldenSelection(t *testing.T) {
	wf := fixtureParallel()
	d := Build(wf)
	s := Layout(d, DefaultOptions())
	plain := Render(d, s, ViewState{}, Theme{})
	selected := Render(d, s, ViewState{Selected: "unit"}, Theme{})

	compareGolden(t, "parallel-selected.txt", strings.Join(selected, "\n")+"\n")
	if len(plain) != len(selected) {
		t.Fatalf("selection changed the row count: %d vs %d", len(plain), len(selected))
	}
	diff := 0
	for i := range plain {
		if plain[i] != selected[i] {
			diff++
		}
		if ansi.StringWidth(plain[i]) != ansi.StringWidth(selected[i]) {
			t.Errorf("row %d changed width when selected", i)
		}
	}
	if diff == 0 {
		t.Error("selection changed nothing on screen")
	}
}

func compareGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run: go test ./internal/tui/workflowgraph -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("render does not match %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// Styling is decoration over a picture that already reads: stripping every
// escape sequence must give exactly the plain render back (decision 6).
func TestStylingDoesNotChangeThePicture(t *testing.T) {
	theme := Theme{
		Node:      lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		Selected:  lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true),
		Frame:     lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Edge:      lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		EdgeLabel: lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	}
	for name, wf := range corpus() {
		d := Build(wf)
		s := Layout(d, DefaultOptions())
		plain := Render(d, s, ViewState{Selected: "plan"}, Theme{})
		styled := Render(d, s, ViewState{Selected: "plan"}, theme)
		if len(plain) != len(styled) {
			t.Fatalf("%s: styled render has %d rows, plain has %d", name, len(styled), len(plain))
		}
		for i := range plain {
			if got := ansi.Strip(styled[i]); got != plain[i] {
				t.Errorf("%s row %d: stripped %q, want %q", name, i, got, plain[i])
			}
		}
	}
}

// Labels are cut to a display width, not a rune or byte count, so a node of
// wide characters still occupies exactly its column budget.
func TestWideLabelsRespectDisplayWidth(t *testing.T) {
	opts := DefaultOptions()
	d := Build(fixtureWideLabels())
	s := Layout(d, opts)
	rows := Render(d, s, ViewState{}, Theme{})
	for i, row := range rows {
		if w := ansi.StringWidth(row); w > s.Width {
			t.Errorf("row %d is %d columns wide, want at most the scene's %d", i, w, s.Width)
		}
	}
	for _, n := range s.Nodes {
		label := rows[n.Y+1]
		if ansi.StringWidth(label) > s.Width {
			t.Errorf("node %s spilled its label past the canvas", n.ID)
		}
	}
}

func TestTruncateMeasuresDisplayWidth(t *testing.T) {
	for _, tc := range []struct {
		in    string
		width int
		want  int
	}{
		{"short", 10, 5},
		{"a very long label indeed", 8, 8},
		{"日本語テキスト", 6, 6},
		{"🚀🚀🚀🚀", 5, 5},
		{"anything", 0, 0},
	} {
		if got := ansi.StringWidth(truncate(tc.in, tc.width)); got > tc.want {
			t.Errorf("truncate(%q, %d) is %d columns, want at most %d", tc.in, tc.width, got, tc.want)
		}
	}
}

// The type is what a reader names a box by, so badges are what give way when
// the two cannot both fit.
func TestKindLineDropsBadgesBeforeTheType(t *testing.T) {
	n := Node{Kind: KindCondition, Badges: []string{"if"}}
	if got := kindLine(n, 18); !strings.HasPrefix(got, "condition") || !strings.HasSuffix(got, "if") {
		t.Errorf("kindLine = %q, want the type and its badge", got)
	}
	if got := kindLine(n, 9); got != "condition" {
		t.Errorf("kindLine at 9 = %q, want the type alone", got)
	}
	if got := ansi.StringWidth(kindLine(n, 5)); got > 5 {
		t.Errorf("kindLine at 5 is %d columns, want at most 5", got)
	}
}

// A workflow reference prints as `workflow`, because `workflow_ref` is a name
// this codebase invented and the YAML never says.
func TestWorkflowReferenceKindLabel(t *testing.T) {
	if got := kindLabel(KindWorkflowRef); got != "workflow" {
		t.Errorf("kindLabel = %q, want workflow", got)
	}
	for _, k := range []NodeKind{KindAgent, KindCommand, KindLoop, KindFanOut, KindCondition} {
		if got := kindLabel(k); got != string(k) {
			t.Errorf("kindLabel(%s) = %q, want the §8.2 word itself", k, got)
		}
	}
}

// A fan_out lane is a child task the workflow language names and may guard,
// so its id and its `if` are on screen rather than only in the inspector.
func TestFanOutLaneCaptionsAreDrawn(t *testing.T) {
	d := Build(fixtureFanOut())
	s := Layout(d, DefaultOptions())
	out := strings.Join(Render(d, s, ViewState{}, Theme{}), "\n")
	for _, want := range []string{"api", "web if"} {
		if !strings.Contains(out, want) {
			t.Errorf("render does not caption the lane %q:\n%s", want, out)
		}
	}
}

// A parallel group's columns are not lanes and have nothing to caption.
func TestParallelHasNoLaneCaptions(t *testing.T) {
	d := Build(fixtureParallel())
	for _, g := range d.Groups {
		for _, col := range g.Columns {
			if col.Label != "" {
				t.Errorf("parallel column %v carries a caption %q", col.Nodes, col.Label)
			}
		}
	}
}

// A branch label is only dropped when there is genuinely nowhere free, and
// the corpus must not contain such a case: a `true` or `false` a reader
// cannot see is a topology they have to guess at.
func TestEveryBranchLabelIsDrawn(t *testing.T) {
	for name, wf := range corpus() {
		d := Build(wf)
		s := Layout(d, DefaultOptions())
		out := strings.Join(Render(d, s, ViewState{}, Theme{}), "\n")
		for _, e := range d.Edges {
			if e.Label == "" {
				continue
			}
			if !strings.Contains(out, e.Label) {
				t.Errorf("%s: edge %s->%s lost its %q label:\n%s",
					name, e.From, e.To, e.Label, out)
			}
		}
	}
}

// The join is named for the step it belongs to, so a reader can tell whose
// merge it is when a workflow fans out more than once.
func TestMergeNodeIsNamedForItsStep(t *testing.T) {
	d := Build(fixtureFanOut())
	for _, n := range d.Nodes {
		if n.Kind != KindMerge {
			continue
		}
		if n.Label != "spread" {
			t.Errorf("merge label = %q, want the fan_out step's name", n.Label)
		}
	}
}
