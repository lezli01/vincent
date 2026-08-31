package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/github"
)

// createPullTimeout bounds the one client call that causes a write to
// GitHub (task 069). It is longer than actionTimeout because the daemon does
// two network round trips inside it — a `git push` bounded at
// gitx.RemoteTimeout and a create bounded at github.RemoteTimeout — and a
// client that gave up between them would leave a human looking at a form for
// a pull request that had in fact been created.
const createPullTimeout = 3 * time.Minute

// The task workspace's pull-request section (task 052.6, task 069).
//
// GET /v1/tasks/{id}/github/pull always answers 200, deliberately: a task
// workspace asks it on every open, and refusing the whole row because GitHub
// is switched off would take the stored link away from a client that can
// still render it. So three states render inline here and none of them is an
// error — a linked pull request with its live state, the daemon's named
// reason when the integration is unusable, and the compare-URL offer when
// nothing is linked.

// taskPullMsg carries GET /v1/tasks/{id}/github/pull.
type taskPullMsg struct {
	taskID int64
	pull   apiclient.GitHubTaskPull
	err    error
}

// pullCmd fetches this task's pull request, live. It is refetched on open, on
// re-entering the workspace, and on the reconciler's own
// task.github_pull_changed — everything else on the row is live by nature and
// caching it would be the snapshot 052 refused to store.
func (t *taskView) pullCmd() tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		pull, err := client.TaskGitHubPull(ctx, id)
		return taskPullMsg{taskID: id, pull: pull, err: err}
	}
}

func (t *taskView) applyPull(msg taskPullMsg) {
	if msg.taskID != t.detail.taskID {
		return
	}
	t.pullLoaded = true
	if msg.err != nil {
		t.pull, t.pullErr = apiclient.GitHubTaskPull{}, errString(msg.err)
		t.leaveAbsentPullTab()
		return
	}
	t.pull, t.pullErr = msg.pull, ""
	t.leaveAbsentPullTab()
	if t.pullFormPending {
		// The takeover's intent, now that the prefill it needs has arrived.
		// openCreatePR reports its own refusal into pullNote when the task
		// turns out not to be eligible after all, which is the same sentence
		// pressing P here would have produced.
		t.pullFormPending = false
		t.openCreatePR()
	}
}

// pullWebURL is the page `o` opens: GitHub's own URL when the live fetch
// carried one, and otherwise the one built from the stored link. A task's
// link is a repo and a number and nothing else, so a merged pull request
// whose fetch failed still has somewhere to go.
func (t *taskView) pullWebURL() string {
	if t.pull.Pull != nil && t.pull.Pull.URL != "" {
		return t.pull.Pull.URL
	}
	if !t.pull.Linked || t.pull.Number == 0 {
		return ""
	}
	owner, name, ok := strings.Cut(t.pull.Repo, "/")
	if !ok {
		return ""
	}
	return github.PullURL(github.Repo{Owner: owner, Name: name}, t.pull.Number)
}

// openPullCmd is `o` on the Task Details tab.
func (t *taskView) openPullCmd() tea.Cmd {
	url := t.pullWebURL()
	if url == "" {
		t.pullNote, t.pullNoteBad = "no pull request is linked to this task", true
		return nil
	}
	t.pullNote, t.pullNoteBad = "", false
	return openURLCmd(url)
}

// openCreatePR is `P`: the pull-request form. The offer exists only while
// nothing is linked and the daemon could build a compare URL, which needs the
// task to have both a branch and a base — the same two fields the push and
// the create need, so one condition covers both actions (task 069).
func (t *taskView) openCreatePR() tea.Cmd {
	if t.pull.Linked {
		t.pullNote, t.pullNoteBad = "this task already has a pull request — press o to open it", true
		return nil
	}
	if t.pull.CompareURL == "" {
		t.pullNote, t.pullNoteBad = pullNoOfferReason(t.pull, t.detail.task.BranchName), true
		return nil
	}
	form, err := newCreatePRForm(t.detail.taskID, t.pull.CompareURL, t.detail.task.BranchName)
	if err != nil {
		t.pullNote, t.pullNoteBad = "the daemon's compare URL will not parse: "+errString(err), true
		return nil
	}
	form.openEditor = t.detail.editCreatePRBody
	form.submit = t.createPullCmd
	t.createPR = form
	t.popup = true
	t.pullNote, t.pullNoteBad = "", false
	return nil
}

// pullNoOfferReason says why there is nothing to open, in the daemon's own
// vocabulary where it named one.
func pullNoOfferReason(pull apiclient.GitHubTaskPull, branch string) string {
	if pull.Reason != "" {
		return "GitHub is not usable for this project: " + pull.Reason
	}
	if branch == "" {
		return "this task has no branch yet"
	}
	return "the daemon offered no compare URL for this task"
}

// pullSectionLines renders the "GitHub pull request" section of the Task
// Details tab.
func (t *taskView) pullSectionLines(width int) []string {
	if !t.pullLoaded {
		if t.pullErr != "" {
			return []string{styleBad.Render("  could not read this task's pull request: " + t.pullErr)}
		}
		return []string{styleDim.Render("  loading…")}
	}
	if t.pullErr != "" {
		return []string{styleBad.Render("  could not read this task's pull request: " + t.pullErr)}
	}
	out := make([]string, 0, 12)
	switch {
	case t.pull.Linked:
		out = append(out, renderTaskDetailFacts(width, t.linkedPullFacts())...)
		if t.pull.Reason != "" {
			// A stored link vincent cannot currently read. The link is still
			// true; only the live half is missing, and saying which is which
			// is the difference between a stale row and a broken one.
			out = append(out, "", styleWarn.Render(
				"  ⚠ could not read its current state: "+t.pull.Reason))
		}
		out = append(out, "", styleDim.Render("  o opens it in a browser"))
	case t.pull.Reason != "":
		out = append(out, styleWarn.Render("  GitHub is not usable for this project: "+t.pull.Reason))
	case t.pull.CompareURL != "":
		out = append(out, styleDim.Render("  No pull request is linked to this task."))
		out = append(out, "")
		out = append(out, renderTaskDetailFacts(width, []taskDetailFact{
			{"branch", valueOr(t.detail.task.BranchName, "not created")},
			{"base", valueOr(t.detail.task.BaseBranch, "unknown")},
		})...)
		out = append(out, "", styleDim.Render(
			"  P pushes this branch to origin and opens its pull request — the prefill is editable first"))
	default:
		out = append(out, styleDim.Render("  No pull request is linked to this task."))
		out = append(out, "", styleDim.Render(
			"  "+pullNoOfferReason(t.pull, t.detail.task.BranchName)))
	}
	if t.pullNote != "" {
		style := styleDim
		if t.pullNoteBad {
			style = styleBad
		}
		out = append(out, "", style.Render("  "+t.pullNote))
	}
	return out
}

func (t *taskView) linkedPullFacts() []taskDetailFact {
	facts := []taskDetailFact{
		{"pull request", t.pull.Repo + "#" + strconv.Itoa(t.pull.Number)},
		{"linked by", valueOr(t.pull.Source, "unknown")},
	}
	if p := t.pull.Pull; p != nil {
		facts = append(facts,
			taskDetailFact{"title", p.Title},
			taskDetailFact{"state", p.Status()},
			taskDetailFact{"head", valueOr(p.HeadBranch, "unknown")},
			taskDetailFact{"base", valueOr(p.BaseBranch, "unknown")},
			taskDetailFact{"author", valueOr(p.Author, "unknown")},
			taskDetailFact{"url", p.URL},
		)
		return facts
	}
	return append(facts, taskDetailFact{"url", t.pullWebURL()})
}

// taskPullCreatedMsg carries POST /v1/tasks/{id}/github/pull/create.
type taskPullCreatedMsg struct {
	taskID  int64
	created apiclient.GitHubPullCreated
	err     error
}

// createPullCmd is the form's ctrl+s: the daemon pushes the task's branch to
// origin and opens the pull request (task 069).
//
// It is the only command in the TUI that causes a write to GitHub, and the
// client still makes no request there — it posts to the daemon, per the
// ownership invariant, and the daemon owns the credential, the push and the
// create.
//
// actionTimeout is not used: this call runs a network git push and a GitHub
// create back to back, each bounded daemon-side at its own remote timeout,
// and a client that gave up first would leave a human looking at a form for
// a pull request that was in fact created.
func (t *taskView) createPullCmd(title, body string, draft bool) tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), createPullTimeout)
		defer cancel()
		created, err := client.CreateGitHubPull(ctx, id, apiclient.GitHubPullCreateRequest{
			Title: title, Body: body, Draft: draft,
		})
		return taskPullCreatedMsg{taskID: id, created: created, err: err}
	}
}

// applyCreatedPull is what comes back. Three outcomes, and only one of them
// closes the form:
//
//   - the pull request exists — the form closes, the section refetches, and
//     the link the daemon already wrote renders as `human`;
//   - the daemon pushed but could not create it — the form stays open and
//     ctrl+o now leads to a page whose branch is on the remote, which is the
//     issue's second complaint fixed even here;
//   - the call itself failed — the form stays open with the reason, so the
//     human can fix it and press ctrl+s again.
func (t *taskView) applyCreatedPull(msg taskPullCreatedMsg) tea.Cmd {
	if msg.taskID != t.detail.taskID || t.createPR == nil {
		return nil
	}
	if msg.err != nil {
		t.createPR.failed(errString(msg.err))
		return nil
	}
	if !msg.created.Created {
		t.createPR.failed("pushed " + msg.created.Branch + " to " + msg.created.Remote +
			", but could not open the pull request (" + msg.created.Reason +
			") — ctrl+o opens GitHub's page for the pushed branch")
		return nil
	}
	t.createPR, t.popup = nil, false
	t.pullNote, t.pullNoteBad = "opened "+msg.created.Pull.Repo+"#"+
		strconv.Itoa(msg.created.Pull.Number), false
	return t.pullCmd()
}
