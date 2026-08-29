package workflowgraph

import "strconv"

// The runtime overlay (task 051) is what turns a definition viewer into a
// picture of one task. It is deliberately a *second* input to Render rather
// than a rewrite of the Diagram: the diagram is what the workflow says, the
// overlay is what happened, and keeping them apart is what lets an overlay
// land without a re-layout — every node keeps the coordinates it had, so a
// reader watching a running task never has the picture move under them
// (task 017 decisions 3 and 5).
//
// Meaning is carried by words and glyphs, never by color (task 017 decision
// 6). A false `if:` guard (§7.7), the human `skip` action (§6) and a node the
// task never reached are three different things, and they read as three
// different things with every escape sequence stripped:
//
//	skipped if   — the guard was false
//	skipped      — a human skipped it
//	(nothing)    — never reached

// RunState is what one node's step did in this task. The zero value is "never
// reached", which is why the overlay's maps hold only the nodes that ran.
type RunState struct {
	// State is the §5.4 step-run state of the newest attempt: running,
	// succeeded, failed, interrupted, approved, rejected, skipped, stopped.
	State string
	// SkipReason is "condition" for a guard skip and empty for the human
	// `skip` action — the two share State "skipped" and mean different
	// things.
	SkipReason string
	// Attempt is the newest attempt's 1-based number; a retry is a fact
	// about the run worth seeing without selecting.
	Attempt int
	// Iteration is which pass of an enclosing `loop` produced it, 0 outside
	// a loop (§7.8). The graph draws a loop once with a back-edge, so this
	// is how far round it is, not another row of nodes.
	Iteration int
	// Current marks the node the task's cursor is on.
	Current bool
	// Task is the task-level state when it is a fact about *this* node —
	// `blocked`, `awaiting_input` or `paused` on the step that owns the
	// parked task, and empty everywhere else.
	Task string
	// BlockReason is the §12.2 `block_reason` behind Task, as its
	// snake_case constant.
	BlockReason string
	// ChildTaskID is a fan_out lane's child task (§7.6), 0 elsewhere. It
	// rides on the lane caption, never on the lane's inline step nodes: those
	// run in the child, so the parent holds no step_run for them and cannot
	// honestly paint them (task 051 decision 1).
	ChildTaskID int64
}

// Overlay is the whole runtime picture for one task.
type Overlay struct {
	// Nodes is keyed by Node.ID. A node absent from it was never reached.
	Nodes map[string]RunState
	// Lanes is keyed by Column.Key — LaneKey(fanOutNodeID, laneID) — because
	// a lane id alone is unique only inside its own fan_out.
	Lanes map[string]RunState
	// Off is the attempts no node answers for, drawn under END by
	// AttachOffGraph (decision 3).
	Off []OffGraphRun
}

// Empty reports an overlay with nothing to say — a task that has not run a
// step yet, whose every node is pending and which has no current marker.
func (o Overlay) Empty() bool {
	return len(o.Nodes) == 0 && len(o.Lanes) == 0 && len(o.Off) == 0
}

// stateGlyph is the marker a node prints before its label. It is a shape, not
// a color: `✔` and `✖` are still different characters in a screenshot with
// every style stripped.
func stateGlyph(rs RunState) string {
	switch rs.Task {
	case "blocked":
		return "!"
	case "awaiting_input":
		return "?"
	case "paused":
		return "⏸"
	}
	switch rs.State {
	case "running":
		return "▶"
	case "succeeded", "approved":
		return "✔"
	case "failed", "rejected":
		return "✖"
	case "interrupted":
		return "⚡"
	case "skipped":
		return "⊘"
	case "stopped":
		return "■"
	}
	if rs.Current {
		return "›"
	}
	return ""
}

// stateWords are the badges a node prints for its run: the state, what
// qualifies it, and how many times it has been tried. They go on the node's
// label row, where a truncation costs a label a reader can recover by
// selecting rather than the state this whole surface exists to show.
func stateWords(rs RunState) []string {
	var out []string
	if rs.Task != "" {
		out = append(out, rs.Task)
		if rs.BlockReason != "" {
			out = append(out, rs.BlockReason)
		}
	}
	if rs.State != "" && rs.State != rs.Task {
		word := rs.State
		if rs.State == "skipped" && rs.SkipReason != "" {
			// "skipped if" is a false guard; a bare "skipped" is a human.
			word += " if"
		}
		out = append(out, word)
	}
	if rs.Iteration > 0 {
		out = append(out, "it "+strconv.Itoa(rs.Iteration))
	}
	if rs.Attempt > 1 {
		out = append(out, "try "+strconv.Itoa(rs.Attempt))
	}
	return out
}

// Reached reports whether the overlay says anything about a node at all.
func (o Overlay) Reached(id string) (RunState, bool) {
	rs, ok := o.Nodes[id]
	return rs, ok
}
