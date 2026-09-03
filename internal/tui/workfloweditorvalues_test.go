package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// readerKeys is the set of field names a reader table answers for.
func readerKeys[T any](readers map[string]func(T) string) map[string]bool {
	out := make(map[string]bool, len(readers))
	for name := range readers {
		out[name] = true
	}
	return out
}

// TestEveryPublishedFieldHasAReader walks the served descriptor — every block
// of it — and fails on a field no form can show a value for. It is the guard
// the old switch had none of: twelve fields the daemon publishes, `timeout`
// and `max_retries` among them, fell through to "" and were drawn as
// `(unset)` on a step that had set them (issue #320).
//
// A structural control has no reader by decision, recorded with its reason in
// wfStructuralControls: its row shows a summary the builder computes and enter
// opens the block behind it.
func TestEveryPublishedFieldHasAReader(t *testing.T) {
	schema := servedSchema()
	blocks := []struct {
		name    string
		fields  []apiclient.WorkflowSchemaField
		readers map[string]bool
	}{
		{"top_level", schema.TopLevel, readerKeys(wfTopLevelReaders)},
		{"common", schema.Common, readerKeys(wfStepReaders)},
		{"defaults", schema.Defaults, readerKeys(wfDefaultsReaders)},
		{"field", schema.Field, readerKeys(wfFieldReaders)},
		{"lane", schema.Lane, readerKeys(wfLaneReaders)},
		{"merge", schema.Merge, readerKeys(wfMergeReaders)},
		{"container", schema.Container, readerKeys(wfContainerReaders)},
	}
	for _, s := range schema.Steps {
		blocks = append(blocks, struct {
			name    string
			fields  []apiclient.WorkflowSchemaField
			readers map[string]bool
		}{"steps." + s.Type, s.Fields, readerKeys(wfStepReaders)})
	}
	checked := 0
	for _, b := range blocks {
		for _, f := range b.fields {
			checked++
			if reason, ok := wfStructuralControls[f.Control]; ok {
				t.Logf("%s.%s has no reader: %s", b.name, f.Name, reason)
				continue
			}
			if !b.readers[f.Name] {
				t.Errorf("%s publishes %s (control %s) and no reader answers for it — "+
					"the form would draw it as (unset) however the file spells it",
					b.name, f.Name, f.Control)
			}
		}
	}
	// A descriptor that arrived empty would pass by checking nothing, which
	// is the one way this test could lie about the forms.
	if checked < 50 {
		t.Fatalf("only %d published fields were checked; the descriptor cannot be that small", checked)
	}
}

// TestEditorRowsReportWhatTheFileSets is the row-level half: the two fields
// the report named, on a fan-out-shaped definition, read back as the file
// wrote them. `max_retries: 0` is the sharp one — unset and set-to-zero are
// different answers, which is why the field is a pointer, and a form that
// showed both as (unset) would offer to remove a key the file does set.
func TestEditorRowsReportWhatTheFileSets(t *testing.T) {
	w := editorFixtureWith(t, dagDefinition())
	descendTo(t, w, "steps[0]")
	got := map[string]string{}
	for _, r := range w.editor.rows {
		got[r.field.Name] = r.value
	}
	if got["timeout"] != "2m" {
		t.Errorf("timeout = %q, want the 2m the file sets", got["timeout"])
	}
	if got["max_retries"] != "0" {
		t.Errorf("max_retries = %q, want 0 — set-to-zero is not unset", got["max_retries"])
	}
	// A field the file genuinely leaves alone still reads as unset, which is
	// the distinction the pointers exist to carry.
	if got["retry_backoff"] != "" {
		t.Errorf("retry_backoff = %q, want unset", got["retry_backoff"])
	}
	// The rendered form says the same thing: the row is not `(unset)`.
	out := w.render(100, 40)
	if !containsRow(out, "timeout", "2m") || !containsRow(out, "max_retries", "0") {
		t.Errorf("the drawn form does not report what the file sets:\n%s", out)
	}
}

// containsRow reports whether a rendered form holds a row with this label and
// value; the two are padded apart, so the check is per line.
func containsRow(out, label, value string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, label) && strings.Contains(line, value) {
			return true
		}
	}
	return false
}

// TestValueRenderersMatchWhatACommitSends pins the shapes the write path
// parses back: a list is what renderFlowList reads, and a map's keys are
// sorted so the row is stable between two reads of the same file.
func TestValueRenderersMatchWhatACommitSends(t *testing.T) {
	if got := wfList([]string{"api", "tui"}); got != "api, tui" {
		t.Errorf("wfList = %q", got)
	}
	if got := renderFlowList(wfList([]string{"api", "tui"})); got != "[api, tui]" {
		t.Errorf("a list row does not round-trip: %q", got)
	}
	if got := wfMap(map[string]string{"b": "2", "a": "1"}); got != "a=1, b=2" {
		t.Errorf("wfMap = %q, want sorted keys", got)
	}
	if got := wfMap(nil); got != "" {
		t.Errorf("an absent map = %q, want unset", got)
	}
	if got := wfInt(ptr(0)); got != "0" {
		t.Errorf("wfInt(0) = %q, want 0 rather than unset", got)
	}
	if got := wfInt(nil); got != "" {
		t.Errorf("wfInt(nil) = %q, want unset", got)
	}
	// A plain bool has no "absent": the wire omits a false one, so false and
	// unset are one value and writing "false" would offer a key that means
	// nothing. A *pointer* bool keeps all three answers.
	if got := wfBool(false); got != "" {
		t.Errorf("wfBool(false) = %q, want unset", got)
	}
	if got := wfBoolPtr(ptr(false)); got != "false" {
		t.Errorf("wfBoolPtr(false) = %q, want false", got)
	}
	if got := wfBoolPtr(nil); got != "" {
		t.Errorf("wfBoolPtr(nil) = %q, want unset", got)
	}
}

// A field this client has never heard of — a newer daemon's — reads as unset
// rather than panicking, which is what keeps an old TUI usable against a new
// registry.
func TestUnknownFieldReadsAsUnset(t *testing.T) {
	if got := wfRead(wfStepReaders, apiclient.WorkflowStepDef{ID: "a"}, "invented_later"); got != "" {
		t.Errorf("an unknown field read %q", got)
	}
}
