package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// richYAML exercises every construct the graph has to draw: guards on
// ordinary steps, a condition, a parallel group, a fan_out with an inline
// lane, a named-workflow lane and an agent merge, and a loop whose body
// carries a break. Its shape is the acceptance corpus of task 017 in one
// file, which is what makes it the right fixture for the DTO's fidelity.
const richYAML = `name: rich
description: every construct
defaults:
  agent: claude
  model: sonnet
  max_retries: 2
  timeout: 30m
steps:
  - id: plan
    type: agent
    prompt: plan it
    check: go build ./...
    check_timeout: 5m
  - id: guarded
    type: command
    run: echo hi
    shell: sh
    env: {FOO: bar}
    if: '{{ eq .Fields.mode "full" }}'
    allow_failure: true
  - id: gate
    type: condition
    if: '{{ eq .Fields.mode "full" }}'
  - id: group
    type: parallel
    max_parallel: 2
    steps:
      - {id: lint, type: command, run: echo lint}
      - {id: unit, type: command, run: echo unit, if: '{{ .Fields.tests }}'}
  - id: spread
    type: fan_out
    lanes:
      - id: api
        steps:
          - {id: api_impl, type: agent, prompt: do api}
      - id: web
        workflow: other
        if: '{{ .Fields.web }}'
        fields: {area: web}
        agent: claude
        priority: 3
    merge:
      on_conflict: agent
      agent:
        id: fixup
        type: agent
        prompt: resolve it
  - id: repeat
    type: loop
    count: 3
    max_iterations: 5
    steps:
      - {id: body, type: agent, prompt: again}
      - {id: enough, type: break, if: '{{ .Steps.body.Success }}'}
`

func decodeDefinition(t *testing.T, body []byte) workflowDefinitionResponse {
	t.Helper()
	var out workflowDefinitionResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("definition not JSON: %v (%s)", err, body)
	}
	return out
}

func (h *workflowHarness) definition(t *testing.T, name string, projectID int64) (*http.Response, []byte) {
	t.Helper()
	path := "/v1/workflows/definition?name=" + name
	if projectID != 0 {
		path += "&project_id=" + strconv.FormatInt(projectID, 10)
	}
	return h.doJSON(t, http.MethodGet, path, nil)
}

func TestWorkflowDefinitionBuiltin(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.definition(t, "adhoc", 0)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	got := decodeDefinition(t, body)
	if got.Name != "adhoc" || got.Scope != "builtin" {
		t.Errorf("entry = %+v, want the built-in adhoc", got)
	}
	// A built-in has no file to open, which is exactly why the TUI cannot be
	// asked to parse one itself (decision 4).
	if got.File != "" {
		t.Errorf("file = %q, want empty for a built-in", got.File)
	}
	if got.Definition == nil {
		t.Fatalf("definition is null for a valid built-in: %s", body)
	}
	if len(got.Definition.Steps) != 1 || got.Definition.Steps[0].Type != "agent" {
		t.Errorf("steps = %+v, want one agent step", got.Definition.Steps)
	}
}

func TestWorkflowDefinitionShadowing(t *testing.T) {
	h := newWorkflowHarness(t)
	repo := testrepo.Init(t, "main")
	writeWorkflowFile(t, h.globalDir, "feature", manualYAML("feature", "global"))
	writeWorkflowFile(t, filepath.Join(repo, workflow.ProjectDirName), "feature", manualYAML("feature", "project"))
	h.reg.ReloadGlobal()

	p := h.mustCreate(t, map[string]any{"path": repo})
	id := int64(p["id"].(float64))

	_, body := h.definition(t, "feature", 0)
	global := decodeDefinition(t, body)
	if global.Scope != "global" || global.Definition == nil || global.Definition.Description != "global" {
		t.Errorf("unscoped feature = %+v, want the global copy", global)
	}

	_, body = h.definition(t, "feature", id)
	scoped := decodeDefinition(t, body)
	if scoped.Scope != "project" || scoped.Definition == nil || scoped.Definition.Description != "project" {
		t.Errorf("scoped feature = %+v, want the project copy", scoped)
	}
	if scoped.ProjectID == nil || *scoped.ProjectID != id {
		t.Errorf("project_id = %v, want %d", scoped.ProjectID, id)
	}
	// Scope and file are what tell a client which of two same-named entries
	// it was served (decision 10).
	if scoped.File == "" {
		t.Error("project entry has no file path")
	}
}

func TestWorkflowDefinitionBrokenIs200WithFindings(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "broken", "name: broken\nsteps:\n  - {id: a, type: nonsense}\n")
	h.reg.ReloadGlobal()

	resp, body := h.definition(t, "broken", 0)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a file that does not parse: %s", resp.StatusCode, body)
	}
	got := decodeDefinition(t, body)
	if got.Definition != nil {
		t.Errorf("definition = %+v, want null for a broken file", got.Definition)
	}
	if len(got.Errors) == 0 || got.Error == nil {
		t.Fatalf("broken workflow carries no findings: %s", body)
	}
}

func TestWorkflowDefinitionNotFound(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.definition(t, "nope", 0)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, body)
	}
}

func TestWorkflowDefinitionRejectsMissingName(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.doJSON(t, http.MethodGet, "/v1/workflows/definition", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 without a name: %s", resp.StatusCode, body)
	}
}

func TestWorkflowDefinitionRejectsUnknownProject(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.definition(t, "adhoc", 999)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown project: %s", resp.StatusCode, body)
	}
}

// TestWorkflowDefinitionStructure is the fidelity test: every construct
// survives the trip, nested and in source order.
func TestWorkflowDefinitionStructure(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "rich", richYAML)
	h.reg.ReloadGlobal()

	resp, body := h.definition(t, "rich", 0)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	got := decodeDefinition(t, body)
	if len(got.Errors) > 0 {
		t.Fatalf("rich workflow did not parse: %s", body)
	}
	def := got.Definition
	if def == nil {
		t.Fatalf("definition is null: %s", body)
	}

	ids := make([]string, 0, len(def.Steps))
	for _, st := range def.Steps {
		ids = append(ids, st.ID)
	}
	want := []string{"plan", "guarded", "gate", "group", "spread", "repeat"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("top-level ids = %v, want %v", ids, want)
	}

	byID := map[string]workflowStepDef{}
	for _, st := range def.Steps {
		byID[st.ID] = st
	}

	if plan := byID["plan"]; plan.Check != "go build ./..." || plan.CheckTimeout == nil || *plan.CheckTimeout != "5m0s" {
		t.Errorf("plan check = %q/%v, want the authored check and its timeout", plan.Check, plan.CheckTimeout)
	}
	if guarded := byID["guarded"]; guarded.If == "" || !guarded.AllowFailure || guarded.Env["FOO"] != "bar" {
		t.Errorf("guarded = %+v, want its guard, allow_failure and env", guarded)
	}
	if gate := byID["gate"]; gate.Type != "condition" || gate.If == "" {
		t.Errorf("gate = %+v, want a condition carrying its expression", gate)
	}

	group := byID["group"]
	if group.MaxParallel == nil || *group.MaxParallel != 2 || len(group.Steps) != 2 {
		t.Fatalf("group = %+v, want two members and max_parallel 2", group)
	}
	if group.Steps[0].ID != "lint" || group.Steps[1].ID != "unit" || group.Steps[1].If == "" {
		t.Errorf("group members = %+v, want source order with the guard kept", group.Steps)
	}

	spread := byID["spread"]
	if len(spread.Lanes) != 2 {
		t.Fatalf("lanes = %+v, want two", spread.Lanes)
	}
	if spread.Lanes[0].ID != "api" || len(spread.Lanes[0].Steps) != 1 {
		t.Errorf("inline lane = %+v, want one inline step", spread.Lanes[0])
	}
	web := spread.Lanes[1]
	if web.Workflow != "other" || web.If == "" || web.Fields["area"] != "web" ||
		web.Priority == nil || *web.Priority != 3 || web.Agent != "claude" {
		t.Errorf("named lane = %+v, want its reference and overrides", web)
	}
	if len(web.Steps) != 0 {
		t.Errorf("named lane carries steps %+v; a reference is collapsed, not expanded", web.Steps)
	}
	if spread.Merge == nil || spread.Merge.OnConflict != "agent" || spread.Merge.Agent == nil {
		t.Fatalf("merge = %+v, want the agent policy and its step", spread.Merge)
	}
	if spread.Merge.Agent.ID != "fixup" || spread.Merge.Agent.Prompt != "resolve it" {
		t.Errorf("merge agent = %+v, want the whole nested step", spread.Merge.Agent)
	}

	repeat := byID["repeat"]
	if repeat.Count == nil || *repeat.Count != 3 || repeat.MaxIterations == nil || *repeat.MaxIterations != 5 {
		t.Errorf("loop drivers = count %v, max %v, want 3 and 5", repeat.Count, repeat.MaxIterations)
	}
	if len(repeat.Steps) != 2 || repeat.Steps[1].Type != "break" || repeat.Steps[1].If == "" {
		t.Errorf("loop body = %+v, want the body and a guarded break", repeat.Steps)
	}
}

// TestWorkflowDefinitionIsAsAuthored pins decision 12: a step that inherits a
// default reports nothing, and the default is readable in its own block. The
// alternative — folding defaults into every step — would make these two
// indistinguishable and cost §8.6 its meaning.
func TestWorkflowDefinitionIsAsAuthored(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "inherit",
		"name: inherit\ndefaults:\n  agent: claude\n  model: sonnet\n  timeout: 30m\n"+
			"steps:\n  - {id: own, type: agent, prompt: p, model: opus}\n"+
			"  - {id: inherited, type: agent, prompt: p}\n")
	h.reg.ReloadGlobal()

	_, body := h.definition(t, "inherit", 0)
	def := decodeDefinition(t, body).Definition
	if def == nil {
		t.Fatalf("definition is null: %s", body)
	}
	if def.Defaults.Agent != "claude" || def.Defaults.Model != "sonnet" {
		t.Errorf("defaults = %+v, want the authored block", def.Defaults)
	}
	if def.Defaults.Timeout == nil || *def.Defaults.Timeout != "30m0s" {
		t.Errorf("defaults timeout = %v, want the string form", def.Defaults.Timeout)
	}
	if got := def.Steps[0].Model; got != "opus" {
		t.Errorf("own model = %q, want the step's own value", got)
	}
	if got := def.Steps[1].Model; got != "" {
		t.Errorf("inherited model = %q, want empty — the default must not be folded in", got)
	}
	if got := def.Steps[1].Agent; got != "" {
		t.Errorf("inherited agent = %q, want empty", got)
	}
	if def.Steps[1].Timeout != nil {
		t.Errorf("inherited timeout = %v, want nil", def.Steps[1].Timeout)
	}
}

// TestWorkflowDefinitionCoversEveryField is the guard the hand-written mapping
// needs: the DTO restates internal/workflow rather than embedding it
// (decision 4), so a field added to the parser's model is invisible on the
// wire until someone maps it. This fails when that happens, naming the field.
//
// A field deliberately left off the wire belongs in the omit set below, with
// the reason.
func TestWorkflowDefinitionCoversEveryField(t *testing.T) {
	// FieldDefinition's three unexported members are decode bookkeeping: they
	// remember the *shape* a `default:` or `values:` node had so validation
	// can report it at its own source path (task 058). A workflow that
	// reaches the wire has already passed validation, so they are always
	// zero by then and carry nothing a client could use.
	omit := map[string]map[string]string{
		"FieldDefinition": {
			"defaultShape": "decode bookkeeping, consumed by validation before the wire",
			"valuesShape":  "decode bookkeeping, consumed by validation before the wire",
			"defaultSeq":   "decode bookkeeping, consumed by validation before the wire",
		},
	}
	pairs := []struct {
		name  string
		model any
		dto   any
	}{
		{"Workflow", workflow.Workflow{}, workflowDefinition{}},
		{"FieldDefinition", workflow.FieldDefinition{}, workflowFieldResponse{}},
		{"Defaults", workflow.Defaults{}, workflowDefaults{}},
		{"Step", workflow.Step{}, workflowStepDef{}},
		{"Lane", workflow.Lane{}, workflowLaneDef{}},
		{"Merge", workflow.Merge{}, workflowMergeDef{}},
	}
	for _, p := range pairs {
		mapped := map[string]bool{}
		dt := reflect.TypeOf(p.dto)
		for i := range dt.NumField() {
			mapped[dt.Field(i).Name] = true
		}
		mt := reflect.TypeOf(p.model)
		for i := range mt.NumField() {
			f := mt.Field(i).Name
			if reason, ok := omit[p.name][f]; ok {
				t.Logf("%s.%s deliberately off the wire: %s", p.name, f, reason)
				continue
			}
			if !mapped[f] {
				t.Errorf("workflow.%s.%s has no counterpart in the API DTO — map it, "+
					"or record why it stays off the wire", p.name, f)
			}
		}
	}
}

// TestGateCorpusIsServable holds the acceptance corpus honest. The task 017
// gate is a manual walkthrough of `docs/gates/corpus/*.yaml`, and a walkthrough
// whose fixtures no longer parse is worse than no walkthrough: it fails at the
// first step for a reason that has nothing to do with what is being judged.
//
// This loads them through the real registry and serves each one, which also
// exercises every construct the graph draws against the endpoint at once.
func TestGateCorpusIsServable(t *testing.T) {
	corpus := filepath.Join("..", "..", "docs", "gates", "corpus")
	files, err := os.ReadDir(corpus)
	if err != nil {
		t.Fatalf("read the gate corpus: %v", err)
	}
	h := newWorkflowHarness(t)
	names := make([]string, 0, len(files))
	for _, f := range files {
		if filepath.Ext(f.Name()) != ".yaml" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(corpus, f.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", f.Name(), err)
		}
		name := strings.TrimSuffix(f.Name(), ".yaml")
		writeWorkflowFile(t, h.globalDir, name, string(src))
		names = append(names, name)
	}
	if len(names) < 11 {
		t.Fatalf("the corpus has %d workflows; the gate documents eleven", len(names))
	}
	h.reg.ReloadGlobal()

	for _, name := range names {
		resp, body := h.definition(t, name, 0)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, body %s", name, resp.StatusCode, body)
			continue
		}
		got := decodeDefinition(t, body)
		if len(got.Errors) > 0 {
			t.Errorf("%s does not parse — the gate would fail before it began: %v", name, got.Errors)
			continue
		}
		if got.Definition == nil || len(got.Definition.Steps) == 0 {
			t.Errorf("%s served no steps", name)
		}
	}
}
