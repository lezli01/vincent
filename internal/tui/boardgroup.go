package tui

import (
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Grouping for the task table (§15, task 009). The board's rows are nested
// under headers by project and then by workflow, which is how a board with
// more than one repository on it is actually read: you look at one project,
// and within it the workflow is what says what a task is *doing*. It is
// configuration — `tui.board.group_by` in config.yaml, served on
// `GET /v1/config` — because the shape that suits three projects and one
// workflow is not the shape that suits one project and six.
//
// Grouping is a view over the same sorted list, never a second ordering: the
// tasks are sorted by band exactly as they always were, and the groups take
// the order of their first task. A group holding a task that needs a human
// therefore rises to the top, and §15's pinning rule survives grouping —
// which is the one property this feature was not allowed to cost.

// groupKey is one grouping level. The values are the wire strings the daemon
// serves, duplicated here rather than imported from internal/config for the
// same reason the task states are (boardrows.go): the TUI is an API client,
// and what it renders comes from the wire.
type groupKey string

const (
	groupProject  groupKey = "project"
	groupWorkflow groupKey = "workflow"
)

// grouping is the levels in effect, outermost first. Empty is a flat table —
// exactly what every version before this one rendered.
type grouping []groupKey

// defaultGrouping is what the board renders before the daemon's config
// arrives, and what it keeps when no daemon is reachable. It matches
// config.Default(); boardgroup_test.go holds the two together.
func defaultGrouping() grouping { return grouping{groupProject, groupWorkflow} }

// groupingCycle is what `g` steps through: the default, each level alone,
// then flat. A configured grouping outside this cycle (the levels reversed,
// say) is honoured on load and cycles from the start on the first press —
// the key is a quick look at the board another way, not an editor for the
// config file.
func groupingCycle() []grouping {
	return []grouping{
		{groupProject, groupWorkflow},
		{groupProject},
		{groupWorkflow},
		{},
	}
}

// next is the grouping `g` moves to.
func (g grouping) next() grouping {
	cycle := groupingCycle()
	for i, c := range cycle {
		if g.equal(c) {
			return cycle[(i+1)%len(cycle)]
		}
	}
	return cycle[0]
}

func (g grouping) has(k groupKey) bool { return slices.Contains(g, k) }

// equal compares levels in order: two groupings with the same levels in a
// different order are different views, not the same one.
func (g grouping) equal(other grouping) bool { return slices.Equal(g, other) }

// label names the grouping for the panel title.
func (g grouping) label() string {
	if len(g) == 0 {
		return "ungrouped"
	}
	parts := make([]string, 0, len(g))
	for _, level := range g {
		parts = append(parts, string(level))
	}
	return "by " + strings.Join(parts, " › ")
}

// parseGrouping reads the levels the daemon served. An unknown level is
// dropped rather than refused: a newer daemon may serve one this TUI predates,
// and a board that renders one level of a two-level grouping is still a board.
// A duplicate is dropped for the same reason — the daemon rejects one at load,
// so seeing it here means the two disagree, and repeating a level would nest a
// group inside itself.
func parseGrouping(levels []string) grouping {
	out := make(grouping, 0, len(levels))
	for _, l := range levels {
		k := groupKey(l)
		switch k {
		case groupProject, groupWorkflow:
		default:
			continue
		}
		if !out.has(k) {
			out = append(out, k)
		}
	}
	return out
}

// boardRow is one line of the task table: a task, or the header of a group of
// them. Headers are labels — the cursor steps over them (board.skipHeaders)
// and they carry no actions, because a group is not something the daemon has
// a state for.
type boardRow struct {
	// header marks a group header; task is the zero value on one.
	header bool
	task   apiclient.Task
	// depth is the nesting level: 0 for an outermost header, len(grouping)
	// for every task row.
	depth int
	// label, count and attention describe the group a header opens. The
	// attention count is on the header so a collapsed-looking board can never
	// hide that something is waiting on a human (§15).
	label     string
	count     int
	attention int
}

// groupRows interleaves group headers into an already-sorted task list.
// Groups appear in the order their first task does, so the band sort decides
// group order as well as row order.
func groupRows(tasks []apiclient.Task, g grouping) []boardRow {
	if len(g) == 0 {
		out := make([]boardRow, 0, len(tasks))
		for _, t := range tasks {
			out = append(out, boardRow{task: t})
		}
		return out
	}
	out := make([]boardRow, 0, len(tasks)+len(g))
	var walk func(tasks []apiclient.Task, depth int)
	walk = func(tasks []apiclient.Task, depth int) {
		if depth == len(g) {
			for _, t := range tasks {
				out = append(out, boardRow{task: t, depth: depth})
			}
			return
		}
		for _, grp := range partitionTasks(tasks, g[depth]) {
			out = append(out, boardRow{
				header:    true,
				depth:     depth,
				label:     grp.key,
				count:     len(grp.tasks),
				attention: countAttention(grp.tasks),
			})
			walk(grp.tasks, depth+1)
		}
	}
	walk(tasks, 0)
	return out
}

type taskGroup struct {
	key   string
	tasks []apiclient.Task
}

// partitionTasks splits a sorted list by one level, preserving both the order
// of the groups (first appearance) and the order within them.
func partitionTasks(tasks []apiclient.Task, k groupKey) []taskGroup {
	out := make([]taskGroup, 0, 4)
	index := make(map[string]int, 4)
	for _, t := range tasks {
		key := groupValue(t, k)
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, taskGroup{key: key})
		}
		out[i].tasks = append(out[i].tasks, t)
	}
	return out
}

// groupUnnamed labels a group whose key is empty — a task the daemon reported
// without a project name or a workflow. It is the dash the board already uses
// for a figure nobody reported, rather than a blank header that would read as
// a rendering bug.
const groupUnnamed = "—"

func groupValue(t apiclient.Task, k groupKey) string {
	var v string
	switch k {
	case groupProject:
		v = t.ProjectName
	case groupWorkflow:
		v = t.Workflow
	}
	if strings.TrimSpace(v) == "" {
		return groupUnnamed
	}
	return v
}

// groupGlyph opens a header line. A glyph rather than colour alone, so the
// nesting survives NO_COLOR and a 16-colour terminal (§15 Colour). It marks a
// group, not a collapse control: nothing here folds away, because a board
// whose whole job is showing you every task has no business hiding rows.
const groupGlyph = "▾"

// styleGroup is the header label. Bold rather than coloured: the state
// colours are the board's colour vocabulary and a header holds no state.
var styleGroup = lipgloss.NewStyle().Bold(true)

// headerCell renders a group header into the title column — the widest column
// and the only one guaranteed to be there at any width (boardcols.go).
func (r boardRow) headerCell() string {
	label := strings.Repeat(groupIndent, r.depth) + groupGlyph + " " + r.label
	out := styleGroup.Render(label) + styleDim.Render("  "+strconv.Itoa(r.count))
	if r.attention > 0 {
		out += "  " + styleWarn.Render(attentionBadge+" "+strconv.Itoa(r.attention))
	}
	return out
}

// groupIndent is one level of nesting, for headers and for the task titles
// under them.
const groupIndent = "  "

// firstTaskRow is the row a cursor with nothing to restore belongs on: the
// first actual task. Zero when there is nothing at all, which is the same
// answer the ungrouped board gave.
func firstTaskRow(rows []boardRow) int {
	for i := range rows {
		if !rows[i].header {
			return i
		}
	}
	return 0
}

// groupSummary is the one-line description of a grouping for the daemon
// view's "config in effect" block, where the levels are what the file says
// rather than what any one client is currently showing.
func groupSummary(levels []string) string {
	g := parseGrouping(levels)
	if len(g) == 0 {
		return "off (one flat list)"
	}
	return g.label()
}
