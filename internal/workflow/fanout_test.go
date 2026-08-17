package workflow

import (
	"strings"
	"testing"
)

// registry builds a LookupFunc over parsed sources, standing in for the real
// registry's shadowing.
func registry(t *testing.T, sources map[string]string) LookupFunc {
	t.Helper()
	parsed := map[string]*Workflow{}
	for name, src := range sources {
		wf, _, err := Parse([]byte(src), Options{})
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed[name] = wf
	}
	return func(name string) (*Workflow, bool) {
		wf, ok := parsed[name]
		return wf, ok
	}
}

func mustParse(t *testing.T, src string) *Workflow {
	t.Helper()
	wf, _, err := Parse([]byte(src), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return wf
}

const rootFanOut = `
name: root
steps:
  - id: build
    type: fan_out
    lanes:
      - { id: api,  workflow: module }
      - { id: docs, steps: [{ id: write, type: command, run: make docs }] }
`

// TestResolveTreeInlinesNamedLanes is decision 4: the registry is read once,
// at creation, and what it said is written into the snapshot. The lane's
// workflow name moves to resolved_from — it is still the child's
// workflow_name — because a lane carrying both a name and steps would not
// re-parse.
func TestResolveTreeInlinesNamedLanes(t *testing.T) {
	lookup := registry(t, map[string]string{
		"module": "name: module\nsteps:\n  - {id: impl, type: command, run: make}\n",
	})
	got, tasks, err := ResolveTree(mustParse(t, rootFanOut), lookup, Limits{MaxDepth: 3, MaxTasks: 64})
	if err != nil {
		t.Fatalf("ResolveTree: %v", err)
	}
	if tasks != 2 {
		t.Errorf("descendants = %d, want 2", tasks)
	}
	api := got.Steps[0].Lanes[0]
	if api.ResolvedFrom != "module" || api.Workflow != "" {
		t.Errorf("lane provenance = resolved_from %q / workflow %q, want the name moved aside "+
			"so the snapshot still parses", api.ResolvedFrom, api.Workflow)
	}
	if len(api.Steps) != 1 || api.Steps[0].ID != "impl" {
		t.Errorf("named lane steps = %+v, want the registry's inlined", api.Steps)
	}
	// The inline lane is untouched, and the original workflow is not mutated.
	if got.Steps[0].Lanes[1].Steps[0].ID != "write" {
		t.Error("the inline lane lost its steps")
	}
	if src := mustParse(t, rootFanOut); len(src.Steps[0].Lanes[0].Steps) != 0 {
		t.Error("ResolveTree mutated its input")
	}
}

// TestResolveTreeDetectsCycles: A naming B while B names A is an infinite
// spawn, and the error names the path — "there is a cycle" would send the
// reader to grep every workflow file they own.
func TestResolveTreeDetectsCycles(t *testing.T) {
	lookup := registry(t, map[string]string{
		"verify": "name: verify\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: build}]}\n",
		"build":  "name: build\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: verify}]}\n",
	})
	root := mustParse(t, "name: build\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: verify}]}\n")
	_, _, err := ResolveTree(root, lookup, Limits{MaxDepth: 8, MaxTasks: 64})
	if err == nil {
		t.Fatal("ResolveTree accepted a cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error = %q, want it to say cycle", err)
	}
	for _, name := range []string{"build", "verify"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error = %q, want the path to name %q", err, name)
		}
	}
}

// TestResolveTreeEnforcesMaxDepth: depth is unlimited by design and bounded
// by a config default, so the bound is what reports, naming the key to edit.
func TestResolveTreeEnforcesMaxDepth(t *testing.T) {
	lookup := registry(t, map[string]string{
		"a": "name: a\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: b}]}\n",
		"b": "name: b\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: c}]}\n",
		"c": "name: c\nsteps:\n  - {id: s, type: command, run: make}\n",
	})
	root := mustParse(t, "name: root\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: a}]}\n")

	if _, _, err := ResolveTree(root, lookup, Limits{MaxDepth: 3, MaxTasks: 64}); err != nil {
		t.Fatalf("depth 3 refused at max_depth 3: %v", err)
	}
	_, _, err := ResolveTree(root, lookup, Limits{MaxDepth: 2, MaxTasks: 64})
	if err == nil {
		t.Fatal("ResolveTree accepted a tree past max_depth")
	}
	if !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("error = %q, want it to name fan_out.max_depth", err)
	}
}

// TestResolveTreeEnforcesMaxTasks counts descendants only (decision 28): the
// bound caps what a fan-out creates, and counting the root would put the
// number off by one against the sentence explaining it.
func TestResolveTreeEnforcesMaxTasks(t *testing.T) {
	src := "name: root\nsteps:\n  - id: f\n    type: fan_out\n    lanes:\n"
	for _, id := range []string{"a", "b", "c"} {
		src += "      - {id: " + id + ", steps: [{id: s" + id + ", type: command, run: make}]}\n"
	}
	root := mustParse(t, src)

	if _, tasks, err := ResolveTree(root, nil, Limits{MaxDepth: 3, MaxTasks: 3}); err != nil || tasks != 3 {
		t.Fatalf("three lanes at max_tasks 3: tasks=%d err=%v", tasks, err)
	}
	_, _, err := ResolveTree(root, nil, Limits{MaxDepth: 3, MaxTasks: 2})
	if err == nil {
		t.Fatal("ResolveTree accepted more lanes than max_tasks")
	}
	if !strings.Contains(err.Error(), "max_tasks") {
		t.Errorf("error = %q, want it to name fan_out.max_tasks", err)
	}
}

// TestResolveTreeUnknownLaneWorkflow: a lane naming a workflow this project
// cannot see is a 400 at creation, not a failure hours later.
func TestResolveTreeUnknownLaneWorkflow(t *testing.T) {
	root := mustParse(t, "name: root\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: nope}]}\n")
	_, _, err := ResolveTree(root, registry(t, nil), Limits{MaxDepth: 3, MaxTasks: 64})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found naming the lane workflow", err)
	}
	if !strings.Contains(err.Error(), "lanes[0]") {
		t.Errorf("err = %q, want the offending lane's path", err)
	}
}

// TestLaneWorkflowNameCannotCollide: an inline lane's child needs a
// workflow_name for a NOT NULL column, and the synthetic one is safe because
// validation rejects `/` in a real workflow name.
func TestLaneWorkflowNameCannotCollide(t *testing.T) {
	name := LaneWorkflowName(Lane{ID: "api"}, "root", "build")
	if name != "root/build/api" {
		t.Errorf("synthetic name = %q, want root/build/api", name)
	}
	// A resolved lane carries the registry name it came from instead.
	if got := LaneWorkflowName(Lane{ID: "api", ResolvedFrom: "module"}, "root", "build"); got != "module" {
		t.Errorf("resolved lane name = %q, want module", got)
	}
	src := "name: " + name + "\nsteps:\n  - {id: s, type: command, run: make}\n"
	if _, _, err := Parse([]byte(src), Options{}); err == nil {
		t.Error("a workflow could be named root/build/api; the synthetic name can collide")
	}
}

// TestLaneCycleWarningsAreWarnings: at registry load a cycle is advisory,
// because it is real only once a task picks a root and shadowing decides
// which files those are.
func TestLaneCycleWarningsAreWarnings(t *testing.T) {
	lookup := registry(t, map[string]string{
		"b": "name: b\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: a}]}\n",
	})
	a := mustParse(t, "name: a\nsteps:\n  - {id: f, type: fan_out, lanes: [{id: l, workflow: b}]}\n")
	warns := LaneCycleWarnings(a, lookup)
	if len(warns) != 1 || !strings.Contains(warns[0], "cycle") {
		t.Errorf("warnings = %v, want one naming a cycle", warns)
	}
	// A tree with no cycle warns about nothing, or the log stops meaning
	// anything.
	clean := mustParse(t, "name: c\nsteps:\n  - {id: s, type: command, run: make}\n")
	if w := LaneCycleWarnings(clean, lookup); len(w) != 0 {
		t.Errorf("warnings = %v for an acyclic workflow, want none", w)
	}
}
