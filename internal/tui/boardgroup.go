package tui

import (
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Grouping for the task table (§15, task 009). The board's rows are nested
// under headers by vincent issue (task 148) — what a task is *for* — or by
// workflow, which is what says what a task is *doing*. It is
// configuration — `tui.board.group_by` in config.yaml, served on
// `GET /v1/config` — because the shape that suits one workflow is not the
// shape that suits six. There is no project level: the board shows one
// project at a time (task 132), so that level had one value and drew no
// header, and it was deprecated (task 132.9, decision 6).
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
	groupIssue    groupKey = "issue"
	groupWorkflow groupKey = "workflow"
)

// grouping is the levels in effect, outermost first. Empty is a flat table —
// exactly what every version before this one rendered.
type grouping []groupKey

// defaultGrouping is what the board renders before the daemon's config
// arrives, and what it keeps when no daemon is reachable. It matches
// config.Default(); boardgroup_test.go holds the two together.
func defaultGrouping() grouping { return grouping{groupIssue} }

// groupingCycle is what `g` steps through: by issue, by workflow, then flat
// (task 148). The key is a quick look at the board another way, not an
// editor for the config file, so a two-level grouping is reachable only
// through config.yaml, and `g` from one jumps to the head of the cycle.
func groupingCycle() []grouping {
	return []grouping{
		{groupIssue},
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
// group inside itself. The deprecated `project` level falls under the first
// rule: a daemon from before task 132.9 still serves it, and dropping it here
// renders what that daemon's board would have, since on a board scoped to one
// project the level had one value and drew no header.
func parseGrouping(levels []string) grouping {
	out := make(grouping, 0, len(levels))
	for _, l := range levels {
		k := groupKey(l)
		if k != groupIssue && k != groupWorkflow {
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
	// line is which line of a wrapped task this row renders (task 050
	// decision 5): 0 for the row itself, 1 and up for its continuations. A
	// task occupies exactly one boardRow per line and a group header stays
	// exactly one, which is what keeps line == row true — bubbles' table
	// counts rows throughout its scroll model, so a multi-line row that was
	// one table.Row would break paging, the height budget and the click math
	// all at once.
	//
	// The continuation carries a copy of its task, so anything asking a row
	// what it is on gets the same answer wherever in the block the cursor
	// happens to be.
	line int
	// depth is the *grouping* nesting level: 0 for an outermost header, the
	// number of levels drawing headers (shownLevels) for every task row — a fan-out lane included, so a
	// collapsed group swallows an expanded subtree whole (applyFolds compares
	// depths).
	depth int
	// lane is the fan-out nesting of a task row (boardlanes.go, issue #316):
	// 0 for a task the daemon listed, 1 for a lane hanging under one, and up
	// to fan_out.max_depth. It indents the title and nothing else — a lane is
	// an ordinary task row to folding, to the bulk selection, to the attention
	// badge and to the column ladder, which is the property the tree was built
	// not to cost.
	lane int
	// label, count and attention describe the group a header opens. The
	// attention count is on the header so a collapsed group can never hide
	// that something is waiting on a human (§15, task 054 decision 3). The
	// label is display text only; the group is named by its key, which is
	// what path carries (task 148: an issue's label holds its title, which
	// may change, and its key `#<id>`, which does not). closed adds the dim
	// `· closed` an issue header ends its label with.
	label     string
	closed    bool
	count     int
	attention int
	// path names the group for the fold set (boardfold.go): this header's
	// key and every key above it, outermost first. Empty on a task row.
	path foldPath
	// collapsed and marked are filled in by applyFolds, never by groupRows:
	// a folded header stands in for rows that are not on screen, so it says
	// how many of them the bulk selection holds.
	collapsed bool
	marked    int
}

// selectable reports whether the cursor may rest on this row. An expanded
// header is a label and a continuation is the tail of the row above it:
// neither is a thing a key acts on, so both are stepped over
// (board.skipHeaders) and skipped by everything that walks tasks rather than
// lines. A *collapsed* header stands in for tasks that are not on screen, so
// it is a row the cursor stops on (task 054 decision 2).
func (r boardRow) selectable() bool { return (!r.header || r.collapsed) && r.line == 0 }

// shownLevels splits a grouping into the levels that draw headers and the
// values of the ones that do not (task 129 decision 4). A level whose tasks
// all share one value renders no header: two header lines above a board that
// is all one project say nothing the panel title cannot, and they cost two
// rows. Each level is judged on its own, over the tasks being shown — a
// filter that narrows the board to one project quiets that level too — and
// the value a skipped level would have named is returned, in level order, for
// the panel title to carry instead.
//
// What is returned is the skipped group's display label (groupTitle), never
// its key: the panel title reads `Tasks · no issue`, not `Tasks · #none`.
//
// An empty task list skips nothing: there is no value to name, and no header
// to draw either way.
func shownLevels(tasks []apiclient.Task, g grouping) (shown grouping, skipped []string) {
	if len(tasks) == 0 {
		return g, nil
	}
	shown = make(grouping, 0, len(g))
	for _, k := range g {
		first := groupValue(tasks[0], k)
		single := true
		for _, t := range tasks[1:] {
			if groupValue(t, k) != first {
				single = false
				break
			}
		}
		if single {
			label, closed := groupTitle(tasks[0], k)
			if closed {
				label += groupLabelSep + issueStateClosed
			}
			skipped = append(skipped, label)
			continue
		}
		shown = append(shown, k)
	}
	return shown, skipped
}

// groupRows interleaves group headers into an already-sorted task list.
// Groups appear in the order their first task does, so the band sort decides
// group order as well as row order.
//
// A level shownLevels skips contributes no header and no nesting depth, but
// its value stays in the fold path of every header under it: a path names a
// group by every configured level above it, so a fold remembered on a board
// with two projects means the same group again once a second project returns,
// and a skipped level's own fold entry is neither read nor written — there is
// no header to carry it, so it cannot hide a row (task 054 decision 4 keeps
// the file; task 129 decision 4 keeps it untouched).
func groupRows(tasks []apiclient.Task, g grouping) []boardRow {
	shown, _ := shownLevels(tasks, g)
	if len(shown) == 0 {
		out := make([]boardRow, 0, len(tasks))
		for _, t := range tasks {
			out = append(out, boardRow{task: t})
		}
		return out
	}
	out := make([]boardRow, 0, len(tasks)+len(g))
	var walk func(tasks []apiclient.Task, level, depth int, path foldPath)
	walk = func(tasks []apiclient.Task, level, depth int, path foldPath) {
		if level == len(g) {
			for _, t := range tasks {
				out = append(out, boardRow{task: t, depth: depth})
			}
			return
		}
		for _, grp := range partitionTasks(tasks, g[level]) {
			// A fresh slice per group: append onto a shared backing array
			// would leave two sibling headers naming the same path.
			sub := make(foldPath, len(path), len(path)+1)
			copy(sub, path)
			sub = append(sub, grp.key)
			if !shown.has(g[level]) {
				// One group, and no header: its tasks nest under whatever
				// header is above, which is where its `! n` now lands.
				walk(grp.tasks, level+1, depth, sub)
				continue
			}
			out = append(out, boardRow{
				header:    true,
				depth:     depth,
				label:     grp.label,
				closed:    grp.closed,
				count:     len(grp.tasks),
				attention: countAttention(grp.tasks),
				path:      sub,
			})
			walk(grp.tasks, level+1, depth+1, sub)
		}
	}
	walk(tasks, 0, 0, nil)
	return out
}

// taskGroup is one group of one level. key names it — partitioning, fold
// paths, pruning — and label and closed only draw it; for a workflow the two
// are the same string, which is what keeps every pre-148 fold meaning what it
// meant. The label comes from the group's first task in band order.
type taskGroup struct {
	key    string
	label  string
	closed bool
	tasks  []apiclient.Task
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
			label, closed := groupTitle(t, k)
			out = append(out, taskGroup{key: key, label: label, closed: closed})
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

// groupNoIssue is the key of the group holding every task with no vincent
// issue — a handoff's task among them (task 148 decision 8). It is not
// groupUnnamed: under [issue, workflow] a fold on the no-issue group and one
// on the unnamed-workflow group would otherwise be the same path segment and
// alias each other. A `#` cannot open a workflow name
// (`^[a-z0-9][a-z0-9._-]*$`) and `none` is not an issue id, so it collides
// with neither level's keys.
const groupNoIssue = "#none"

// groupNoIssueLabel is what the no-issue group's header, and the panel title
// when it is the only group, say.
const groupNoIssueLabel = "no issue"

// groupLabelSep joins the parts of an issue header's label.
const groupLabelSep = " · "

// issueStateClosed is §5.6's closed state, as the wire spells it — the same
// literal issuestate.Closed holds, compared here rather than imported for the
// reason groupKey's values are.
const issueStateClosed = "closed"

// groupValue is a task's key at one level: the string fold paths, partitions
// and pruning compare. An issue's is `#<id>`, a deleted issue's frozen
// snapshot included (task 130 decision 6) — it keeps its id.
func groupValue(t apiclient.Task, k groupKey) string {
	var v string
	switch k {
	case groupIssue:
		if t.Issue == nil {
			return groupNoIssue
		}
		return "#" + strconv.FormatInt(t.Issue.ID, 10)
	case groupWorkflow:
		v = t.Workflow
	}
	if strings.TrimSpace(v) == "" {
		return groupUnnamed
	}
	return v
}

// groupTitle is how a task's group at one level is drawn: the display label,
// and whether it ends in a dim `· closed`. An issue reads
// `issue #<id> · <title> · <short ref>`, the ref only when it was imported —
// the short form the daemon rendered, falling back to the full one (task
// 142.8). Every other level's label is its key.
func groupTitle(t apiclient.Task, k groupKey) (label string, closed bool) {
	if k != groupIssue {
		return groupValue(t, k), false
	}
	iss := t.Issue
	if iss == nil {
		return groupNoIssueLabel, false
	}
	label = "issue " + groupValue(t, k)
	if title := strings.TrimSpace(iss.Title); title != "" {
		label += groupLabelSep + title
	}
	if ref := issueRef(iss); ref != "" {
		label += groupLabelSep + ref
	}
	return label, strings.TrimSpace(iss.State) == issueStateClosed
}

// issueRef is an imported issue's compact reference, or "" for a local one.
func issueRef(iss *apiclient.TaskIssue) string {
	if iss == nil || iss.Source == nil {
		return ""
	}
	if ref := strings.TrimSpace(iss.Source.ShortRef); ref != "" {
		return ref
	}
	return strings.TrimSpace(iss.Source.Ref)
}

// groupGlyphOpen and groupGlyphFolded open a header line. A glyph rather than
// colour alone, so the state survives NO_COLOR and a 16-colour terminal
// (§15 Colour).
//
// It is a disclosure control since task 054, which superseded 009 decision
// 4's "nothing folds away": on a six-project installation the board's job is
// showing you every task you can act on rather than every task there is. What
// a fold is not allowed to hide is that something needs a human, and the
// count and the attention badge below survive the fold for that reason.
const (
	groupGlyphOpen   = "▾"
	groupGlyphFolded = "▸"
)

// styleGroup is the header label. Bold rather than coloured: the state
// colours are the board's colour vocabulary and a header holds no state.
var styleGroup = lipgloss.NewStyle().Bold(true)

// headerCell renders a group header into the title column — the widest column
// and the only one guaranteed to be there at any width (boardcols.go). width
// is that column's width; the label is what gives way when it is too narrow,
// so the count, the `! n` badge and the marked count — what a fold must never
// hide (task 054 decision 3) — stay on screen whatever an issue's title runs
// to. Zero or less renders the whole label.
func (r boardRow) headerCell(width int) string {
	glyph := groupGlyphOpen
	if r.collapsed {
		glyph = groupGlyphFolded
	}
	lead := strings.Repeat(groupIndent, r.depth) + glyph + " "
	label := r.label
	tail := styleDim.Render("  " + strconv.Itoa(r.count))
	if r.attention > 0 {
		tail += "  " + styleWarn.Render(attentionBadge+" "+strconv.Itoa(r.attention))
	}
	if r.marked > 0 {
		// What a bulk action would touch inside a group whose rows are not on
		// screen. The selection is a set of tasks and not a view (task 011),
		// so folding one away must not make it invisible.
		tail += "  " + styleKey.Render(markGlyph+" "+strconv.Itoa(r.marked))
	}
	closed := ""
	if r.closed {
		closed = groupLabelSep + issueStateClosed
	}
	if width > 0 {
		room := width - ansi.StringWidth(lead) - ansi.StringWidth(tail)
		if ansi.StringWidth(label)+ansi.StringWidth(closed) > room {
			// `closed` outranks the end of a title: it is state, the title
			// is a name the header has already begun.
			label = ansi.Truncate(label, max(room-ansi.StringWidth(closed), 1), "…")
		}
	}
	out := styleGroup.Render(lead + label)
	if closed != "" {
		out += styleDim.Render(closed)
	}
	return out + tail
}

// groupIndent is one level of nesting, for headers and for the task titles
// under them.
const groupIndent = "  "

// firstTaskRow is the row a cursor with nothing to restore belongs on: the
// first row it may rest on — a task, or a collapsed header standing in for
// tasks (task 054 decision 2). Zero when there is nothing at all, which is
// the same answer the ungrouped board gave.
func firstTaskRow(rows []boardRow) int {
	if i := landingRow(rows, 0); i >= 0 {
		return i
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
