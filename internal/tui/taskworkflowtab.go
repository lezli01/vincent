package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/tui/workflowgraph"
)

// The Workflow tab (task 051) is the task workspace's fifth surface: the
// task's own §5.3 snapshot drawn as a control-flow graph, with a run-state
// overlay that advances as the task does.
//
// The graph is the snapshot and never the registry entry of the same name.
// The registry's copy is whatever the file says right now; the snapshot is
// what ran — includes already spliced (§7.9), any `edit + retry` rewrite
// reflected (§6) — so drawing the registry copy would show a different
// workflow than the one that ran the moment the file is edited or shadowed
// differently.
//
// The definition is fetched once per open and refetched only when the
// snapshot itself can have changed. Everything else — which node is running,
// what was skipped and why, where the task is parked — is derived from the
// step rows the detail sub-model already refreshes off its own subscription
// (task 049 decision 4), so this tab opens no second stream.

// taskWorkflowMsg carries GET /v1/tasks/{id}/workflow.
type taskWorkflowMsg struct {
	taskID int64
	def    apiclient.TaskWorkflow
	err    error
}

// taskLanesMsg carries GET /v1/tasks?parent_id= — the lanes of a fan_out, in
// merge order, each with the state that goes on its caption (decision 1).
type taskLanesMsg struct {
	taskID   int64
	children []apiclient.Task
	err      error
}

// workflowTab is the tab's own state. The component holds the picture; this
// holds what was fetched for it and what the fetch is doing.
type workflowTab struct {
	graph  workflowgraph.Model
	taskID int64

	loading bool
	loaded  bool
	err     string
	// findings are a snapshot that did not parse: a 200 with errors and a
	// null definition, which the tab says out loud rather than rendering as
	// an empty pane.
	findings []apiclient.WorkflowFinding

	// lanes is the child task per lane id (§7.6). Lane ids repeat across two
	// fan_outs only in a workflow that has two, and the graph's own lane keys
	// are what the overlay joins on; this map is keyed by the id the child
	// task carries.
	lanes map[string]apiclient.Task
	// overrideRun is the newest attempt carrying a human edit at the time the
	// definition was fetched. `edit + retry` is the one writer of a running
	// task's snapshot (§6), so a newer one means the graph is stale.
	overrideRun int64

	width  int
	height int
}

func newWorkflowTab() *workflowTab {
	g := workflowgraph.New()
	g.SetTheme(graphTheme())
	// `tab` is the workspace's tab cycle here, so the component's own
	// source-order walk stands down rather than shadowing it (decision 5).
	g.SetSourceWalk(false)
	return &workflowTab{graph: g, lanes: map[string]apiclient.Task{}}
}

// open points the tab at a task, dropping anything fetched for another one.
func (t *taskView) openWorkflowTab() tea.Cmd {
	w := t.workflow
	if w == nil || t.detail.taskID == 0 {
		return nil
	}
	if w.taskID != t.detail.taskID {
		*w = *newWorkflowTab()
		w.taskID = t.detail.taskID
	}
	t.sizeWorkflow()
	if w.loaded && !w.snapshotStale(t.detail) {
		t.applyRunOverlay()
		return t.laneCmd()
	}
	w.loading = true
	return tea.Batch(t.workflowCmd(), t.laneCmd())
}

// snapshotStale reports a snapshot that can have been rewritten since the
// fetch. `edit + retry` is the one writer of a running task's own snapshot
// (§6), and it always lands as a new attempt carrying a prompt or run
// override — so a graph is refetched exactly when one appears, and never on
// an ordinary step advancing.
func (w *workflowTab) snapshotStale(d *detail) bool {
	for _, r := range d.task.Steps {
		if r.PromptOverride || r.RunOverride {
			return r.ID != w.overrideRun
		}
	}
	return false
}

func (t *taskView) workflowCmd() tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		def, err := client.GetTaskWorkflow(ctx, id)
		return taskWorkflowMsg{taskID: id, def: def, err: err}
	}
}

// laneCmd fetches this task's fan-out lanes. It is the existing subtree
// listing rather than a new endpoint: the rows already carry `lane_id`,
// `lane_order` and `state`, which is the whole of what a lane caption needs
// (decision 1).
func (t *taskView) laneCmd() tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		kids, err := client.ListTasks(ctx, apiclient.ListTasksOptions{ParentID: id})
		return taskLanesMsg{taskID: id, children: kids, err: err}
	}
}

func (t *taskView) applyWorkflow(msg taskWorkflowMsg) {
	w := t.workflow
	if w == nil || w.taskID != msg.taskID {
		return
	}
	w.loading = false
	if msg.err != nil {
		w.err = errString(msg.err)
		return
	}
	w.err = ""
	w.findings = msg.def.Errors
	if msg.def.Definition == nil {
		return
	}
	w.findings = nil
	w.loaded = true
	w.overrideRun = latestOverrideRun(t.detail)
	// Selection survives by node id, which is what semantic identity buys
	// (task 017 decision 19).
	w.graph.SetDefinition(msg.def.Definition)
	t.sizeWorkflow()
	t.applyRunOverlay()
}

func (t *taskView) applyLanes(msg taskLanesMsg) {
	w := t.workflow
	if w == nil || w.taskID != msg.taskID || msg.err != nil {
		return
	}
	w.lanes = make(map[string]apiclient.Task, len(msg.children))
	for _, c := range msg.children {
		if c.LaneID != nil {
			w.lanes[*c.LaneID] = c
		}
	}
	t.applyRunOverlay()
}

// latestOverrideRun is the newest attempt carrying a human edit, which is
// what an `edit + retry` rewrite of the snapshot leaves behind.
func latestOverrideRun(d *detail) int64 {
	var out int64
	for _, r := range d.task.Steps {
		if (r.PromptOverride || r.RunOverride) && r.ID > out {
			out = r.ID
		}
	}
	return out
}

// applyRunOverlay recomputes the whole overlay from the task rows the detail
// sub-model holds. It is cheap and total rather than incremental: the rows
// are the authoritative picture the daemon already refreshed, and an
// incremental overlay would be a second copy of the engine's state machine
// living in a viewer.
func (t *taskView) applyRunOverlay() {
	w := t.workflow
	if w == nil || !w.loaded {
		return
	}
	w.graph.SetOverlay(buildOverlay(t.detail.task, w.graph.Nodes(), w.graph.Lanes(), w.lanes))
}

// buildOverlay joins a task's step rows onto the graph's nodes.
//
// The join is `step_run.step_id` to `Node.StepID`, not to `Node.ID`: since
// task 051 a lane's inline steps carry a namespaced node id, so the raw id is
// what a row compares against. A lane's own steps run in a *child* task, so
// the parent holds no row for them and they are left unpainted — their lane's
// caption carries the child's state instead (decision 1).
func buildOverlay(task apiclient.TaskDetail, nodes []workflowgraph.Node, laneCols []workflowgraph.Column, lanes map[string]apiclient.Task) workflowgraph.Overlay {
	out := workflowgraph.Overlay{
		Nodes: map[string]workflowgraph.RunState{},
		Lanes: map[string]workflowgraph.RunState{},
	}
	// byStep is every node an authored step id can paint. A step inside a
	// fan_out lane is deliberately excluded: it runs in the lane's child
	// task, so the parent holds no row for it (decision 1).
	laneInner := map[string]bool{}
	for _, col := range laneCols {
		for _, id := range col.Nodes {
			laneInner[id] = true
		}
	}
	byStep := map[string][]string{}
	for _, n := range nodes {
		if n.StepID == "" || laneInner[n.ID] {
			continue
		}
		byStep[n.StepID] = append(byStep[n.StepID], n.ID)
	}

	newest := map[string]apiclient.StepRun{}
	order := []string{}
	for _, r := range task.Steps {
		prev, seen := newest[r.StepID]
		if !seen {
			order = append(order, r.StepID)
		}
		if !seen || r.ID >= prev.ID {
			newest[r.StepID] = r
		}
	}

	current := currentStepID(task)
	for _, stepID := range order {
		r := newest[stepID]
		ids, ok := byStep[stepID]
		if !ok {
			// An attempt no node answers for — a follow-up round's step, a
			// repair's rewrite. It is drawn off-graph rather than dropped
			// (decision 3).
			out.Off = append(out.Off, workflowgraph.OffGraphRun{
				StepID: r.StepID, Label: stepLabel(r), Type: r.StepType,
			})
			continue
		}
		rs := workflowgraph.RunState{
			State:     r.State,
			Attempt:   r.Attempt,
			Iteration: r.Iteration,
			Current:   r.StepID == current,
		}
		if r.SkipReason != nil {
			rs.SkipReason = *r.SkipReason
		}
		if rs.Current {
			rs.Task, rs.BlockReason = parkedState(task)
		}
		for _, id := range ids {
			out.Nodes[id] = rs
		}
	}
	sort.Slice(out.Off, func(i, j int) bool { return out.Off[i].StepID < out.Off[j].StepID })

	// A lane's caption is the only place its child task's state can be told,
	// so it is filled the way a node's is rather than with the state alone:
	// a lane blocked on `worktree_dirty` has to say `worktree_dirty` on the
	// caption, because the lane's own steps run in the child and the parent
	// paints none of them (decision 1).
	for _, col := range laneCols {
		child, ok := lanes[col.ID]
		if !ok {
			continue
		}
		rs := workflowgraph.RunState{State: child.State, ChildTaskID: child.ID}
		rs.Task, rs.BlockReason = parkedFrom(child.State, child.BlockReason, child.PauseRequested)
		out.Lanes[col.Key] = rs
	}
	return out
}

// currentStepID is the authored step the task's cursor is on. It is the
// snapshot's step at CurrentStep, which the detail view already carries as
// the step rows' own ids.
func currentStepID(task apiclient.TaskDetail) string {
	if task.CurrentStep < 0 || task.CurrentStep >= len(task.WorkflowSteps) {
		return ""
	}
	return task.WorkflowSteps[task.CurrentStep].ID
}

// parkedState is the task-level state worth painting on the step that owns
// it: a task waiting on a human or stopped by the daemon says *where* it is
// stuck, which is the gap this tab exists to close.
func parkedState(task apiclient.TaskDetail) (state, reason string) {
	return parkedFrom(task.State, task.BlockReason, task.PauseRequested)
}

// parkedFrom is the judgement itself, over the three fields it needs. It is
// separate from parkedState because a fan_out lane is parked on exactly these
// terms and carries them on an `apiclient.Task` rather than a TaskDetail: one
// definition, so a lane and the task it hangs off cannot disagree about what
// `blocked` means.
func parkedFrom(state string, blockReason *string, pauseRequested bool) (string, string) {
	switch state {
	case "blocked", "awaiting_input":
	default:
		if pauseRequested {
			return "paused", ""
		}
		return "", ""
	}
	reason := ""
	if blockReason != nil {
		reason = *blockReason
	}
	return state, reason
}

// sizeWorkflow gives the component the room the tab body leaves it. The
// TooNarrow fallback is measured at *tab-body* width, not at layer width:
// this pane is one tab of a workspace, and a threshold measured anywhere else
// would misreport the terminal (task 017 decision 8).
func (t *taskView) sizeWorkflow() {
	w := t.workflow
	if w == nil {
		return
	}
	w.width, w.height = t.width, max(t.height-workflowTabChrome, 1)
	w.graph.SetSize(max(w.width, 1), max(w.height, 1))
}

// workflowTabChrome is the tab strip, its blank line, the inspector rule and
// the two inspector rows. It is fixed so the viewport's arithmetic does not
// change when a node with more detail is selected.
const workflowTabChrome = 7

// updateWorkflowKey is the tab's keyboard. It is deliberately short: `tab`,
// `[`/`]` and `1`–`5` are the workspace's and never reach here, and `e`/`R`
// from the workflows-screen layer are meaningless against a snapshot — there
// is no file to open and no registry entry to re-read.
func (t *taskView) updateWorkflowKey(msg tea.KeyPressMsg) tea.Cmd {
	w := t.workflow
	if w == nil {
		return nil
	}
	updated, cmd := w.graph.Update(msg)
	w.graph = updated
	return cmd
}

func (t *taskView) renderWorkflow(width, height int) string {
	w := t.workflow
	if w == nil {
		return ""
	}
	t.width = width
	w.width, w.height = width, max(height-workflowTabChrome+2, 1)
	w.graph.SetSize(max(width, 1), max(w.height, 1))

	switch {
	case w.err != "":
		return "\n  " + styleBad.Render("⚠ "+w.err)
	case len(w.findings) > 0:
		return "\n" + w.renderFindings(width)
	case w.loading && !w.loaded:
		return "\n  " + styleDim.Render("loading the task's workflow…")
	case !w.loaded:
		return "\n  " + styleDim.Render("no workflow to draw")
	case w.graph.TooNarrow():
		return "\n  " + styleWarn.Render(fmt.Sprintf(
			"the terminal is too narrow to draw the graph — it needs at least %d columns",
			w.graph.MinWidth()))
	}
	return w.graph.View() + "\n" + w.renderInspector(width)
}

// renderFindings says a snapshot did not parse, rather than leaving the pane
// blank. It is the same 200-with-findings contract the workflows screen
// renders (task 017 decision 11), and a snapshot that does not parse is worth
// shouting about: it was valid when the task was created.
func (w *workflowTab) renderFindings(width int) string {
	rows := []string{"  " + styleBad.Render("⚠ this task's workflow snapshot does not parse")}
	for _, f := range w.findings {
		line := f.Message
		if f.Line > 0 {
			line = "line " + strconv.Itoa(f.Line) + ": " + line
		}
		rows = append(rows, "    "+styleDim.Render(ansi.Truncate(line, max(width-4, 1), "…")))
	}
	return strings.Join(rows, "\n")
}

// renderInspector is the strip under the graph: the selected node's identity
// and what its step did. Run state comes first — this surface exists to
// answer "where is it", and the authored fields are the same ones the
// workflows screen shows.
func (w *workflowTab) renderInspector(width int) string {
	rule := styleDim.Render(strings.Repeat("─", max(width, 1)))
	node, ok := w.graph.SelectedNode()
	if !ok {
		return rule + "\n" + styleDim.Render("  no node selected")
	}
	var parts []string
	if rs, reached := w.graph.Overlay().Reached(node.ID); reached {
		parts = append(parts, "run: "+describeRun(rs))
	} else if node.StepID != "" {
		parts = append(parts, "run: not reached")
	}
	for _, f := range w.graph.Detail() {
		parts = append(parts, f.Label+": "+f.Value)
	}
	return rule + "\n  " + ansi.Truncate(strings.Join(parts, "   "), max(width-2, 1), "…")
}

// describeRun spells one node's run state in words. The glyph on the node is
// the shape; this is the sentence.
func describeRun(rs workflowgraph.RunState) string {
	var b strings.Builder
	if rs.Task != "" {
		b.WriteString(rs.Task)
		if rs.BlockReason != "" {
			b.WriteString(" (" + rs.BlockReason + ")")
		}
		b.WriteString(" · ")
	}
	b.WriteString(rs.State)
	if rs.State == "skipped" {
		if rs.SkipReason != "" {
			b.WriteString(" (a false if: guard)")
		} else {
			b.WriteString(" (by hand)")
		}
	}
	if rs.Iteration > 0 {
		b.WriteString(" · iteration " + strconv.Itoa(rs.Iteration))
	}
	if rs.Attempt > 0 {
		b.WriteString(" · attempt " + strconv.Itoa(rs.Attempt))
	}
	return b.String()
}
