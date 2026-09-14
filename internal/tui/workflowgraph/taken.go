package workflowgraph

import "strings"

// Which edges a run took (task 097 decisions 4 and 5). It is a pure function
// of the diagram, the scene and the overlay, and it uses only the words the
// overlay already carries — `running`, `succeeded`, `stopped` — so the rule
// lives next to the topology it reads and the palette stays the host's.
//
// "Both ends reached" alone is not the rule. It would light a passed
// condition's `false` edge the moment the task reached END, which draws the
// one branch the engine did not take. The engine records the verdict on the
// step's own row (§7.7, §7.8): a condition that holds `succeeded`, one that
// ends its sequence `stopped`; a break that leaves its loop `stopped`, one
// that does not `succeeded`. The newest row governs, as it does for a node.

// Step-run states the edge rules read.
const (
	runRunning   = "running"
	runSucceeded = "succeeded"
	runStopped   = "stopped"
)

// edgeRun is what the renderer needs to know about one routed edge.
type edgeRun struct {
	taken bool
	// state is the run state the edge is styled by, when stated is true: its
	// source's, else — for a derived header or merge, which has no state of
	// its own — its target's.
	state  RunState
	stated bool
}

// takenEdges answers for every edge in s.Edges, index for index.
func takenEdges(d Diagram, s Scene, o Overlay) []edgeRun {
	r := newReach(d, o)
	out := make([]edgeRun, len(s.Edges))
	for i, e := range s.Edges {
		if !r.taken(e) {
			continue
		}
		out[i].taken = true
		if rs, ok := r.stated(e.From); ok {
			out[i].state, out[i].stated = rs, true
		} else if rs, ok := r.stated(e.To); ok {
			out[i].state, out[i].stated = rs, true
		}
	}
	return out
}

// reach decides whether the run got to a node. A node with a row got there. A
// `parallel` or `loop` header, and a fan_out's merge, write no row of their
// own, so they are reached by derivation — for edge purposes only: their boxes
// have no words to restate and are never painted (decision 5).
type reach struct {
	o       Overlay
	byID    map[string]Node
	byGroup map[string][]string
	parent  map[string]string
	// laneInner is every node inside a fan_out lane. Those steps run in the
	// child task (task 051 decision 1), so no edge into or out of one is ever
	// the parent's to light.
	laneInner map[string]bool
	flowsOut  map[string][]string

	memo  map[string]bool
	doing map[string]bool
}

func newReach(d Diagram, o Overlay) *reach {
	r := &reach{
		o: o, byID: map[string]Node{}, byGroup: map[string][]string{},
		parent: map[string]string{}, laneInner: map[string]bool{},
		flowsOut: map[string][]string{},
		memo:     map[string]bool{}, doing: map[string]bool{},
	}
	for _, n := range d.Nodes {
		r.byID[n.ID] = n
		if n.Group != "" {
			r.byGroup[n.Group] = append(r.byGroup[n.Group], n.ID)
		}
	}
	for _, g := range d.Groups {
		r.parent[g.ID] = g.Parent
		if g.Kind != GroupFanOut {
			continue
		}
		for _, col := range g.Columns {
			for _, id := range col.Nodes {
				r.laneInner[id] = true
			}
		}
	}
	for _, e := range d.Edges {
		if e.Kind == EdgeFlow {
			r.flowsOut[e.From] = append(r.flowsOut[e.From], e.To)
		}
	}
	return r
}

// stated is a node's own run state: a row the parent holds for it.
func (r *reach) stated(id string) (RunState, bool) {
	if r.laneInner[id] {
		return RunState{}, false
	}
	return r.o.Reached(id)
}

func (r *reach) reached(id string) bool {
	if done, ok := r.memo[id]; ok {
		return done
	}
	// A loop body's merge flows back to its header, and the header derives
	// from the body: a node already being asked about answers "not by this
	// path" rather than recursing forever.
	if r.doing[id] {
		return false
	}
	r.doing[id] = true
	got := r.derive(id)
	delete(r.doing, id)
	r.memo[id] = got
	return got
}

func (r *reach) derive(id string) bool {
	if id == EndNodeID {
		return r.o.Done
	}
	if r.laneInner[id] {
		return false
	}
	if _, ok := r.stated(id); ok {
		return true
	}
	n, ok := r.byID[id]
	if !ok {
		return false
	}
	switch n.Kind {
	case KindParallel, KindLoop:
		for _, member := range r.byGroup[groupID(id)] {
			if r.reached(member) {
				return true
			}
		}
	case KindMerge:
		header := strings.TrimPrefix(id, mergeNodeID(""))
		if rs, ok := r.stated(header); ok && rs.State != "" && rs.State != runRunning {
			return true
		}
		for _, next := range r.flowsOut[id] {
			if r.reached(next) {
				return true
			}
		}
	}
	return false
}

// taken applies decision 4's rules to one edge.
func (r *reach) taken(e RoutedEdge) bool {
	if e.Kind == EdgeNeeds || r.laneInner[e.From] || r.laneInner[e.To] {
		return false
	}
	from, _ := r.stated(e.From)
	if e.Kind == EdgeBranch {
		// A condition's `false` and a break's `true` are the departures the
		// engine records as `stopped`, and nothing else lights them.
		return from.State == runStopped
	}
	// The onward edge of a condition or a break — an ordinary one, or the
	// back-edge when it ends a loop body — was followed only by a condition
	// that held or a break that did not leave.
	switch r.byID[e.From].Kind {
	case KindCondition, KindBreak:
		if from.State != runSucceeded {
			return false
		}
	}
	switch e.Kind {
	case EdgeBack:
		return r.reached(e.From) && r.iterated(e.To)
	case EdgeFlow:
		return r.reached(e.From) && r.reached(e.To)
	}
	return false
}

// iterated reports a loop that went round at least twice: some node in its
// body, at any depth, ran a second pass. A single-pass loop's back-edge was
// never followed.
func (r *reach) iterated(header string) bool {
	loop := groupID(header)
	for id, rs := range r.o.Nodes {
		if rs.Iteration < 2 || r.laneInner[id] {
			continue
		}
		if r.within(r.byID[id].Group, loop) {
			return true
		}
	}
	return false
}

// within reports whether group g is the group want or nested inside it.
func (r *reach) within(g, want string) bool {
	for seen := 0; g != "" && seen <= len(r.parent); seen++ {
		if g == want {
			return true
		}
		g = r.parent[g]
	}
	return false
}
