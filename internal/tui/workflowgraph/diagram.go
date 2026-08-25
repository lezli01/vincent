package workflowgraph

import (
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// NodeKind is what a node *is*. Authored steps keep their §8.2 type as their
// kind, so the vocabulary the YAML uses is the vocabulary the picture uses;
// the three synthetic kinds are the structural artifacts a control-flow graph
// needs and a step list does not.
type NodeKind string

// The authored step types, spelled as §8.2 spells them.
const (
	KindAgent     NodeKind = "agent"
	KindCommand   NodeKind = "command"
	KindManual    NodeKind = "manual"
	KindParallel  NodeKind = "parallel"
	KindFanOut    NodeKind = "fan_out"
	KindCondition NodeKind = "condition"
	KindLoop      NodeKind = "loop"
	KindBreak     NodeKind = "break"
	KindInclude   NodeKind = "include"

	// KindMerge is a fan_out's join (§7.6). It is a node because it is work
	// that runs and can block on a conflict, unlike a parallel group's join,
	// which is nothing but the members finishing.
	KindMerge NodeKind = "merge"
	// KindWorkflowRef is a reference to another registry workflow: a fan_out
	// lane naming one, or — since task 019 — an `include` step splicing one
	// in. It stays collapsed: expanding it is navigation, not rendering (task
	// 017 non-goals), and making includes the exception would give one
	// question two answers (task 019 decision 12).
	//
	// The two references differ in what they produce — a lane becomes a child
	// task, an include becomes steps in this one — which the inspector says
	// and the node does not: at node size, "this is another workflow" is the
	// whole of what fits.
	KindWorkflowRef NodeKind = "workflow_ref"
	// KindEnd terminates the top-level sequence. Exactly one exists, and only
	// at the top level: a `condition` inside a loop body ends its iteration
	// rather than the workflow, so it routes to the loop header instead
	// (decision 16).
	KindEnd NodeKind = "end"
)

// GroupKind is what a frame encloses.
type GroupKind string

// The frames a diagram draws, one per structure step that encloses others.
const (
	GroupParallel GroupKind = "parallel"
	GroupFanOut   GroupKind = "fan_out"
	GroupLoop     GroupKind = "loop"
)

// EdgeKind is why an edge exists, which is what decides how it is drawn and
// labelled. Topology must survive having color stripped (decision 6), so the
// kind is carried in the model rather than implied by a style.
type EdgeKind string

const (
	// EdgeFlow is ordinary succession: this then that.
	EdgeFlow EdgeKind = "flow"
	// EdgeBranch is a `condition`'s or `break`'s guarded departure, labelled
	// `true` or `false`.
	EdgeBranch EdgeKind = "branch"
	// EdgeBack returns to a loop header for another iteration.
	EdgeBack EdgeKind = "back"
)

// Synthetic ids are prefixed with `#`, which an authored step id may contain
// but in practice never does. The prefix is not enforced by workflow
// validation on purpose: adding a charset rule to the workflow language for
// a viewer's benefit would be a breaking change for the benefit of the wrong
// party (decision 6, round 3).
const (
	syntheticPrefix = "#"
	// EndNodeID is the id of the single terminal node.
	EndNodeID = "#end"
)

func mergeNodeID(stepID string) string { return syntheticPrefix + "merge:" + stepID }
func refNodeID(stepID, lane string) string {
	return syntheticPrefix + "ref:" + stepID + ":" + lane
}
func groupID(stepID string) string { return syntheticPrefix + "group:" + stepID }

// Synthetic reports whether an id names a structural artifact rather than an
// authored step. A builder uses it to know what it may not edit.
func Synthetic(id string) bool { return strings.HasPrefix(id, syntheticPrefix) }

// Diagram is the semantic graph: what connects to what, and what encloses
// what. It carries no coordinates — screen position is never identity
// (decision 3), which is what lets a re-layout keep a selection.
type Diagram struct {
	Nodes  []Node
	Edges  []Edge
	Groups []Group
	// Root is the top-level sequence in source order, ending at EndNodeID.
	// Layout needs the skeleton the edges alone would make it re-derive.
	Root []string
}

// Node is one box.
type Node struct {
	ID   string
	Kind NodeKind
	// Label is what the box prints: a step's authored name, else its id.
	Label string
	// Badges are the presences a reader needs without selecting — `if`,
	// `chk`, a loop's driver. Their content lives in Detail (decision 15).
	Badges []string
	// StepID is the authored step this came from, empty for a synthetic node.
	StepID string
	// Group is the enclosing group, empty at the top level.
	Group  string
	Detail []DetailField
}

// DetailField is one inspector row. The renderer prints them; nothing here
// knows how wide the strip is.
type DetailField struct {
	Label string
	Value string
}

// Edge is one connection. From and To are node ids.
type Edge struct {
	From  string
	To    string
	Kind  EdgeKind
	Label string
}

// Group is a frame. Columns are its horizontal divisions: a parallel group's
// members (one node each), a fan_out's lanes (each a sequence, or a single
// collapsed reference), or a loop's one column carrying its body.
type Group struct {
	ID    string
	Kind  GroupKind
	Label string
	// Header is the node that heads the group — the `parallel`, `fan_out` or
	// `loop` step itself, drawn above the frame.
	Header  string
	Parent  string
	Columns []Column
}

// Column is one division of a group. ID and Label are a fan_out lane's; a
// parallel member and a loop body leave them empty, because neither is a
// thing the workflow language names.
type Column struct {
	ID     string
	Label  string
	Badges []string
	Nodes  []string
	Detail []DetailField
}

// Build converts a workflow definition into its semantic graph. It is pure:
// same definition, same diagram, every time.
func Build(wf *apiclient.WorkflowBody) Diagram {
	b := &builder{}
	if wf == nil {
		return b.d
	}
	_, exits := b.sequence(wf.Steps, flow{next: EndNodeID, end: EndNodeID})
	// END is appended last so Nodes reads in source order, which is the
	// deterministic fallback order keyboard navigation falls back to.
	b.add(Node{ID: EndNodeID, Kind: KindEnd, Label: "END"})
	b.linkAll(exits, EndNodeID, EdgeFlow)
	b.d.Root = make([]string, 0, len(wf.Steps)+1)
	for _, st := range wf.Steps {
		b.d.Root = append(b.d.Root, st.ID)
	}
	b.d.Root = append(b.d.Root, EndNodeID)
	return b.d
}

// flow is the context a sequence is built in: where its guarded departures
// go. There is no "next" here — succession inside a sequence is known from
// the step list, and where the sequence *itself* goes is the caller's to
// draw, which is what lets a loop body's terminal link be a back-edge rather
// than an ordinary one.
type flow struct {
	// next is where this step's sequence goes when it finishes normally. A
	// sequence knows it for every step but the last, whose successor is the
	// caller's business — which is why it is passed in rather than derived.
	// Only `break` and `loop` read it: everything else leaves through the
	// exits the caller links.
	next string
	// end is where a false `condition` goes (§7.7): the workflow's END at the
	// top level, the loop header inside a loop body, the merge inside a lane.
	end string
	// brk is where a true `break` goes: the loop's own exit. Empty outside a
	// loop, where `break` is not valid anyway.
	brk string
	// group is the enclosing group id.
	group string
}

type builder struct {
	d Diagram
}

func (b *builder) add(n Node) { b.d.Nodes = append(b.d.Nodes, n) }

func (b *builder) link(from, to string, kind EdgeKind, label string) {
	if from == "" || to == "" {
		return
	}
	b.d.Edges = append(b.d.Edges, Edge{From: from, To: to, Kind: kind, Label: label})
}

// linkAll connects a set of exits to one target. It takes no label because a
// labelled edge is always a single guarded departure — a `condition`'s false
// or a `break`'s true — never a convergence.
func (b *builder) linkAll(from []string, to string, kind EdgeKind) {
	for _, f := range from {
		b.link(f, to, kind, "")
	}
}

// sequence builds an ordered run of steps, linking each to the next. It
// returns the entry node and the exits — the nodes the *caller* must connect
// onward.
func (b *builder) sequence(steps []apiclient.WorkflowStepDef, f flow) (entry string, exits []string) {
	var prev []string
	for i, st := range steps {
		// Each step is built knowing its own successor; the last one inherits
		// the sequence's, which is what lets a `break` inside a loop body
		// name the step after the loop.
		sf := f
		if i+1 < len(steps) {
			sf.next = steps[i+1].ID
		}
		stepExits := b.step(st, sf)
		if i == 0 {
			entry = st.ID
		} else {
			b.linkAll(prev, st.ID, EdgeFlow)
		}
		prev = stepExits
	}
	return entry, prev
}

// step builds one step and returns its exits. Escaping edges — a condition's
// false, a break's true, a loop's back-edge — are drawn here, because only
// this level knows where they go. Ordinary succession is the sequence's job.
func (b *builder) step(st apiclient.WorkflowStepDef, f flow) []string {
	switch NodeKind(st.Type) {
	case KindParallel:
		return b.parallel(st, f)
	case KindFanOut:
		return b.fanOut(st, f)
	case KindLoop:
		return b.loop(st, f)
	case KindCondition:
		b.add(b.plainNode(st, f))
		// False ends the sequence (§7.7). True is the exits, drawn by the
		// caller, and labelled there.
		b.link(st.ID, f.end, EdgeBranch, "false")
		return []string{st.ID}
	case KindBreak:
		b.add(b.plainNode(st, f))
		b.link(st.ID, f.brk, EdgeBranch, "true")
		return []string{st.ID}
	case KindInclude:
		// One collapsed node labelled with the workflow it splices in. The
		// graph draws the file as authored, and as authored this *is* one
		// step; what it expands to is decided at task creation, against a
		// registry this view is not resolving (task 019 decision 12).
		n := b.plainNode(st, f)
		n.Kind, n.Label = KindWorkflowRef, st.Workflow
		b.add(n)
		return []string{st.ID}
	default:
		b.add(b.plainNode(st, f))
		return []string{st.ID}
	}
}

func (b *builder) parallel(st apiclient.WorkflowStepDef, f flow) []string {
	b.add(b.plainNode(st, f))
	g := Group{
		ID:     groupID(st.ID),
		Kind:   GroupParallel,
		Label:  st.DisplayName(),
		Header: st.ID,
		Parent: f.group,
	}
	inner := flow{next: f.next, end: f.end, brk: f.brk, group: g.ID}
	var exits []string
	for _, member := range st.Steps {
		memberExits := b.step(member, inner)
		b.link(st.ID, member.ID, EdgeFlow, "")
		exits = append(exits, memberExits...)
		g.Columns = append(g.Columns, Column{Nodes: []string{member.ID}})
	}
	b.d.Groups = append(b.d.Groups, g)
	// A parallel group's join is the members finishing, not an operation, so
	// it gets no node: every member simply flows on to whatever follows
	// (§7.5 — no branch, no child task, nothing to merge).
	return exits
}

func (b *builder) fanOut(st apiclient.WorkflowStepDef, f flow) []string {
	b.add(b.plainNode(st, f))
	merge := mergeNodeID(st.ID)
	g := Group{
		ID:     groupID(st.ID),
		Kind:   GroupFanOut,
		Label:  st.DisplayName(),
		Header: st.ID,
		Parent: f.group,
	}
	inner := flow{next: merge, end: merge, brk: "", group: g.ID}
	for _, lane := range st.Lanes {
		col := Column{ID: lane.ID, Label: lane.ID, Detail: laneDetail(lane)}
		if lane.If != "" {
			col.Badges = append(col.Badges, "if")
		}
		var entry string
		var exits []string
		if lane.Workflow != "" {
			// A named lane is one collapsed node. Its body is another
			// workflow's, resolved at task creation, and 017 does not open it.
			id := refNodeID(st.ID, lane.ID)
			b.add(Node{
				ID: id, Kind: KindWorkflowRef, Label: lane.Workflow,
				Group: g.ID, Detail: laneDetail(lane),
			})
			entry, exits = id, []string{id}
			col.Nodes = []string{id}
		} else {
			entry, exits = b.sequence(lane.Steps, inner)
			for _, s := range lane.Steps {
				col.Nodes = append(col.Nodes, s.ID)
			}
		}
		b.link(st.ID, entry, EdgeFlow, "")
		b.linkAll(exits, merge, EdgeFlow)
		g.Columns = append(g.Columns, col)
	}
	b.d.Groups = append(b.d.Groups, g)
	b.add(Node{
		// The join is named for the step it belongs to: "merge / merge" says
		// the same thing twice, while "spread / merge" says whose join it is.
		ID: merge, Kind: KindMerge, Label: st.DisplayName(), Group: f.group,
		Badges: mergeBadges(st), Detail: mergeDetail(st),
	})
	return []string{merge}
}

func (b *builder) loop(st apiclient.WorkflowStepDef, f flow) []string {
	b.add(b.plainNode(st, f))
	g := Group{
		ID:     groupID(st.ID),
		Kind:   GroupLoop,
		Label:  st.DisplayName(),
		Header: st.ID,
		Parent: f.group,
	}
	// Inside the body, a false `condition` ends the iteration, which is what
	// `continue` means (§7.8) — so it routes to the header, exactly where the
	// body's own end routes. A `break` leaves the loop entirely: it goes
	// where the loop itself goes, never back to the header, which would draw
	// the one thing a break means not to happen.
	inner := flow{next: st.ID, end: st.ID, brk: f.next, group: g.ID}
	entry, exits := b.sequence(st.Steps, inner)
	b.link(st.ID, entry, EdgeFlow, "")
	b.linkAll(exits, st.ID, EdgeBack)
	col := Column{}
	for _, s := range st.Steps {
		col.Nodes = append(col.Nodes, s.ID)
	}
	g.Columns = []Column{col}
	b.d.Groups = append(b.d.Groups, g)
	// The loop exits where it decides not to iterate again: at the header.
	return []string{st.ID}
}

func (b *builder) plainNode(st apiclient.WorkflowStepDef, f flow) Node {
	return Node{
		ID:     st.ID,
		Kind:   NodeKind(st.Type),
		Label:  st.DisplayName(),
		StepID: st.ID,
		Group:  f.group,
		Badges: badges(st),
		Detail: stepDetail(st),
	}
}

// badges are the presences a node prints beside its type. Guard and check are
// presence-only on purpose: a template truncated into a bounded node reads as
// noise, and the inspector is where its text belongs (decision 15). A loop's
// driver is the exception — `×3` is a fact about the shape of the run, short
// enough to fit and worth seeing without selecting (decision 18).
func badges(st apiclient.WorkflowStepDef) []string {
	var out []string
	if st.If != "" {
		out = append(out, "if")
	}
	if st.Check != "" {
		out = append(out, "chk")
	}
	switch NodeKind(st.Type) {
	case KindLoop:
		switch {
		case st.Count != nil:
			out = append(out, "×"+strconv.Itoa(*st.Count))
		case len(st.ForEach) > 0:
			// How many iterations a for_each becomes is discovered when the
			// loop runs, so a definition viewer can only name the driver.
			out = append(out, "for_each")
		}
		if st.MaxIterations != nil {
			out = append(out, "max "+strconv.Itoa(*st.MaxIterations))
		}
	case KindParallel:
		if st.MaxParallel != nil {
			out = append(out, "max "+strconv.Itoa(*st.MaxParallel))
		}
	}
	return out
}

// mergeBadges marks a join that may be resolved by an agent. `block` gets no
// badge: it is the default, and the difference worth seeing is the one where
// an agent may resolve a conflict unread (§16).
func mergeBadges(st apiclient.WorkflowStepDef) []string {
	if st.Merge != nil && st.Merge.ConflictPolicy() == "agent" {
		return []string{"agent"}
	}
	return nil
}

func stepDetail(st apiclient.WorkflowStepDef) []DetailField {
	out := []DetailField{{Label: "id", Value: st.ID}, {Label: "type", Value: st.Type}}
	add := func(label, value string) {
		if value != "" {
			out = append(out, DetailField{Label: label, Value: value})
		}
	}
	add("name", st.Name)
	// An authored include says what it will splice in; a step in a task's
	// snapshot says what it was spliced *through*. They never appear
	// together, because expansion replaces the one with the other (§7.9).
	add("workflow", st.Workflow)
	if len(st.ResolvedFrom) > 0 {
		add("from", strings.Join(st.ResolvedFrom, " → "))
	}
	add("if", st.If)
	add("check", st.Check)
	add("agent", st.Agent)
	add("model", st.Model)
	add("effort", st.Effort)
	add("shell", st.Shell)
	add("on_input", st.OnInput)
	if st.AllowFailure {
		add("allow_failure", "true")
	}
	if st.MaxRetries != nil {
		add("max_retries", strconv.Itoa(*st.MaxRetries))
	}
	if st.RetryBackoff != nil {
		add("retry_backoff", *st.RetryBackoff)
	}
	if st.Timeout != nil {
		add("timeout", *st.Timeout)
	}
	return out
}

func laneDetail(lane apiclient.WorkflowLaneDef) []DetailField {
	out := []DetailField{{Label: "lane", Value: lane.ID}}
	add := func(label, value string) {
		if value != "" {
			out = append(out, DetailField{Label: label, Value: value})
		}
	}
	add("workflow", lane.Workflow)
	add("if", lane.If)
	add("agent", lane.Agent)
	add("model", lane.Model)
	if lane.Priority != nil {
		add("priority", strconv.Itoa(*lane.Priority))
	}
	return out
}

func mergeDetail(st apiclient.WorkflowStepDef) []DetailField {
	policy := "block"
	out := []DetailField{{Label: "join", Value: st.ID}}
	if st.Merge != nil {
		policy = st.Merge.ConflictPolicy()
		if st.Merge.Agent != nil {
			out = append(out, DetailField{Label: "resolver", Value: st.Merge.Agent.DisplayName()})
		}
	}
	return append([]DetailField{{Label: "on_conflict", Value: policy}}, out...)
}
