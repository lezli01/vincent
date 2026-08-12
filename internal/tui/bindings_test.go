package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// T4.18. The binding registry is the single source `?`, the palette and the
// footer render from, which is exactly what makes an unhandled row worse than
// a missing one: the help promises a key and the app ignores it. `[` and `]`
// sat in the registry for a release doing nothing, because rendering a row
// proves only that the row exists.
//
// So the registry is walked, and every panel-scoped row has to prove itself
// against the real view. A row with no probe fails the test, which is what
// stops the next key from being added to the help alone.

// registryKey turns a registry key string back into the press that produces
// it. The round-trip assertion is the load-bearing half: a probe that fed a
// key the registry does not actually publish would prove nothing about the
// row it claims to cover, and a registry key no press can produce is itself
// a defect.
func registryKey(t *testing.T, key string) tea.KeyPressMsg {
	t.Helper()
	var msg tea.KeyPressMsg
	switch key {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		msg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "space":
		msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+s":
		msg = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+v":
		msg = tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}
	case "ctrl+c":
		msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		r := []rune(key)
		if len(r) != 1 {
			t.Fatalf("registryKey: no press known for %q — teach this helper", key)
		}
		msg = tea.KeyPressMsg{Code: r[0], Text: key}
	}
	if got := msg.String(); got != key {
		t.Fatalf("registryKey(%q) presses as %q — the registry key and the press disagree", key, got)
	}
	return msg
}

// offlineClient is a client the probes never call. Views whose whole effect
// is "start a fetch" guard on a nil client and hand back a nil command, so a
// probe for such a key needs a client to exist at all — and since no probe
// executes a returned command, this one never dials. Port 1 rather than a
// live test server: the probe is about the key, not about a round trip.
func offlineClient() *apiclient.Client {
	return apiclient.New("http://127.0.0.1:1", "probe-token")
}

// panelKeyProbes proves one panel-scoped binding each. A probe drives the
// real view with the key the registry publishes and asserts the effect the
// label promises — not that the key was merely swallowed, which is what `[`
// and `]` did.
//
// The two scroll rows are the one place a probe asserts routing rather than
// movement: with no rendered content a viewport cannot scroll, so they assert
// the press reached the pane's scroll path (which re-syncs follow) and leave
// the scrolling itself to the follow and mouse-wheel tests.
var panelKeyProbes = map[bindingContext]map[string]func(*testing.T){
	ctxTasks: {
		"down": func(t *testing.T) {
			s, _ := newShellFixture(t, task(1, stateRunning), task(2, stateRunning))
			s.focus = panelTasks
			before := s.board.selectedID
			s.update(registryKey(t, "down"))
			if s.board.selectedID == before {
				t.Fatalf("down did not move the task selection (still %d)", before)
			}
		},
		"enter": func(t *testing.T) {
			s, _ := newShellFixture(t, task(9, stateRunning))
			s.focus = panelTasks
			s.update(registryKey(t, "enter"))
			if s.detail.taskID != 9 {
				t.Fatalf("enter did not open the selected task: detail task = %d, want 9", s.detail.taskID)
			}
		},
		"/": func(t *testing.T) {
			s, _ := newShellFixture(t, task(1, stateRunning))
			s.focus = panelTasks
			s.update(registryKey(t, "/"))
			if !s.board.filtering {
				t.Fatal("/ did not open the task filter")
			}
		},
	},

	ctxTimeline: {
		"down": func(t *testing.T) {
			d := newTestDetail(t)
			d.taskID = 4
			loadDetail(d, []apiclient.StepRun{
				attempt(1, 0, 1, "implement", "failed", false),
				attempt(2, 0, 2, "implement", "succeeded", false),
			})
			d.focus = focusTimeline
			d.selectedRun = 1
			d.updateKey(registryKey(t, "down"))
			if d.selectedRun == 1 {
				t.Fatal("down did not move the timeline selection off the first attempt")
			}
		},
	},

	ctxOutput: {
		// The T4.18 defect itself: promised by the registry, handled nowhere.
		"]": func(t *testing.T) {
			d := newTestDetail(t)
			d.taskID = 4
			loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
			d.focus = focusOutput
			if d.tab != tabOutput {
				t.Fatalf("fixture starts on tab %v, want output", d.tab)
			}
			d.updateKey(registryKey(t, "]"))
			if d.tab != tabDiff {
				t.Fatal("] did not switch the output pane to the diff tab")
			}
			d.updateKey(registryKey(t, "]"))
			if d.tab != tabOutput {
				t.Fatal("] did not switch back to the output tab")
			}
		},
		"f": func(t *testing.T) {
			d := newTestDetail(t)
			d.taskID = 4
			loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
			d.focus = focusOutput
			d.following = false // as if the user had scrolled up
			d.updateKey(registryKey(t, "f"))
			if !d.following {
				t.Fatal("f did not re-arm follow")
			}
		},
		"v": func(t *testing.T) {
			d := newTestDetail(t)
			d.taskID = 4
			loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
			d.focus = focusOutput
			before := d.level
			d.updateKey(registryKey(t, "v"))
			if d.level == before {
				t.Fatalf("v did not change the output level (still %v)", before)
			}
		},
		"down": func(t *testing.T) {
			d := newTestDetail(t)
			d.taskID = 4
			loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
			d.focus = focusOutput
			d.following = false
			d.updateKey(registryKey(t, "down"))
			if !d.following {
				t.Fatal("down did not reach the output pane's scroll path (follow was never re-synced)")
			}
		},
		"e": func(t *testing.T) {
			var opened []string
			path := writeWholeTranscript(t)
			d := transcriptDetail(t, path, &opened)
			if cmd := d.updateKey(registryKey(t, "e")); cmd != nil {
				runCmd(t, cmd, 10*time.Second)
			}
			if len(opened) != 1 || opened[0] != path {
				t.Fatalf("e opened %v, want the attempt's transcript at %s", opened, path)
			}
		},
	},

	ctxNewTask: {
		"enter": func(t *testing.T) {
			n := loadedForm(t)
			moveTo(n, ntProject)
			if cmd := press(n, "enter"); n.mode == ntNavigating && cmd == nil {
				t.Fatal("enter opened neither a picker nor an editor on the focused field")
			}
		},
		"e": func(t *testing.T) {
			n := loadedForm(t)
			moveTo(n, ntDescription)
			if cmd := press(n, "e"); cmd == nil {
				t.Fatal("e did not open the description in $EDITOR")
			}
		},
		"+": func(t *testing.T) {
			n := loadedForm(t)
			moveTo(n, ntPriority)
			before := n.priority.Value()
			press(n, "+")
			if n.priority.Value() == before {
				t.Fatalf("+ did not nudge the priority (still %q)", before)
			}
		},
		"R": func(t *testing.T) {
			n := loadedForm(t)
			n.client = offlineClient()
			if cmd := press(n, "R"); cmd == nil {
				t.Fatal("R did not start a re-probe")
			}
		},
		"ctrl+s": func(t *testing.T) {
			n := loadedForm(t)
			cmd := press(n, "ctrl+s")
			// With no title typed, submitting is refused rather than sent —
			// either answer proves the key was handled, and a silent no-op
			// would prove it was not.
			if cmd == nil && n.err == "" && len(n.rowErr) == 0 {
				t.Fatal("ctrl+s neither submitted nor reported why it would not")
			}
		},
	},

	ctxProjects: {
		"a": func(t *testing.T) {
			p := newProjectsView()
			loadedProjects(p, []apiclient.Project{testProject(1, "api")}, nil)
			p.updateKey(registryKey(t, "a"))
			if p.form == nil {
				t.Fatal("a did not open the registration form")
			}
		},
		"enter": func(t *testing.T) {
			p := newProjectsView()
			loadedProjects(p, []apiclient.Project{testProject(1, "api")}, nil)
			p.updateKey(registryKey(t, "enter"))
			if p.form == nil {
				t.Fatal("enter did not open the selected project for editing")
			}
		},
		"d": func(t *testing.T) {
			p := newProjectsView()
			loadedProjects(p, []apiclient.Project{testProject(1, "api")}, nil)
			p.updateKey(registryKey(t, "d"))
			if p.confirm == nil {
				t.Fatal("d did not ask before removing the project")
			}
		},
		"/": func(t *testing.T) {
			p := newProjectsView()
			loadedProjects(p, []apiclient.Project{testProject(1, "api")}, nil)
			p.updateKey(registryKey(t, "/"))
			if !p.filtering {
				t.Fatal("/ did not open the project filter")
			}
		},
		"ctrl+s": func(t *testing.T) {
			p := newProjectsView()
			loadedProjects(p, []apiclient.Project{testProject(1, "api")}, nil)
			p.updateKey(registryKey(t, "a"))
			if p.form == nil {
				t.Fatal("a did not open the form this row is about")
			}
			_, cmd := p.updateKey(registryKey(t, "ctrl+s"))
			if cmd == nil && p.form.err == "" && !p.form.saving {
				t.Fatal("ctrl+s neither saved nor reported why it would not")
			}
		},
	},

	ctxWorkflows: {
		"enter": func(t *testing.T) {
			w := newWorkflowsView()
			loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
			before := w.expanded
			w.updateKey(registryKey(t, "enter"))
			if w.expanded == before {
				t.Fatalf("enter did not toggle the step list (still %v)", before)
			}
		},
		"e": func(t *testing.T) {
			w := newWorkflowsView()
			loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
			if _, cmd := w.updateKey(registryKey(t, "e")); cmd == nil {
				t.Fatal("e did not open the workflow file in $EDITOR")
			}
		},
		"R": func(t *testing.T) {
			w := newWorkflowsView()
			w.client = offlineClient()
			loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
			w.err = "a stale note the reload should clear"
			_, cmd := w.updateKey(registryKey(t, "R"))
			if cmd == nil {
				t.Fatal("R did not re-read the registry")
			}
			if w.err != "" {
				t.Errorf("R left the previous error on screen: %q", w.err)
			}
		},
	},

	ctxDaemon: {
		"R": func(t *testing.T) {
			d := newTestDaemonView([]string{"a log line"}, nil)
			if _, cmd := d.updateKey(registryKey(t, "R")); cmd == nil {
				t.Fatal("R did not refresh the daemon view")
			}
		},
		"f": func(t *testing.T) {
			d := newTestDaemonView([]string{"a log line"}, nil)
			d.following = false
			d.updateKey(registryKey(t, "f"))
			if !d.following {
				t.Fatal("f did not re-arm the log follow")
			}
		},
		"down": func(t *testing.T) {
			d := newTestDaemonView([]string{"a log line"}, nil)
			d.following = false
			d.updateKey(registryKey(t, "down"))
			if !d.following {
				t.Fatal("down did not reach the log pane's scroll path (follow was never re-synced)")
			}
		},
	},

	ctxForm: {
		"space": func(t *testing.T) {
			f := newAnswerForm(questionRequest())
			f.update(registryKey(t, "space"), nil, 1)
			if len(f.answers) == 0 {
				t.Fatal("space did not pick the highlighted option")
			}
		},
		"e": func(t *testing.T) {
			f := newAnswerForm(questionRequest())
			f.update(registryKey(t, "e"), nil, 1)
			if !f.editing {
				t.Fatal("e did not open the free-text field")
			}
		},
		"enter": func(t *testing.T) {
			f := newAnswerForm(questionRequest())
			cmd, exit := f.update(registryKey(t, "enter"), nil, 1)
			// Nothing is answered yet, so submitting is refused — with a
			// reason. Any of the three proves the key was handled.
			if cmd == nil && !exit && f.err == "" {
				t.Fatal("enter neither submitted nor said why it would not")
			}
		},
		"esc": func(t *testing.T) {
			f := newAnswerForm(questionRequest())
			if _, exit := f.update(registryKey(t, "esc"), nil, 1); !exit {
				t.Fatal("esc did not close the form")
			}
		},
	},
}

// TestEveryPanelKeyIsHandled walks the registry. Every panel-scoped row must
// have a probe, and every probe must pass — so a key can reach the help only
// by way of a view that answers it.
func TestEveryPanelKeyIsHandled(t *testing.T) {
	covered := make(map[string]bool)
	for _, b := range bindings {
		if b.scope != scopePanel || b.key == "" {
			continue
		}
		name := string(b.context) + "/" + b.key
		covered[name] = true
		probe, ok := panelKeyProbes[b.context][b.key]
		if !ok {
			t.Errorf("%s: the registry promises this key and nothing proves the view handles it — "+
				"add a probe to panelKeyProbes", name)
			continue
		}
		t.Run(name, probe)
	}

	// A probe outliving its row is dead weight that reads as coverage.
	for ctx, keys := range panelKeyProbes {
		for key := range keys {
			if !covered[string(ctx)+"/"+key] {
				t.Errorf("%s/%s: probe for a binding the registry no longer has", ctx, key)
			}
		}
	}
}

// TestOutputTabKeysAreAliases pins the aliasing the label claims: `[`, `]`
// and `d` are one control, so the help can keep naming `]` canonical while
// the other two keep working. Separate from the registry walk, which only
// asks whether the canonical key does anything.
func TestOutputTabKeysAreAliases(t *testing.T) {
	for _, key := range []string{"]", "[", "d"} {
		d := newTestDetail(t)
		d.taskID = 4
		loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
		d.focus = focusOutput

		d.updateKey(registryKey(t, key))
		if d.tab != tabDiff {
			t.Errorf("%q did not switch to the diff tab", key)
			continue
		}
		d.updateKey(registryKey(t, key))
		if d.tab != tabOutput {
			t.Errorf("%q did not switch back", key)
		}
	}
}

// TestOutputTabKeysWorkFromEitherFocus: the keys act on the output pane, and
// having to focus the pane first is a step nobody would guess at — the same
// reasoning `v` already carries.
func TestOutputTabKeysWorkFromEitherFocus(t *testing.T) {
	for _, focus := range []detailFocus{focusTimeline, focusOutput} {
		d := newTestDetail(t)
		d.taskID = 4
		loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
		d.focus = focus

		d.updateKey(registryKey(t, "]"))
		if d.tab != tabDiff {
			t.Errorf("focus %v: ] did not switch the tab", focus)
		}
	}
}
