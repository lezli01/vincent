package tui

import (
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// loadStamp is what a list load carries out and back (task 132.5, decision
// 18): the project it was issued for and its place in the view's sequence.
// Commands run on their own goroutines, so an older response can land after
// a newer one, and since a project switch reloads, one issued for the
// previous project can land after the switch. Either would paint rows the
// view no longer stands for. A per-switch epoch would catch only the second;
// the pair catches both with one check.
//
// The zero stamp is untracked: tests that build a loaded message directly
// carry no stamp, and it is always accepted.
type loadStamp struct {
	project int64
	seq     uint64
}

// loadStamps is a view's one sequence. A mode stamp a view already carries —
// the boards' `archived`, the issues view's `state` — stays beside it and is
// checked next to it (task 132 decision 27): this guard orders loads and pins
// them to a project, and says nothing about which mode they were for.
type loadStamps struct {
	// project is the project of the newest load issued: a switch always
	// issues one (decision 25), so it is the view's current project.
	project int64
	issued  uint64
	applied uint64
}

// next stamps a new load for project and makes project the current one.
func (s *loadStamps) next(project int64) loadStamp {
	s.issued++
	s.project = project
	return loadStamp{project: project, seq: s.issued}
}

// accepts says whether a response may be applied: issued for the current
// project and newer than the last one applied.
func (s *loadStamps) accepts(st loadStamp) bool {
	return s.ownProject(st) && (st.seq == 0 || st.seq > s.applied)
}

// ownProject is accepts' project half alone, for a load ordered by a counter
// of its own: the board's lanes take their seq from the board's sequence but
// order it per parent (boardlanes.go).
func (s *loadStamps) ownProject(st loadStamp) bool {
	return st == (loadStamp{}) || st.project == s.project
}

// apply records st as applied. A view calls it once it has installed a
// response, which is why it is separate from accepts: the board keeps its
// rows on a failed refresh and does not count the failure as applied.
func (s *loadStamps) apply(st loadStamp) {
	if st.seq > s.applied {
		s.applied = st.seq
	}
}

// drop makes every load already issued stale and project the current one,
// for a view that has just decided to show nothing: with no project
// selected, or one it may no longer list, a response still in flight would
// install rows the screen has stopped standing for (review F1 on PR #720).
// Advancing applied past issued is what does it; a fresh stamp alone would
// not, since accepts compares against applied.
func (s *loadStamps) drop(project int64) {
	s.issued++
	s.project = project
	s.applied = s.issued
}

// forProject says whether a project-bearing view should react to ev with sel
// selected (task 132 decision 16): the stream is one global stream, filtered
// here. An event with no project — the registry, quota, a pull-request change
// the daemon does not attribute — still reaches every view, which is what
// keeps the filter correct without a daemon change (decision 28).
// A sel of 0 is "no project", and an event for any project passes then too:
// nothing is selected to filter by.
//
// A project.* event passes whatever project it names. The store attributes
// it to the project itself, but it describes the project list, which every
// view still renders whole: a foreign create, rename or delete must reach
// the views that show that project's rows, headings or blocks.
func forProject(ev apiclient.Event, sel int64) bool {
	return ev.ProjectID == nil || sel == 0 || *ev.ProjectID == sel ||
		strings.HasPrefix(ev.Type, "project.")
}
