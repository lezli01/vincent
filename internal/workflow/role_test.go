package workflow

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

const handoffYAML = `name: integrate
role: handoff
steps:
  - id: per-issue
    type: fan_out
    for_each: ['{{ .Handoff.ItemLines }}']
    lane:
      id: 'issue-{{ .Item.issue.id }}'
      fields: {source_branch: '{{ .Item.branch }}'}
      steps:
        - {id: open, type: command, run: 'git status'}
`

// role: handoff parses; any other value, and a non-string, is refused at
// load (task 137.8).
func TestRoleValidation(t *testing.T) {
	wf, _, err := Parse([]byte(handoffYAML), Options{})
	if err != nil {
		t.Fatalf("Parse(role: handoff) = %v", err)
	}
	if wf.Role != RoleHandoff || !wf.IsHandoff() {
		t.Errorf("Role = %q, IsHandoff = %v", wf.Role, wf.IsHandoff())
	}
	ordinary, _, err := Parse([]byte("name: plain\nsteps:\n  - {id: a, type: command, run: 'git status'}\n"), Options{})
	if err != nil || ordinary.IsHandoff() {
		t.Fatalf("ordinary workflow: IsHandoff = %v, err = %v", ordinary.IsHandoff(), err)
	}
	for name, role := range map[string]string{"unknown": "integrator", "case": "Handoff", "map": "{a: b}", "list": "[handoff]"} {
		src := "name: x\nrole: " + role + "\nsteps:\n  - {id: a, type: command, run: 'git status'}\n"
		if _, _, err := Parse([]byte(src), Options{}); err == nil {
			t.Errorf("%s: role %s parsed, want a validation error", name, role)
		}
	}
}

// The schema descriptor offers the key, as the enum it is.
func TestSchemaOffersRole(t *testing.T) {
	for _, f := range SchemaDescriptor().TopLevel {
		if f.Name == "role" {
			if f.Control != ControlEnum || !slices.Equal(f.Values, []string{RoleHandoff}) {
				t.Errorf("role = %+v", f)
			}
			return
		}
	}
	t.Fatal("the schema descriptor has no top-level role")
}

// A role belongs to the including workflow: an included callee contributes
// steps, never its role (§7.9).
func TestIncludedRoleContributesNothing(t *testing.T) {
	callee, _, err := Parse([]byte(handoffYAML), Options{})
	if err != nil {
		t.Fatal(err)
	}
	caller, _, err := Parse([]byte("name: outer\nsteps:\n  - {id: inc, type: include, workflow: integrate}\n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Expand(caller, ExpandOptions{
		Lookup: func(name string) (*Workflow, bool) { return callee, name == "integrate" },
		Limits: IncludeLimits{MaxDepth: 3},
	})
	if err != nil {
		t.Fatalf("Expand = %v", err)
	}
	if out.Role != "" {
		t.Errorf("expanded Role = %q, want empty", out.Role)
	}
}

// A fan_out lane naming a role: handoff workflow is a creation-time
// TreeError (decision 3).
func TestLaneNamingHandoffWorkflowIsATreeError(t *testing.T) {
	target, _, err := Parse([]byte(handoffYAML), Options{})
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := Parse([]byte("name: root\nsteps:\n  - id: fan\n    type: fan_out\n    lanes:\n      - {id: a, workflow: integrate}\n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ResolveTree(root, func(string) (*Workflow, bool) { return target, true }, Limits{MaxDepth: 3, MaxTasks: 10})
	var te *TreeError
	if !errors.As(err, &te) || !strings.Contains(te.Message, "role: handoff") {
		t.Fatalf("ResolveTree = %v, want a TreeError naming the role", err)
	}
}

const handoffFileJSON = `{
  "id": 7,
  "grant": ["issue.comment"],
  "items": [
    {"issue": {"number": 41, "title": "first", "body": "b", "url": "https://x/41", "state": "OPEN",
      "labels": [{"name": "bug"}], "author": {"login": "ann"}, "comments": [], "id": 3, "kind": "bug",
      "priority": 1, "source": {"provider": "github", "repo": "o/r", "number": 41, "url": "https://x/41"}},
     "delivery": {"kind": "change", "title": "t", "head_sha": "abc", "branch": "vincent/1-a", "base": "main", "ready": true},
     "task_id": 11, "branch": "vincent/1-a", "head_sha": "abc"},
    {"issue": {"number": null, "title": "second", "state": "OPEN", "labels": [], "author": {"login": ""},
      "comments": [], "id": 4, "kind": "", "priority": 0, "source": null},
     "delivery": {"kind": "change", "head_sha": "def", "branch": "vincent/2-b", "ready": true},
     "task_id": 12, "branch": "vincent/2-b", "head_sha": "def"}
  ]
}`

// ParseHandoffFile binds the file as `.Handoff`: the typed items, and one
// ItemLines line per element, each the element compacted.
func TestParseHandoffFile(t *testing.T) {
	hc, err := ParseHandoffFile([]byte(handoffFileJSON))
	if err != nil {
		t.Fatal(err)
	}
	if hc.ID != 7 || len(hc.Items) != 2 || !slices.Equal(hc.Grant, []string{"issue.comment"}) {
		t.Fatalf("handoff = %+v", hc)
	}
	first := hc.Items[0]
	if first.Issue.Number != 3 || first.Issue.Source.Number != 41 || first.Issue.State != "open" ||
		!slices.Equal(first.Issue.Labels, []string{"bug"}) || first.TaskID != 11 ||
		first.Branch != "vincent/1-a" || first.Delivery.Kind != "change" || !first.Delivery.Ready {
		t.Errorf("first item = %+v", first)
	}
	lines := strings.Split(hc.ItemLines, "\n")
	if len(lines) != 2 {
		t.Fatalf("ItemLines = %q, want two lines", hc.ItemLines)
	}
	var f struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(handoffFileJSON), &f); err != nil {
		t.Fatal(err)
	}
	for i, line := range lines {
		var got, want any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		_ = json.Unmarshal(f.Items[i], &want)
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		if string(gb) != string(wb) {
			t.Errorf("line %d = %s, want the file's element %s", i, gb, wb)
		}
	}
}

// `{{ if .Handoff.ID }}` renders on both a handoff and an ordinary task, and
// VINCENT_HANDOFF_FILE is exported only when the file was written.
func TestHandoffRenderAndEnv(t *testing.T) {
	const text = `{{ if .Handoff.ID }}{{ range .Handoff.Items }}{{ .Issue.Title }};{{ end }}{{ else }}none{{ end }}`
	got, err := Render("prompt", text, RenderContext{})
	if err != nil || got != "none" {
		t.Fatalf("ordinary render = %q, %v", got, err)
	}
	hc, err := ParseHandoffFile([]byte(handoffFileJSON))
	if err != nil {
		t.Fatal(err)
	}
	got, err = Render("prompt", text, RenderContext{Handoff: hc})
	if err != nil || got != "first;second;" {
		t.Fatalf("handoff render = %q, %v", got, err)
	}
	for _, kv := range Env(RenderContext{}) {
		if strings.HasPrefix(kv, "VINCENT_HANDOFF_FILE=") {
			t.Errorf("ordinary task exports %s", kv)
		}
	}
	env := Env(RenderContext{HandoffEnv: HandoffEnv{File: "/g/vincent-handoff.json"}})
	if !slices.Contains(env, "VINCENT_HANDOFF_FILE=/g/vincent-handoff.json") {
		t.Errorf("handoff task env = %v", env)
	}
	// Each line is one `.Item` of a derived lane.
	var item map[string]any
	if err := json.Unmarshal([]byte(strings.Split(hc.ItemLines, "\n")[0]), &item); err != nil {
		t.Fatal(err)
	}
	id, err := RenderLane("lane.id", `issue-{{ .Item.issue.id }}@{{ .Item.branch }}`, RenderContext{Handoff: hc}, item)
	if err != nil || id != "issue-3@vincent/1-a" {
		t.Errorf("lane id = %q, %v", id, err)
	}
}
