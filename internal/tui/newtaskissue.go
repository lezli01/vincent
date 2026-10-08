package tui

import (
	"context"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// A draft seeded from a vincent issue (§15 view 3, task 130.13).
//
// The source is chosen where the issue is on screen — `a` on the issue list
// or the issue detail — exactly as a pull request is chosen on its takeover
// (task 130 decision 7, superseding 035 decision 4 for this form). The form
// has no picker for it and no way to unlink it: a plain task is `esc` and
// `n` (decision 21.5).
//
// The prefill is the daemon's, previewed from GET /v1/issues/{id}?workflow=W
// for the workflow the draft settles on; the TUI never computes one (035
// decision 2). Declared fields differ per workflow, so unlike the pull
// request's apply-once seed it is re-applied on every workflow the draft
// settles on — but only to rows the human has not typed in (decision 21.2).

// ntIssueMsg carries GET /v1/issues/{id}?workflow= for one draft state. The
// project, workflow and issue travel with it so an answer for a draft that
// has moved on is dropped, the guard applyPullPrefill applies.
type ntIssueMsg struct {
	projectID int64
	workflow  string
	issueID   int64
	issue     apiclient.Issue
	err       error
}

// issueCmd previews the seeded issue's prefill against the draft's workflow.
// It is a no-op for every draft that was not seeded from an issue, and it is
// a vincent call, never a GitHub one: the issue is the daemon's own row.
func (n *newTask) issueCmd() tea.Cmd {
	client := n.client
	if client == nil || n.issueID == 0 || n.projectID == 0 {
		return nil
	}
	projectID, workflow, id := n.projectID, n.workflow, n.issueID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		iss, err := client.GetIssue(ctx, id, workflow)
		return ntIssueMsg{projectID: projectID, workflow: workflow, issueID: id, issue: iss, err: err}
	}
}

// applyIssue takes an answer only while it still describes the draft. A
// failure is parked on the source row rather than raised as a form error: the
// create call reports anything that would actually stop it.
func (n *newTask) applyIssue(msg ntIssueMsg) {
	if n.issueID == 0 || msg.issueID != n.issueID ||
		msg.projectID != n.projectID || msg.workflow != n.workflow {
		return
	}
	if msg.err != nil {
		n.issueErr = errString(msg.err)
		return
	}
	iss := msg.issue
	n.issue, n.issueErr = &iss, ""
	// The worktree row's default (task 134.16 decision 4): `separate` while
	// another task holds the main worktree, so the task runs now, `main`
	// otherwise. A human's choice stands.
	if !n.worktreePicked {
		n.setSeparate(iss.MainWorktree != nil && iss.MainWorktree.OccupantTaskID != nil)
	}
	if iss.Prefill != nil {
		n.applyIssuePrefill(*iss.Prefill)
	}
}

// applyIssuePrefill drops a prefill into the rows the human has not typed in.
//
// "Not typed in" is a row that is empty or still holds what the previous
// prefill wrote there: a value that differs from both was typed, and is never
// overwritten. A field the previous workflow's prefill filled and this one's
// does not is withdrawn the same way — a value the human never chose should
// not ride along as a custom field of a workflow that does not declare it.
func (n *newTask) applyIssuePrefill(p apiclient.GitHubPrefill) {
	prev := n.issuePrefill
	if untouched(n.titleIn.Value(), prev, func(q apiclient.GitHubPrefill) string { return q.Title }) {
		n.titleIn.SetValue(p.Title)
	}
	if untouched(n.desc.Value(), prev, func(q apiclient.GitHubPrefill) string { return q.Description }) {
		n.desc.SetValue(p.Description)
	}
	for name, value := range p.Fields {
		n.replaceUntouchedField(name, value, prev)
	}
	if prev != nil {
		for name, was := range prev.Fields {
			if _, still := p.Fields[name]; still {
				continue
			}
			n.withdrawField(name, was)
		}
	}
	n.issuePrefill = &p
}

// untouched reports whether a row may take a new prefill: it is blank, or it
// holds exactly what the previous prefill put there.
func untouched(current string, prev *apiclient.GitHubPrefill, of func(apiclient.GitHubPrefill) string) bool {
	if strings.TrimSpace(current) == "" {
		return true
	}
	return prev != nil && current == of(*prev)
}

// replaceUntouchedField writes a prefilled value into the matching row,
// adding a custom row when the workflow declares no such field.
func (n *newTask) replaceUntouchedField(name, value string, prev *apiclient.GitHubPrefill) {
	for i := range n.fields {
		if n.fields[i].key != name {
			continue
		}
		was, hadPrev := "", false
		if prev != nil {
			was, hadPrev = prev.Fields[name]
		}
		current := n.fields[i].value
		if strings.TrimSpace(current) == "" || (hadPrev && current == was) {
			n.fields[i].value = value
		}
		return
	}
	n.fields = append(n.fields, kv{key: name, value: value})
}

// withdrawField empties a row that still holds a value only a previous
// prefill put there. A declared row stays, blank; a custom one goes.
func (n *newTask) withdrawField(name, was string) {
	out := n.fields[:0]
	for _, f := range n.fields {
		if f.key == name && f.value == was {
			if !f.declared {
				continue
			}
			f.value = ""
		}
		out = append(out, f)
	}
	n.fields = out
}

// issueSummary is the source row for an issue-seeded draft: `#id title`, its
// state, the GitHub reference when it is imported, and — on a second line —
// how many tasks it has already started (decision 21.3). Starting another is
// allowed; the line is there so it is done knowingly.
func (n *newTask) issueSummary() string {
	if n.issueID == 0 {
		return ""
	}
	id := "#" + strconv.FormatInt(n.issueID, 10)
	if n.issue == nil {
		if n.issueErr != "" {
			return id + "  " + styleWarn.Render("could not read the issue: "+n.issueErr)
		}
		return id + "  " + styleDim.Render("reading the issue…")
	}
	iss := *n.issue
	out := id + " " + iss.Title + "  " + issueStateStyle(iss).Render(issueStateBadge(iss))
	if badge := issueSourceBadge(iss); badge != "" {
		out += "  " + styleDim.Render(badge)
	}
	if note := issueStartedNote(iss.Tasks); note != "" {
		out += "\n" + styleWarn.Render(note)
	}
	return out
}

// issueStartedNote is the "already started" line: the count from the issue's
// own tasks block, and how many of them are unsettled. No extra fetch — the
// list lives on the issue detail (decision 21.1), not here.
func issueStartedNote(tasks apiclient.IssueTasks) string {
	if tasks.Count == 0 {
		return ""
	}
	verb := " already started from this issue"
	out := plural(tasks.Count, "task", "tasks") + verb
	if active := len(tasks.ActiveIDs); active > 0 {
		out += " (" + strconv.Itoa(active) + " active)"
	}
	return out
}

// worktreeValue is the worktree row (task 134.16 decision 4), with the note
// naming the occupant while the main worktree is busy.
func (n *newTask) worktreeValue() string {
	value := "main  " + styleDim.Render("on the issue's branch · enter for separate")
	if n.separate {
		value = "separate  " + styleDim.Render("its own worktree, merged back when done · enter for main")
	}
	if mw := n.issue.MainWorktree; mw != nil && mw.OccupantTaskID != nil {
		value += "\n" + styleWarn.Render("main worktree busy with #"+strconv.FormatInt(*mw.OccupantTaskID, 10)+
			" — choose separate to run now, or main to queue behind it")
	}
	return value
}

// mergeBackValue is the merge-back row of a separate draft.
func (n *newTask) mergeBackValue() string {
	if n.mergeAgent {
		return "agent  " + styleDim.Render("an agent resolves a conflict · enter for manual")
	}
	return "manual  " + styleDim.Render("a conflict blocks for you · enter for agent")
}
