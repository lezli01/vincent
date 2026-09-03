package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/tui/workflowgraph"
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
	case "right":
		msg = tea.KeyPressMsg{Code: tea.KeyRight}
	case "left":
		msg = tea.KeyPressMsg{Code: tea.KeyLeft}
	case "space":
		msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+s":
		msg = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+o":
		msg = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	case "ctrl+v":
		msg = tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}
	case "ctrl+c":
		msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+t":
		msg = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	case "ctrl+x":
		msg = tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	case "ctrl+y":
		msg = tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
	case "ctrl+r":
		msg = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	case "ctrl+g":
		msg = tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}
	case "pgup":
		msg = tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		msg = tea.KeyPressMsg{Code: tea.KeyPgDown}
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

// diffTabDetail is a detail view sitting on the diff tab with a diff loaded,
// which is the only state the ctxDiff rows exist in.
func diffTabDetail(t *testing.T) *detail {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
	d.focus = focusOutput
	d.tab = tabDiff
	d.diff.openTask(d.taskID)
	d.diff.apply(diffLoadedMsg{taskID: d.taskID, text: twoFileDiff})
	return d
}

func tabbedTaskFixture(t *testing.T, tab taskViewTab) *taskView {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{
		attempt(1, 0, 1, "implement", "failed", false),
		attempt(2, 0, 2, "implement", "succeeded", false),
	})
	v := newTaskView(d)
	v.tab = tab
	return v
}

// repairFormFixture is a repair popup as `R` on a blocked task opens it.
func repairFormFixture() *repairForm {
	return newRepairForm(7, "check_failed", "build")
}

// foldingShell is a shell whose board is grouped and focused on the task
// table — the only state the four fold keys exist in. newShellFixture's board
// is flat, where they are deliberately inert (task 054 decision 5).
func foldingShell(t *testing.T) *shell {
	t.Helper()
	s, _ := newShellFixture(t,
		task(1, stateQueued, inProject("api"), inWorkflow("build")),
		task(2, stateQueued, inProject("web"), inWorkflow("build")),
	)
	s.focus = panelTasks
	s.board.group, s.board.configGroup = defaultGrouping(), defaultGrouping()
	s.board.dataDir, s.board.foldsLoaded = "", true
	s.render(120, 37)
	return s
}

// laneOpenProbe is the shared `l` assertion: on the given tab, the press
// resolves the reader's position to a lane and opens that lane's workspace,
// remembering the parent so `esc` comes back to it (issue #316).
func laneOpenProbe(tab taskViewTab) func(*testing.T) {
	return func(t *testing.T) {
		v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
		v.width, v.height = 140, 40
		v.setTab(tab)
		cmd := v.updateKey(registryKey(t, "l"))
		if cmd == nil {
			t.Fatalf("l on tab %v produced no command", tab)
		}
		msg, ok := cmd().(openTaskMsg)
		if !ok {
			t.Fatalf("l on tab %v produced %T, want openTaskMsg", tab, cmd())
		}
		if msg.id != 42 || msg.from != 7 {
			t.Fatalf("l on tab %v opened %+v, want lane task 42 from parent 7", tab, msg)
		}
	}
}

// laneTaskView is a lane: a task whose parent is the fan-out that derived it,
// which is the only state `U` exists in.
func laneTaskView(t *testing.T, tab taskViewTab) *taskView {
	t.Helper()
	d := taskDetailFixture(t)
	parent := int64(7)
	d.taskID = 42
	d.task.Task = apiclient.Task{
		ID: 42, ProjectID: 3, Title: "api lane", State: stateBlocked, ParentTaskID: &parent,
	}
	v := newTaskView(d)
	v.width, v.height = 140, 40
	v.setTab(tab)
	return v
}

// laneParentProbe is the shared `U` assertion: from a lane, the press opens
// the fan-out that derived it.
func laneParentProbe(tab taskViewTab) func(*testing.T) {
	return func(t *testing.T) {
		v := laneTaskView(t, tab)
		if id, ok := v.parentJump(); !ok || id != 7 {
			t.Fatalf("parentJump on tab %v = (%d, %v), want (7, true)", tab, id, ok)
		}
		cmd := v.updateKey(registryKey(t, "U"))
		if cmd == nil {
			t.Fatalf("U on tab %v produced no command", tab)
		}
		msg, ok := cmd().(openTaskMsg)
		if !ok || msg.id != 7 || msg.from != 42 {
			t.Fatalf("U on tab %v opened %+v, want parent 7 from lane 42", tab, msg)
		}
	}
}

// laneSelectProbe is the shared `<` / `>` assertion: the press walks the
// Output pane's lane selector, whose cycle includes the task's own output —
// so one press from there lands on the first lane going forwards and on the
// last going back.
func laneSelectProbe(delta int, want int64) func(*testing.T) {
	return func(t *testing.T) {
		key := ">"
		if delta < 0 {
			key = "<"
		}
		v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
		v.width, v.height = 140, 40
		v.setTab(taskTabOutput)
		if _, ok := v.outputLane(); ok {
			t.Fatal("the Output pane starts on a lane, want the task's own output")
		}
		v.updateKey(registryKey(t, key))
		if id, ok := v.outputLane(); !ok || id != want {
			t.Fatalf("%s selected (%d, %v), want lane task %d", key, id, ok, want)
		}
	}
}

// fanOutGraphTaskView is the parent's Workflow tab with a fan_out drawn on it
// and the cursor inside the `web` lane — the one tab whose `l` resolves to a
// lane the failure does not blame.
func fanOutGraphTaskView(t *testing.T) *taskView {
	t.Helper()
	v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
	v.width, v.height = 160, 40
	v.setTab(taskTabWorkflow)
	v.update(taskWorkflowMsg{taskID: 7, def: apiclient.TaskWorkflow{
		TaskID: 7, Name: "fixture", Definition: &apiclient.WorkflowBody{
			Name: "fixture", Steps: []apiclient.WorkflowStepDef{
				{ID: "plan", Type: "agent"},
				{ID: "lanes", Type: stepTypeFanOut, Lanes: []apiclient.WorkflowLaneDef{
					{ID: "api", Steps: []apiclient.WorkflowStepDef{{ID: "build", Type: "command"}}},
					{ID: "web", Steps: []apiclient.WorkflowStepDef{{ID: "build", Type: "command"}}},
				}},
			},
		},
	}})
	for _, col := range v.workflow.graph.Lanes() {
		if col.Key == workflowgraph.LaneKey("lanes", "web") && len(col.Nodes) > 0 {
			v.workflow.graph.Select(col.Nodes[0])
			return v
		}
	}
	t.Fatalf("the graph drew no node inside the web lane")
	return nil
}

func followUpFormFixture() *followUpForm {
	return newFollowUpForm(7, 1, "done")
}

// popupTaskViewFixture is a task workspace with one of the three form popups
// raised on it. ctrl+t belongs to the view rather than to a form (task 059
// decision 4) — it is taken before the form ever sees the press — so its
// probes drive this rather than a bare form.
func popupTaskViewFixture(t *testing.T, open func(*detail)) *taskView {
	t.Helper()
	v := newTaskView(taskDetailFixture(t))
	open(v.detail)
	v.openPopup()
	v.width, v.height = 100, 30
	return v
}

// popupTabProbe is the shared ctrl+t assertion: the press reaches the view
// through whatever the form has open, and the popup changes tab.
func popupTabProbe(open func(*detail)) func(*testing.T) {
	return func(t *testing.T) {
		v := popupTaskViewFixture(t, open)
		v.updateKey(registryKey(t, "ctrl+t"))
		if v.popupTab != popupTabDetails {
			t.Fatal("ctrl+t did not switch the popup to its Task details tab")
		}
		if !v.popup {
			t.Fatal("ctrl+t closed the popup instead of switching its tab")
		}
	}
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
		"g": func(t *testing.T) {
			s, _ := newShellFixture(t, task(1, stateRunning))
			s.focus = panelTasks
			s.board.group = defaultGrouping()
			s.update(registryKey(t, "g"))
			if s.board.group.equal(defaultGrouping()) {
				t.Fatalf("g did not change the grouping (still %s)", s.board.group.label())
			}
		},
		"space": func(t *testing.T) {
			s, _ := newShellFixture(t, task(7, stateDone))
			s.focus = panelTasks
			s.update(registryKey(t, "space"))
			if !s.board.marks.has(7) {
				t.Fatalf("space did not select the task under the cursor (marks %v)", s.board.marks)
			}
			s.update(registryKey(t, "space"))
			if s.board.hasMarks() {
				t.Fatalf("space did not deselect it again (marks %v)", s.board.marks)
			}
		},
		"V": func(t *testing.T) {
			s, _ := newShellFixture(t, task(1, stateDone), task(2, stateDone))
			s.focus = panelTasks
			s.update(registryKey(t, "V"))
			if len(s.board.marks) != 2 {
				t.Fatalf("V selected %v, want both visible tasks", s.board.marks)
			}
			s.update(registryKey(t, "V"))
			if s.board.hasMarks() {
				t.Fatalf("V did not clear the selection (marks %v)", s.board.marks)
			}
		},
		"L": func(t *testing.T) {
			s, _ := newShellFixture(t, task(4, stateAwaitingChildren))
			s.focus = panelTasks
			s.update(registryKey(t, "L"))
			if !s.board.lanes.expanded.has(4) {
				t.Fatalf("L did not expand the fan-out (expanded %v)", s.board.lanes.expanded)
			}
			s.update(registryKey(t, "L"))
			if s.board.lanes.expanded.has(4) {
				t.Fatalf("L did not collapse it again (expanded %v)", s.board.lanes.expanded)
			}
			// A task with no lanes answers the press with none: the fetch
			// comes back empty and the row closes itself, so the marker never
			// promises rows that are not there.
			s2, _ := newShellFixture(t, task(5, stateRunning))
			s2.focus = panelTasks
			s2.update(registryKey(t, "L"))
			s2.update(boardLanesMsg{parentID: 5})
			if s2.board.lanes.expanded.has(5) {
				t.Fatalf("an empty lane fetch left task 5 expanded (%v)", s2.board.lanes.expanded)
			}
		},
		"left": func(t *testing.T) {
			s := foldingShell(t)
			s.update(registryKey(t, "left"))
			s.render(120, 37)
			if !s.board.folds.has(foldPath{"api", "build"}) {
				t.Fatalf("left did not collapse the cursor's group (folds %v)", s.board.folds)
			}
			// Again, on the header it just closed: ← walks outwards.
			s.update(registryKey(t, "left"))
			s.render(120, 37)
			if !s.board.folds.has(foldPath{"api"}) {
				t.Fatalf("a second left did not collapse the parent (folds %v)", s.board.folds)
			}
		},
		"right": func(t *testing.T) {
			s := foldingShell(t)
			s.update(registryKey(t, "left"))
			s.render(120, 37)
			s.update(registryKey(t, "right"))
			s.render(120, 37)
			if s.board.folds.has(foldPath{"api", "build"}) {
				t.Fatalf("right did not expand the group under the cursor (folds %v)", s.board.folds)
			}
		},
		"C": func(t *testing.T) {
			s := foldingShell(t)
			s.update(registryKey(t, "C"))
			s.render(120, 37)
			for _, want := range []foldPath{{"api"}, {"api", "build"}} {
				if !s.board.folds.has(want) {
					t.Fatalf("C did not collapse %v (folds %v)", want, s.board.folds)
				}
			}
		},
		"O": func(t *testing.T) {
			s := foldingShell(t)
			s.update(registryKey(t, "C"))
			s.render(120, 37)
			s.update(registryKey(t, "O"))
			s.render(120, 37)
			if len(s.board.folds) != 0 {
				t.Fatalf("O left folds behind: %v", s.board.folds)
			}
		},
	},

	ctxTimeline: {
		"tab": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabSteps)
			v.updateKey(registryKey(t, "tab"))
			if v.tab != taskTabDetails {
				t.Fatalf("tab moved to %v, want Task Details", v.tab)
			}
		},
		"]": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabSteps)
			v.updateKey(registryKey(t, "]"))
			if v.tab != taskTabDetails {
				t.Fatalf("] moved to %v, want Task Details", v.tab)
			}
		},
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
		"enter": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabSteps)
			selected := v.detail.selectedRun
			v.updateKey(registryKey(t, "enter"))
			if v.tab != taskTabOutput || v.detail.selectedRun != selected {
				t.Fatalf("enter opened tab %v with run %d, want Output with run %d", v.tab, v.detail.selectedRun, selected)
			}
		},
		// The five fold keys (issue #317). Each drives the workspace on a
		// task whose step 1 has three passes, where the two earlier tiers are
		// folded shut and the cursor starts on the first of them.
		"space": func(t *testing.T) {
			v := tierTaskView(t, 1, 2, 3)
			selectTier(t, v.detail, 1)
			v.updateKey(registryKey(t, "space"))
			if v.detail.timelineFolded() {
				t.Fatal("space did not open the tier under the cursor")
			}
			v.updateKey(registryKey(t, "space"))
			if !v.detail.timelineFolded() {
				t.Fatal("space did not close it again")
			}
		},
		"right": func(t *testing.T) {
			v := tierTaskView(t, 1, 2, 3)
			selectTier(t, v.detail, 1)
			v.updateKey(registryKey(t, "right"))
			if v.detail.timelineFolded() {
				t.Fatal("→ did not open the folded tier")
			}
		},
		"left": func(t *testing.T) {
			v := tierTaskView(t, 1, 2, 3)
			selectTier(t, v.detail, 3)
			v.updateKey(registryKey(t, "left"))
			if !v.detail.timelineFolded() {
				t.Fatal("← did not close the tier the cursor is in")
			}
		},
		"O": func(t *testing.T) {
			v := tierTaskView(t, 1, 2, 3)
			v.updateKey(registryKey(t, "O"))
			if got := v.detail.renderTimeline(200); strings.Contains(got, diffFoldClosed) {
				t.Fatalf("O left a tier folded:\n%s", got)
			}
		},
		"C": func(t *testing.T) {
			v := tierTaskView(t, 1, 2, 3)
			v.updateKey(registryKey(t, "C"))
			got := v.detail.renderTimeline(200)
			if strings.Contains(got, diffFoldOpen) {
				t.Fatalf("C left a tier open:\n%s", got)
			}
		},
		"l": laneOpenProbe(taskTabSteps),
		"U": laneParentProbe(taskTabSteps),
	},

	ctxTaskDetails: {
		"tab": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabDetails)
			v.updateKey(registryKey(t, "tab"))
			if v.tab != taskTabOutput {
				t.Fatalf("tab moved to %v, want Output", v.tab)
			}
		},
		"]": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabDetails)
			v.updateKey(registryKey(t, "]"))
			if v.tab != taskTabOutput {
				t.Fatalf("] moved to %v, want Output", v.tab)
			}
		},
		"down": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabDetails)
			v.renderDetails(100, 24)
			v.updateKey(registryKey(t, "down"))
			if v.details.section != "Overview" {
				t.Fatalf("down selected %q, want Overview", v.details.section)
			}
		},
		"o": func(t *testing.T) {
			withFakeOpener(t, nil)
			v := taskPullFixture(t, apiclient.GitHubTaskPull{
				Linked: true, Repo: "octo/api", Number: 41,
				Pull: &apiclient.GitHubPullRequest{URL: "https://github.com/octo/api/pull/41"},
			})
			if cmd := v.updateKey(registryKey(t, "o")); cmd == nil {
				t.Fatal("o did not open the task's pull request")
			}
		},
		"P": func(t *testing.T) {
			v := taskPullFixture(t, apiclient.GitHubTaskPull{CompareURL: compareURLFixture})
			v.updateKey(registryKey(t, "P"))
			if v.createPR == nil {
				t.Fatal("P did not open the compare-URL editor")
			}
		},
		"l": laneOpenProbe(taskTabDetails),
		"U": laneParentProbe(taskTabDetails),
	},

	// The chat surfaces (task 067). Every probe drives the real view with
	// the key the registry publishes and asserts the effect the label
	// promises.
	ctxChats: {
		"enter": func(t *testing.T) {
			v := chatsFixture()
			_, cmd := v.updateKey(registryKey(t, "enter"))
			if cmd == nil {
				t.Fatal("enter did not open the selected chat's workspace")
			}
			if _, ok := drain(cmd).(openChatMsg); !ok {
				t.Fatalf("enter produced %T, want openChatMsg", drain(cmd))
			}
		},
		"n": func(t *testing.T) {
			v := chatsFixture()
			if _, cmd := v.updateKey(registryKey(t, "n")); cmd != nil {
				drain(cmd)
			}
			if v.create == nil {
				t.Fatal("n did not open the new-chat form")
			}
		},
		"a": func(t *testing.T) {
			v := chatsFixture()
			v.updateKey(registryKey(t, "a"))
			if v.confirm == nil {
				t.Fatal("a did not ask before archiving")
			}
		},
		"/": func(t *testing.T) {
			v := chatsFixture()
			v.updateKey(registryKey(t, "/"))
			if !v.filtering {
				t.Fatal("/ did not open the chats filter")
			}
		},
		"left": func(t *testing.T) {
			v := chatsFixture()
			v.cursor = 0
			if _, cmd := v.updateKey(registryKey(t, "left")); cmd != nil {
				drain(cmd)
			}
			if !v.folds.has(foldPath{"repo"}) {
				t.Fatal("left did not collapse the project group")
			}
		},
		"right": func(t *testing.T) {
			v := chatsFixture()
			v.folds = foldSet{foldPath{"repo"}}
			v.cursor = 0
			if _, cmd := v.updateKey(registryKey(t, "right")); cmd != nil {
				drain(cmd)
			}
			if v.folds.has(foldPath{"repo"}) {
				t.Fatal("right did not expand the project group")
			}
		},
		"s": func(t *testing.T) {
			v := chatsFixture()
			v.client = nil
			if _, cmd := v.updateKey(registryKey(t, "s")); cmd != nil {
				drain(cmd)
			}
			if v.scope != apiclient.ArchivedOnly {
				t.Fatalf("s left the listing at %q, want the archived one", v.scope)
			}
		},
		"r": func(t *testing.T) {
			v := chatsFixture()
			v.client = nil
			// With no client the reload is a nil command; what the probe
			// asserts is that the key reached the reload path rather than
			// being swallowed by the filter or the confirmation.
			if _, cmd := v.updateKey(registryKey(t, "r")); cmd != nil {
				drain(cmd)
			}
			if v.filtering || v.confirm != nil {
				t.Fatal("r was taken by another layer")
			}
		},
	},
	ctxChat: {
		"enter": func(t *testing.T) {
			v := chatViewFixture()
			v.client = offlineClient()
			v.composer.SetValue("hello")
			if _, cmd := v.updateKey(registryKey(t, "enter")); cmd == nil {
				t.Fatal("enter did not send the message")
			}
			if v.composer.Value() != "" {
				t.Fatalf("the composer still holds %q after a send", v.composer.Value())
			}
		},
		"ctrl+x": func(t *testing.T) {
			v := chatViewFixture()
			v.client = offlineClient()
			v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "running"}}
			if _, cmd := v.updateKey(registryKey(t, "ctrl+x")); cmd == nil {
				t.Fatal("ctrl+x did not stop the running turn")
			}
		},
		"ctrl+t": func(t *testing.T) {
			v := chatViewFixture()
			v.client = offlineClient()
			_, cmd := v.updateKey(registryKey(t, "ctrl+t"))
			if cmd == nil {
				t.Fatal("ctrl+t did not open the handoff form")
			}
			msg, ok := cmd().(newTaskFromChatMsg)
			if !ok {
				t.Fatalf("ctrl+t produced %T, want newTaskFromChatMsg", cmd())
			}
			if msg.chat.ID != v.chat.ID {
				t.Fatalf("handoff seeded chat %d, want %d", msg.chat.ID, v.chat.ID)
			}
			if v.composer.Value() != "" {
				t.Fatalf("ctrl+t reached the composer: %q", v.composer.Value())
			}
		},
		"ctrl+r": func(t *testing.T) {
			v := chatViewFixture()
			before := v.level.get()
			v.updateKey(registryKey(t, "ctrl+r"))
			if v.level.get() == before {
				t.Fatal("ctrl+r did not cycle the level")
			}
			if v.composer.Value() != "" {
				t.Fatalf("ctrl+r reached the composer: %q", v.composer.Value())
			}
		},
		"pgup": func(t *testing.T) {
			v := chatViewFixture()
			v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "done", Prompt: "hi"}}
			for i := range 200 {
				v.turnRecords[1] = append(v.turnRecords[1],
					apiclient.TranscriptRecord{Type: "agent.output", Text: fmt.Sprintf("line %d", i)})
			}
			v.bodyDirty = true
			v.render(60, 30)
			v.updateKey(registryKey(t, "pgup"))
			if v.following {
				t.Fatal("pgup left the body following the live end")
			}
		},
		"ctrl+o": func(t *testing.T) {
			v := chatViewFixture()
			before := v.raw.get()
			v.updateKey(registryKey(t, "ctrl+o"))
			if v.raw.get() == before {
				t.Fatal("ctrl+o did not toggle raw mode")
			}
			if v.composer.Value() != "" {
				t.Fatalf("ctrl+o reached the composer: %q", v.composer.Value())
			}
		},
		"ctrl+y": func(t *testing.T) {
			v := chatViewFixture()
			v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "done", Prompt: "hi"}}
			v.turnRecords[1] = []apiclient.TranscriptRecord{
				{Type: "agent.output", Text: "# Findings\n\nall good"},
			}
			_, cmd := v.updateKey(registryKey(t, "ctrl+y"))
			if cmd == nil {
				t.Fatal("ctrl+y did not open the copy picker")
			}
			msg, ok := drain(cmd).(openCopyPickerMsg)
			if !ok || len(msg.items) == 0 {
				t.Fatalf("ctrl+y produced %#v, want a copy picker with rows", drain(cmd))
			}
			if v.composer.Value() != "" {
				t.Fatalf("ctrl+y reached the composer: %q", v.composer.Value())
			}
		},
		"ctrl+g": func(t *testing.T) {
			v := chatViewFixture()
			v.following = false
			v.updateKey(registryKey(t, "ctrl+g"))
			if !v.following {
				t.Fatal("ctrl+g did not re-arm follow")
			}
		},
		"esc": func(t *testing.T) {
			v := chatViewFixture()
			_, cmd := v.updateKey(registryKey(t, "esc"))
			if cmd == nil {
				t.Fatal("esc did not leave the chat workspace")
			}
			msg, ok := drain(cmd).(selectViewMsg)
			if !ok || msg.id != viewChats {
				t.Fatalf("esc went to %v, want the chats board", drain(cmd))
			}
		},
	},
	ctxNewChat: {
		"ctrl+s": func(t *testing.T) {
			v := chatsFixture()
			v.create = newNewChatForm(nil, 7)
			v.create.title.SetValue("a chat")
			if _, cmd := v.updateKey(registryKey(t, "ctrl+s")); cmd != nil {
				drain(cmd)
			}
			if v.create == nil || v.create.err != "not connected" {
				t.Fatalf("ctrl+s did not attempt to create the chat (err = %q)",
					func() string {
						if v.create == nil {
							return "<form closed>"
						}
						return v.create.err
					}())
			}
		},
		"enter": func(t *testing.T) {
			v := chatsFixture()
			v.create = newNewChatForm(nil, 0)
			v.create.applyFields(newChatFieldsMsg{projects: []apiclient.Project{
				{ID: 1, Name: "one"}, {ID: 2, Name: "two"},
			}})
			v.create.focus = ncProject
			v.updateKey(registryKey(t, "enter"))
			if v.create == nil || v.create.pick == nil {
				t.Fatal("enter on the project row opened no list")
			}
		},
		"tab": func(t *testing.T) {
			v := chatsFixture()
			v.create = newNewChatForm(nil, 7)
			before := v.create.focus
			v.updateKey(registryKey(t, "tab"))
			if v.create.focus == before {
				t.Fatalf("tab did not move the focus (still %d)", before)
			}
		},
		"left": func(t *testing.T) {
			v := chatsFixture()
			v.create = newNewChatForm(nil, 0)
			v.create.applyFields(newChatFieldsMsg{projects: []apiclient.Project{
				{ID: 1, Name: "one"}, {ID: 2, Name: "two"},
			}})
			v.create.focus = ncProject
			v.updateKey(registryKey(t, "left"))
			if v.create.projectID != 2 {
				t.Fatalf("left chose project %d, want the previous one", v.create.projectID)
			}
			if v.create.pick != nil {
				t.Fatal("left opened the project list; it steps in place")
			}
		},
		"esc": func(t *testing.T) {
			v := chatsFixture()
			v.create = newNewChatForm(nil, 7)
			v.updateKey(registryKey(t, "esc"))
			if v.create != nil {
				t.Fatal("esc did not discard the new-chat draft")
			}
		},
	},
	ctxPullRequests: {
		"enter": func(t *testing.T) {
			v := pullRequestsFixture(testPull(11, "claimed", claimedBy(7, "auto")))
			if _, cmd := v.updateKey(registryKey(t, "enter")); cmd == nil {
				t.Fatal("enter did not open the claiming task's workspace")
			}
		},
		"o": func(t *testing.T) {
			opened := withFakeOpener(t, nil)
			v := pullRequestsFixture(testPull(11, "ship it"))
			if _, cmd := v.updateKey(registryKey(t, "o")); cmd != nil {
				drain(cmd)
			}
			if len(*opened) != 1 {
				t.Fatalf("o opened %v, want the selected row", *opened)
			}
		},
		"l": func(t *testing.T) {
			v := pullRequestsFixture(testPull(11, "unclaimed"))
			v.updateKey(registryKey(t, "l"))
			if v.picker == nil {
				t.Fatal("l did not open the task picker")
			}
		},
		// Task 069: the takeover's own offer to create. It picks a task with
		// a branch and no pull request rather than acting on the row under
		// the cursor, because a task with no pull request is not a row here.
		"P": func(t *testing.T) {
			v := pullRequestsFixture(testPull(11, "unclaimed"))
			v.updateKey(registryKey(t, "P"))
			if v.picker == nil || !v.pickerCreate {
				t.Fatal("P did not open the create picker")
			}
		},
		"u": func(t *testing.T) {
			v := pullRequestsFixture(testPull(11, "claimed", claimedBy(8, "human")))
			v.updateKey(registryKey(t, "u"))
			if v.confirm == nil {
				t.Fatal("u did not ask before unlinking")
			}
		},
		"c": func(t *testing.T) {
			v := pullRequestsFixture(testPull(11, "unclaimed"))
			_, cmd := v.updateKey(registryKey(t, "c"))
			if cmd == nil {
				t.Fatal("c did not seed the new-task form")
			}
			if _, ok := cmd().(newTaskFromPullMsg); !ok {
				t.Fatalf("c produced %T, want newTaskFromPullMsg", cmd())
			}
		},
		"s": func(t *testing.T) {
			v := pullRequestsFixture(testPull(11, "ship it"))
			if _, cmd := v.updateKey(registryKey(t, "s")); cmd == nil {
				t.Fatal("s did not re-list with the new state")
			}
			if v.state != "closed" {
				t.Fatalf("state = %q, want the cycle to have advanced to closed", v.state)
			}
		},
		"R": func(t *testing.T) {
			v := pullRequestsFixture(testPull(11, "ship it"))
			if _, cmd := v.updateKey(registryKey(t, "R")); cmd == nil {
				t.Fatal("R did not re-list")
			}
		},
		"down": func(t *testing.T) {
			v := pullRequestsFixture(testPull(11, "one"), testPull(12, "two"))
			v.updateKey(registryKey(t, "down"))
			if v.cursor != 1 {
				t.Fatalf("down left the cursor at %d, want 1", v.cursor)
			}
		},
		"/": func(t *testing.T) {
			v := pullRequestsFixture(testPull(11, "ship it"))
			v.updateKey(registryKey(t, "/"))
			if !v.filtering {
				t.Fatal("/ did not open the filter")
			}
		},
	},

	ctxOutput: {
		"tab": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabOutput)
			v.updateKey(registryKey(t, "tab"))
			if v.tab != taskTabDiff {
				t.Fatalf("tab moved to %v, want Diff", v.tab)
			}
		},
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
			before := d.level.get()
			d.updateKey(registryKey(t, "v"))
			if d.level.get() == before {
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
		"ctrl+o": func(t *testing.T) {
			d := newTestDetail(t)
			d.taskID = 4
			loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
			d.focus = focusOutput
			before := d.raw.get()
			d.updateKey(registryKey(t, "ctrl+o"))
			if d.raw.get() == before {
				t.Fatal("ctrl+o did not toggle raw mode")
			}
			if !d.outputDirty {
				t.Fatal("ctrl+o left the pane's rendered content standing")
			}
		},
		"ctrl+y": func(t *testing.T) {
			d := newTestDetail(t)
			d.taskID = 4
			loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
			d.focus = focusOutput
			d.records = []apiclient.TranscriptRecord{
				{Type: "agent.output", Text: "# Findings\n\nall good"},
			}
			cmd := d.updateKey(registryKey(t, "ctrl+y"))
			if cmd == nil {
				t.Fatal("ctrl+y did not open the copy picker")
			}
			msg, ok := drain(cmd).(openCopyPickerMsg)
			if !ok || len(msg.items) == 0 {
				t.Fatalf("ctrl+y produced %#v, want a copy picker with rows", drain(cmd))
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
		"right": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabOutput)
			v.detail.selectedRun = 1
			v.updateKey(registryKey(t, "right"))
			if v.detail.selectedRun != 2 {
				t.Fatalf("right selected run %d, want 2", v.detail.selectedRun)
			}
		},
		"l": laneOpenProbe(taskTabOutput),
		"<": laneSelectProbe(-1, 43),
		">": laneSelectProbe(1, 42),
	},

	ctxDiff: {
		"tab": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabDiff)
			v.updateKey(registryKey(t, "tab"))
			if v.tab != taskTabWorkflow {
				t.Fatalf("tab moved to %v, want Workflow", v.tab)
			}
		},
		// The way back off the tab has to stay on screen, so `]` is a row here
		// too — and a row is only a promise until something presses it.
		"]": func(t *testing.T) {
			d := diffTabDetail(t)
			d.updateKey(registryKey(t, "]"))
			if d.tab != tabOutput {
				t.Fatal("] did not switch the diff tab back to the output")
			}
		},
		"down": func(t *testing.T) {
			d := diffTabDetail(t)
			before := d.diff.cursorPath
			d.updateKey(registryKey(t, "down"))
			if d.diff.cursorPath == before {
				t.Fatalf("down did not move the file cursor (still %q)", before)
			}
		},
		"enter": func(t *testing.T) {
			d := diffTabDetail(t)
			d.updateKey(registryKey(t, "enter"))
			if len(d.diff.open) != 1 {
				t.Fatalf("enter did not fold the file under the cursor (folds %v)", d.diff.open)
			}
			d.updateKey(registryKey(t, "enter"))
			if len(d.diff.open) != 0 {
				t.Fatalf("enter did not fold it shut again (folds %v)", d.diff.open)
			}
		},
		"O": func(t *testing.T) {
			d := diffTabDetail(t)
			d.updateKey(registryKey(t, "O"))
			if len(d.diff.open) != 2 {
				t.Fatalf("O expanded %d files, want both", len(d.diff.open))
			}
		},
		"C": func(t *testing.T) {
			d := diffTabDetail(t)
			d.updateKey(registryKey(t, "O"))
			d.updateKey(registryKey(t, "C"))
			if len(d.diff.open) != 0 {
				t.Fatalf("C left %d files expanded", len(d.diff.open))
			}
		},
		"l": laneOpenProbe(taskTabDiff),
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
		"g": func(t *testing.T) {
			w := newWorkflowsView()
			w.client = offlineClient()
			loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
			w.updateKey(registryKey(t, "g"))
			if w.graph == nil {
				t.Fatal("g did not open the graph layer")
			}
		},
		"i": func(t *testing.T) {
			w := newWorkflowsView()
			w.client = offlineClient()
			loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
			w.updateKey(registryKey(t, "i"))
			if w.editor == nil {
				t.Fatal("i did not open the structured editor")
			}
		},
		"a": func(t *testing.T) {
			w := newWorkflowsView()
			w.client = offlineClient()
			loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
			w.updateKey(registryKey(t, "a"))
			if w.create == nil || w.create.fork {
				t.Fatal("a did not open the create prompt")
			}
		},
		"f": func(t *testing.T) {
			w := newWorkflowsView()
			w.client = offlineClient()
			loadedWorkflows(w, wfBlock{name: "global", entries: []apiclient.WorkflowEntry{globalEntry("review")}})
			w.updateKey(registryKey(t, "f"))
			if w.create == nil || !w.create.fork {
				t.Fatal("f did not open the fork prompt")
			}
		},
	},

	ctxWorkflowEditor: {
		"up/down": func(t *testing.T) {
			w := editorFixture(t)
			w.updateKey(registryKey(t, "down"))
			if w.editor.cursor == 0 {
				t.Fatal("down did not move the row cursor")
			}
			w.updateKey(registryKey(t, "up"))
			if w.editor.cursor != 0 {
				t.Fatalf("up did not move back (cursor %d)", w.editor.cursor)
			}
		},
		"enter": func(t *testing.T) {
			w := editorFixture(t)
			w.editor.cursor = editorRowIndex(t, w, "description")
			w.updateKey(registryKey(t, "enter"))
			if w.editor.input == nil {
				t.Fatal("enter on a text row did not focus a field")
			}
		},
		"R": func(t *testing.T) {
			w := editorFixture(t)
			w.editor.err, w.editor.stale = "the file moved", true
			_, cmd := w.updateKey(registryKey(t, "R"))
			if cmd == nil {
				t.Fatal("R did not re-read the file")
			}
			if w.editor.err != "" || w.editor.stale {
				t.Error("R left the stale-write offer on screen")
			}
		},
		"esc": func(t *testing.T) {
			w := editorFixture(t)
			w.updateKey(registryKey(t, "esc"))
			if w.editor != nil {
				t.Fatal("esc did not close the editor")
			}
		},
	},

	ctxWorkflowCreate: {
		"tab": func(t *testing.T) {
			w := createFixture(t)
			before := w.create.row
			w.updateKey(registryKey(t, "tab"))
			if w.create.row == before {
				t.Fatal("tab did not move between the rows")
			}
		},
		"left/right": func(t *testing.T) {
			w := createFixture(t)
			w.create.row = wfCreateRowScope
			before := w.create.scope
			w.updateKey(registryKey(t, "right"))
			if w.create.scope == before && len(w.create.scopes) > 1 {
				t.Fatal("right did not change the scope")
			}
		},
		"enter": func(t *testing.T) {
			w := createFixture(t)
			w.create.name.SetValue("fresh")
			if _, cmd := w.updateKey(registryKey(t, "enter")); cmd == nil {
				t.Fatal("enter did not submit the create")
			}
		},
		"esc": func(t *testing.T) {
			w := createFixture(t)
			w.updateKey(registryKey(t, "esc"))
			if w.create != nil {
				t.Fatal("esc did not close the prompt")
			}
		},
	},

	ctxWorkflowGraph: {
		"down": func(t *testing.T) {
			w := graphFixture(t)
			before := w.graph.graph.Selected()
			w.updateKey(registryKey(t, "down"))
			if w.graph.graph.Selected() == before {
				t.Fatalf("down did not move the graph selection (still %q)", before)
			}
		},
		"shift+down": func(t *testing.T) {
			w := graphFixture(t)
			before := w.graph.graph.Selected()
			w.updateKey(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
			if got := w.graph.graph.Selected(); got != before {
				t.Fatalf("shift+down moved the selection to %q; panning must not", got)
			}
			if w.graph.graph.ScrollPercent() == 0 {
				t.Fatal("shift+down did not pan the canvas")
			}
		},
		"tab": func(t *testing.T) {
			w := graphFixture(t)
			before := w.graph.graph.Selected()
			w.updateKey(registryKey(t, "tab"))
			if w.graph.graph.Selected() == before {
				t.Fatalf("tab did not walk to the next node (still %q)", before)
			}
		},
		"e": func(t *testing.T) {
			w := graphFixture(t)
			if _, cmd := w.updateKey(registryKey(t, "e")); cmd == nil {
				t.Fatal("e did not open the workflow file from inside the graph")
			}
		},
		"R": func(t *testing.T) {
			w := graphFixture(t)
			w.graph.err = "a stale fetch failure"
			_, cmd := w.updateKey(registryKey(t, "R"))
			if cmd == nil {
				t.Fatal("R did not re-fetch the definition")
			}
			if w.graph.err != "" {
				t.Errorf("R left the previous error on screen: %q", w.graph.err)
			}
		},
		"enter": func(t *testing.T) {
			w := graphFixture(t)
			w.render(100, 40)
			w.updateKey(registryKey(t, "enter"))
			if w.graph.modal == nil {
				t.Fatal("enter did not open the selected node's detail")
			}
		},
	},

	ctxWorkflowStep: {
		"down": func(t *testing.T) {
			w := modalFixture(t)
			w.render(100, 16)
			before := w.graph.modal.vp.View()
			w.updateKey(registryKey(t, "down"))
			if w.graph.modal.vp.View() == before {
				t.Fatal("down did not scroll the open detail")
			}
		},
		"e": func(t *testing.T) {
			w := modalFixture(t)
			if _, cmd := w.updateKey(registryKey(t, "e")); cmd == nil {
				t.Fatal("e did not open the workflow file from inside the detail")
			}
		},
		"R": func(t *testing.T) {
			w := modalFixture(t)
			if _, cmd := w.updateKey(registryKey(t, "R")); cmd == nil {
				t.Fatal("R did not re-fetch the definition from inside the detail")
			}
		},
		"esc": func(t *testing.T) {
			w := modalFixture(t)
			w.updateKey(registryKey(t, "esc"))
			if w.graph == nil || w.graph.modal != nil {
				t.Fatal("esc did not close the detail back to the graph")
			}
		},
	},

	ctxTaskPull: {
		"6": func(t *testing.T) {
			v := pullTabFixture(t)
			v.tab = taskTabDetails
			v.updateKey(registryKey(t, "6"))
			if v.tab != taskTabPull {
				t.Fatalf("6 moved to %v, want the Pull Request tab", v.tab)
			}
		},
		"down": func(t *testing.T) {
			v := pullTabFixture(t)
			v.updateKey(registryKey(t, "down"))
			if v.pullTab.cursor != 1 {
				t.Fatalf("down selected row %d, want 1", v.pullTab.cursor)
			}
		},
		"c": func(t *testing.T) {
			withFakeOpener(t, nil)
			v := pullTabFixture(t)
			if cmd := v.updateKey(registryKey(t, "c")); cmd == nil {
				t.Fatal("c did not open the selected check")
			}
		},
		"o": func(t *testing.T) {
			withFakeOpener(t, nil)
			v := pullTabFixture(t)
			if cmd := v.updateKey(registryKey(t, "o")); cmd == nil {
				t.Fatal("o did not open the pull request")
			}
		},
		"r": func(t *testing.T) {
			v := pullTabFixture(t)
			if cmd := v.updateKey(registryKey(t, "r")); cmd == nil {
				t.Fatal("r did not refetch the pull request and its checks")
			}
		},
		"u": func(t *testing.T) {
			v := pullTabFixture(t)
			if cmd := v.updateKey(registryKey(t, "u")); cmd == nil {
				t.Fatal("u did not unlink the pull request")
			}
		},
		"l": laneOpenProbe(taskTabPull),
	},
	ctxTaskWorkflow: {
		"down": func(t *testing.T) {
			v := workflowTabFixture(t)
			before := v.workflow.graph.Selected()
			v.updateKey(registryKey(t, "down"))
			if v.workflow.graph.Selected() == before {
				t.Fatalf("down did not move the graph selection (still %q)", before)
			}
		},
		"shift+down": func(t *testing.T) {
			v := workflowTabFixture(t)
			before := v.workflow.graph.Selected()
			v.updateKey(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
			if got := v.workflow.graph.Selected(); got != before {
				t.Fatalf("shift+down moved the selection to %q; panning must not", got)
			}
		},
		"5": func(t *testing.T) {
			v := tabbedTaskFixture(t, taskTabSteps)
			v.updateKey(registryKey(t, "5"))
			if v.tab != taskTabWorkflow {
				t.Fatalf("5 moved to %v, want Workflow", v.tab)
			}
		},
		// The graph's `l` is the one that resolves to the lane under the
		// cursor rather than to the blamed one, so it gets the graph fixture.
		"l": func(t *testing.T) {
			v := fanOutGraphTaskView(t)
			msg, ok := v.updateKey(registryKey(t, "l"))().(openTaskMsg)
			if !ok || msg.id != 43 || msg.from != 7 {
				t.Fatalf("l on the Workflow tab opened %+v, want lane task 43 from parent 7", msg)
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
		"tab": func(t *testing.T) {
			d := newTestDaemonView([]string{"a log line"}, nil)
			d.updateKey(registryKey(t, "tab"))
			if !d.focusConfig {
				t.Fatal("tab did not move the arrows onto the config list")
			}
		},
		"enter": func(t *testing.T) {
			d := newTestDaemonView([]string{"a log line"}, nil)
			d.update(daemonConfigMsg{config: testConfig()})
			d.updateKey(registryKey(t, "enter"))
			if d.form == nil {
				t.Fatal("enter did not open the config editor")
			}
		},
		"i": func(t *testing.T) {
			d := newTestDaemonView([]string{"a log line"}, nil)
			d.updateKey(registryKey(t, "i"))
			if d.statusLine == nil {
				t.Fatal("i did not open the status-line flow")
			}
			if !d.capturesInput() {
				t.Fatal("the status-line flow does not own the keyboard")
			}
		},
	},

	ctxConfigEdit: {
		"left": func(t *testing.T) {
			d := openConfigForm(t, "log_level")
			before := d.form.value()
			d.updateKey(registryKey(t, "left"))
			if d.form.value() == before {
				t.Fatal("left did not move the chooser")
			}
		},
		"enter": func(t *testing.T) {
			d := openConfigForm(t, "listen")
			d.updateKey(registryKey(t, "enter"))
			if !d.form.confirming {
				t.Fatal("enter on a dangerous key did not reach the confirmation step")
			}
		},
		"y": func(t *testing.T) {
			d := openConfigForm(t, "listen")
			d.form.confirming = true
			d.updateKey(registryKey(t, "y"))
			if d.form.confirming {
				t.Fatal("y did not answer the confirmation")
			}
		},
		"esc": func(t *testing.T) {
			d := openConfigForm(t, "log_level")
			d.updateKey(registryKey(t, "esc"))
			if d.form != nil {
				t.Fatal("esc did not close the config editor")
			}
		},
	},

	ctxRepairForm: {
		"enter": func(t *testing.T) {
			f := repairFormFixture()
			f.update(registryKey(t, "enter"), nil)
			if !f.editing {
				t.Fatal("enter did not open the prompt field")
			}
		},
		"e": func(t *testing.T) {
			f := repairFormFixture()
			opened := false
			f.openEditor = func(string) tea.Cmd { opened = true; return nil }
			f.update(registryKey(t, "e"), nil)
			if !opened {
				t.Fatal("e did not hand the prompt to $EDITOR")
			}
		},
		"ctrl+s": func(t *testing.T) {
			f := repairFormFixture()
			f.update(registryKey(t, "ctrl+s"), nil)
			// With no prompt typed, submitting is refused — with a reason,
			// which is what proves the key was handled.
			if f.err == "" {
				t.Fatal("ctrl+s neither started the repair nor said why it would not")
			}
		},
		"esc": func(t *testing.T) {
			f := repairFormFixture()
			if _, exit := f.update(registryKey(t, "esc"), nil); !exit {
				t.Fatal("esc did not close the repair form")
			}
		},
		"ctrl+t": popupTabProbe(func(d *detail) { d.repair = repairFormFixture() }),
	},

	ctxFollowUpForm: {
		"enter": func(t *testing.T) {
			f := followUpFormFixture()
			// The cursor starts on the run-form chooser, so enter opens its
			// list; one row down it opens the prompt field instead.
			f.update(registryKey(t, "enter"), nil)
			if f.picker == nil {
				t.Fatal("enter did not open the run-form list")
			}
			f.picker = nil
			f.cursor = fuBody
			f.update(registryKey(t, "enter"), nil)
			if !f.editing {
				t.Fatal("enter did not open the prompt field")
			}
		},
		"e": func(t *testing.T) {
			f := followUpFormFixture()
			f.cursor = fuBody
			opened := false
			f.openEditor = func(string) tea.Cmd { opened = true; return nil }
			f.update(registryKey(t, "e"), nil)
			if !opened {
				t.Fatal("e did not hand the prompt to $EDITOR")
			}
		},
		"ctrl+s": func(t *testing.T) {
			f := followUpFormFixture()
			f.update(registryKey(t, "ctrl+s"), nil)
			// With nothing typed, submitting is refused — with a reason,
			// which is what proves the key was handled.
			if f.err == "" {
				t.Fatal("ctrl+s neither started the follow-up nor said why it would not")
			}
		},
		"esc": func(t *testing.T) {
			f := followUpFormFixture()
			if _, exit := f.update(registryKey(t, "esc"), nil); !exit {
				t.Fatal("esc did not close the follow-up form")
			}
		},
		"ctrl+t": popupTabProbe(func(d *detail) { d.followUp = followUpFormFixture() }),
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
		"ctrl+t": popupTabProbe(func(d *detail) { d.form = newAnswerForm(questionRequest()) }),
	},

	ctxCreatePR: {
		"enter": func(t *testing.T) {
			f := createPRFixture(t)
			f.update(registryKey(t, "enter"))
			if !f.editing {
				t.Fatal("enter did not open the row under the cursor")
			}
		},
		"e": func(t *testing.T) {
			f := createPRFixture(t)
			called := false
			f.openEditor = func(string) tea.Cmd { called = true; return nil }
			f.update(registryKey(t, "e"))
			if !called {
				t.Fatal("e did not reach $EDITOR")
			}
		},
		"space": func(t *testing.T) {
			f := createPRFixture(t)
			f.cursor = cprDraft
			f.update(registryKey(t, "space"))
			if !f.draft {
				t.Fatal("space did not toggle the draft row")
			}
		},
		// ctrl+s is the daemon call now (task 069): it posts and leaves the
		// popup open, because the answer has to have somewhere to land.
		"ctrl+s": func(t *testing.T) {
			f := createPRFixture(t)
			sent := false
			f.submit = func(string, string, bool) tea.Cmd { sent = true; return nil }
			if _, exit := f.update(registryKey(t, "ctrl+s")); exit {
				t.Fatal("ctrl+s closed the popup before the daemon answered")
			}
			if !sent {
				t.Fatal("ctrl+s did not reach the daemon")
			}
		},
		"ctrl+o": func(t *testing.T) {
			opened := withFakeOpener(t, nil)
			f := createPRFixture(t)
			cmd, exit := f.update(registryKey(t, "ctrl+o"))
			if !exit {
				t.Fatal("ctrl+o did not close the popup")
			}
			drain(cmd)
			if len(*opened) != 1 {
				t.Fatalf("ctrl+o opened %v, want the compare URL", *opened)
			}
		},
		"esc": func(t *testing.T) {
			f := createPRFixture(t)
			if _, exit := f.update(registryKey(t, "esc")); !exit {
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

// TestLevelKeysNameEveryLevel: the two rows that spell the cycle out are the
// only place the help says what the key does, so a level missing from a label
// is a level a reader has no way to know exists. Both rows are checked
// because the two panes cycle one shared holder — a label naming three levels
// beside one naming four would describe two different keys.
func TestLevelKeysNameEveryLevel(t *testing.T) {
	rows := map[bindingContext]string{ctxOutput: "v", ctxChat: "ctrl+r"}
	found := map[bindingContext]bool{}
	for _, b := range bindings {
		key, ok := rows[b.context]
		if !ok || b.key != key {
			continue
		}
		found[b.context] = true
		for _, l := range []outputLevel{levelQuiet, levelCompact, levelNormal, levelVerbose} {
			if !strings.Contains(b.label, l.String()) {
				t.Errorf("the %s row for %q does not name %s: %q",
					b.context, key, l, b.label)
			}
		}
		// Named in the order the key walks them, so the label is a promise
		// about the next press rather than a list of the levels that exist.
		want := levelQuiet.String() + " → " + levelCompact.String() +
			" → " + levelNormal.String() + " → " + levelVerbose.String()
		if !strings.Contains(b.label, want) {
			t.Errorf("the %s row for %q does not spell the cycle %q: %q",
				b.context, key, want, b.label)
		}
	}
	for ctx, key := range rows {
		if !found[ctx] {
			t.Errorf("no %s row for %q in the registry", ctx, key)
		}
	}
}
