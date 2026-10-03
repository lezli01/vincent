package tui

import (
	"context"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The new-task form's GitHub side: a draft seeded with a pull request (task
// 064). The form has no GitHub issue row any more — a task is started from a
// vincent issue on the issue screens (task 130 decision 7), see
// newtaskissue.go — so this is all the form asks GitHub about, and it asks
// only for a draft that was seeded from a pull request.
//
// Everything here is a request to the daemon and a render of its answer. The
// TUI never parses a remote, reads a token, or calls GitHub, and it never
// computes a prefill: the daemon does that, the form previews it in editable
// rows, and POST /v1/tasks recomputes the same thing from the same code (035
// decision 2).
//
// A draft seeded from the pull-requests takeover carries a number and nothing
// else. The prefill is fetched from the daemon against the workflow the draft
// settles on. `state=all` because the takeover can now offer a closed or
// merged pull request, and a listing that dropped it would leave the seeded
// draft with no prefill at all.

// pullPrefillCmd fetches the prefill for the seeded pull request. It is a
// no-op for every draft that was not seeded from one, which is what keeps an
// ordinary new task free of any GitHub call.
func (n *newTask) pullPrefillCmd() tea.Cmd {
	client := n.client
	if client == nil || n.pull == nil || n.pullPrefilled || n.projectID == 0 || n.workflow == "" {
		return nil
	}
	projectID, workflow, number := n.projectID, n.workflow, n.pull.Number
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		pulls, err := client.ListGitHubPulls(ctx, projectID, apiclient.GitHubPullsOptions{
			State: "all", Workflow: workflow,
		})
		return ntPullPrefillMsg{projectID: projectID, workflow: workflow, number: number, pulls: pulls, err: err}
	}
}

// applyPullPrefill drops the daemon's prefill into the form's own editable
// rows. Nothing is locked: previewing a guess is only worth doing if a human
// can change it before the task exists. The branch is the one exception, and
// it is not a row — a pull-request task's branch is the pull request's head,
// resolved server-side above even a typed literal (decision 1).
func (n *newTask) applyPullPrefill(msg ntPullPrefillMsg) {
	if n.pull == nil || msg.err != nil || n.pullPrefilled {
		return
	}
	if msg.projectID != n.projectID || msg.workflow != n.workflow || msg.number != n.pull.Number {
		return
	}
	for i := range msg.pulls {
		if msg.pulls[i].Number != msg.number {
			continue
		}
		n.pull = &msg.pulls[i]
		if prefill := msg.pulls[i].Prefill; prefill != nil {
			n.titleIn.SetValue(prefill.Title)
			n.desc.SetValue(prefill.Description)
			for name, filled := range prefill.Fields {
				n.setFieldValue(name, filled)
			}
		}
		n.pullPrefilled = true
		return
	}
}

// pullSummary is the one line the form shows for a seeded pull request,
// including the fork warning. A fork's head is fetched with no upstream, so
// nothing can push back; that is stated here rather than discovered when a
// delivery step fails (decision 5).
func (n *newTask) pullSummary() string {
	if n.pull == nil {
		return ""
	}
	out := "#" + strconv.Itoa(n.pull.Number) + " " + n.pull.Title +
		" · branch " + n.pull.HeadBranch
	if n.pull.Fork() {
		out += " · fork (" + n.pull.HeadRepo + "): nothing can be pushed back"
	}
	return out
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
