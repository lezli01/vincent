package tui

import (
	"context"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/github"
)

// The task workspace's pull-request section (task 052.6).
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
		return
	}
	t.pull, t.pullErr = msg.pull, ""
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

// openCreatePR is `P`: the compare-URL editor. The offer exists only while
// nothing is linked and the daemon could build a compare URL, which needs the
// task to have both a branch and a base.
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
			"  P opens GitHub's own page with an editable prefill — vincent sends nothing"))
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
