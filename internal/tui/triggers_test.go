package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// triggersFixture is a loaded triggers view with two triggers, one project and
// a two-row ledger on the first. Its client is never dialled: the probes only
// ask whether a key produced a command, never run it.
func triggersFixture() *triggersView {
	v := newTriggersView()
	v.client = &apiclient.Client{}
	v.loaded = true
	v.project = projectSel{id: 1, name: "vincent"}
	v.projects = []apiclient.Project{{ID: 1, Name: "vincent"}}
	v.list = apiclient.TriggerList{Triggers: []apiclient.TriggerSummary{
		{ID: "alpha", File: "/cfg/triggers/alpha.yaml", Version: "v1", ProjectID: 1, SourceType: "command", ActionType: "create", Valid: true},
		{ID: "beta", File: "/cfg/triggers/beta.yaml", Version: "v2", ProjectID: 1, SourceType: "command", ActionType: "create", Valid: true, Enabled: true},
	}}
	v.restoreSelection()
	taskID := int64(42)
	v.ledgerID = "alpha"
	v.ledger = []apiclient.TriggerDelivery{{Outcome: "fired", TaskID: &taskID}, {Outcome: "filtered"}}
	return v
}

// triggersFormFixture is the fixture with the form open on one editable row.
func triggersFormFixture() *triggersView {
	v := triggersFixture()
	v.openFormOn("alpha", "/cfg/triggers/alpha.yaml", "v1")
	v.form.loading = false
	v.form.rows = []trigRow{
		{wfEditRow: wfEditRow{path: "limits.max_per_hour", value: "10"}},
		{wfEditRow: wfEditRow{path: "limits.max_per_day", value: "50"}},
	}
	return v
}

func TestTriggersSampleIsSessionOnly(t *testing.T) {
	v := triggersFixture()
	v.updateKey(registryKey(t, "T"))
	v.dry.pane.SetValue(`{"id":"x","secret":"do not persist"}`)
	v.updateKey(registryKey(t, "esc"))
	if got := v.samples["alpha"]; got != `{"id":"x","secret":"do not persist"}` {
		t.Fatalf("sample kept for the session = %q", got)
	}
	// Reopening the dry run brings the edited sample back; nothing about it
	// is a tuiState member, so it cannot reach tui.json (decision 27).
	v.updateKey(registryKey(t, "T"))
	if got := v.dry.pane.Value(); got != `{"id":"x","secret":"do not persist"}` {
		t.Fatalf("reopened sample = %q", got)
	}
}

func TestTriggersDisableDoesNotAsk(t *testing.T) {
	v := triggersFixture()
	v.move(1)
	_, cmd := v.updateKey(registryKey(t, "space"))
	if v.confirm != nil || cmd == nil {
		t.Fatalf("disabling asked (%v) or sent nothing (%v)", v.confirm != nil, cmd == nil)
	}
}

func TestTriggersInvalidCannotBeEnabled(t *testing.T) {
	v := triggersFixture()
	v.list.Triggers[0].Valid = false
	v.updateKey(registryKey(t, "space"))
	if v.confirm != nil || v.err == "" {
		t.Fatalf("an invalid trigger offered to enable: confirm=%v err=%q", v.confirm != nil, v.err)
	}
}

// triggerProbes are panelKeyProbes' rows for the five trigger contexts.
var triggerProbes = map[bindingContext]map[string]func(*testing.T){
	ctxTriggers: {
		"down": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "down"))
			if v.selectedID != "beta" {
				t.Fatalf("down selected %q, want beta", v.selectedID)
			}
		},
		"enter": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "enter"))
			if v.form == nil || v.form.id != "alpha" {
				t.Fatal("enter did not open the form on the selected trigger")
			}
		},
		"space": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "space"))
			if v.confirm == nil {
				t.Fatal("space on a disabled trigger did not ask before enabling it")
			}
		},
		"a": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "a"))
			if v.create == nil {
				t.Fatal("a did not open the create prompt")
			}
		},
		"e": func(t *testing.T) {
			v := triggersFixture()
			if _, cmd := v.updateKey(registryKey(t, "e")); cmd == nil {
				t.Fatal("e did not open the file in $EDITOR")
			}
		},
		"D": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "D"))
			if v.confirm == nil {
				t.Fatal("D did not ask before deleting")
			}
		},
		"T": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "T"))
			if v.dry == nil || v.dry.poll {
				t.Fatal("T did not open the sample-event dry run")
			}
		},
		"X": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "X"))
			if v.dry == nil || !v.dry.poll {
				t.Fatal("X did not open the live-poll dry run")
			}
		},
		"tab": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "tab"))
			if v.focus != trigFocusLedger {
				t.Fatal("tab did not move to the ledger")
			}
		},
		"B": func(t *testing.T) {
			v := triggersFixture()
			_, cmd := v.updateKey(registryKey(t, "B"))
			if cmd == nil {
				t.Fatal("B sent nothing")
			}
			if msg, ok := cmd().(openConfigKeyMsg); !ok || msg.path != "triggers.enabled" {
				t.Fatalf("B sent %T, want openConfigKeyMsg for triggers.enabled", msg)
			}
		},
		"R": func(t *testing.T) {
			v := triggersFixture()
			if _, cmd := v.updateKey(registryKey(t, "R")); cmd == nil {
				t.Fatal("R did not re-read")
			}
		},
		"/": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "/"))
			if !v.filtering {
				t.Fatal("/ did not open the filter")
			}
		},
	},

	ctxTriggerLedger: {
		"down": func(t *testing.T) {
			v := triggersFixture()
			v.focus = trigFocusLedger
			v.updateKey(registryKey(t, "down"))
			if v.ledgerCursor != 1 {
				t.Fatalf("down left the ledger cursor on %d", v.ledgerCursor)
			}
		},
		"enter": func(t *testing.T) {
			v := triggersFixture()
			v.focus = trigFocusLedger
			_, cmd := v.updateKey(registryKey(t, "enter"))
			if cmd == nil {
				t.Fatal("enter on a fired delivery opened nothing")
			}
			if msg, ok := cmd().(selectTaskMsg); !ok || msg.id != 42 {
				t.Fatal("enter did not open the delivery's task")
			}
		},
		"tab": func(t *testing.T) {
			v := triggersFixture()
			v.focus = trigFocusLedger
			v.updateKey(registryKey(t, "tab"))
			if v.focus != trigFocusList {
				t.Fatal("tab did not return to the list")
			}
		},
		"R": func(t *testing.T) {
			v := triggersFixture()
			v.focus = trigFocusLedger
			if _, cmd := v.updateKey(registryKey(t, "R")); cmd == nil {
				t.Fatal("R did not re-read")
			}
		},
	},

	ctxTriggerForm: {
		"down": func(t *testing.T) {
			v := triggersFormFixture()
			v.updateKey(registryKey(t, "down"))
			if v.form.cursor != 1 {
				t.Fatalf("down left the form cursor on %d", v.form.cursor)
			}
		},
		"enter": func(t *testing.T) {
			v := triggersFormFixture()
			v.updateKey(registryKey(t, "enter"))
			if v.form.input == nil {
				t.Fatal("enter did not open the field")
			}
		},
		"R": func(t *testing.T) {
			v := triggersFormFixture()
			v.updateKey(registryKey(t, "R"))
			if !v.form.loading {
				t.Fatal("R did not re-read the trigger")
			}
		},
		"esc": func(t *testing.T) {
			v := triggersFormFixture()
			v.updateKey(registryKey(t, "esc"))
			if v.form != nil {
				t.Fatal("esc did not close the form")
			}
		},
	},

	ctxTriggerCreate: {
		"tab": func(t *testing.T) {
			v := triggersFixture()
			v.openCreate()
			v.updateKey(registryKey(t, "tab"))
			if v.create.row != trigCreateRowProject {
				t.Fatalf("tab left the prompt on row %d", v.create.row)
			}
		},
		"enter": func(t *testing.T) {
			v := triggersFixture()
			v.openCreate()
			v.create.id.SetValue("gamma")
			if _, cmd := v.updateKey(registryKey(t, "enter")); cmd == nil || !v.create.saving {
				t.Fatal("enter did not send the create")
			}
		},
		"esc": func(t *testing.T) {
			v := triggersFixture()
			v.openCreate()
			v.updateKey(registryKey(t, "esc"))
			if v.create != nil {
				t.Fatal("esc did not close the prompt")
			}
		},
	},

	ctxTriggerDryRun: {
		"ctrl+s": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "T"))
			if _, cmd := v.updateKey(registryKey(t, "ctrl+s")); cmd == nil || !v.dry.running {
				t.Fatal("ctrl+s did not run the dry run")
			}
		},
		"esc": func(t *testing.T) {
			v := triggersFixture()
			v.updateKey(registryKey(t, "T"))
			v.updateKey(registryKey(t, "esc"))
			if v.dry != nil {
				t.Fatal("esc did not close the dry run")
			}
		},
	},
}

func init() {
	// Registered here rather than in bindings_test.go's literal so the view's
	// probes live beside its fixtures.
	for ctx, probes := range triggerProbes {
		panelKeyProbes[ctx] = probes
	}
}

// TestTriggersScheduleRendersAClock: a schedule never polls, so the poll cell
// reports a clock rather than sitting on "not yet" forever, and the armed
// hint says what the next tick actually does (task 121).
func TestTriggersScheduleRendersAClock(t *testing.T) {
	v := triggersFixture()
	v.list.Triggers[0].SourceType = "schedule"
	v.list.Triggers[0].Enabled = true
	v.list.Triggers[0].Armed = true
	cell, _ := trigPollCell(v.list.Triggers[0])
	if cell != "clock" {
		t.Errorf("poll cell = %q, want %q", cell, "clock")
	}
	body := v.render(120, 24)
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "alpha") && strings.Contains(line, "not yet") {
			t.Errorf("the schedule's row reported an unpolled source:\n%s", line)
		}
	}
	if !strings.Contains(body, "clock") {
		t.Errorf("no clock cell in:\n%s", body)
	}
	hint := strings.Join(v.detailLines(120), "\n")
	if !strings.Contains(hint, "anchors its clock") {
		t.Errorf("armed hint = %q, want the clock wording", hint)
	}
	// Once anchored there is no hint to give, and a pushed source keeps its
	// own cell.
	v.list.Triggers[0].Poll.Seeded = true
	if hint := strings.Join(v.detailLines(120), "\n"); strings.Contains(hint, "anchors its clock") {
		t.Errorf("an anchored schedule still offered the hint: %q", hint)
	}
	v.list.Triggers[0].SourceType = "http"
	if cell, _ := trigPollCell(v.list.Triggers[0]); cell != "push" {
		t.Errorf("poll cell for a pushed source = %q, want %q", cell, "push")
	}
}

// scopedTriggersFixture lists triggers in projects a (1) and b (2), an
// invalid file peeked to b, an invalid file whose project could not be read,
// and a valid trigger of a removed project (9), with a selected.
func scopedTriggersFixture() *triggersView {
	v := newTriggersView()
	v.client = &apiclient.Client{}
	v.loaded = true
	v.project = projectSel{id: 1, name: "a"}
	v.projects = []apiclient.Project{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	v.list = apiclient.TriggerList{Triggers: []apiclient.TriggerSummary{
		{ID: "a-one", ProjectID: 1, SourceType: "command", ActionType: "create", Valid: true},
		{ID: "b-broken", ProjectID: 2},
		{ID: "b-one", ProjectID: 2, SourceType: "command", ActionType: "create", Valid: true},
		{ID: "lost", File: "/cfg/triggers/lost.yaml"},
		{ID: "a-two", ProjectID: 1, SourceType: "schedule", ActionType: "create", Valid: true},
		{ID: "gone", ProjectID: 9, SourceType: "command", ActionType: "create", Valid: true, Enabled: true},
	}}
	v.restoreSelection()
	return v
}

func visibleIDs(v *triggersView) string {
	var ids []string
	for _, s := range v.visible() {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ",")
}

// The takeover lists the selected project's triggers, then the unassigned
// band, which every project's view shows (task 132 decisions 8 and 41): the
// file with no readable project, and the trigger whose project was removed
// (review F3). Another registered project's triggers — valid, or invalid
// with a readable project — are not listed.
func TestTriggersScopeToTheSelectedProject(t *testing.T) {
	v := scopedTriggersFixture()
	if got := visibleIDs(v); got != "a-one,a-two,lost,gone" {
		t.Errorf("a selected: rows = %s, want a's triggers then the unassigned band", got)
	}
	out := v.render(200, 40)
	if strings.Count(out, "unassigned") != 1 {
		t.Errorf("the unassigned band does not have exactly one heading:\n%s", out)
	}
	if strings.Contains(out, "PROJECT") {
		t.Errorf("the project column is still drawn:\n%s", out)
	}

	// Switching while open shows b's set and the same band.
	v.setProject(projectSel{id: 2, name: "b"})
	v.restoreSelection()
	if got := visibleIDs(v); got != "b-broken,b-one,lost,gone" {
		t.Errorf("b selected: rows = %s, want b's triggers then the unassigned band", got)
	}
	if got := v.hintedProject(); got != 2 {
		t.Errorf("hintedProject() = %d, want the selection", got)
	}
}

// The filter matches id, source and action, never a project name: the list
// is one project's.
func TestTriggersFilterDoesNotMatchTheProject(t *testing.T) {
	v := scopedTriggersFixture()
	v.filter.SetValue("schedule")
	if got := visibleIDs(v); got != "a-two" {
		t.Errorf("filter schedule = %s, want a-two", got)
	}
	v.filter.SetValue(v.project.name + "-")
	if got := visibleIDs(v); got != "a-one,a-two" {
		t.Errorf("filter a- = %s, want the ids alone to match", got)
	}
	v.project.name = "zzz"
	v.filter.SetValue("zzz")
	if got := visibleIDs(v); got != "" {
		t.Errorf("filter on the project name matched %s", got)
	}
}

// The global-off banner is unchanged by the scoping.
func TestTriggersScopedStillShowsTheGlobalOffBanner(t *testing.T) {
	v := scopedTriggersFixture()
	v.list.Enabled = false
	if out := v.render(200, 40); !strings.Contains(out, "triggers.enabled is off") {
		t.Errorf("no banner with triggers globally off:\n%s", out)
	}
}

// The new-trigger prompt's project is the selection, read-only, and the
// trigger is created in it.
func TestTriggersCreateIsLockedToTheSelection(t *testing.T) {
	v := scopedTriggersFixture()
	v.openCreate()
	f := v.create
	f.id.SetValue("fresh")
	f.focusRow(trigCreateRowProject)
	v.updateCreateKey(registryKey(t, "right"))
	if f.projectID != 1 {
		t.Fatalf("the project row moved to %d", f.projectID)
	}
	if out := v.render(160, 30); !strings.Contains(out, "a — the selected project") {
		t.Errorf("the project row does not show the selection:\n%s", out)
	}
	if cmd := v.createCmd(); cmd == nil {
		t.Fatalf("create sent nothing: %q", f.err)
	}
	// The request is built before the command runs; nothing here dials.
}

// An existing trigger's project row is read-only; one opened from the
// unassigned band stays editable, because assigning it is the repair. The
// form loads only a valid file, so the band's loadable row is a trigger
// whose project was removed (review F4).
func TestTriggersFormProjectRowLocksOnlyAnAssignedFile(t *testing.T) {
	sf := apiclient.TriggerSchemaField{Name: "project", Control: apiclient.TriggerControlProject}
	v := scopedTriggersFixture()
	v.openFormOn("a-one", "", "")
	v.form.def = map[string]any{"source": map[string]any{"project": 1}}
	if row := v.form.leaf("source", sf); row.readOnly == "" {
		t.Error("an assigned trigger's project row is editable")
	}
	v.openFormOn("gone", "", "")
	v.form.def = map[string]any{"source": map[string]any{"project": 9}}
	if row := v.form.leaf("source", sf); row.readOnly != "" {
		t.Errorf("a removed project's trigger has its project row locked: %q", row.readOnly)
	}
}
