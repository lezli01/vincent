package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The references a person copies to paste elsewhere (#1076): a task's id,
// branch, worktree path and pull request, an issue's id, reference and page.
// ctrl+y used to offer assistant messages and code blocks only.

// taskCopyItems are a task's references as copy rows.
func taskCopyItems(t apiclient.Task) []copyItem {
	var out []copyItem
	add := func(label, text string) {
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, copyItem{group: "task #" + strconv.FormatInt(t.ID, 10), label: label, snippet: text, text: text})
		}
	}
	add("id", strconv.FormatInt(t.ID, 10))
	add("branch", t.BranchName)
	if t.WorktreePath != nil {
		add("worktree path", *t.WorktreePath)
	}
	if p := t.GitHubPull; p != nil && p.Repo != "" && p.Number > 0 {
		add("pull request", "https://github.com/"+p.Repo+"/pull/"+strconv.Itoa(p.Number))
	}
	return out
}

// issueCopyItems are an issue's references as copy rows.
func issueCopyItems(iss apiclient.Issue) []copyItem {
	var out []copyItem
	group := "issue #" + strconv.FormatInt(iss.ID, 10)
	add := func(label, text string) {
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, copyItem{group: group, label: label, snippet: text, text: text})
		}
	}
	add("id", strconv.FormatInt(iss.ID, 10))
	add("title", iss.Title)
	if s := iss.Source; s != nil {
		add("reference", s.Ref)
	}
	add("url", issueURL(iss))
	return out
}

// openCopyItems raises the copy picker over items, saying so when there
// are none rather than opening an empty list.
func openCopyItems(items []copyItem) tea.Cmd {
	if len(items) == 0 {
		return nil
	}
	return func() tea.Msg { return openCopyPickerMsg{items: items} }
}
