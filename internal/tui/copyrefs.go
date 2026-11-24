package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// diffCopyItems are the Diff tab's copy rows (#1076): the hunk under the hunk
// cursor, and the whole diff of the file the cursor is on, as plain text.
func (p *diffPane) diffCopyItems() []copyItem {
	var out []copyItem
	if sec, file, h, ok := p.hunkPos(); ok {
		f := &p.sections[sec].files[file]
		end := len(f.body)
		if h+1 < len(f.hunks) {
			end = f.hunks[h+1].start
		}
		text := plainJoin(f.body[f.hunks[h].start:end])
		out = append(out, copyItem{group: "diff", label: "this hunk", snippet: f.path + " " + firstLine(text), text: text})
	}
	if len(p.nodes) > 0 {
		n := p.nodes[min(max(p.cursor, 0), len(p.nodes)-1)]
		if n.file >= 0 && n.section < len(p.sections) && n.file < len(p.sections[n.section].files) {
			if f := &p.sections[n.section].files[n.file]; len(f.body) > 0 {
				out = append(out, copyItem{group: "diff", label: "this file's diff", snippet: f.path, text: plainJoin(f.body)})
			}
		}
	}
	return out
}

// outputCopyItem is the displayed attempt's output as plain text (#1076), as
// the pane draws it at its current level, without colour.
func (d *detail) outputCopyItem() (copyItem, bool) {
	if len(d.records) == 0 {
		return copyItem{}, false
	}
	text := strings.TrimSpace(plainJoin(d.outputLines()))
	if text == "" {
		return copyItem{}, false
	}
	return copyItem{group: "output", label: "this attempt's output", snippet: firstLine(text), text: text}, true
}

// plainJoin is lines without their colour, one per line.
func plainJoin(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return strings.Join(out, "\n")
}

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
