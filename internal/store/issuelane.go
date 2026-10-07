package store

import (
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/taskstate"
)

// The SQL that derives an issue's activity, lane and attention from its root
// tasks (spec §5.6, task 134 decisions 1–3). It is written once here and
// every reader — issueSelect, ActiveIssueTaskIDs, projectIssueStats and the
// lane filter of ListIssues — builds from it, so the rule cannot drift
// between a row, a count and a filter. Each fragment returns its SQL and the
// bind arguments it needs, in order; the state sets come from taskstate, so
// a new §6 state reaches every reader without an edit here.
//
// Only root tasks count (task 130 decision 5): a fan-out lane is part of its
// parent's work, never a piece of the issue's own. Every fragment correlates
// on the issue alias `i`.

// sqlFrag is a SQL fragment and its bind arguments, in order.
type sqlFrag struct {
	sql  string
	args []any
}

// statesWhere lists the task states for which keep is true, as bind args.
func statesWhere(keep func(taskstate.State) bool) []any {
	var out []any
	for _, s := range taskstate.All {
		if keep(s) {
			out = append(out, string(s))
		}
	}
	return out
}

// settledTaskStates is §6's settled set, derived from taskstate rather than
// spelled out here, so a new settled state reaches Active without an edit.
func settledTaskStates() []any { return statesWhere(taskstate.Settled) }

// needsHumanTaskStates is the set taskstate.NeedsHuman names (decision 3).
func needsHumanTaskStates() []any { return statesWhere(taskstate.NeedsHuman) }

// rootTaskExists is EXISTS over the issue's root tasks matching cond.
func rootTaskExists(cond sqlFrag) sqlFrag {
	return sqlFrag{
		sql: `EXISTS (SELECT 1 FROM tasks t WHERE t.issue_id = i.id AND t.parent_task_id IS NULL
			AND ` + cond.sql + `)`,
		args: cond.args,
	}
}

// unsettledCond matches a task that is not settled — Issue.Active's rule.
func unsettledCond() sqlFrag {
	settled := settledTaskStates()
	return sqlFrag{`t.state NOT IN ` + placeholders(len(settled)), settled}
}

// handedOffCond matches a task that finished `done`, including one archived
// from `done` (decision 20): archiving tidies the board, it does not undo
// the hand-off.
func handedOffCond() sqlFrag {
	return sqlFrag{
		`(t.state = ? OR (t.state = ? AND t.archived_from = ?))`,
		[]any{string(taskstate.Done), string(taskstate.Archived), string(taskstate.Done)},
	}
}

// attentionCond matches a task waiting on a person (decision 3). `paused`
// is not one: a person parked it and nobody is being asked anything.
func attentionCond() sqlFrag {
	nh := needsHumanTaskStates()
	return sqlFrag{`t.state IN ` + placeholders(len(nh)), nh}
}

// activeExpr is Issue.Active: some root task is unsettled.
func activeExpr() sqlFrag { return rootTaskExists(unsettledCond()) }

// attentionExpr is Issue.Attention: some root task needs a person. It is
// computed for a closed issue too, whose lane is `done` regardless.
func attentionExpr() sqlFrag { return rootTaskExists(attentionCond()) }

// laneExpr is issuestate.LaneOf in SQL: closed is `done` whatever the tasks
// do (decision 4); otherwise an unsettled root task is `in_progress`, a
// handed-off one `hand_off`, and anything else — no task, or only aborted
// ones (decision 19) — `open`.
func laneExpr() sqlFrag {
	unsettled, handedOff := activeExpr(), rootTaskExists(handedOffCond())
	args := []any{string(issuestate.Closed), string(issuestate.LaneDone)}
	args = append(args, unsettled.args...)
	args = append(args, string(issuestate.LaneInProgress))
	args = append(args, handedOff.args...)
	args = append(args, string(issuestate.LaneHandOff), string(issuestate.LaneOpen))
	return sqlFrag{
		sql: `(CASE WHEN i.state = ? THEN ?
			WHEN ` + unsettled.sql + ` THEN ?
			WHEN ` + handedOff.sql + ` THEN ?
			ELSE ? END)`,
		args: args,
	}
}
