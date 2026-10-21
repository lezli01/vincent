package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The #803 reproductions: the projects and daemon takeovers at 80×24, and the
// footer in their sub-modes.

// daemonRoot is a connected root at w×h showing a test daemon view with every
// source loaded and logLines as its tail.
func daemonRoot(t *testing.T, w, h int, logLines []string) (*root, *daemonView) {
	t.Helper()
	m := connectedRoot(t)
	d := newTestDaemonView(logLines, nil)
	d.update(daemonInfoMsg{info: testInfo()})
	d.update(daemonConfigMsg{config: testConfig()})
	d.update(daemonLogMsg{lines: logLines})
	m.views[viewDaemon] = d
	m.active = viewDaemon
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m, d
}

// DM-6: the config editor, the status-line flow and the skills offer each own
// the keyboard, so the footer has to come from their rows, not ctxDaemon's.
func TestDaemonFooterFollowsItsSubModes(t *testing.T) {
	m, d := daemonRoot(t, 80, 24, []string{"level=INFO msg=ready"})
	if ctx := m.activeContext(); ctx != ctxDaemon {
		t.Fatalf("the daemon view's own context = %q", ctx)
	}
	for i, k := range d.keys {
		if k.path == "log_level" {
			d.cursor = i
			d.form = newConfigForm(k, d.config)
		}
	}
	if ctx := m.activeContext(); ctx != ctxConfigEdit {
		t.Errorf("with the config editor open the context is %q, want %q", ctx, ctxConfigEdit)
	}
	d.form = nil

	d.skills = &skillsFlow{}
	if ctx := m.activeContext(); ctx != ctxSkills {
		t.Errorf("with the skills offer open the context is %q, want %q", ctx, ctxSkills)
	}
	d.skills = nil

	d.statusLine = newStatusLineFlow(d.statusLinePlan, d.statusLineErr)
	if ctx := m.activeContext(); ctx == ctxDaemon {
		t.Errorf("with the status-line flow open the footer still shows %q rows", ctx)
	}
}

// DM-7: with the log focused, enter must not open the editor on a config key
// the user cannot see (the cursor's key, listen by default — a dangerous one).
func TestDaemonEnterOnTheLogNeverOpensTheEditor(t *testing.T) {
	_, d := daemonRoot(t, 80, 24, []string{"level=INFO msg=ready"})
	d.focusConfig = false
	d.update(namedKey("enter"))
	if d.form != nil {
		t.Errorf("enter with the log focused opened the editor on %q", d.form.key.path)
	}
}

// DM-1: at 80×24 and 78×20 the daemon view fits its frame, with the adapters
// and at least three log lines on screen and nothing cut by frame().
func TestDaemonViewShowsTheLogAt80x24(t *testing.T) {
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("level=INFO msg=line-%02d", i))
	}
	for _, sz := range [][2]int{{80, 24}, {78, 20}} {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			m, _ := daemonRoot(t, sz[0], sz[1], lines)
			screen := ansi.Strip(content(m))
			if !strings.Contains(screen, "claude") {
				t.Errorf("the adapters block is not on screen:\n%s", screen)
			}
			for _, want := range []string{"line-37", "line-38", "line-39"} {
				if !strings.Contains(screen, want) {
					t.Errorf("log line %s is not on screen:\n%s", want, screen)
				}
			}
			assertNoFrameClip(t, screen)
		})
	}
}

// With the log focused the blocks pane is pinned to its end, and it starts at
// a block's heading under a cue naming the key that reaches what it hides:
// no config row runs on under identity, and no "↑ more" offers a scroll no
// key performs (review F24).
func TestDaemonPinnedBlocksStartAtAHeading(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {78, 20}, {80, 30}} {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			_, d := daemonRoot(t, sz[0], sz[1], []string{"level=INFO msg=ready"})
			d.focusConfig = false
			lines := strings.Split(ansi.Strip(d.render(sz[0]-2, sz[1]-3)), "\n")
			at := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "/data/logs/daemon.log") })
			if at < 0 || at+2 >= len(lines) {
				t.Fatalf("no identity block:\n%s", strings.Join(lines, "\n"))
			}
			next := strings.TrimSpace(lines[at+1])
			if strings.Contains(next, "↑") {
				if !strings.Contains(next, "tab") {
					t.Errorf("the cue names no key: %q", next)
				}
				next = strings.TrimSpace(lines[at+2])
			}
			heading := slices.ContainsFunc([]string{"config in effect", "database", "adapters"}, func(h string) bool {
				return strings.HasPrefix(next, h)
			})
			if next != "" && !heading {
				t.Errorf("the pane starts mid-block, under identity: %q\n%s", next, strings.Join(lines, "\n"))
			}
			for _, l := range lines {
				if strings.Contains(l, "↑ more") {
					t.Errorf("a scroll cue no key answers: %q", l)
				}
			}
		})
	}
}

// DM-4: confirming a long notify.command shows the whole value and the keys
// that answer the question, at 80 columns.
func TestConfigConfirmationShowsALongValueAndItsKeys(t *testing.T) {
	d := openConfigForm(t, "notify.command")
	value := "/usr/local/bin/notify-send --app-name=vincent --urgency=critical " +
		strings.Repeat("--hint=string:x-vincent:abcdefgh ", 4) + "--expire-time=10000 END"
	d.form.input.SetValue(value)
	d.update(namedKey("enter"))
	if !d.form.confirming {
		t.Fatal("notify.command did not ask for confirmation")
	}
	out := ansi.Strip(strings.Join(d.form.render(80), "\n"))
	for _, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("confirmation line is %d columns wide, the frame clips it: %q", w, l)
		}
	}
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, "END") || !strings.Contains(flat, "y confirm") {
		t.Errorf("the confirmation does not show the full value and its keys at 80 columns:\n%s", out)
	}
}

// PR-3: inside the project form the footer shows the form's keys, not the
// table's add/remove/filter rows.
func TestProjectFormFooterShowsNoListRows(t *testing.T) {
	m := connectedRoot(t)
	m.Update(selectViewMsg{id: viewProjects})
	m.Update(key("a"))
	m.Update(enterKey())
	if m.views[viewProjects].(*projectsView).form == nil {
		t.Fatal("the add form did not open")
	}
	foot := ansi.Strip(m.footerLine())
	for _, row := range []string{"a add", "D remove", "/ filter", "tab needs you"} {
		if strings.Contains(foot, row) {
			t.Errorf("the project form's footer offers the list row %q: %q", row, foot)
		}
	}
	// a then enter leaves the path field being typed into (review F7): the
	// form's ↑/↓ and "enter edit" do nothing there, and ctrl+s saves.
	for _, row := range []string{"↑/↓ fields", "enter edit"} {
		if strings.Contains(foot, row) {
			t.Errorf("the footer offers %q while a field is being typed into: %q", row, foot)
		}
	}
	for _, row := range []string{"enter done", "ctrl+s save", "esc done"} {
		if !strings.Contains(foot, row) {
			t.Errorf("the footer is missing %q while a field is being typed into: %q", row, foot)
		}
	}
	p := m.views[viewProjects].(*projectsView)
	_, cmd := p.updateKey(registryKey(t, "ctrl+s"))
	if p.form.editing || cmd == nil && p.form.err == "" && !p.form.saving {
		t.Errorf("ctrl+s in the path field did nothing: editing=%v err=%q", p.form.editing, p.form.err)
	}
}

// PR-4: a 110-character project name must not push #id, state and title off
// a needs-you row at 80 columns.
func TestProjectNeedsYouRowsSurviveALongName(t *testing.T) {
	p := newProjectsView()
	long := strings.Repeat("very-long-project-name-", 5)[:110]
	loadedProjects(p,
		[]apiclient.Project{testProject(1, long)},
		[]apiclient.Task{{
			ID: 42, ProjectID: 1, ProjectName: long, State: stateBlocked, Title: "fix-the-thing",
			UpdatedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		}})
	out := ansi.Strip(p.render(80, 22))
	for _, want := range []string{"#42", "fix-the-thing"} {
		if !strings.Contains(out, want) {
			t.Errorf("the needs-you row lost %q behind the long name:\n%s", want, out)
		}
	}
}
