package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The subject wraps rather than truncating (#804 PL-8): its end says whose
// credential posts the comment.
func TestPullCommentSubjectWraps(t *testing.T) {
	f := newPullCommentForm(7, "#12 Refactor the session cache")
	text := strings.Join(strings.Fields(ansi.Strip(f.render(60, 30))), " ")
	if !strings.Contains(text, "ctrl+s is the confirmation") {
		t.Errorf("the 60-column subject was cut:\n%s", f.render(60, 30))
	}
}

// With a long body and an error in a short popup the error and the keys stay
// visible (#804 PL-1).
func TestPullCommentKeepsErrorAndKeysWithALongBody(t *testing.T) {
	f := newPullCommentForm(7, "#12")
	f.stopEdit()
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("- point %d", i+1)
	}
	f.body = strings.Join(lines, "\n")
	f.err = "comment refused"
	out := ansi.Strip(f.render(60, 12))
	if n := len(strings.Split(out, "\n")); n > 12 {
		t.Errorf("render(60, 12) is %d lines tall", n)
	}
	if !strings.Contains(out, "comment refused") {
		t.Errorf("the error is off-screen in a 12-row popup:\n%s", out)
	}
	f.err = ""
	if out := ansi.Strip(f.render(60, 12)); !strings.Contains(out, "esc discard") {
		t.Errorf("the key line is off-screen in a 12-row popup:\n%s", out)
	}
}
