package workflow

import (
	"strings"
	"testing"
)

// handOffWorkflow is the smallest valid workflow carrying one `hand_off:`
// line, or none when mode is empty.
func handOffWorkflow(mode string) string {
	src := "name: w\n"
	if mode != "" {
		src += "hand_off: " + mode + "\n"
	}
	return src + "steps:\n  - {id: gate, type: manual, instructions: review}\n"
}

// TestHandOffParses pins task 137 decision 13's closed set and its default:
// an absent key and `auto` both read as auto, which is the rule every issue
// followed before the key existed.
func TestHandOffParses(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"", HandOffAuto},
		{HandOffAuto, HandOffAuto},
		{HandOffExplicit, HandOffExplicit},
	} {
		wf, _, err := Parse([]byte(handOffWorkflow(tc.src)), Options{})
		if err != nil {
			t.Fatalf("hand_off %q: Parse: %v", tc.src, err)
		}
		if wf.HandOff != tc.src {
			t.Errorf("hand_off %q: HandOff = %q", tc.src, wf.HandOff)
		}
		if got := wf.HandOffMode(); got != tc.want {
			t.Errorf("hand_off %q: HandOffMode = %q, want %q", tc.src, got, tc.want)
		}
	}
}

func TestHandOffModeDefaultsToAuto(t *testing.T) {
	if got := (&Workflow{}).HandOffMode(); got != HandOffAuto {
		t.Errorf("empty HandOffMode = %q, want %q", got, HandOffAuto)
	}
	var nilWF *Workflow
	if got := nilWF.HandOffMode(); got != HandOffAuto {
		t.Errorf("nil HandOffMode = %q, want %q", got, HandOffAuto)
	}
}

// An unknown mode — a different case included — fails at load, at its own
// path, naming the members it could have been.
func TestHandOffRefusesUnknownValue(t *testing.T) {
	for _, bad := range []string{"manual", "Explicit", "true"} {
		_, _, err := Parse([]byte(handOffWorkflow(bad)), Options{})
		var errs Errors
		if !asErrors(err, &errs) {
			t.Fatalf("hand_off %q: err = %v, want validation errors", bad, err)
		}
		msg := findingAt(errs, "hand_off")
		if msg == "" {
			t.Fatalf("hand_off %q: no finding at hand_off: %v", bad, errs)
		}
		for _, want := range []string{HandOffAuto, HandOffExplicit, bad} {
			if !strings.Contains(msg, want) {
				t.Errorf("hand_off %q: message %q does not name %q", bad, msg, want)
			}
		}
		if errs[0].Line != 2 {
			t.Errorf("hand_off %q: line = %d, want 2", bad, errs[0].Line)
		}
	}
}

// TestHandOffSurvivesTheSnapshot is the path taskrun reads the mode from: a
// task's snapshot is the expanded workflow marshalled and parsed again, so
// the key has to come through Expand and Marshal. An included callee's own
// `hand_off` does not count — only the including workflow's does, the way a
// callee's name and description never reach the caller either.
func TestHandOffSurvivesTheSnapshot(t *testing.T) {
	lookup := registry(t, map[string]string{
		"checks": "name: checks\nhand_off: auto\nsteps:\n" +
			"  - {id: lint, type: command, run: make lint}\n",
	})
	root := mustParse(t, `
name: root
hand_off: explicit
steps:
  - {id: implement, type: agent, prompt: go}
  - {id: verify, type: include, workflow: checks}
`)
	expanded, err := Expand(root, expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	snapshot, err := Marshal(expanded)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	wf, _, err := Parse(snapshot, Options{})
	if err != nil {
		t.Fatalf("Parse snapshot: %v\n%s", err, snapshot)
	}
	if got := wf.HandOffMode(); got != HandOffExplicit {
		t.Errorf("snapshot HandOffMode = %q, want %q\n%s", got, HandOffExplicit, snapshot)
	}

	// A callee that says explicit does not make an auto caller explicit.
	lookup = registry(t, map[string]string{
		"checks": "name: checks\nhand_off: explicit\nsteps:\n" +
			"  - {id: lint, type: command, run: make lint}\n",
	})
	root = mustParse(t, "name: root\nsteps:\n  - {id: verify, type: include, workflow: checks}\n")
	expanded, err = Expand(root, expandOpts(lookup))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got := expanded.HandOffMode(); got != HandOffAuto {
		t.Errorf("callee's hand_off leaked into the caller: %q", got)
	}
}

// The merge-back's synthesized snapshot names no mode: it is not a root task
// whose finish says anything about an issue's readiness.
func TestMergeBackSnapshotHasNoHandOff(t *testing.T) {
	if strings.Contains(MergeBackSource(1, 2, "vincent/1-x"), "hand_off") {
		t.Error("the merge-back snapshot carries hand_off")
	}
}

// hand_off is a top-level scalar like description, so the structured editor
// sets and clears it with the same ops.
func TestEditSetsAndClearsHandOff(t *testing.T) {
	src := []byte(handOffWorkflow(""))
	set, err := Edit(src, []Op{{Kind: OpSet, Path: "hand_off", Value: HandOffExplicit}})
	if err != nil {
		t.Fatalf("Edit set: %v", err)
	}
	wf, _, err := Parse(set, Options{})
	if err != nil {
		t.Fatalf("Parse after set: %v\n%s", err, set)
	}
	if wf.HandOff != HandOffExplicit {
		t.Fatalf("HandOff after set = %q\n%s", wf.HandOff, set)
	}
	cleared, err := Edit(set, []Op{{Kind: OpRemove, Path: "hand_off"}})
	if err != nil {
		t.Fatalf("Edit remove: %v", err)
	}
	if strings.Contains(string(cleared), "hand_off") {
		t.Fatalf("hand_off not removed:\n%s", cleared)
	}
	if string(cleared) != string(src) {
		t.Errorf("set then remove did not restore the file:\n%s", cleared)
	}
}
