package tui

import (
	"context"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The new-task form's GitHub issue row (§15, task 035).
//
// Everything here is a request to the daemon and a render of its answer. The
// TUI never parses a remote, reads a token, or calls GitHub — the ownership
// invariant is not relaxed for this — and it never computes a prefill: the
// daemon does that, the form previews it in editable rows, and POST /v1/tasks
// recomputes the same thing from the same code (decision 2).

// issuesKey identifies the draft state an issue listing describes. The
// workflow is part of it because each row carries the prefill computed against
// that workflow's declared fields, so switching workflow makes the listing's
// prefills stale even though its issues are unchanged.
type issuesKey struct {
	projectID int64
	workflow  string
}

func (n *newTask) issuesKey() issuesKey {
	return issuesKey{projectID: n.projectID, workflow: n.workflow}
}

// githubCmd asks whether this project's issues can be read. It is the one
// call the form makes before a human has expressed any interest in GitHub,
// which is why it is a cheap probe with a daemon-side TTL rather than a
// listing (decision 4).
func (n *newTask) githubCmd(projectID int64) tea.Cmd {
	client := n.client
	if client == nil || projectID == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		status, err := client.ProjectGitHub(ctx, projectID)
		return ntGitHubMsg{projectID: projectID, status: status, err: err}
	}
}

// applyGitHub records the probe and fetches the listing when it says yes. A
// probe for a project the draft has moved past is dropped; a failed probe is
// treated as "not available", because a form that cannot ask the daemon
// whether GitHub works must not offer a row that would then fail.
func (n *newTask) applyGitHub(msg ntGitHubMsg) tea.Cmd {
	if msg.projectID != n.projectID {
		return nil
	}
	n.githubProject = msg.projectID
	if msg.err != nil {
		n.github = apiclient.GitHubStatus{}
		return nil
	}
	n.github = msg.status
	if !n.github.Available {
		// A row that is not offered cannot hold a linked issue.
		n.issue, n.issues, n.issuesFor = nil, nil, issuesKey{}
		return nil
	}
	return n.issuesCmd()
}

// issuesCmd fetches the listing the picker offers, with the prefill computed
// for the selected workflow. It is fired when the probe says yes and again
// when the workflow changes; it is a no-op while the probe says no, which is
// what keeps a disabled integration from producing any GitHub call at all.
func (n *newTask) issuesCmd() tea.Cmd {
	client := n.client
	key := n.issuesKey()
	if client == nil || !n.githubAvailable() || key.projectID == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		issues, err := client.ListGitHubIssues(ctx, key.projectID, apiclient.GitHubIssuesOptions{
			Workflow: key.workflow,
		})
		return ntIssuesMsg{key: key, issues: issues, err: err}
	}
}

// applyIssues takes a listing only while it still describes the draft. A
// failure is parked on the row rather than raised as a form error: the rest of
// the form works perfectly without an issue, and the create button is not
// blocked by it.
func (n *newTask) applyIssues(msg ntIssuesMsg) {
	if msg.key != n.issuesKey() {
		return
	}
	n.issuesFor = msg.key
	if msg.err != nil {
		n.issues, n.issuesErr = nil, errString(msg.err)
		return
	}
	n.issues, n.issuesErr = msg.issues, ""
}

// issueOptions are the picker's rows: the linked issue can always be removed,
// then every issue the daemon listed, newest first.
func (n *newTask) issueOptions() []pickerOption {
	out := make([]pickerOption, 0, len(n.issues)+1)
	out = append(out, pickerOption{value: "", label: "(none)", note: "no linked issue"})
	for _, issue := range n.issues {
		note := issue.State
		if labels := issue.LabelList(); labels != "" {
			note += " · " + labels
		}
		out = append(out, pickerOption{
			value: strconv.Itoa(issue.Number),
			label: "#" + strconv.Itoa(issue.Number) + "  " + issue.Title,
			note:  note,
		})
	}
	return out
}

// findIssue resolves a picker value back to the listed issue.
func (n *newTask) findIssue(value string) *apiclient.GitHubIssue {
	number, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	for i := range n.issues {
		if n.issues[i].Number == number {
			return &n.issues[i]
		}
	}
	return nil
}

// applyIssuePick links the draft to an issue and drops the daemon's prefill
// into the form's own rows.
//
// Every value lands in an editable row and nothing is locked: the point of
// previewing a guess is that a human can see it and change it before the task
// exists. Clearing the row (the "(none)" option) unlinks the issue and leaves
// the text alone — deleting a description somebody may have since rewritten
// would be a worse surprise than a paragraph they no longer want.
func (n *newTask) applyIssuePick(value string) {
	if strings.TrimSpace(value) == "" {
		n.issue = nil
		return
	}
	issue := n.findIssue(value)
	if issue == nil {
		return
	}
	n.issue = issue
	prefill := issue.Prefill
	if prefill == nil {
		return
	}
	n.titleIn.SetValue(prefill.Title)
	n.desc.SetValue(prefill.Description)
	for name, filled := range prefill.Fields {
		n.setFieldValue(name, filled)
	}
}

// setFieldValue writes a prefilled value into the matching row, adding a
// custom row when the workflow declares no such field. It never overwrites a
// value the human already typed: the prefill is a starting point, not a
// correction.
func (n *newTask) setFieldValue(name, value string) {
	for i := range n.fields {
		if n.fields[i].key != name {
			continue
		}
		if strings.TrimSpace(n.fields[i].value) == "" {
			n.fields[i].value = value
		}
		return
	}
	n.fields = append(n.fields, kv{key: name, value: value})
}

// issueSummary is the collapsed row's one line.
func (n *newTask) issueSummary() string {
	if n.issue != nil {
		return "#" + strconv.Itoa(n.issue.Number) + "  " + n.issue.Title
	}
	switch {
	case n.issuesErr != "":
		return styleWarn.Render("could not list issues: " + n.issuesErr)
	case n.issuesFor != n.issuesKey():
		return styleDim.Render("loading issues…")
	case len(n.issues) == 0:
		return styleDim.Render("no open issues in " + n.github.Repo)
	default:
		return styleDim.Render("(none) · enter to pick from " + n.github.Repo)
	}
}

// issueValue is the picker's current selection: the linked issue's number, or
// "" for the "(none)" row.
func (n *newTask) issueValue() string {
	if n.issue == nil {
		return ""
	}
	return strconv.Itoa(n.issue.Number)
}
