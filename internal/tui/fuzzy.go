package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The palette's matching (#1080): substring-only matching missed on word
// order ("archive tasks" for "archived tasks — history") and on a typo
// ("dameon"). fuzzyScore takes the query's words in any order and accepts
// each one as a substring, a prefix of a word, the letters in order, or a
// word one edit away; the best matches sort first.

// fuzzyScore rates hay against the query's words; 0 is no match.
func fuzzyScore(hay string, words []string) int {
	hay = strings.ToLower(hay)
	hayWords := strings.FieldsFunc(hay, func(r rune) bool { return !wordRune(r) })
	total := 0
	for _, w := range words {
		best := 0
		for _, hw := range hayWords {
			switch {
			case hw == w:
				best = max(best, 6)
			case strings.HasPrefix(hw, w):
				best = max(best, 5)
			case strings.Contains(hw, w):
				best = max(best, 3)
			case len([]rune(w)) >= 4 && editWithin1(hw, w):
				best = max(best, 2)
			}
		}
		if best == 0 && strings.Contains(hay, w) {
			best = 3
		}
		if best == 0 && len([]rune(w)) >= 3 && subsequence(hay, w) {
			best = 1
		}
		if best == 0 {
			return 0
		}
		total += best
	}
	return total
}

// subsequence reports whether every rune of w appears in s, in order.
func subsequence(s, w string) bool {
	r := []rune(w)
	i := 0
	for _, c := range s {
		if i < len(r) && c == r[i] {
			i++
		}
	}
	return i == len(r)
}

// editWithin1 reports whether a and b are at most one insertion, deletion,
// substitution or adjacent swap apart.
func editWithin1(a, b string) bool {
	x, y := []rune(a), []rune(b)
	if len(x) < len(y) {
		x, y = y, x
	}
	if len(x)-len(y) > 1 {
		return false
	}
	i := 0
	for i < len(y) && x[i] == y[i] {
		i++
	}
	if i == len(y) {
		return true
	}
	if len(x) == len(y) {
		if string(x[i+1:]) == string(y[i+1:]) {
			return true
		}
		return i+1 < len(x) && x[i] == y[i+1] && x[i+1] == y[i] && string(x[i+2:]) == string(y[i+2:])
	}
	return string(x[i+1:]) == string(y[i:])
}

// paletteItems are the tasks, chats and issues the palette can jump to
// (#1080), listed only once something is typed: the project's own, by
// number and title.
func (m *root) paletteItems() []paletteEntry {
	var out []paletteEntry
	if s, ok := m.views[viewHome].(*shell); ok {
		for _, t := range s.board.projectTasks() {
			id, pid, state := t.ID, t.ProjectID, t.State
			out = append(out, paletteEntry{
				group: "tasks", label: "#" + strconv.FormatInt(id, 10) + " " + oneLine(t.Title) + " — " + stateWords(state),
				item:   true,
				action: func() tea.Msg { return selectTaskMsg{id: id, state: state, projectID: pid} },
			})
		}
	}
	if v, ok := m.views[viewChats].(*chatsView); ok {
		for _, c := range v.chats {
			id, pid := c.ID, c.ProjectID
			out = append(out, paletteEntry{
				group: "chats", label: "chat #" + strconv.FormatInt(id, 10) + " " + oneLine(c.Title) + " — " + chatStateLabel(c.State),
				item:   true,
				action: func() tea.Msg { return openChatMsg{id: id, projectID: pid, back: viewChats} },
			})
		}
	}
	if v, ok := m.views[viewIssues].(*issuesView); ok {
		for _, iss := range v.issues {
			id, pid := iss.ID, v.project.id
			label := "issue #" + strconv.FormatInt(id, 10) + " " + oneLine(iss.Title)
			if iss.Source != nil && iss.Source.Ref != "" {
				label += " — " + iss.Source.Ref
			}
			out = append(out, paletteEntry{
				group: "issues", label: label, item: true,
				action: func() tea.Msg { return openIssueMsg{id: id, projectID: pid, back: viewIssues} },
			})
		}
	}
	return out
}

// wordRune is a rune that belongs to a word of a palette row: letters,
// digits, and the punctuation ids and references are spelled with.
func wordRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 127:
		return true
	}
	return strings.ContainsRune("#_-/.", r)
}
