package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1082: an empty board says when work waits on you in another project.
func TestEmptyBoardNamesWorkElsewhere(t *testing.T) {
	b := newBoard()
	b.project = projectSel{id: 1, name: "api"}
	b.loaded = true
	b.tasks = []apiclient.Task{{ID: 9, ProjectID: 2, State: stateAwaitingInput, Title: "x"}}
	body, ok := b.emptyBody(nil)
	if !ok {
		t.Fatal("no empty state for an empty board")
	}
	got := ansi.Strip(body)
	if !strings.Contains(got, "no tasks yet") || !strings.Contains(got, "1 waiting on you in other projects") {
		t.Fatalf("the empty board reads %q", got)
	}
	b.tasks = nil
	if got, _ := b.emptyBody(nil); strings.Contains(ansi.Strip(got), "other projects") {
		t.Fatalf("an empty board with nothing elsewhere reads %q", ansi.Strip(got))
	}
}

// #1082: typing in a value picker filters it without `/` first; its own
// keys keep their meaning.
func TestPickerFiltersAsYouType(t *testing.T) {
	p := newPicker(0, "model", bigCatalog(180), true, "")
	pick(p, "o", "p", "u", "s")
	if !p.filtering || len(p.matches) != 1 {
		t.Fatalf("typing opus: filtering=%v matches=%d, want the one opus id", p.filtering, len(p.matches))
	}
	q := newPicker(0, "model", bigCatalog(20), true, "")
	pick(q, "j")
	if q.filtering || q.cursor != 1 {
		t.Fatalf("j filtered=%v cursor=%d, want it to move down", q.filtering, q.cursor)
	}
}
