package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// localIssueFixture is the detail on an issue that takes a comment: no
// remote, so its body is editable and its thread its own (task 130
// decision 24).
func localIssueFixture() *issueView {
	v := issueFixture()
	v.issue.Source = nil
	v.issue.Editable = []string{"title", "body", "labels", "kind", "priority"}
	v.issue.Commentable = true
	return v
}

func init() {
	panelKeyProbes[ctxIssue]["W"] = func(t *testing.T) {
		v := localIssueFixture()
		v.w.exec = fakeExec(t, func(path string) error {
			return os.WriteFile(path, []byte("written in vi"), 0o600)
		}, nil)
		_, cmd := v.update(registryKey(t, "W"))
		if cmd == nil {
			t.Fatal("W did not open $EDITOR")
		}
		if _, post := v.update(cmd()); post == nil || !v.posting {
			t.Fatal("a saved comment was not posted")
		}
	}
}

func TestIssueCommentKeyWithheldOnAMirroredIssue(t *testing.T) {
	v := issueFixture() // imported, live remote: not commentable
	if hasCommentRow(v.liveBindings(bindingsFor(ctxIssue))) {
		t.Fatal("W is offered on a mirrored issue")
	}
	v.w.exec = fakeExec(t, func(string) error {
		t.Error("W opened $EDITOR on a mirrored issue")
		return nil
	}, nil)
	if _, cmd := v.update(registryKey(t, "W")); cmd != nil {
		t.Fatal("W did something on a mirrored issue")
	}
	if !v.noteBad || !strings.Contains(v.note, "mirrored") {
		t.Errorf("note = %q, want the refusal", v.note)
	}
	if !hasCommentRow(localIssueFixture().liveBindings(bindingsFor(ctxIssue))) {
		t.Error("W is withheld on a local issue")
	}
}

func TestIssueThreadRendersEmptyAndFailed(t *testing.T) {
	v := localIssueFixture()
	out := ansi.Strip(v.render(120, 200))
	if !strings.Contains(out, "no comment yet · W writes one") {
		t.Errorf("an empty thread on a local issue does not say how to start one:\n%s", out)
	}
	v.commentsErr = &apiclient.Error{Code: "internal", Message: "boom"}
	if out = ansi.Strip(v.render(120, 200)); !strings.Contains(out, "could not read the comments") {
		t.Errorf("a failed thread read is not shown:\n%s", out)
	}
}
