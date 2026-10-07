package issuestate

// Lane is where an issue sits on the issues board (spec §5.6, task 134
// decisions 1–4). It is derived, never stored (decision 1): from the issue's
// own state and from its root tasks, which internal/store reduces in SQL to
// the two facts LaneOf takes. This package never sees a task state, so it
// stays free of internal/taskstate.
type Lane string

// The four lanes (decision 2).
const (
	// LaneOpen: no root task is unsettled and none was handed off — no task
	// at all, or only aborted ones (decision 19).
	LaneOpen Lane = "open"
	// LaneInProgress: some root task is not settled.
	LaneInProgress Lane = "in_progress"
	// LaneHandOff: every root task is settled and at least one finished
	// `done` — or was archived from `done` (decision 20). The work is back
	// with a person to close.
	LaneHandOff Lane = "hand_off"
	// LaneDone: the issue is closed, whatever its tasks are doing
	// (decision 4).
	LaneDone Lane = "done"
)

// Lanes lists every lane in board order.
var Lanes = []Lane{LaneOpen, LaneInProgress, LaneHandOff, LaneDone}

// ValidLane reports whether s names a lane.
func ValidLane(s string) bool {
	for _, l := range Lanes {
		if string(l) == s {
			return true
		}
	}
	return false
}

// LaneOf is the lane rule (decisions 2 and 4). hasUnsettled is whether any
// root task of the issue is not settled; hasDone whether any root task is
// `done` or archived from `done`. Closing wins over live tasks; a reopened
// issue goes back to the lane its tasks give it. s is normalized first, so
// an unknown stored state reads as open, as everywhere else.
func LaneOf(s State, hasUnsettled, hasDone bool) Lane {
	switch {
	case Normalize(s) == Closed:
		return LaneDone
	case hasUnsettled:
		return LaneInProgress
	case hasDone:
		return LaneHandOff
	default:
		return LaneOpen
	}
}
