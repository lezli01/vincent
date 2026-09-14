package trigger

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/internal/workflow"
)

// docFor is a minimal valid trigger for one source and action type, as nested
// maps so a test can add, change or delete one key.
func docFor(source, action string) map[string]any {
	doc := map[string]any{"id": "t1"}
	switch source {
	case SourceGitHubIssues:
		doc["source"] = map[string]any{"type": source, "project": 1}
		doc["match"] = map[string]any{"action": "labeled"}
	case SourceGitHubPRs:
		doc["source"] = map[string]any{"type": source, "project": 1}
		doc["match"] = map[string]any{"action": "merged"}
	case SourceHTTP:
		doc["source"] = map[string]any{"type": source, "project": 1, "signature": map[string]any{
			"scheme": SignatureGitHubHMACSHA256, "secret_env": "HOOK_SECRET",
		}}
	default:
		doc["source"] = map[string]any{"type": SourceCommand, "project": 1, "poll_interval": "1m", "command": []any{"poll"}}
	}
	switch action {
	case ActionFollowUp:
		doc["action"] = map[string]any{"type": action, "target": TargetBranch, "branch": "{{ .Event.ref }}", "prompt": "fix it"}
	case ActionRetry:
		doc["action"] = map[string]any{"type": action, "target": TargetBranch, "branch": "{{ .Event.ref }}"}
	case ActionCancel:
		doc["action"] = map[string]any{"type": action, "target": TargetBranch, "branch": "{{ .Event.ref }}"}
		doc["on_fire"] = OnFireCreate
	default:
		doc["action"] = map[string]any{"type": ActionCreateTask, "title": "{{ .Event.id }}"}
	}
	return doc
}

func validDoc() map[string]any { return docFor(SourceCommand, ActionCreateTask) }

func parseDoc(t *testing.T, doc map[string]any) workflow.Errors {
	t.Helper()
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, errs := Parse(b, "t1")
	return errs
}

// setPath sets (or, with del, deletes) a dotted key in a nested map document.
func setPath(doc map[string]any, path string, v any, del bool) {
	parts := strings.Split(path, ".")
	m := doc
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	if del {
		delete(m, parts[len(parts)-1])
		return
	}
	m[parts[len(parts)-1]] = v
}

func hasPath(errs workflow.Errors, path string) bool {
	for _, e := range errs {
		if e.Path == path {
			return true
		}
	}
	return false
}

// yamlKeys lists a struct type's yaml tag names.
func yamlKeys(v any) []string {
	var out []string
	rt := reflect.TypeOf(v)
	for i := range rt.NumField() {
		if tag := strings.Split(rt.Field(i).Tag.Get("yaml"), ",")[0]; tag != "" {
			out = append(out, tag)
		}
	}
	slices.Sort(out)
	return out
}

func names(fs []SchemaField) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Name)
	}
	slices.Sort(out)
	return out
}

// unionNames is every field name any variant takes.
func unionNames(vs []SchemaVariant) []string {
	var out []string
	for _, v := range vs {
		for _, f := range v.Fields {
			if !slices.Contains(out, f.Name) {
				out = append(out, f.Name)
			}
		}
	}
	slices.Sort(out)
	return out
}

// sample is a valid value for a field of this control, used to prove the
// validator accepts every field the descriptor offers.
func sample(f SchemaField) any {
	switch f.Control {
	case workflow.ControlBool:
		return false
	case workflow.ControlInt, ControlProject:
		return 1
	case ControlNumber:
		return 1.5
	case workflow.ControlDuration:
		return "5m"
	case workflow.ControlList:
		return []any{"x"}
	case workflow.ControlMap:
		return map[string]any{"k": "v"}
	case ControlMatch:
		return map[string]any{"fields.status": "open"}
	case workflow.ControlEnum:
		return f.Values[0]
	default:
		return "x"
	}
}

// walk checks one section of the descriptor against the validator over base:
// every described field is accepted with a sample value, every required field
// is refused at its own path when absent, and every enum member is accepted
// while a value outside the set is refused at that path.
func walk(t *testing.T, prefix string, fields []SchemaField, base func(path string) map[string]any) {
	t.Helper()
	for _, f := range fields {
		path := prefix + f.Name
		switch f.Control {
		case ControlSource, ControlAction, ControlLimits, ControlSignature:
			continue // descents: their fields are walked as their own section
		}
		isType := path == "source.type" || path == "action.type"
		if path != "id" && !isType {
			doc := base(path)
			setPath(doc, path, sample(f), false)
			if errs := parseDoc(t, doc); len(errs) > 0 {
				t.Errorf("%s = %v refused: %v", path, sample(f), errs)
			}
		}
		if f.Required {
			doc := base(path)
			setPath(doc, path, nil, true)
			if errs := parseDoc(t, doc); !hasPath(errs, path) {
				t.Errorf("%s is required in the descriptor, but its absence was %v", path, errs)
			}
		}
		if f.Control == workflow.ControlEnum {
			// A variant's `type` member switches variants, and the switched
			// document legitimately lacks the new variant's required keys;
			// each type is walked as its own base instead.
			if !isType {
				for _, v := range f.Values {
					doc := base(path)
					setPath(doc, path, v, false)
					if errs := parseDoc(t, doc); len(errs) > 0 {
						t.Errorf("%s = %q (a served member) refused: %v", path, v, errs)
					}
				}
			}
			doc := base(path)
			setPath(doc, path, "no-such-value", false)
			if errs := parseDoc(t, doc); !hasPath(errs, path) {
				t.Errorf("%s = no-such-value accepted or refused elsewhere: %v", path, errs)
			}
		}
		for _, dv := range f.Dangerous {
			if dv.Warning == "" {
				t.Errorf("%s: dangerous value %q has no warning", path, dv.Value)
			}
		}
	}
}

// TestTriggerSchemaMatchesValidation walks the descriptor against the
// validator in both directions: every key the decoder accepts is described by
// some variant, and every variant's fields are walked over a document of that
// variant.
func TestTriggerSchemaMatchesValidation(t *testing.T) {
	s := SchemaDescriptor()
	if got := names(s.TopLevel); !slices.Equal(got, yamlKeys(Definition{})) {
		t.Errorf("top level: descriptor names %v, decoder keys %v", got, yamlKeys(Definition{}))
	}
	if got := unionNames(s.Sources); !slices.Equal(got, yamlKeys(Source{})) {
		t.Errorf("source: descriptor names %v, decoder keys %v", got, yamlKeys(Source{}))
	}
	if got := unionNames(s.Actions); !slices.Equal(got, yamlKeys(Action{})) {
		t.Errorf("action: descriptor names %v, decoder keys %v", got, yamlKeys(Action{}))
	}
	if got := names(s.Limits); !slices.Equal(got, yamlKeys(Limits{})) {
		t.Errorf("limits: descriptor names %v, decoder keys %v", got, yamlKeys(Limits{}))
	}
	if got := names(s.Signature); !slices.Equal(got, yamlKeys(Signature{})) {
		t.Errorf("signature: descriptor names %v, decoder keys %v", got, yamlKeys(Signature{}))
	}
	if len(s.Sources) != len(SourceTypes()) || len(s.Actions) != len(ActionTypes()) {
		t.Fatalf("variants %d/%d, want one per source and action type", len(s.Sources), len(s.Actions))
	}

	walk(t, "", s.TopLevel, func(path string) map[string]any {
		if path == "allowed_actors" {
			return docFor(SourceGitHubIssues, ActionCreateTask)
		}
		return validDoc()
	})
	for _, v := range s.Sources {
		if errs := parseDoc(t, docFor(v.Type, ActionCreateTask)); len(errs) > 0 {
			t.Fatalf("base document for source %s is invalid: %v", v.Type, errs)
		}
		walk(t, "source.", v.Fields, func(string) map[string]any { return docFor(v.Type, ActionCreateTask) })
	}
	for _, v := range s.Actions {
		if errs := parseDoc(t, docFor(SourceCommand, v.Type)); len(errs) > 0 {
			t.Fatalf("base document for action %s is invalid: %v", v.Type, errs)
		}
		walk(t, "action.", v.Fields, func(string) map[string]any { return docFor(SourceCommand, v.Type) })
	}
	walk(t, "limits.", s.Limits, func(string) map[string]any { return validDoc() })
	walk(t, "source.signature.", s.Signature, func(string) map[string]any { return docFor(SourceHTTP, ActionCreateTask) })

	// The three values decision 19 names are marked, and nothing else.
	var marked []string
	for _, f := range s.TopLevel {
		for _, dv := range f.Dangerous {
			marked = append(marked, f.Name+"="+dv.Value)
		}
	}
	slices.Sort(marked)
	if want := []string{"enabled=true", "on_fire=create", "permission=workflow"}; !slices.Equal(marked, want) {
		t.Errorf("dangerous values %v, want %v", marked, want)
	}
}

// TestParseRefusals: each rule reports at the path a form renders it against.
func TestParseRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		val  any
		want string
	}{
		{"id must match the file", "id", "other", "id"},
		{"interval under the floor", "source.poll_interval", "500ms", "source.poll_interval"},
		{"interval not a duration", "source.poll_interval", "soon", "source.poll_interval"},
		{"empty argv", "source.command", []any{}, "source.command"},
		{"bad if template", "if", "{{ .Event.id", "if"},
		{"bad title template", "action.title", "{{ end }}", "action.title"},
		{"match on a map", "match", map[string]any{"a": map[string]any{"b": 1}}, "match.a"},
		{"negative limit", "limits.max_per_hour", -1, "limits.max_per_hour"},
		{"issue and pull", "action.github_pull", "1", "action.github_pull"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := validDoc()
			if tc.name == "issue and pull" {
				setPath(doc, "action.github_issue", "2", false)
			}
			setPath(doc, tc.path, tc.val, false)
			errs := parseDoc(t, doc)
			if !hasPath(errs, tc.want) {
				t.Errorf("errors %v, want one at %s", errs, tc.want)
			}
			for _, e := range errs {
				if e.Path == tc.want && e.Line == 0 {
					t.Errorf("error at %s has no line", e.Path)
				}
			}
		})
	}

	// An unknown key is refused by the strict decoder, with its line.
	_, errs := Parse([]byte("id: t1\ncontainer:\n  image: x\n"), "t1")
	if len(errs) != 1 || errs[0].Line == 0 || !strings.Contains(errs[0].Message, "container") {
		t.Errorf("unknown key: %v, want one located decode error naming it", errs)
	}
}
