package tui

import (
	tea "charm.land/bubbletea/v2"
)

// Stepping to the neighbouring task or issue (#1077). Chats had alt+. and
// alt+,; a task workspace and an opened issue had no way to the next
// one but going back to the board. The same keys walk the board's own order
// — the order the reader sees, filters and sort included — and wrap.

// stepItemMsg asks the root for the task (issue=false) or issue after, or
// before, from.
type stepItemMsg struct {
	from  int64
	delta int
	issue bool
}

func stepItemCmd(from int64, delta int, issue bool) tea.Cmd {
	if from == 0 {
		return nil
	}
	return func() tea.Msg { return stepItemMsg{from: from, delta: delta, issue: issue} }
}

// stepItem opens the neighbour of msg.from in the board's order, the way
// any open of it would. Nothing happens when from is not on the board — a
// filter hid it, or it is a lane — or when it is alone there.
func (m *root) stepItem(msg stepItemMsg) tea.Cmd {
	if msg.issue {
		v, ok := m.views[viewIssues].(*issuesView)
		if !ok {
			return nil
		}
		rows := v.rows()
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.issue.ID
		}
		next, ok := stepIn(ids, msg.from, msg.delta)
		if !ok {
			return nil
		}
		v.selectIssue(next)
		open := openIssueMsg{id: next, projectID: v.project.id, back: viewIssues}
		if iv, ok := m.views[viewIssue].(*issueView); ok {
			// The next issue opens on the tab this one was read on (#1157).
			open.back, open.keep, open.keepTab = iv.back, true, iv.tab
		}
		return m.openObject(open)
	}
	s, ok := m.views[viewHome].(*shell)
	if !ok {
		return nil
	}
	var ids []int64
	if s.board.cards {
		// The cards read column by column, the order the eye walks them.
		for _, col := range s.board.cardStops(s.board.cardGroups()) {
			for _, st := range col {
				if !st.header {
					ids = append(ids, st.id)
				}
			}
		}
	} else {
		for _, t := range s.board.visible() {
			ids = append(ids, t.ID)
		}
	}
	next, ok := stepIn(ids, msg.from, msg.delta)
	if !ok {
		return nil
	}
	t, _ := s.board.taskByID(next)
	open := selectTaskMsg{id: next, state: t.State, projectID: t.ProjectID}
	// The next task opens on the tab this one was read on (#1157).
	if tv, ok := m.views[viewTask].(*taskView); ok && m.active == viewTask {
		open.keep, open.keepTab = true, tv.tab
	}
	return m.openObject(open)
}

// stepIn is the id delta places from from in ids, wrapping; false when from
// is not there or is alone.
func stepIn(ids []int64, from int64, delta int) (int64, bool) {
	n := len(ids)
	for i, id := range ids {
		if id == from {
			if n < 2 {
				return 0, false
			}
			return ids[((i+delta)%n+n)%n], true
		}
	}
	return 0, false
}
