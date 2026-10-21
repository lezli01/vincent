package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestNewChatFormWindowsOnTheOpenList holds #801 F13 for every list row: at
// any height the form returns no more lines than it was given, the pinned
// key hint is always the last of them, and once there is room for the row
// and its list together, the list's highlighted option is on screen — the
// window follows the focused element's lines, not its row index.
func TestNewChatFormWindowsOnTheOpenList(t *testing.T) {
	for _, row := range []ncRow{ncAgent, ncModel, ncEffort, ncBranch} {
		for height := 1; height <= 16; height++ {
			f := chatFormWithCatalogs()
			f.applyBranches(newChatBranchesMsg{projectID: 1, branches: []apiclient.Branch{
				{Name: "main"}, {Name: "feature/one"}, {Name: "feature/two"},
			}})
			openAt(t, f, row)
			if f.pick == nil {
				t.Fatalf("enter on row %d opened no list", row)
			}
			want := f.pick.options[f.pick.matches[f.pick.cursor]].label
			lines := stripLines(f.render(80, height))
			if len(lines) > height {
				t.Fatalf("row %d at height %d: %d lines returned", row, height, len(lines))
			}
			if last := lines[len(lines)-1]; !strings.Contains(last, "esc close the list") {
				t.Errorf("row %d at height %d: the key hint is not pinned last: %q", row, height, last)
			}
			// The row, the list heading, every option and the hint: past
			// that the highlighted option has no excuse to be off screen.
			if height >= 4+len(f.pick.options)+1 {
				out := strings.Join(lines, "\n")
				if !strings.Contains(out, want) {
					t.Errorf("row %d at height %d: the highlighted option %q is off screen:\n%s",
						row, height, want, out)
				}
			}
		}
	}
}

// TestNewChatFormFitsWhenClosed: with no list open the form still honours
// its height, keeps the focused row in view and its default hint pinned.
func TestNewChatFormFitsWhenClosed(t *testing.T) {
	for height := 3; height <= 12; height++ {
		f := chatFormWithCatalogs()
		f.focus = ncBranch
		lines := stripLines(f.render(80, height))
		if len(lines) > height {
			t.Fatalf("height %d: %d lines returned", height, len(lines))
		}
		if !strings.Contains(lines[len(lines)-1], "ctrl+s create") {
			t.Errorf("height %d: the key hint is not pinned last: %q", height, lines[len(lines)-1])
		}
		if !strings.Contains(strings.Join(lines, "\n"), "▸ branch") {
			t.Errorf("height %d: the focused branch row is off screen:\n%s", height, strings.Join(lines, "\n"))
		}
	}
}

// TestNewChatFormBranchErrorWraps: the branch row's own error is as long as
// the daemon made it, and wraps within the pane like the create error.
func TestNewChatFormBranchErrorWraps(t *testing.T) {
	f := chatFormWithCatalogs()
	f.branchErr = strings.Repeat("that branch is checked out somewhere vincent cannot use ", 3)
	for _, line := range stripLines(f.render(60, 30)) {
		if w := ansi.StringWidth(line); w > 60 {
			t.Errorf("a new-chat line is %d columns wide in a 60-column pane: %q", w, line)
		}
	}
}
