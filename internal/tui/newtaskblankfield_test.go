package tui

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/workflow"
)

// blankFieldDeclarations is one declaration per (type, requiredness) pair the
// vocabulary of spec §8.1.2 allows, including both string shapes (with and
// without a pattern) and both enum shapes (single and `multiple`).
func blankFieldDeclarations() []workflow.FieldDefinition {
	var out []workflow.FieldDefinition
	for _, base := range []workflow.FieldDefinition{
		{Name: "plain", Type: workflow.FieldString},
		{Name: "patterned", Type: workflow.FieldString, Pattern: `^OPS-[0-9]+$`},
		{Name: "retries", Type: workflow.FieldInteger},
		{Name: "ratio", Type: workflow.FieldNumber},
		{Name: "draft", Type: workflow.FieldBoolean},
		{Name: "environment", Type: workflow.FieldEnum, Values: []string{"dev", "staging", "prod"}},
		{Name: "reviewers", Type: workflow.FieldEnum, Multiple: true, Values: []string{"ana", "bo", "cy"}},
	} {
		optional := base
		out = append(out, optional)
		required := base
		required.Name = base.Name + "_req"
		required.Required = true
		out = append(out, required)
	}
	return out
}

// validBlankFieldValue is a value the declaration certainly accepts, so the
// table proves the two implementations agree on the ordinary case too and not
// only on the blank ones.
func validBlankFieldValue(field workflow.FieldDefinition) string {
	switch field.Type {
	case workflow.FieldInteger:
		return "3"
	case workflow.FieldNumber:
		return "1.5"
	case workflow.FieldBoolean:
		return "true"
	case workflow.FieldEnum:
		if field.Multiple {
			return "ana,cy"
		}
		return "staging"
	default:
		if field.Pattern != "" {
			return "OPS-123"
		}
		return "anything"
	}
}

// asAPIField is the same declaration as GET /v1/workflows publishes it, so the
// TUI row under test carries exactly what the daemon declared.
func asAPIField(field workflow.FieldDefinition) apiclient.WorkflowField {
	return apiclient.WorkflowField{
		Name:     field.Name,
		Type:     field.Type,
		Required: field.Required,
		Pattern:  field.Pattern,
		Values:   field.Values,
		Multiple: field.Multiple,
		Default:  field.Default,
	}
}

// TestBlankDeclaredFieldAgreesWithDaemon holds internal/tui's
// fieldValidationMessage and internal/workflow's ValidateTaskFields to one
// meaning for a whitespace-only value of a declared field (§8.1.2, task 022,
// issue #575).
//
// The two are the same contract seen from both sides — decision 5 says the
// daemon is the authoritative gate and the TUI mirrors the *same* pure checks
// so an error lands on the Fields row — so a value one accepts and the other
// rejects is a defect whichever of them is right. The table asserts the
// accept/reject verdict only, never the sentence: the daemon names the field
// key and the TUI its display label, by design.
func TestBlankDeclaredFieldAgreesWithDaemon(t *testing.T) {
	blanks := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"space", " "},
		{"tab", "\t"},
		{"mixed", "  \t "},
	}
	for _, field := range blankFieldDeclarations() {
		wf := &workflow.Workflow{Fields: []workflow.FieldDefinition{field}}
		api := asAPIField(field)
		cases := append([]struct {
			name  string
			value string
		}{}, blanks...)
		cases = append(cases, struct {
			name  string
			value string
		}{"valid", validBlankFieldValue(field)})

		for _, tc := range cases {
			t.Run(field.Name+"/"+tc.name, func(t *testing.T) {
				daemon := wf.ValidateTaskFields(map[string]string{field.Name: tc.value})
				tui := fieldValidationMessage(kv{
					key: field.Name, value: tc.value, declared: true, definition: api,
				})
				daemonRejects, tuiRejects := len(daemon) > 0, tui != ""
				if daemonRejects != tuiRejects {
					t.Errorf("value %q on %s (required=%t): daemon rejects=%t (%v), TUI rejects=%t (%q); "+
						"the two must mean the same thing",
						tc.value, field.Type, field.Required,
						daemonRejects, daemon, tuiRejects, tui)
				}
			})
		}
	}
}

// TestBlankOptionalFieldIsNotStoredVerbatim is the create-path half of issue
// #575: `retries: "  "` on an optional integer field must not be accepted and
// written to the task row untouched.
//
// ValidateTaskFields trims *above* its call to validateTaskFieldValue
// (internal/workflow/fields.go:391), so a whitespace-only value reads as
// absent, an optional field takes the `continue`, no type check ever runs, and
// the untrimmed string is what PrepareTaskFields leaves in the map that
// POST /v1/tasks inserts.
//
// The assertion is deliberately rule-agnostic: either the daemon rejects the
// value or it does not keep it, and it does neither today.
func TestBlankOptionalFieldIsNotStoredVerbatim(t *testing.T) {
	wf := &workflow.Workflow{Fields: []workflow.FieldDefinition{
		{Name: "retries", Type: workflow.FieldInteger},
	}}
	prepared := wf.PrepareTaskFields(map[string]string{"retries": "  "})
	errs := wf.ValidateTaskFields(prepared)
	if len(errs) > 0 {
		return
	}
	if got, ok := prepared["retries"]; ok && got == "  " {
		t.Errorf(`optional integer "retries" accepted and stored as %q; `+
			"a whitespace-only value must be rejected or dropped, not kept verbatim", got)
	}
}
