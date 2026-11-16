package tui

import (
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
)

// An issue's title and labels are one-line text the daemon accepts almost
// verbatim: a local create trims only the ends, and a sync takes GitHub's or
// Jira's as they are. So a title may carry a line break, a tab, or a
// terminal escape — which, drawn as is, moves the cursor, clears the screen
// or retitles the terminal, and splits a card across rows.
//
// cleanIssue is applied where an issue enters the TUI — every read and every
// write that answers with one — so no screen draws a raw title, and none has
// to remember to clean it. The daemon's copy is untouched: the TUI shows the
// title, it never rewrites it unless a person saves the issue form.

// oneLine is s as one line of plain text: escape sequences and controls
// removed, every run of whitespace — line breaks and tabs included — one
// space.
func oneLine(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return isTerminalControl(r) || r == '\n' || r == '\t' }) < 0 {
		return s
	}
	// Whitespace controls first, so a break between two words leaves a
	// space rather than joining them once sanitizeText drops it.
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t', '\v', '\f':
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(sanitizeText(s)), " ")
}

// cleanIssue is iss with its one-line text made safe to draw.
func cleanIssue(iss apiclient.Issue) apiclient.Issue {
	iss.Title = oneLine(iss.Title)
	if len(iss.Labels) > 0 {
		labels := make([]string, len(iss.Labels))
		for i, l := range iss.Labels {
			labels[i] = oneLine(l)
		}
		iss.Labels = labels
	}
	if iss.Prefill != nil {
		p := *iss.Prefill
		p.Title = oneLine(p.Title)
		iss.Prefill = &p
	}
	return iss
}

// cleanIssues cleans every issue of a listing, in place.
func cleanIssues(list []apiclient.Issue) []apiclient.Issue {
	for i := range list {
		list[i] = cleanIssue(list[i])
	}
	return list
}

// A task's title is the same kind of text: the API trims only its ends, and
// one created from an issue starts from the issue's title. cleanTask is
// applied where a task enters the TUI — every listing, every read, and every
// action that answers with the task — for the same reason cleanIssue is.

// cleanTask is t with its title, and its issue's title, drawn as one line.
func cleanTask(t apiclient.Task) apiclient.Task {
	t.Title = oneLine(t.Title)
	if t.Issue != nil {
		iss := *t.Issue
		iss.Title = oneLine(iss.Title)
		t.Issue = &iss
	}
	return t
}

// cleanTasks cleans every task of a listing, in place.
func cleanTasks(list []apiclient.Task) []apiclient.Task {
	for i := range list {
		list[i] = cleanTask(list[i])
	}
	return list
}

// cleanTaskDetail is cleanTask for a detail read.
func cleanTaskDetail(d apiclient.TaskDetail) apiclient.TaskDetail {
	d.Task = cleanTask(d.Task)
	return d
}
