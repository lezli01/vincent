package workflow

import (
	"reflect"
	"strings"
	"testing"
)

// parseFieldWorkflow wraps one `fields:` block in the smallest valid workflow.
func parseFieldWorkflow(t *testing.T, fields string) (*Workflow, Errors) {
	t.Helper()
	src := "name: w\nfields:\n" + fields + "steps:\n  - {id: gate, type: manual, instructions: review}\n"
	wf, _, err := Parse([]byte(src), Options{})
	if err != nil {
		var errs Errors
		if !asErrors(err, &errs) {
			t.Fatalf("Parse: %v", err)
		}
		return nil, errs
	}
	return wf, nil
}

// findingAt is the message reported for one source path, and "" when the path
// reported nothing. The path is what makes a schema error actionable, so every
// rule below asserts on it rather than on the message alone.
func findingAt(errs Errors, path string) string {
	for _, e := range errs {
		if e.Path == path {
			return e.Message
		}
	}
	return ""
}

func TestParseEnumFieldDeclaration(t *testing.T) {
	wf, errs := parseFieldWorkflow(t, `  - name: environment
    label: Environment
    type: enum
    required: true
    values: [dev, staging, prod]
    default: staging
  - name: reviewers
    type: enum
    multiple: true
    values: [ana, bo, cy]
    default: [cy, ana]
`)
	if len(errs) > 0 {
		t.Fatalf("Parse: %v", errs)
	}
	env := wf.Fields[0]
	if env.Type != FieldEnum || !env.Required || env.Default != "staging" {
		t.Errorf("environment = %+v", env)
	}
	if want := []string{"dev", "staging", "prod"}; !reflect.DeepEqual(env.Values, want) {
		t.Errorf("values = %v, want %v", env.Values, want)
	}
	rev := wf.Fields[1]
	if !rev.Multiple {
		t.Errorf("reviewers.multiple = false, want true")
	}
	// A sequence default is normalized exactly as a task value is: declared
	// order, not the order it was written in.
	if rev.Default != "ana,cy" {
		t.Errorf("reviewers default = %q, want %q", rev.Default, "ana,cy")
	}
}

// TestFieldNativeScalarsCanonicalize pins decision 1: an author writes the
// value the way its type is spelled in YAML and never has to know it is a
// string underneath. The literal source text is what survives, so 1.50 does
// not become 1.5.
func TestFieldNativeScalarsCanonicalize(t *testing.T) {
	wf, errs := parseFieldWorkflow(t, `  - name: flag
    type: boolean
    default: true
  - name: count
    type: integer
    default: 3
  - name: ratio
    type: number
    default: 1.50
  - name: environment
    type: enum
    values: [1, 2]
    default: 2
  - name: stage
    type: string
    default: staging
`)
	if len(errs) > 0 {
		t.Fatalf("Parse: %v", errs)
	}
	want := []string{"true", "3", "1.50", "2", "staging"}
	for i, field := range wf.Fields {
		if field.Default != want[i] {
			t.Errorf("%s default = %q, want %q", field.Name, field.Default, want[i])
		}
	}
	if got := wf.Fields[3].Values; !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Errorf("enum values = %v, want [1 2]", got)
	}
}

func TestEnumDeclarationErrors(t *testing.T) {
	cases := []struct {
		name   string
		fields string
		path   string
		want   string
	}{
		{"values missing", "  - name: e\n    type: enum\n", "fields[0].values", "values is required"},
		{"values empty", "  - name: e\n    type: enum\n    values: []\n", "fields[0].values", "values is required"},
		{
			"values duplicated",
			"  - name: e\n    type: enum\n    values: [a, a]\n",
			"fields[0].values[1]", "already declared",
		},
		{
			"member empty",
			"  - name: e\n    type: enum\n    values: [a, '']\n",
			"fields[0].values[1]", "must not be empty",
		},
		{
			"member with a comma",
			"  - name: e\n    type: enum\n    values: [a, 'b,c']\n",
			"fields[0].values[1]", "must not contain",
		},
		{
			"values on a string field",
			"  - name: e\n    type: string\n    values: [a]\n",
			"fields[0].values", "only valid for enum",
		},
		{
			"multiple on a boolean field",
			"  - name: e\n    type: boolean\n    multiple: true\n",
			"fields[0].multiple", "only valid for enum",
		},
		{
			"pattern alongside enum",
			"  - name: e\n    type: enum\n    values: [a]\n    pattern: '^a$'\n",
			"fields[0].pattern", "not valid for enum",
		},
		{
			"values is not a list",
			"  - name: e\n    type: enum\n    values: a\n",
			"fields[0].values", "must be a list of scalar values",
		},
		{
			"default is a mapping",
			"  - name: e\n    type: string\n    default: {a: 1}\n",
			"fields[0].default", "must be a scalar value",
		},
		{
			"default is a list on a single-choice enum",
			"  - name: e\n    type: enum\n    values: [a, b]\n    default: [a, b]\n",
			"fields[0].default", "may only be a list",
		},
		{
			"default is a list on a string field",
			"  - name: e\n    type: string\n    default: [a, b]\n",
			"fields[0].default", "may only be a list",
		},
		{
			"default is not a member",
			"  - name: e\n    type: enum\n    values: [a, b]\n    default: c\n",
			"fields[0].default", `must be one of a, b; got "c"`,
		},
		{
			"default fails an integer field",
			"  - name: e\n    type: integer\n    default: nope\n",
			"fields[0].default", "base-10 integer",
		},
		{
			"default fails a number field",
			"  - name: e\n    type: number\n    default: nope\n",
			"fields[0].default", "finite decimal number",
		},
		{
			"default fails a boolean field",
			"  - name: e\n    type: boolean\n    default: nope\n",
			"fields[0].default", "must be true or false",
		},
		{
			"default fails a string field's pattern",
			"  - name: e\n    type: string\n    pattern: '^OPS-[0-9]+$'\n    default: nope\n",
			"fields[0].default", "must match pattern",
		},
		{
			"one bad member of a multiple default",
			"  - name: e\n    type: enum\n    multiple: true\n    values: [a, b]\n    default: [a, z]\n",
			"fields[0].default", `got "z"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := parseFieldWorkflow(t, tc.fields)
			got := findingAt(errs, tc.path)
			if got == "" {
				t.Fatalf("no finding at %s; got %v", tc.path, errs)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("%s = %q, want it to contain %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestEnumDeclarationRejectsUnknownKey holds the custom unmarshaler to the
// strictness the outer decoder has: a typo inside a field item is a §8.2
// error, not a silently dropped key.
func TestEnumDeclarationRejectsUnknownKey(t *testing.T) {
	_, errs := parseFieldWorkflow(t, "  - name: e\n    valuse: [a]\n")
	if len(errs) == 0 {
		t.Fatal("a misspelled field key parsed cleanly")
	}
	if !strings.Contains(errs.Error(), "valuse") {
		t.Errorf("errors = %v, want the unknown key named", errs)
	}
}

// TestFieldDefinitionAliasCoversEveryKey guards the one duplication
// UnmarshalYAML costs: the alias struct restates FieldDefinition's yaml keys,
// and a key added to one and not the other would decode as an unknown field.
func TestFieldDefinitionAliasCoversEveryKey(t *testing.T) {
	typ := reflect.TypeOf(FieldDefinition{})
	var pairs []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" || !f.IsExported() {
			continue
		}
		pairs = append(pairs, tag+": x")
	}
	// Every declared key at once. A key the alias forgot would be rejected by
	// the DisallowUnknownField re-applied inside UnmarshalYAML.
	src := "  - " + strings.Join(pairs, "\n    ") + "\n"
	_, errs := parseFieldWorkflow(t, src)
	if msg := errs.Error(); strings.Contains(msg, "unknown field") {
		t.Errorf("a declared key is missing from the decode alias: %v", errs)
	}
}

func TestEnumValueValidation(t *testing.T) {
	single := FieldDefinition{Name: "environment", Type: FieldEnum, Values: []string{"dev", "staging", "prod"}}
	multi := FieldDefinition{Name: "reviewers", Type: FieldEnum, Multiple: true, Values: []string{"ana", "bo", "cy"}}

	if got := single.Validate("staging"); got != "" {
		t.Errorf("member rejected: %s", got)
	}
	if got := single.Validate("nope"); !strings.Contains(got, `got "nope"`) {
		t.Errorf("non-member message = %q, want the element named", got)
	}
	if got := multi.Validate("ana,cy"); got != "" {
		t.Errorf("member set rejected: %s", got)
	}
	if got := multi.Validate("ana,zed"); !strings.Contains(got, `got "zed"`) {
		t.Errorf("bad element message = %q, want the element named", got)
	}
	// Empty is absent, which ValidateTaskFields judges by requiredness.
	optional := &Workflow{Fields: []FieldDefinition{single}}
	if errs := optional.ValidateTaskFields(map[string]string{"environment": ""}); len(errs) > 0 {
		t.Errorf("empty optional enum rejected: %v", errs)
	}
	single.Required = true
	required := &Workflow{Fields: []FieldDefinition{single}}
	if errs := required.ValidateTaskFields(map[string]string{}); len(errs) != 1 {
		t.Errorf("required enum with no value = %v, want one error", errs)
	}
}

func TestNormalizeMultipleEnum(t *testing.T) {
	field := FieldDefinition{
		Name: "reviewers", Type: FieldEnum, Multiple: true,
		Values: []string{"ana", "bo", "cy"},
	}
	for _, tc := range []struct{ in, want string }{
		{"cy, ana", "ana,cy"},
		{"cy,cy", "cy"},
		{"ana,cy", "ana,cy"},
		{"cy,bo,ana", "ana,bo,cy"},
		{"  ", ""},
		{"ana,,cy", "ana,cy"},
		// An unknown element survives normalization so validation can name it.
		{"cy,zed", "cy,zed"},
	} {
		if got := field.Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Every other type is returned untouched: normalization is the multi-value
	// encoding's business and nothing else's.
	plain := FieldDefinition{Name: "note", Type: FieldString}
	if got := plain.Normalize(" b, a "); got != " b, a " {
		t.Errorf("string Normalize rewrote the value: %q", got)
	}
}

// TestPrepareTaskFieldsAppliesRequiredDefaults pins decision 3: only a
// required field's default is substituted server-side, and normalization runs
// before membership is judged.
func TestPrepareTaskFieldsAppliesRequiredDefaults(t *testing.T) {
	wf := &Workflow{Fields: []FieldDefinition{
		{Name: "environment", Type: FieldEnum, Required: true, Values: []string{"dev", "staging", "prod"}, Default: "staging"},
		{Name: "channel", Type: FieldEnum, Values: []string{"alpha", "beta"}, Default: "beta"},
		{Name: "reviewers", Type: FieldEnum, Multiple: true, Values: []string{"ana", "bo", "cy"}},
	}}
	out := wf.PrepareTaskFields(map[string]string{"reviewers": "cy, ana", "custom": "kept"})
	if out["environment"] != "staging" {
		t.Errorf("required default = %q, want staging", out["environment"])
	}
	if _, ok := out["channel"]; ok {
		t.Errorf("optional default was invented server-side: %q", out["channel"])
	}
	if out["reviewers"] != "ana,cy" {
		t.Errorf("reviewers = %q, want ana,cy", out["reviewers"])
	}
	if out["custom"] != "kept" {
		t.Errorf("undeclared field dropped: %v", out)
	}
	if errs := wf.ValidateTaskFields(out); len(errs) > 0 {
		t.Errorf("prepared fields failed validation: %v", errs)
	}
	// A key present but empty is never defaulted, at any requiredness — which
	// is what makes the required field's 400 survive an explicit blank.
	blank := wf.PrepareTaskFields(map[string]string{"environment": ""})
	if blank["environment"] != "" {
		t.Errorf("an explicit empty value was defaulted to %q", blank["environment"])
	}
	if errs := wf.ValidateTaskFields(blank); len(errs) != 1 {
		t.Errorf("explicit empty required field = %v, want one error", errs)
	}
	// Nothing to do means nothing copied: a workflow declaring no fields hands
	// back exactly the map it was given.
	empty := &Workflow{}
	in := map[string]string{"a": "b"}
	if got := empty.PrepareTaskFields(in); !reflect.DeepEqual(got, in) {
		t.Errorf("PrepareTaskFields rewrote an undeclared map: %v", got)
	}
}

// TestPreviewBindsDefaultsOverSentinels pins the preview rule: a required
// field binds a value the workflow could actually receive, because
// SentinelField is by construction not a member of its own enum.
func TestPreviewBindsDefaultsOverSentinels(t *testing.T) {
	wf := &Workflow{Fields: []FieldDefinition{
		{Name: "environment", Type: FieldEnum, Required: true, Values: []string{"dev", "staging", "prod"}, Default: "staging"},
		{Name: "channel", Type: FieldEnum, Required: true, Values: []string{"alpha", "beta"}},
		{Name: "ticket", Type: FieldString, Required: true, Default: "OPS-1"},
		{Name: "note", Type: FieldString, Required: true},
		{Name: "optional", Type: FieldEnum, Values: []string{"x"}, Default: "x"},
	}}
	got := NewPreviewContext(wf, PreviewInput{}).Task.Fields
	want := map[string]string{
		"environment": "staging",
		"channel":     "alpha",
		"ticket":      "OPS-1",
		"note":        SentinelField("note"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preview fields = %v, want %v", got, want)
	}
}
