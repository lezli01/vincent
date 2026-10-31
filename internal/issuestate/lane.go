package issuestate

// Lane is where an issue sits on the issues board (spec §5.6, task 134
// decisions 1–4). It is derived, never stored (decision 1): from the issue's
// own state and from its root tasks, which internal/store reduces in SQL to
// the three facts LaneOf takes. This package never sees a task state, so it
// stays free of internal/taskstate.
type Lane string

// The four lanes (decision 2; `hand_off` narrowed to readiness by task 137
// decision 13).
const (
	// LaneOpen: no root task is unsettled and none finished — no task at
	// all, or only aborted ones (decision 19).
	LaneOpen Lane = "open"
	// LaneInProgress: some root task is not settled, or work finished but
	// is not ready to hand off — its latest delivery was marked not ready,
	// or the only finished work is a side task's. The second is idle: it
	// waits for someone to mark it ready, or for more work.
	LaneInProgress Lane = "in_progress"
	// LaneHandOff: every root task is settled and the latest main-line one
	// that finished `done` — or was archived from `done` (decision 20) —
	// carries a ready delivery. The work is back with a person to close.
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

// LaneOf is the lane rule (decisions 2 and 4, task 137 decision 13, task 140
// decision 5). hasUnsettled is whether any root task of the issue is not
// settled, or a chat on the issue is live; hasFinished whether any root
// task, main-line or side, is `done` or archived from `done`; latestReady
// whether the latest finished main-line root task exists and its delivery is
// ready; chatLatest whether a chat opened on the issue, and not handed off,
// is the latest main-line work — created after that task finished, or with
// no such task at all. A chat is work in progress whatever its own state, so
// chatLatest wins over a ready delivery: the person went back to the issue.
// Closing wins over everything; a reopened issue goes back to the lane its
// work gives it. s is normalized first, so an unknown stored state reads as
// open, as everywhere else.
func LaneOf(s State, hasUnsettled, hasFinished, latestReady, chatLatest bool) Lane {
	switch {
	case Normalize(s) == Closed:
		return LaneDone
	case hasUnsettled, chatLatest:
		return LaneInProgress
	case latestReady:
		return LaneHandOff
	case hasFinished:
		return LaneInProgress
	default:
		return LaneOpen
	}
}
