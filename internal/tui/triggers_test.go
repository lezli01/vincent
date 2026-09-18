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
