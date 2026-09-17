package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/workflow"
)

// runWorkflowCLI runs one `vincent workflow …` invocation in-process against
// isolated directories, returning its combined output and exit code. Nothing
// here starts a daemon: `render` with no flags must work on a CI runner with
// no daemon and no agent CLI, which is the property under test.
func runWorkflowCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv(config.EnvDataDir, t.TempDir())

	var buf bytes.Buffer
	root := newRootCmd()
	root.SilenceErrors = true
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"workflow"}, args...))
	// Execute before reading the buffer: operands of one return statement are
	// evaluated left to right, so `return buf.String(), asExitCode(...)`
	// reports an empty buffer.
	code := asExitCode(root.ExecuteContext(context.Background()))
	return buf.String(), code
}

// writeWorkflow drops a workflow file in a temp dir and returns its path.
func writeWorkflow(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wf.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return path
}

// TestRenderCatchesWhatValidateCannot is issue #93's acceptance, both halves:
// a typo'd struct field and an unsupplied task field pass `validate` and are
// caught by `render`, naming the step and the reference — with no daemon
// running and no task ever created.
func TestRenderCatchesWhatValidateCannot(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "typo'd struct field",
			body: workflowWith(`  - id: plan
    type: agent
    prompt: "Do {{.Task.Titel}}"`),
			want: "Titel",
		},
		{
			name: "undeclared task field",
			body: workflowWith(`  - id: plan
    type: agent
    prompt: "Do {{.Task.Fields.ticket}}"`),
			want: "ticket",
		},
		{
			name: "typo'd result field on a prior step",
			body: workflowWith(`  - id: plan
    type: agent
    prompt: "p"
  - id: use
    type: command
    run: "echo {{.Steps.plan.Reslt}}"`),
			want: "Reslt",
		},
		{
			name: "unknown step id",
			body: workflowWith(`  - id: plan
    type: agent
    prompt: "p"
  - id: use
    type: command
    run: "echo {{.Steps.pln.Result}}"`),
			want: "pln",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := writeWorkflow(t, tc.body)

			out, code := runWorkflowCLI(t, "validate", file)
			if code != 0 {
				t.Fatalf("validate rejected the file (code %d): %s", code, out)
			}

			out, code = runWorkflowCLI(t, "render", file)
			if code != 1 {
				t.Fatalf("render exit code = %d, want 1: %s", code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("render output does not name %q: %s", tc.want, out)
			}
			if !strings.Contains(out, "step ") {
				t.Errorf("render output does not name the step: %s", out)
			}
		})
	}
}

// TestRenderFieldFlagSatisfiesAReference: supplying the value a real task
// would carry renders the same workflow clean, which is the correct verdict
// either way — a real task without it would fail too.
func TestRenderFieldFlagSatisfiesAReference(t *testing.T) {
	file := writeWorkflow(t, workflowWith(`  - id: plan
    type: agent
    prompt: "Fix {{.Task.Fields.ticket}}"`))

	out, code := runWorkflowCLI(t, "render", file, "--field", "ticket=ABC-1")
	if code != 0 {
		t.Fatalf("render --field exit code = %d, want 0: %s", code, out)
	}
	if !strings.Contains(out, "Fix ABC-1") {
		t.Errorf("rendered prompt does not carry the supplied field: %s", out)
	}
}

// TestRenderDeclaredRequiredFieldBinds: a workflow that declares `ticket`
// required and reads it non-defensively renders clean unflagged, because
// POST /v1/tasks guarantees a real task carries it. An optional declared
// field read the same way is still an error.
func TestRenderDeclaredRequiredFieldBinds(t *testing.T) {
	required := writeWorkflow(t, `name: demo
fields:
  - name: ticket
    required: true
steps:
  - id: plan
    type: agent
    prompt: "Fix {{.Task.Fields.ticket}}"
`)
	out, code := runWorkflowCLI(t, "render", required)
	if code != 0 {
		t.Fatalf("required field: exit code = %d, want 0: %s", code, out)
	}
	if !strings.Contains(out, "<field.ticket>") {
		t.Errorf("required field did not bind to its sentinel: %s", out)
	}

	optional := writeWorkflow(t, `name: demo
fields:
  - name: note
steps:
  - id: plan
    type: agent
    prompt: "Note {{.Task.Fields.note}}"
`)
	if out, code := runWorkflowCLI(t, "render", optional); code != 1 {
		t.Fatalf("optional field read non-defensively: exit code = %d, want 1: %s", code, out)
	}
}

// TestRenderGuardIsShownNotJudged: a guard is rendered and reported, and a
// non-boolean result under preview is a warning rather than a failure — a
// sentinel can legitimately make a guard non-boolean.
func TestRenderGuardIsShownNotJudged(t *testing.T) {
	file := writeWorkflow(t, workflowWith(`  - id: plan
    type: agent
    prompt: "p"
  - id: guarded
    type: command
    if: "{{ .Steps.plan.Result }}"
    run: "echo hi"`))

	out, code := runWorkflowCLI(t, "render", file)
	if code != 0 {
		t.Fatalf("non-boolean guard exit code = %d, want 0: %s", code, out)
	}
	if !strings.Contains(out, "warning:") {
		t.Errorf("non-boolean guard did not warn: %s", out)
	}
	if !strings.Contains(out, "<steps.plan.result>") {
		t.Errorf("guard output was not shown: %s", out)
	}
}

// TestRenderUnresolvedIncludeIsReported: offline there is no registry, so an
// include is reported with a pointer to --project — and every other step
// still renders, exit 0.
func TestRenderUnresolvedIncludeIsReported(t *testing.T) {
	file := writeWorkflow(t, workflowWith(`  - id: shared
    type: include
    workflow: checks
  - id: plan
    type: agent
    prompt: "Do {{.Task.Title}}"`))

	out, code := runWorkflowCLI(t, "render", file)
	if code != 0 {
		t.Fatalf("unresolved include exit code = %d, want 0: %s", code, out)
	}
	if !strings.Contains(out, "unresolved:") || !strings.Contains(out, "--project") {
		t.Errorf("include was not reported with a pointer to --project: %s", out)
	}
	if !strings.Contains(out, "Do <task.title>") {
		t.Errorf("the other steps did not render: %s", out)
	}
}

// TestRenderJSONShape: --json emits the rendered bodies and the §8.6 triple
// with the level that supplied each field, which is what a script wraps.
func TestRenderJSONShape(t *testing.T) {
	file := writeWorkflow(t, `name: demo
defaults:
  agent: codex
steps:
  - id: plan
    type: agent
    model: gpt-5
    prompt: "Do {{.Task.Title}}"
`)
	out, code := runWorkflowCLI(t, "render", file, "--json")
	if code != 0 {
		t.Fatalf("render --json exit code = %d, want 0: %s", code, out)
	}
	var got renderResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json is not JSON: %v (%s)", err, out)
	}
	if !got.OK || got.Name != "demo" || len(got.Steps) != 1 {
		t.Fatalf("unexpected result: %+v", got)
	}
	step := got.Steps[0]
	if step.Selection == nil {
		t.Fatal("agent step carries no selection")
	}
	if step.Selection.Agent.Value != "codex" || step.Selection.Agent.Source != "workflow" {
		t.Errorf("agent = %+v, want codex from the workflow defaults", step.Selection.Agent)
	}
	if step.Selection.Model.Value != "gpt-5" || step.Selection.Model.Source != "step" {
		t.Errorf("model = %+v, want gpt-5 from the step", step.Selection.Model)
	}
	if len(step.Fields) != 1 || !strings.Contains(step.Fields[0].Output, "<task.title>") {
		t.Errorf("fields = %+v, want the rendered prompt", step.Fields)
	}
}

// TestRenderOverrideFlags: --agent/--model/--effort fill §8.6 level 2, which
// is otherwise unpreviewable offline.
func TestRenderOverrideFlags(t *testing.T) {
	file := writeWorkflow(t, workflowWith(`  - id: plan
    type: agent
    prompt: "p"`))
	out, code := runWorkflowCLI(t, "render", file, "--json", "--agent", "codex", "--effort", "high")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, out)
	}
	var got renderResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json is not JSON: %v (%s)", err, out)
	}
	sel := got.Steps[0].Selection
	if sel.Agent.Value != "codex" || sel.Agent.Source != "task" {
		t.Errorf("agent = %+v, want codex from the task level", sel.Agent)
	}
	if sel.Effort.Value != "high" || sel.Effort.Source != "task" {
		t.Errorf("effort = %+v, want high from the task level", sel.Effort)
	}
}

// TestRenderExitCodes pins the `docs/reference/cli.md` contract: 1 is a file
// judged bad, 2 is a daemon that never answered.
func TestRenderExitCodes(t *testing.T) {
	good := writeWorkflow(t, workflowWith(`  - id: plan
    type: agent
    prompt: "Do {{.Task.Title}}"`))
	if out, code := runWorkflowCLI(t, "render", good); code != 0 {
		t.Fatalf("clean render exit code = %d, want 0: %s", code, out)
	}

	// --task with no daemon: exit 2, not 1. The file was never judged.
	if out, code := runWorkflowCLI(t, "render", good, "--task", "1"); code != 2 {
		t.Fatalf("--task with no daemon: exit code = %d, want 2: %s", code, out)
	}

	invalid := writeWorkflow(t, "name: demo\nsteps:\n  - id: plan\n    type: nonsense\n")
	if out, code := runWorkflowCLI(t, "render", invalid); code != 1 {
		t.Fatalf("invalid file exit code = %d, want 1: %s", code, out)
	}
}

// derivedFanOut is a derived fan-out (§7.6, task 080) whose lane template
// carries laneIf as its guard and runBody as its one inline step's `run:`,
// with every field a derived step may carry — `max_lanes`, `schedule: eager`
// and a templated `needs:` — so a render that trips over any of them shows.
func derivedFanOut(laneIf, runBody string) string {
	return workflowWith(`  - id: plan
    type: agent
    prompt: "Plan {{.Task.Title}}"
  - id: build
    type: fan_out
    max_lanes: 8
    schedule: eager
    for_each: '{{ .Steps.plan.Result }}'
    lane:
      id: '{{ .Item.id }}'
      needs: '{{ .Item.needs }}'
      if: '` + laneIf + `'
      steps:
        - id: implement
          type: command
          run: "` + runBody + `"`)
}

// TestRenderDerivedFanOutLane is issue #370 on the file path: a derived
// lane's template is part of the file, so its `run:` body and its `if:` are
// rendered like any declared lane's — a typo in either is exit 1 naming it,
// and a clean one prints what the lane's step would run.
func TestRenderDerivedFanOutLane(t *testing.T) {
	t.Run("typo in the lane's step", func(t *testing.T) {
		file := writeWorkflow(t, derivedFanOut("true", "make {{ .Task.Titel }}"))
		out, code := runWorkflowCLI(t, "render", file)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 for a typo in the derived lane's run body: %s", code, out)
		}
		if !strings.Contains(out, "implement") || !strings.Contains(out, "Titel") {
			t.Errorf("the error does not name the lane step and the reference: %s", out)
		}
	})

	t.Run("typo in the lane's guard", func(t *testing.T) {
		file := writeWorkflow(t, derivedFanOut("{{ .Task.Titel }}", "make {{ .Task.Title }}"))
		out, code := runWorkflowCLI(t, "render", file)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 for a typo in the derived lane's if: %s", code, out)
		}
		if !strings.Contains(out, "Titel") {
			t.Errorf("the error does not name the reference: %s", out)
		}
	})

	t.Run("clean", func(t *testing.T) {
		file := writeWorkflow(t, derivedFanOut("true", "make {{ .Task.Title }}"))
		out, code := runWorkflowCLI(t, "render", file, "--json")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0: %s", code, out)
		}
		var got renderResult
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("--json is not JSON: %v (%s)", err, out)
		}
		var rendered bool
		for _, s := range got.Steps {
			if s.ID != "implement" {
				continue
			}
			for _, f := range s.Fields {
				if f.Field == "run" && strings.Contains(f.Output, "make <task.title>") {
					rendered = true
				}
			}
		}
		if !rendered {
			t.Errorf("the derived lane's run body was never rendered: %+v", got.Steps)
		}
	})
}

// TestWorkflowFromDefinitionCarriesDerivedFanOut is issue #370 on the
// `--project` path: a registry callee comes back through the §13.2
// definition DTO, which carries `lane`, `max_lanes`, `schedule` and a lane's
// `needs`, and the mapping back to the parser's model must not drop them — a
// derived fan-out without its `lane:` is a fan_out with nothing to render.
func TestWorkflowFromDefinitionCarriesDerivedFanOut(t *testing.T) {
	maxLanes := 8
	body := &apiclient.WorkflowBody{
		Name: "callee",
		Steps: []apiclient.WorkflowStepDef{
			{
				ID: "build", Type: workflow.StepFanOut, MaxLanes: &maxLanes, Schedule: workflow.ScheduleEager,
				ForEach: []string{"{{ .Steps.plan.Result }}"},
				Lane: &apiclient.WorkflowLaneDef{
					ID: "{{ .Item.id }}", Needs: []string{"{{ .Item.needs }}"},
					Steps: []apiclient.WorkflowStepDef{{ID: "implement", Type: workflow.StepCommand, Run: "make"}},
				},
			},
			{
				ID: "declared", Type: workflow.StepFanOut,
				Lanes: []apiclient.WorkflowLaneDef{
					{ID: "api", Steps: []apiclient.WorkflowStepDef{{ID: "a", Type: workflow.StepCommand, Run: "x"}}},
					{ID: "docs", Needs: []string{"api"}, Steps: []apiclient.WorkflowStepDef{{ID: "d", Type: workflow.StepCommand, Run: "x"}}},
				},
			},
		},
	}

	wf := workflowFromDefinition(body)
	if len(wf.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(wf.Steps))
	}
	derived := wf.Steps[0]
	if derived.Lane == nil {
		t.Fatal("the derived fan-out's lane template was dropped")
	}
	if derived.Lane.ID != "{{ .Item.id }}" || len(derived.Lane.Steps) != 1 ||
		strings.Join(derived.Lane.Needs, ",") != "{{ .Item.needs }}" {
		t.Errorf("lane template = %+v, want its id, needs and inline step carried", *derived.Lane)
	}
	if derived.MaxLanes == nil || *derived.MaxLanes != 8 {
		t.Errorf("max_lanes = %v, want 8", derived.MaxLanes)
	}
	if derived.Schedule != workflow.ScheduleEager {
		t.Errorf("schedule = %q, want %q", derived.Schedule, workflow.ScheduleEager)
	}
	if lanes := wf.Steps[1].Lanes; len(lanes) != 2 || strings.Join(lanes[1].Needs, ",") != "api" {
		t.Errorf("declared lanes = %+v, want docs to need api", lanes)
	}
}

// renderJSON runs `render --json` on file, which must exit 0, and decodes it.
func renderJSON(t *testing.T, file string) renderResult {
	t.Helper()
	out, code := runWorkflowCLI(t, "render", file, "--json")
	if code != 0 {
		t.Fatalf("render --json exit code = %d, want 0: %s", code, out)
	}
	var got renderResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json is not JSON: %v (%s)", err, out)
	}
	return got
}

// stepAt returns the rendered row at path, failing the test when there is none.
func stepAt(t *testing.T, res renderResult, path string) renderStep {
	t.Helper()
	for _, s := range res.Steps {
		if s.Path == path {
			return s
		}
	}
	t.Fatalf("no rendered row at %s: %+v", path, res.Steps)
	return renderStep{}
}

// laneSummary flattens a lane graph to `id/wave/needs/guarded` terms, so one
// comparison pins every fact the block draws.
func laneSummary(lanes []renderLane) string {
	terms := make([]string, 0, len(lanes))
	for _, l := range lanes {
		term := l.ID + "/" + strconv.Itoa(l.Wave) + "/" + strings.Join(l.Needs, "+")
		if l.Guarded {
			term += "/guarded"
		}
		terms = append(terms, term)
	}
	return strings.Join(terms, " ")
}

// TestRenderDrawsLaneDAG is issue #407's acceptance on corpus entry 12 — task
// 080's `needs:` graph as task 084 fixed it: the fan-out's own row names its
// waves, the edges and `eager`, and the rows beneath it are unchanged.
func TestRenderDrawsLaneDAG(t *testing.T) {
	file := filepath.Join("..", "..", "docs", "gates", "corpus", "lanedag.yaml")

	out, code := runWorkflowCLI(t, "render", file)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, out)
	}
	want := "steps[1] spread (fan_out)\n" +
		"  schedule: eager\n" +
		"  lanes:\n" +
		"    wave 1: api, db\n" +
		"    wave 2: wire (needs api, db)\n" +
		"steps[1].lanes[0].steps[0] api_impl (agent)\n"
	if !strings.Contains(out, want) {
		t.Errorf("the fan-out row does not draw its graph; want\n%s\nin\n%s", want, out)
	}
	if !strings.Contains(out, "6 step(s) rendered") {
		t.Errorf("the lane block changed the step count: %s", out)
	}

	spread := stepAt(t, renderJSON(t, file), "steps[1]")
	if spread.Schedule != workflow.ScheduleEager {
		t.Errorf("schedule = %q, want %q", spread.Schedule, workflow.ScheduleEager)
	}
	if got, want := laneSummary(spread.Lanes), "api/1/ db/1/ wire/2/api+db"; got != want {
		t.Errorf("lanes = %q, want %q", got, want)
	}
}

// TestRenderDerivedLaneListHasUnknownWidth: a `lane:` template's list is as
// wide as a run discovers, so it draws one label and no waves (task 044
// decision 10's per-item pass stays deferred).
func TestRenderDerivedLaneListHasUnknownWidth(t *testing.T) {
	file := writeWorkflow(t, derivedFanOut("true", "make {{ .Task.Title }}"))

	out, code := runWorkflowCLI(t, "render", file)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, out)
	}
	want := "steps[1] build (fan_out)\n" +
		"  schedule: eager\n" +
		"  lanes:\n" +
		"    <derived lane>: unknown width, at most 8, one per item of {{ .Steps.plan.Result }}\n"
	if !strings.Contains(out, want) {
		t.Errorf("want\n%s\nin\n%s", want, out)
	}
	if strings.Contains(out, "wave ") || strings.Contains(out, "runs as barrier") {
		t.Errorf("a derived list drew a wave or judged its flatness: %s", out)
	}

	build := stepAt(t, renderJSON(t, file), "steps[1]")
	if build.Schedule != workflow.ScheduleEager || build.MaxLanes == nil || *build.MaxLanes != 8 {
		t.Errorf("schedule = %q, max_lanes = %v; want eager and 8", build.Schedule, build.MaxLanes)
	}
	if len(build.Lanes) != 1 {
		t.Fatalf("lanes = %+v, want the single derived entry", build.Lanes)
	}
	lane := build.Lanes[0]
	if lane.ID != workflow.SentinelLane || !lane.Derived || lane.Wave != 0 ||
		strings.Join(lane.ForEach, ",") != "{{ .Steps.plan.Result }}" {
		t.Errorf("derived entry = %+v, want %q, derived, no wave, the raw for_each", lane, workflow.SentinelLane)
	}
}

// TestRenderFlatLaneList: `eager` on a list no lane orders says it runs as
// barrier (task 081 decision 4), and an unnamed schedule draws no badge while
// --json still resolves it (task 084 decision 9).
func TestRenderFlatLaneList(t *testing.T) {
	flat := func(schedule string) string {
		return workflowWith(`  - id: spread
    type: fan_out` + schedule + `
    lanes:
      - {id: a, steps: [{id: as, type: command, run: a}]}
      - {id: b, steps: [{id: bs, type: command, run: b}]}`)
	}

	t.Run("eager", func(t *testing.T) {
		out, code := runWorkflowCLI(t, "render", writeWorkflow(t, flat("\n    schedule: eager")))
		if code != 0 {
			t.Fatalf("exit code = %d, want 0: %s", code, out)
		}
		want := "  schedule: eager (runs as barrier: no lane needs another)\n  lanes:\n    wave 1: a, b\n"
		if !strings.Contains(out, want) {
			t.Errorf("want\n%s\nin\n%s", want, out)
		}
	})

	t.Run("unnamed", func(t *testing.T) {
		file := writeWorkflow(t, flat(""))
		out, code := runWorkflowCLI(t, "render", file)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0: %s", code, out)
		}
		if !strings.Contains(out, "  lanes:\n    wave 1: a, b\n") || strings.Contains(out, "wave 2") {
			t.Errorf("want one wave naming both lanes: %s", out)
		}
		if strings.Contains(out, "schedule:") {
			t.Errorf("barrier was badged: %s", out)
		}
		if s := stepAt(t, renderJSON(t, file), "steps[0]"); s.Schedule != workflow.ScheduleBarrier {
			t.Errorf("--json schedule = %q, want %q", s.Schedule, workflow.ScheduleBarrier)
		}
	})
}

// TestRenderGuardedLaneIsTagged: a guarded lane may impose no ordering (task
// 080 decision 8), so it is tagged — and its dependent still draws in wave 2,
// because whether the guard holds is not the preview's to judge.
func TestRenderGuardedLaneIsTagged(t *testing.T) {
	file := writeWorkflow(t, workflowWith(`  - id: spread
    type: fan_out
    lanes:
      - {id: api, steps: [{id: as, type: command, run: a}]}
      - id: db
        if: "{{ .Task.Title }}"
        steps: [{id: ds, type: command, run: d}]
      - {id: wire, needs: [api, db], steps: [{id: ws, type: command, run: w}]}`))

	out, code := runWorkflowCLI(t, "render", file)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, out)
	}
	if want := "    wave 1: api, db (guarded)\n    wave 2: wire (needs api, db)\n"; !strings.Contains(out, want) {
		t.Errorf("want\n%s\nin\n%s", want, out)
	}
	spread := stepAt(t, renderJSON(t, file), "steps[0]")
	if got, want := laneSummary(spread.Lanes), "api/1/ db/1//guarded wire/2/api+db"; got != want {
		t.Errorf("lanes = %q, want %q", got, want)
	}
}

// TestRenderNamedLanesOffline: a lane naming a registry workflow still has its
// id and edges in the file, so the parent draws it; its own unresolved row is
// not a fan-out with lanes and grows no block (decision 7). A fan_out nested
// inside an inline lane draws its own block on its own row.
func TestRenderNamedLanesOffline(t *testing.T) {
	file := writeWorkflow(t, workflowWith(`  - id: spread
    type: fan_out
    lanes:
      - {id: api, workflow: build-api}
      - id: db
        steps:
          - id: inner
            type: fan_out
            lanes:
              - {id: x, steps: [{id: xs, type: command, run: x}]}
              - {id: y, needs: [x], steps: [{id: ys, type: command, run: y}]}
      - {id: wire, needs: [api, db], workflow: wire-up}`))

	out, code := runWorkflowCLI(t, "render", file)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, out)
	}
	if want := "    wave 1: api, db\n    wave 2: wire (needs api, db)\n"; !strings.Contains(out, want) {
		t.Errorf("the parent does not draw its named lanes; want\n%s\nin\n%s", want, out)
	}
	if want := "inner (fan_out)\n  lanes:\n    wave 1: x\n    wave 2: y (needs x)\n"; !strings.Contains(out, want) {
		t.Errorf("the nested fan-out does not draw its own block; want\n%s\nin\n%s", want, out)
	}
	if n := strings.Count(out, "  lanes:\n"); n != 2 {
		t.Errorf("%d lanes: blocks, want 2 — an unresolved lane row grew one: %s", n, out)
	}

	res := renderJSON(t, file)
	for _, path := range []string{"steps[0].lanes[0]", "steps[0].lanes[2]"} {
		if s := stepAt(t, res, path); s.Unresolved == "" || s.Lanes != nil || s.Schedule != "" {
			t.Errorf("%s = %+v, want an unresolved row with no graph", path, s)
		}
	}
	if s := stepAt(t, res, "steps[0].lanes[1].steps[0]"); laneSummary(s.Lanes) != "x/1/ y/2/x" {
		t.Errorf("nested lanes = %q, want x in wave 1 and y after it", laneSummary(s.Lanes))
	}
}

// workflowWith wraps a step list in the smallest valid workflow.
func workflowWith(steps string) string {
	return "name: demo\nsteps:\n" + steps + "\n"
}
