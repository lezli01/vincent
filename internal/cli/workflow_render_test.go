package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
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

// workflowWith wraps a step list in the smallest valid workflow.
func workflowWith(steps string) string {
	return "name: demo\nsteps:\n" + steps + "\n"
}
