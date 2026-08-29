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
	// GroupOffGraph frames the attempts a task ran that its snapshot does not
	// declare — a follow-up round's step, which `internal/api/actions.go`
	// calls one that "is not part of the snapshot", and a repair's rewrite
	// (task 050 decision 3). It hangs below the single END node, which task
	// 017 decision 16 reserved as the runtime overlay's anchor for *reached*:
	// these attempts are neither dropped nor smuggled into the topology as if
	// the workflow had declared them.
	GroupOffGraph GroupKind = "off-graph"
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
	// offGroupID frames the runs no node answers for (decision 3).
	offGroupID = syntheticPrefix + "group:off"
)

func mergeNodeID(stepID string) string { return syntheticPrefix + "merge:" + stepID }
func refNodeID(stepID, lane string) string {
	return syntheticPrefix + "ref:" + stepID + ":" + lane
}
func groupID(stepID string) string   { return syntheticPrefix + "group:" + stepID }
func offNodeID(stepID string) string { return syntheticPrefix + "off:" + stepID }

// LaneKey names one fan_out lane across the whole diagram: a lane id alone is
// unique only within its own step, the same way a lane's step ids are
// (task 014 decision 4). It is the key a runtime overlay hangs a lane's child
// task off (task 050 decision 1).
func LaneKey(fanOutNodeID, laneID string) string { return fanOutNodeID + "." + laneID }

// lanePrefix namespaces a lane's inline step ids. Step-id uniqueness is *per
// body*, so a top-level `build` and a lane's `build` are two different steps
// — and were, until task 050, two nodes answering to one id, which made the
// selection ambiguous and would have made a parent `step_run` paint both
// (decision 2). Node.StepID keeps the raw id, which is what a join against
// `step_run.step_id` still compares.
func lanePrefix(fanOutNodeID, laneID string) string {
	return LaneKey(fanOutNodeID, laneID) + "/"
}

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
	ID string
	// Key is the lane's diagram-wide name — LaneKey(header, ID) — because a
	// lane id is unique only inside its own fan_out. It is what a runtime
	// overlay keys a lane's child task by; empty for the columns nothing
	// names.
	Key    string
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

// OffGraphRun is one attempt the task ran that the snapshot does not declare
// (task 050 decision 3): a follow-up round's step, or a repair's rewrite.
type OffGraphRun struct {
	// StepID is the run's `step_id`, which is what names the node.
	StepID string
	// Label is what the box prints — the run's step name, else its id.
	Label string
	// Type is the run's `step_type`, printed as the node's kind so an
	// off-snapshot command and an off-snapshot agent still read apart.
	Type string
}

// AttachOffGraph hangs runs that no node in d answers for under the single
// END node, as a frame of their own. It re-derives nothing else: the returned
// diagram is d with nodes and one group added, so every authored node keeps
// its identity and a re-layout keeps a selection (decision 3 of task 017).
//
// The frame is anchored on END rather than linked to it by an edge: these
// attempts did not flow out of the workflow's last step, they happened after
// the authored flow was over, and drawing a connector would claim otherwise.
func AttachOffGraph(d Diagram, runs []OffGraphRun) Diagram {
	if len(runs) == 0 {
		return d
	}
	col := Column{}
	nodes := make([]Node, 0, len(runs))
	for _, r := range runs {
		id := offNodeID(r.StepID)
		label := r.Label
		if label == "" {
			label = r.StepID
		}
		kind := NodeKind(r.Type)
		if kind == "" {
			kind = KindAgent
		}
		nodes = append(nodes, Node{
			ID: id, Kind: kind, Label: label, StepID: r.StepID,
			Group:  offGroupID,
			Detail: []DetailField{{Label: "id", Value: r.StepID}, {Label: "type", Value: r.Type}, {Label: "off-snapshot", Value: "ran outside the authored flow"}},
		})
		col.Nodes = append(col.Nodes, id)
	}
	out := d
	out.Nodes = append(append([]Node{}, d.Nodes...), nodes...)
	out.Groups = append(append([]Group{}, d.Groups...), Group{
		ID: offGroupID, Kind: GroupOffGraph, Label: "off-snapshot",
		Header: EndNodeID, Columns: []Column{col},
	})
	return out
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
	// prefix namespaces the node ids of the body being built. Only a fan_out
	// lane sets one: a lane's inline steps are their own step-id namespace
	// (§7.6), every other body shares its parent's (decision 2).
	prefix string
}

// id is the node id an authored step gets in this body.
func (f flow) id(stepID string) string { return f.prefix + stepID }

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
			sf.next = f.id(steps[i+1].ID)
		}
		stepExits := b.step(st, sf)
		if i == 0 {
			entry = f.id(st.ID)
		} else {
			b.linkAll(prev, f.id(st.ID), EdgeFlow)
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
		b.link(f.id(st.ID), f.end, EdgeBranch, "false")
		return []string{f.id(st.ID)}
	case KindBreak:
		b.add(b.plainNode(st, f))
		b.link(f.id(st.ID), f.brk, EdgeBranch, "true")
		return []string{f.id(st.ID)}
	case KindInclude:
		// One collapsed node labelled with the workflow it splices in. The
		// graph draws the file as authored, and as authored this *is* one
		// step; what it expands to is decided at task creation, against a
		// registry this view is not resolving (task 019 decision 12).
		n := b.plainNode(st, f)
		n.Kind, n.Label = KindWorkflowRef, st.Workflow
		b.add(n)
		return []string{f.id(st.ID)}
	default:
		b.add(b.plainNode(st, f))
		return []string{f.id(st.ID)}
	}
}

func (b *builder) parallel(st apiclient.WorkflowStepDef, f flow) []string {
	b.add(b.plainNode(st, f))
	header := f.id(st.ID)
	g := Group{
		ID:     groupID(header),
		Kind:   GroupParallel,
		Label:  st.DisplayName(),
		Header: header,
		Parent: f.group,
	}
	// A parallel group's members share their parent's step-id namespace, so
	// the prefix carries straight through.
	inner := flow{next: f.next, end: f.end, brk: f.brk, group: g.ID, prefix: f.prefix}
	var exits []string
	for _, member := range st.Steps {
		memberExits := b.step(member, inner)
		b.link(header, inner.id(member.ID), EdgeFlow, "")
		exits = append(exits, memberExits...)
		g.Columns = append(g.Columns, Column{Nodes: []string{inner.id(member.ID)}})
	}
	b.d.Groups = append(b.d.Groups, g)
	// A parallel group's join is the members finishing, not an operation, so
	// it gets no node: every member simply flows on to whatever follows
	// (§7.5 — no branch, no child task, nothing to merge).
	return exits
}

func (b *builder) fanOut(st apiclient.WorkflowStepDef, f flow) []string {
	b.add(b.plainNode(st, f))
	header := f.id(st.ID)
	merge := mergeNodeID(header)
	g := Group{
		ID:     groupID(header),
		Kind:   GroupFanOut,
		Label:  st.DisplayName(),
		Header: header,
		Parent: f.group,
	}
	for _, lane := range st.Lanes {
		col := Column{
			ID: lane.ID, Key: LaneKey(header, lane.ID),
			Label: lane.ID, Detail: laneDetail(lane),
		}
		if lane.If != "" {
			col.Badges = append(col.Badges, "if")
		}
		// Each lane is its own step-id namespace (§7.6, task 014 decision 4),
		// so each gets its own node-id namespace.
		inner := flow{
			next: merge, end: merge, brk: "", group: g.ID,
			prefix: lanePrefix(header, lane.ID),
		}
		var entry string
		var exits []string
		if lane.Workflow != "" {
			// A named lane is one collapsed node. Its body is another
			// workflow's, resolved at task creation, and 017 does not open it.
			id := refNodeID(header, lane.ID)
			b.add(Node{
				ID: id, Kind: KindWorkflowRef, Label: lane.Workflow,
				Group: g.ID, Detail: laneDetail(lane),
			})
			entry, exits = id, []string{id}
			col.Nodes = []string{id}
		} else {
			entry, exits = b.sequence(lane.Steps, inner)
			for _, s := range lane.Steps {
				col.Nodes = append(col.Nodes, inner.id(s.ID))
			}
		}
		b.link(header, entry, EdgeFlow, "")
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
	header := f.id(st.ID)
	g := Group{
		ID:     groupID(header),
		Kind:   GroupLoop,
		Label:  st.DisplayName(),
		Header: header,
		Parent: f.group,
	}
	// Inside the body, a false `condition` ends the iteration, which is what
	// `continue` means (§7.8) — so it routes to the header, exactly where the
	// body's own end routes. A `break` leaves the loop entirely: it goes
	// where the loop itself goes, never back to the header, which would draw
	// the one thing a break means not to happen.
	inner := flow{next: header, end: header, brk: f.next, group: g.ID, prefix: f.prefix}
	entry, exits := b.sequence(st.Steps, inner)
	b.link(header, entry, EdgeFlow, "")
	b.linkAll(exits, header, EdgeBack)
	col := Column{}
	for _, s := range st.Steps {
		col.Nodes = append(col.Nodes, inner.id(s.ID))
	}
	g.Columns = []Column{col}
	b.d.Groups = append(b.d.Groups, g)
	// The loop exits where it decides not to iterate again: at the header.
	return []string{header}
}

func (b *builder) plainNode(st apiclient.WorkflowStepDef, f flow) Node {
	return Node{
		ID:     f.id(st.ID),
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
