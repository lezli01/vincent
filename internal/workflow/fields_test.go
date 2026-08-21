package workflow

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const fieldsSource = `name: release
fields:
  - name: ticket
    label: Ticket
    description: Issue tracker key.
    required: true
    pattern: '^OPS-[0-9]+$'
  - name: retries
    type: integer
  - name: ratio
    type: number
  - name: dry-run
    type: boolean
steps:
  - {id: gate, type: manual, instructions: review}
`

func TestParseWorkflowFields(t *testing.T) {
	wf, _, err := Parse([]byte(fieldsSource), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(wf.Fields) != 4 {
		t.Fatalf("fields = %d, want 4", len(wf.Fields))
	}
	first := wf.Fields[0]
	if first.Name != "ticket" || first.Type != FieldString || !first.Required ||
		first.Label != "Ticket" || first.Pattern != `^OPS-[0-9]+$` {
		t.Errorf("first field = %+v", first)
	}
	if got := first.DisplayLabel(); got != "Ticket" {
		t.Errorf("DisplayLabel = %q, want Ticket", got)
	}
	if got := wf.Fields[1].DisplayLabel(); got != "retries" {
		t.Errorf("fallback DisplayLabel = %q, want retries", got)
	}
	wantTypes := []string{FieldString, FieldInteger, FieldNumber, FieldBoolean}
	gotTypes := make([]string, 0, len(wf.Fields))
	for _, field := range wf.Fields {
		gotTypes = append(gotTypes, field.Type)
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Errorf("types = %v, want %v", gotTypes, wantTypes)
	}
}

func TestWorkflowFieldDefinitionValidation(t *testing.T) {
	tests := []struct {
		name     string
		fields   string
		wantPath string
		want     string
	}{
		{"missing name", "  - {type: string}\n", "fields[0].name", "name is required"},
		{"name must be slug", "  - {name: Ticket}\n", "fields[0].name", "lowercase slug"},
		{"duplicate", "  - {name: ticket}\n  - {name: ticket}\n", "fields[1].name", "already declared"},
		{"unknown type", "  - {name: ticket, type: date}\n", "fields[0].type", "must be one of"},
		{"pattern only on strings", "  - {name: retries, type: integer, pattern: x}\n", "fields[0].pattern", "only valid for string"},
		{"bad pattern", "  - {name: ticket, pattern: '[abc'}\n", "fields[0].pattern", "does not compile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "name: x\nfields:\n" + tt.fields +
				"steps:\n  - {id: gate, type: manual, instructions: review}\n"
			_, _, err := Parse([]byte(src), Options{})
			if err == nil {
				t.Fatal("Parse succeeded")
			}
			var errs Errors
			if !errors.As(err, &errs) {
				t.Fatalf("error = %T %v, want workflow.Errors", err, err)
			}
			found := false
			for _, got := range errs {
				if got.Path == tt.wantPath && strings.Contains(got.Message, tt.want) {
					found = true
					if got.Line == 0 {
						t.Errorf("%s has no source line: %+v", tt.wantPath, got)
					}
				}
			}
			if !found {
				t.Errorf("errors = %+v, want %s containing %q", errs, tt.wantPath, tt.want)
			}
		})
	}
}

func TestWorkflowFieldDefinitionRejectsUnknownKeys(t *testing.T) {
	src := `name: x
fields:
  - name: ticket
    regex: '^OPS-'
steps:
  - {id: gate, type: manual, instructions: review}
`
	_, _, err := Parse([]byte(src), Options{})
	if err == nil || !strings.Contains(err.Error(), "regex") {
		t.Fatalf("Parse error = %v, want the unknown field key", err)
	}
}

func TestValidateTaskFields(t *testing.T) {
	wf, _, err := Parse([]byte(fieldsSource), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"valid and additional field", map[string]string{
			"ticket": "OPS-42", "retries": "-2", "ratio": "0.25", "dry-run": "false", "owner": "alice",
		}, ""},
		{"required absent", map[string]string{}, `field "ticket" is required`},
		{"required blank", map[string]string{"ticket": "  "}, `field "ticket" is required`},
		{"pattern", map[string]string{"ticket": "BUG-42"}, `must match pattern`},
		{"integer", map[string]string{"ticket": "OPS-1", "retries": "1.5"}, `base-10 integer`},
		{"number not finite", map[string]string{"ticket": "OPS-1", "ratio": "NaN"}, `finite decimal number`},
		{"number not decimal", map[string]string{"ticket": "OPS-1", "ratio": "0x1p2"}, `finite decimal number`},
		{"boolean", map[string]string{"ticket": "OPS-1", "dry-run": "yes"}, `true or false`},
		{"optional blank skips type check", map[string]string{"ticket": "OPS-1", "retries": ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := wf.ValidateTaskFields(tt.values)
			if tt.want == "" {
				if len(errs) != 0 {
					t.Fatalf("ValidateTaskFields = %v, want valid", errs)
				}
				return
			}
			if len(errs) == 0 || !strings.Contains(errs.Error(), tt.want) {
				t.Fatalf("ValidateTaskFields = %v, want %q", errs, tt.want)
			}
		})
	}
}
