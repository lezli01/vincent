package tui

import (
	"os"
	"strings"
	"testing"
)

// The issue form and prompt (task 130.12) in isolation. Their round trips
// against the real handlers are in issues_live_test.go.

// issueFormFixture is the list with a create form open on project 1.
func issueFormFixture(t *testing.T) (*issuesView, *issueForm) {
	t.Helper()
	v := issuesFixture()
	v.update(registryKey(t, "n"))
	if v.w.form == nil {
		t.Fatal("n did not open the issue form")
	}
	return v, v.w.form
}

// editFormFixture is the detail with the edit form open on its imported issue.
func editFormFixture(t *testing.T, editable ...string) (*issueView, *issueForm) {
	t.Helper()
	v := issueFixture()
	v.issue.Version = 3
	v.issue.Labels = []string{"ui"}
	v.issue.Editable = editable
	v.update(registryKey(t, "i"))
	if v.w.form == nil {
		t.Fatal("i did not open the issue form")
	}
	return v, v.w.form
}

func init() {
	panelKeyProbes[ctxIssues]["n"] = func(t *testing.T) {
		_, f := issueFormFixture(t)
		if !f.creating() || f.projectID != 1 || f.idemKey == "" {
			t.Fatalf("n opened create=%v project=%d key=%q, want a create on the row's project with a key",
				f.creating(), f.projectID, f.idemKey)
		}
	}
	panelKeyProbes[ctxIssues]["i"] = func(t *testing.T) {
		v := issuesFixture()
		_, cmd := v.update(registryKey(t, "i"))
		if cmd == nil {
			t.Fatal("i did not read the selected issue to edit")
		}
		if _, ok := cmd().(issueEditTargetMsg); !ok {
			t.Fatal("i did not read the issue for the form")
		}
	}
	panelKeyProbes[ctxIssues]["X"] = func(t *testing.T) {
		v := issuesFixture()
		_, cmd := v.update(registryKey(t, "X"))
		if cmd == nil {
			t.Fatal("X did not read the selected issue's available_actions")
		}
		if _, ok := cmd().(issueActionTargetMsg); !ok {
			t.Fatal("X did not read the issue for its actions")
		}
	}
	panelKeyProbes[ctxIssues]["D"] = func(t *testing.T) {
		v := issuesFixture()
		v.update(registryKey(t, "D"))
		if v.w.act == nil || v.w.act.action != issueActDelete || v.w.act.stage != iaConfirm {
			t.Fatal("D did not ask before deleting")
		}
	}
	panelKeyProbes[ctxIssue]["n"] = func(t *testing.T) {
		v := issueFixture()
		v.update(registryKey(t, "n"))
		if v.w.form == nil || !v.w.form.creating() || v.w.form.projectID != 2 {
			t.Fatal("n did not open a create form on the issue's project")
		}
	}
	panelKeyProbes[ctxIssue]["i"] = func(t *testing.T) {
		_, f := editFormFixture(t, "kind", "priority")
		if f.creating() || f.original.ID != 5 {
			t.Fatal("i did not open the edit form on the issue")
		}
	}
	panelKeyProbes[ctxIssue]["X"] = func(t *testing.T) {
		v := issueFixture()
		v.issue.AvailableActions = []string{"close"}
		v.update(registryKey(t, "X"))
		if v.w.act == nil || v.w.act.stage != iaReason {
			t.Fatal("X did not ask for a close reason")
		}
	}
	panelKeyProbes[ctxIssue]["D"] = func(t *testing.T) {
		v := issueFixture()
		v.update(registryKey(t, "D"))
		if v.w.act == nil || v.w.act.action != issueActDelete {
			t.Fatal("D did not ask before deleting")
		}
	}

	panelKeyProbes[ctxIssueForm] = map[string]func(*testing.T){
		"down": func(t *testing.T) {
			v, f := issueFormFixture(t)
			v.update(registryKey(t, "down"))
			if f.cursor != ifBody {
				t.Fatalf("down left the cursor on %s, want description", f.cursor.label())
			}
		},
		"enter": func(t *testing.T) {
			v, f := issueFormFixture(t)
			v.update(registryKey(t, "enter"))
			if !f.editing {
				t.Fatal("enter did not edit the title")
			}
		},
		"e": func(t *testing.T) {
			v, f := issueFormFixture(t)
			f.exec = fakeExec(t, func(path string) error {
				return os.WriteFile(path, []byte("written in vi"), 0o600)
			}, nil)
			f.cursor = ifBody
			_, cmd := v.update(registryKey(t, "e"))
			if cmd == nil {
				t.Fatal("e did not open $EDITOR")
			}
			v.update(cmd())
			if f.body.Value() != "written in vi" {
				t.Fatalf("description = %q, want the edited text", f.body.Value())
			}
		},
		"t": func(t *testing.T) {
			v, f := issueFormFixture(t)
			f.cursor = ifKind
			v.update(registryKey(t, "enter"))
			v.update(registryKey(t, "t"))
			if f.pick == nil || !f.pick.editing {
				t.Fatal("t did not open the kind list's free-text entry")
			}
		},
		"R": func(t *testing.T) {
			v, _ := editFormFixture(t, "kind", "priority")
			if _, cmd := v.update(registryKey(t, "R")); cmd == nil {
				t.Fatal("R did not re-read the issue")
			}
		},
		"ctrl+s": func(t *testing.T) {
			v, f := issueFormFixture(t)
			f.title.SetValue("Crash")
			_, cmd := v.update(registryKey(t, "ctrl+s"))
			if cmd == nil || !f.saving {
				t.Fatal("ctrl+s did not save")
			}
		},
		"esc": func(t *testing.T) {
			v, f := issueFormFixture(t)
			f.title.SetValue("draft")
			v.update(registryKey(t, "esc"))
			if !f.confirming {
				t.Fatal("esc over an unsaved draft did not ask first")
			}
		},
	}
	panelKeyProbes[ctxIssuePrompt] = map[string]func(*testing.T){
		"enter": func(t *testing.T) {
			v := issueFixture()
			v.issue.AvailableActions = []string{"close"}
			v.update(registryKey(t, "X"))
			v.update(registryKey(t, "enter"))
			if v.w.act == nil || v.w.act.reason != "completed" || v.w.act.stage != iaConfirm {
				t.Fatal("enter did not pick the close reason")
			}
		},
		"y": func(t *testing.T) {
			v := issueFixture()
			v.update(registryKey(t, "D"))
			_, cmd := v.update(registryKey(t, "y"))
			if cmd == nil || v.w.act.stage != iaBusy {
				t.Fatal("y did not confirm the delete")
			}
		},
		"esc": func(t *testing.T) {
			v := issueFixture()
			v.update(registryKey(t, "D"))
			v.update(registryKey(t, "esc"))
			if v.w.act != nil {
				t.Fatal("esc did not cancel the prompt")
			}
		},
	}
}

// An imported issue's form renders the mirrored rows read-only from
// `editable`, refuses to edit them, and its PATCH carries only what changed.
func TestIssueFormMirroredRowsAndMinimalPatch(t *testing.T) {
	v, f := editFormFixture(t, "kind", "priority")
	out := v.render(140, 40)
	if strings.Count(out, "mirrored from GitHub") != 3 {
		t.Fatalf("want title, description and labels marked mirrored:\n%s", out)
	}
	f.cursor = ifTitle
	v.update(registryKey(t, "enter"))
	if f.editing || !strings.Contains(f.err, "mirrored") {
		t.Fatalf("enter on a mirrored title: editing=%v err=%q", f.editing, f.err)
	}
	f.priority = 1
	p := f.patch()
	if p.Version != 3 || p.Priority == nil || *p.Priority != 1 ||
		p.Title != nil || p.Body != nil || p.Kind != nil || p.AddLabels != nil || p.RemoveLabels != nil {
		t.Fatalf("patch = %+v, want version 3 and priority alone", p)
	}
}

// Labels travel as a diff against the issue the form read.
func TestIssueFormLabelDiff(t *testing.T) {
	_, f := editFormFixture(t, "title", "body", "labels", "kind", "priority")
	f.labels = []string{"bug"}
	p := f.patch()
	if strings.Join(p.AddLabels, ",") != "bug" || strings.Join(p.RemoveLabels, ",") != "ui" {
		t.Fatalf("add %v remove %v, want +bug -ui", p.AddLabels, p.RemoveLabels)
	}
}

// A rebase onto the current issue keeps the user's edits and takes every
// field they left alone from the new version.
func TestIssueFormReloadKeepsEdits(t *testing.T) {
	_, f := editFormFixture(t, "title", "body", "labels", "kind", "priority")
	f.title.SetValue("My title")
	f.labels = []string{"ui", "mine"}
	cur := *f.original
	cur.Version, cur.Kind, cur.Labels, cur.Body = 4, "bug", []string{"ui", "theirs"}, "new body"
	f.update(issueFormReloadedMsg{issue: cur})
	if f.original.Version != 4 || f.title.Value() != "My title" || f.kind != "bug" ||
		f.body.Value() != "new body" || strings.Join(f.labels, ",") != "ui,theirs,mine" {
		t.Fatalf("after reload: v%d title=%q kind=%q body=%q labels=%v",
			f.original.Version, f.title.Value(), f.kind, f.body.Value(), f.labels)
	}
}

// The close prompt on an imported issue says the change is local.
func TestIssueClosePromptOnImportedIsLocalOnly(t *testing.T) {
	v := issueFixture()
	v.issue.AvailableActions = []string{"close"}
	v.update(registryKey(t, "X"))
	v.update(registryKey(t, "enter"))
	out := strings.Join(v.w.act.lines(400), "\n")
	for _, want := range []string{"vincent's copy only", "not written to GitHub yet", "next sync may overwrite it"} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirmation is missing %q:\n%s", want, out)
		}
	}
}

// `X` offers exactly the daemon's available_actions: none means no prompt.
func TestIssueStateKeyOffersOnlyAvailableActions(t *testing.T) {
	v := issueFixture()
	v.issue.AvailableActions = nil
	v.update(registryKey(t, "X"))
	if v.w.act != nil || !strings.Contains(v.note, "no state change") {
		t.Fatalf("X with no available action: act=%v note=%q", v.w.act, v.note)
	}
	v.issue.AvailableActions = []string{"reopen"}
	v.update(registryKey(t, "X"))
	if v.w.act == nil || v.w.act.action != "reopen" || v.w.act.stage != iaConfirm {
		t.Fatal("X on a closed imported issue did not go straight to the reopen confirmation")
	}
}
