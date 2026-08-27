package workflow

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Field types are a validation and editing vocabulary (§8.1.2, task 022).
// Task fields remain strings in storage, on the wire, and in templates.
const (
	FieldString  = "string"
	FieldInteger = "integer"
	FieldNumber  = "number"
	FieldBoolean = "boolean"
)

var decimalNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// FieldDefinition is one ordered task input declared by a workflow. Name is
// the key in .Task.Fields; Label and Description are presentation hints only.
// Pattern is a Go RE2 expression and belongs only to string fields.
type FieldDefinition struct {
	Name        string `yaml:"name" json:"name"`
	Label       string `yaml:"label,omitempty" json:"label,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Type        string `yaml:"type,omitempty" json:"type"`
	Required    bool   `yaml:"required,omitempty" json:"required"`
	Pattern     string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
}

// DisplayLabel is the human-facing name, falling back to the map key.
func (f FieldDefinition) DisplayLabel() string {
	if f.Label != "" {
		return f.Label
	}
	return f.Name
}

// validateFieldDefinitions checks the declaration itself while the workflow
// is loading. It also normalizes an omitted type to string so every API client
// sees one explicit answer rather than re-deriving the default.
func validateFieldDefinitions(wf *Workflow, add func(string, string, ...any)) {
	seen := make(map[string]int, len(wf.Fields))
	for i := range wf.Fields {
		field := &wf.Fields[i]
		base := "fields[" + strconv.Itoa(i) + "]"
		if field.Name == "" {
			add(base+".name", "field name is required")
		} else if !isSlug(field.Name) {
			add(base+".name", "field name %q must be a lowercase slug", field.Name)
		} else if previous, ok := seen[field.Name]; ok {
			add(base+".name", "field name %q is already declared at fields[%d]", field.Name, previous)
		} else {
			seen[field.Name] = i
		}

		if field.Type == "" {
			field.Type = FieldString
		}
		if !isFieldType(field.Type) {
			add(base+".type", "field type must be one of %s, %s, %s, %s; got %q",
				FieldString, FieldInteger, FieldNumber, FieldBoolean, field.Type)
		}
		if field.Pattern == "" {
			continue
		}
		if field.Type != FieldString {
			add(base+".pattern", "pattern is only valid for string fields")
			continue
		}
		if _, err := regexp.Compile(field.Pattern); err != nil {
			add(base+".pattern", "pattern does not compile: %v", err)
		}
	}
}

func isFieldType(value string) bool {
	return value == FieldString || value == FieldInteger || value == FieldNumber || value == FieldBoolean
}

// ValidateTaskFields validates only declared fields. Additional keys are
// deliberately ignored and remain part of the task's open field map (decision
// 3): adding a form contract must not break existing metadata or scripts.
func (w *Workflow) ValidateTaskFields(values map[string]string) Errors {
	if w == nil {
		return nil
	}
	var errs Errors
	for _, field := range w.Fields {
		value, present := values[field.Name]
		if !present || strings.TrimSpace(value) == "" {
			if field.Required {
				errs = append(errs, Error{
					Path:    "fields." + field.Name,
					Message: "field " + strconv.Quote(field.Name) + " is required",
				})
			}
			continue
		}
		if message := validateTaskFieldValue(field, value); message != "" {
			errs = append(errs, Error{Path: "fields." + field.Name, Message: message})
		}
	}
	return errs
}

// Validate checks one value against this declaration, returning the same
// message ValidateTaskFields would report for it and "" when it passes.
//
// It exists so a caller that wants to *offer* a value can ask whether the
// declaration would accept it without assembling a whole task field map —
// the GitHub issue prefill's "a value is offered only if it satisfies the
// declaration's type and pattern" rule (task 035 decision 7). Exporting the
// same routine ValidateTaskFields uses is what keeps that rule and the create
// call's 400 from ever disagreeing.
func (f FieldDefinition) Validate(value string) string {
	return validateTaskFieldValue(f, value)
}

func validateTaskFieldValue(field FieldDefinition, value string) string {
	switch field.Type {
	case FieldInteger:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return "field " + strconv.Quote(field.Name) + " must be a base-10 integer"
		}
	case FieldNumber:
		number, err := strconv.ParseFloat(value, 64)
		if !decimalNumber.MatchString(value) || err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return "field " + strconv.Quote(field.Name) + " must be a finite decimal number"
		}
	case FieldBoolean:
		if value != "true" && value != "false" {
			return "field " + strconv.Quote(field.Name) + " must be true or false"
		}
	case FieldString:
		if field.Pattern != "" {
			pattern, err := regexp.Compile(field.Pattern)
			if err != nil || !pattern.MatchString(value) {
				return "field " + strconv.Quote(field.Name) + " must match pattern " + strconv.Quote(field.Pattern)
			}
		}
	}
	return ""
}
