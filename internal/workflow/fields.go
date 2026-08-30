package workflow

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// Field types are a validation and editing vocabulary (§8.1.2, task 022).
// Task fields remain strings in storage, on the wire, and in templates.
const (
	FieldString  = "string"
	FieldInteger = "integer"
	FieldNumber  = "number"
	FieldBoolean = "boolean"
	// FieldEnum is a closed set of strings, declared in `values:` (task 058).
	// It is the one type that *publishes* what it accepts, which is what lets
	// a client build a control that cannot be wrong instead of a text box
	// plus a 400 afterwards.
	FieldEnum = "enum"
)

// MultiValueSeparator joins the members of a `multiple: true` enum. Declared
// order, no spaces, deduplicated: two tasks with the same selection produce
// the same string, so template output and branch names are stable (task 058).
const MultiValueSeparator = ","

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
	// Values are the members of a `type: enum` field, in declared order.
	// Declared order is the canonical order a multi-valued selection is
	// written back in, so this slice is the ordering authority.
	Values []string `yaml:"values,omitempty" json:"values,omitempty"`
	// Multiple says an enum accepts more than one member, joined by
	// MultiValueSeparator. It is per field and enum-only: "a boolean that
	// accepts more than one boolean" has no meaning.
	Multiple bool `yaml:"multiple,omitempty" json:"multiple,omitempty"`
	// Default is the value that applies when the caller omits this key. It is
	// not enum-specific — any declared field may carry one — and is checked
	// against this very declaration at load time.
	Default string `yaml:"default,omitempty" json:"default,omitempty"`

	// defaultShape and valuesShape record a node whose *shape* was wrong —
	// a mapping `default:`, a `values:` that is not a sequence — so
	// validateFieldDefinitions can report it at fields[i].default with a
	// source path like every other structural mistake, rather than as a bare
	// yaml type error with no path at all.
	defaultShape string
	valuesShape  string
	// defaultSeq records that `default:` was written as a YAML sequence,
	// which only a `multiple: true` enum may do.
	defaultSeq bool
}

// UnmarshalYAML decodes a field declaration, canonicalizing `default:` and
// `values:` from native YAML scalars to the strings task fields actually
// carry (task 058 decision 1).
//
// It exists because FieldDefinition decodes into Go strings: without it a
// bare `default: true` on a boolean field or `values: [1, 2, 3]` on an enum
// would be a yaml type error rather than a schema error, and an author would
// have to know the value is a string underneath. A scalar's *literal* source
// text becomes the string, so `default: 1.50` is "1.50" and not "1.5".
//
// Nothing here fails: a wrong shape is remembered and reported by
// validateFieldDefinitions, which owns the `fields[i].default` source path.
func (f *FieldDefinition) UnmarshalYAML(b []byte) error {
	// The alias lists every key FieldDefinition declares, because a custom
	// unmarshaler bypasses the outer decoder's strictness; DisallowUnknownField
	// is re-applied here so a typo inside a field item is still an error
	// (§8.2). TestFieldDefinitionAliasCoversEveryKey guards the two lists.
	var raw struct {
		Name        string   `yaml:"name"`
		Label       string   `yaml:"label"`
		Description string   `yaml:"description"`
		Type        string   `yaml:"type"`
		Required    bool     `yaml:"required"`
		Pattern     string   `yaml:"pattern"`
		Values      ast.Node `yaml:"values"`
		Multiple    bool     `yaml:"multiple"`
		Default     ast.Node `yaml:"default"`
	}
	if err := yaml.UnmarshalWithOptions(b, &raw, yaml.DisallowUnknownField()); err != nil {
		return err
	}
	*f = FieldDefinition{
		Name:        raw.Name,
		Label:       raw.Label,
		Description: raw.Description,
		Type:        raw.Type,
		Required:    raw.Required,
		Pattern:     raw.Pattern,
		Multiple:    raw.Multiple,
	}
	if raw.Values != nil {
		if seq, ok := raw.Values.(*ast.SequenceNode); ok {
			f.Values = make([]string, 0, len(seq.Values))
			for _, member := range seq.Values {
				text, ok := scalarText(member)
				if !ok {
					f.valuesShape = "values must be a list of scalar values"
					f.Values = nil
					break
				}
				f.Values = append(f.Values, text)
			}
		} else {
			f.valuesShape = "values must be a list of scalar values"
		}
	}
	if raw.Default != nil {
		switch node := raw.Default.(type) {
		case *ast.SequenceNode:
			f.defaultSeq = true
			members := make([]string, 0, len(node.Values))
			for _, member := range node.Values {
				text, ok := scalarText(member)
				if !ok {
					f.defaultShape = "default must be a scalar value, or a list of them for a multiple enum"
					members = nil
					break
				}
				members = append(members, text)
			}
			f.Default = strings.Join(members, MultiValueSeparator)
		default:
			text, ok := scalarText(raw.Default)
			if !ok {
				f.defaultShape = "default must be a scalar value, or a list of them for a multiple enum"
				break
			}
			f.Default = text
		}
	}
	return nil
}

// scalarText is a YAML scalar's literal source text. A null node — `default:`
// with nothing after it — reads as absent rather than as the string "null",
// which is what an author who deleted a value meant.
func scalarText(node ast.Node) (string, bool) {
	switch node.Type() {
	case ast.StringType, ast.IntegerType, ast.FloatType, ast.BoolType,
		ast.InfinityType, ast.NanType:
		return node.GetToken().Value, true
	case ast.NullType:
		return "", true
	default:
		return "", false
	}
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
			add(base+".type", "field type must be one of %s, %s, %s, %s, %s; got %q",
				FieldString, FieldInteger, FieldNumber, FieldBoolean, FieldEnum, field.Type)
		}
		validateEnumDeclaration(field, base, add)
		validateFieldDefault(field, base, add)

		// An enum's `pattern:` was already reported against the members it
		// contradicts; saying "only valid for string fields" as well would
		// name the weaker of the two reasons twice.
		if field.Pattern == "" || field.Type == FieldEnum {
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

// validateEnumDeclaration checks `values:` and `multiple:` (§8.1.2, task 058).
// Both belong to `type: enum` and to nothing else, and a member may not carry
// the separator a multi-valued selection is joined with — a value that cannot
// be written back unambiguously is a declaration bug, not a run-time one.
func validateEnumDeclaration(field *FieldDefinition, base string, add func(string, string, ...any)) {
	if field.valuesShape != "" {
		add(base+".values", "%s", field.valuesShape)
		return
	}
	if field.Type != FieldEnum {
		if len(field.Values) > 0 {
			add(base+".values", "values is only valid for %s fields", FieldEnum)
		}
		if field.Multiple {
			add(base+".multiple", "multiple is only valid for %s fields", FieldEnum)
		}
		return
	}
	if field.Pattern != "" {
		// Reported here rather than left to the string-only rule below, so
		// the message names the actual conflict.
		add(base+".pattern", "pattern is not valid for %s fields; %s declares its members in values", FieldEnum, FieldEnum)
	}
	if len(field.Values) == 0 {
		add(base+".values", "values is required for %s fields and must not be empty", FieldEnum)
		return
	}
	member := map[string]int{}
	for i, value := range field.Values {
		path := base + ".values[" + strconv.Itoa(i) + "]"
		switch {
		case strings.TrimSpace(value) == "":
			add(path, "enum value must not be empty")
		case strings.Contains(value, MultiValueSeparator):
			add(path, "enum value %q must not contain %q, which joins a multiple selection",
				value, MultiValueSeparator)
		}
		if at, dup := member[value]; dup {
			add(path, "enum value %q is already declared at %s.values[%d]", value, base, at)
			continue
		}
		member[value] = i
	}
}

// validateFieldDefault checks `default:` against the very declaration it sits
// in, so a default that could never be accepted at task creation is a load
// error rather than a surprise later.
func validateFieldDefault(field *FieldDefinition, base string, add func(string, string, ...any)) {
	if field.defaultShape != "" {
		add(base+".default", "%s", field.defaultShape)
		return
	}
	if field.defaultSeq && (field.Type != FieldEnum || !field.Multiple) {
		add(base+".default", "default may only be a list on a multiple %s field", FieldEnum)
		return
	}
	if field.Default == "" {
		return
	}
	// A declaration that is already broken would produce a second, confusing
	// error here — "not one of []" for an enum with no members.
	if field.Type == FieldEnum && len(field.Values) == 0 {
		return
	}
	field.Default = field.Normalize(field.Default)
	if message := validateTaskFieldValue(*field, field.Default); message != "" {
		add(base+".default", "%s", message)
	}
}

func isFieldType(value string) bool {
	switch value {
	case FieldString, FieldInteger, FieldNumber, FieldBoolean, FieldEnum:
		return true
	}
	return false
}

// Normalize canonicalizes one value for this declaration. For a `multiple`
// enum that means splitting on the separator, trimming, dropping empties,
// deduplicating and rejoining in **declared** order; every other type is
// returned unchanged.
//
// It runs ahead of validation on create (task 058 decision 2) so that every
// client — TUI, `--field`, `--fields-file`, curl — produces the same stored
// string for the same selection, and so a membership error names the element
// the caller actually wrote. An element the declaration does not know is
// preserved in place, because rejecting it is validation's job and dropping
// it would hide the mistake.
func (f FieldDefinition) Normalize(value string) string {
	if f.Type != FieldEnum || !f.Multiple {
		return value
	}
	picked := make([]string, 0, len(f.Values))
	var unknown []string
	seen := map[string]bool{}
	for _, part := range strings.Split(value, MultiValueSeparator) {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		if slices.Contains(f.Values, part) {
			picked = append(picked, part)
			continue
		}
		unknown = append(unknown, part)
	}
	slices.SortStableFunc(picked, func(a, b string) int {
		return slices.Index(f.Values, a) - slices.Index(f.Values, b)
	})
	return strings.Join(append(picked, unknown...), MultiValueSeparator)
}

// PrepareTaskFields is what a create call runs before ValidateTaskFields: it
// substitutes a **required** field's declared default for an omitted key and
// canonicalizes every declared value (task 058 decisions 2 and 3).
//
// Only a required field's default is substituted. An optional field's default
// is published through GET /v1/workflows and seeded by clients, but the daemon
// never invents it: an optional field the caller omitted stays genuinely
// absent from .Task.Fields, so `{{ with index .Task.Fields "x" }}` keeps
// meaning what it means today and adding a `default:` to an existing optional
// field is not a silent behaviour change for workflows that guard on presence.
// A key that is present but empty is never defaulted, at any requiredness.
//
// values is never mutated: the map is copied only if something changes, so a
// workflow that declares no fields hands back exactly what it was given.
func (w *Workflow) PrepareTaskFields(values map[string]string) map[string]string {
	if w == nil || len(w.Fields) == 0 {
		return values
	}
	out, copied := values, false
	for _, field := range w.Fields {
		if field.Name == "" {
			continue
		}
		value, present := values[field.Name]
		if !present {
			if !field.Required || field.Default == "" {
				continue
			}
			value = field.Default
		}
		normalized := field.Normalize(value)
		if present && normalized == value {
			continue
		}
		if !copied {
			out = make(map[string]string, len(values)+len(w.Fields))
			for k, v := range values {
				out[k] = v
			}
			copied = true
		}
		out[field.Name] = normalized
	}
	return out
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
	case FieldEnum:
		return validateEnumValue(field, value)
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

// validateEnumValue checks membership, naming the element that failed rather
// than the whole value: for a `multiple` field "reviewers" the useful sentence
// is which of the three names is not a reviewer.
func validateEnumValue(field FieldDefinition, value string) string {
	parts := []string{value}
	if field.Multiple {
		parts = strings.Split(value, MultiValueSeparator)
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !slices.Contains(field.Values, part) {
			return "field " + strconv.Quote(field.Name) + " must be one of " +
				strings.Join(field.Values, ", ") + "; got " + strconv.Quote(part)
		}
	}
	return ""
}
