package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The archived boards (§15 view 10, task 092). They are the live boards in a
// second mode rather than two new screens: `internal/tui/board.go` is where
// grouping, folding, `/` filtering and the bulk selection live, and an archive
// needs every one of them. A copy would drift on the first change to any of
// the four (decision 5).
//
// What the mode adds is the three things only an archive has: a date window,
// pages, and a permanent delete. Delete is not a §6 action — it never appears
// in `available_actions`, which is what gates every scopeTaskAction binding —
// so its key is a scopePanel binding on these two contexts and nowhere else.

// archivedPageSize is how many rows one page of an archived board asks for.
// An archive grows without bound, which is the whole reason the live boards
// exclude it; a page is what keeps the first paint bounded.
const archivedPageSize = 100

// archivedWindow is one of the date presets `d` cycles. Days of 0 means "all",
// which sends no bound at all.
type archivedWindow struct {
	days  int
	label string
}

// archivedWindows are the presets, in the order `d` walks them. Seven days is
// the opening window because the row a human wants to delete is nearly always
// one they archived this week; "all" is last because it is the expensive one.
var archivedWindows = []archivedWindow{
	{days: 7, label: "last 7 days"},
	{days: 30, label: "last 30 days"},
	{days: 0, label: "all time"},
}

// since turns a preset into the ArchivedSince bound, or the zero time for
// "all". The arithmetic is client-side: the daemon takes an absolute RFC3339
// instant, and a relative window resolved on the server would move under a
// board that is paging through it.
func (w archivedWindow) since(now time.Time) time.Time {
	if w.days == 0 {
		return time.Time{}
	}
	return now.AddDate(0, 0, -w.days)
}

// archivedPager is the date window and the page offset the two archived
// boards share. Both boards embed it so the two `d`/`<`/`>` implementations
// cannot disagree about what a press means.
type archivedPager struct {
	window int
	page   int
}

func (p *archivedPager) activeWindow() archivedWindow { return archivedWindows[p.window] }

// cycleWindow advances the preset and returns to page 1: the page number is an
// offset into a listing that just changed length, so keeping it would page
// into a window the human never looked at.
func (p *archivedPager) cycleWindow() {
	p.window = (p.window + 1) % len(archivedWindows)
	p.page = 0
}

// nextPage advances only when the page that is on screen was full. A short
// page is the end of the listing, and there is no count to compare against —
// the endpoint answers with rows, not with a total.
func (p *archivedPager) nextPage(shown int) bool {
	if shown < archivedPageSize {
		return false
	}
	p.page++
	return true
}

func (p *archivedPager) prevPage() bool {
	if p.page == 0 {
		return false
	}
	p.page--
	return true
}

// label is the one line the board writes above the rows: which window it is
// showing and, once past the first, which page.
func (p *archivedPager) label() string {
	s := p.activeWindow().label
	if p.page > 0 {
		s += fmt.Sprintf(" · page %d", p.page+1)
	}
	return s
}

// rowDeletePrompt is the archived boards' confirmation. It is its own type rather
// than an actionBar pending action for the reason the whole feature is: delete
// is not a §6 action, and actionBar's pending state is keyed on one.
//
// Three answers, not two. `y` deletes the rows; `b` deletes them *and* asks
// for their branches; `n` and esc do nothing. The branch answer is a third key
// rather than a second prompt because a human who meant "and the branch" has
// already decided, and §10's rule means the extra answer cannot destroy
// anything a bare `y` would have kept: a branch carrying commits is reported
// and kept whatever was pressed.
type rowDeletePrompt struct {
	ids   []int64
	chats bool
}

// question is what the prompt reads on screen. It names the consequence — and
// names the one thing the answer cannot do — the way confirmPrompt does.
func (p *rowDeletePrompt) question() string {
	noun := "task"
	if p.chats {
		noun = "chat"
	}
	what := fmt.Sprintf("%d %s", len(p.ids), plural(len(p.ids), noun, noun+"s"))
	if len(p.ids) == 1 {
		what = fmt.Sprintf("%s %d", noun, p.ids[0])
	}
	return fmt.Sprintf(
		"delete %s permanently? [y] row and transcripts · [b] and its branch (one with commits is kept) · [n] no",
		what)
}

// deleteResultMsg is the outcome of one sweep: one thing a human asked for and
// one answer, the shape bulkResultMsg has and for the same reason.
type deleteResultMsg struct {
	chats bool
	total int
	// done are the ids the daemon deleted; refused are the ones it answered
	// 409 for, which on an archived board means a lane or a handoff is
	// holding on; failed is everything else.
	done     []int64
	refused  []bulkFailure
	failed   []bulkFailure
	branches int
}

// summary is the line the board shows afterwards. Refusals are counted
// separately from failures because they are not the same news: a refusal names
// a row that is holding on and is acted on by deleting that row first.
func (m deleteResultMsg) summary() string {
	parts := []string{fmt.Sprintf("delete · %d of %d", len(m.done), m.total)}
	if m.branches > 0 {
		parts = append(parts, fmt.Sprintf("%s deleted", plural(m.branches, "branch", "branches")))
	}
	if len(m.refused) > 0 {
		parts = append(parts, fmt.Sprintf("%d refused: %s",
			len(m.refused), errString(m.refused[0].err)))
	}
	if len(m.failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d failed: %s",
			len(m.failed), errString(m.failed[0].err)))
	}
	return strings.Join(parts, " · ")
}

// bad reports whether the report should be shown as a problem.
func (m deleteResultMsg) bad() bool { return len(m.refused) > 0 || len(m.failed) > 0 }

// dispatchDelete sends one DELETE per row, sequentially, in the order the
// board handed them over.
//
// One call per row, because there is no bulk endpoint and there is not going
// to be one (task 011's decision, verbatim): §13.2 lives in the API, and a
// second definition of what a delete means is a second thing to keep in step.
// Sequential for the reason dispatchBulk is — the line a human reads afterwards
// has to be built from outcomes that actually happened.
func dispatchDelete(client *apiclient.Client, ids []int64, branch, chats bool) tea.Cmd {
	if client == nil || len(ids) == 0 {
		return nil
	}
	ids = slices.Clone(ids) // the selection may change while the sweep runs
	return func() tea.Msg {
		msg := deleteResultMsg{chats: chats, total: len(ids)}
		for _, id := range ids {
			out, err := deleteWithTimeout(client, id, branch, chats)
			switch {
			case err == nil:
				msg.done = append(msg.done, id)
				if out.Result == apiclient.BranchDeleted {
					msg.branches++
				}
			case refusedDelete(err):
				msg.refused = append(msg.refused, bulkFailure{id: id, err: err})
			default:
				msg.failed = append(msg.failed, bulkFailure{id: id, err: err})
			}
		}
		return msg
	}
}

// refusedDelete is a 409 from the daemon: a lane or a handoff is holding on,
// or the row was not archived after all — which a board can race into when a
// row was un-archived under it.
func refusedDelete(err error) bool {
	_, ok := apiclient.DeleteRefused(err)
	return ok
}

func deleteWithTimeout(client *apiclient.Client, id int64, branch, chat bool) (apiclient.BranchOutcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if chat {
		return client.DeleteChat(ctx, id, branch)
	}
	return client.DeleteTask(ctx, id, branch)
}
