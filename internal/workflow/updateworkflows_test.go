package workflow

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/skills"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden renders in testdata")

// skillMarker stands in for the embedded vincent-workflows skill in the
// golden renders below. The skill is its own file with its own history; the
// goldens pin what update-workflows says around it, so a skill edit must not
// read as a change to a project run.
const skillMarker = "<<the vincent-workflows skill>>\n"

// projectRunContext is the render context the project-run goldens were frozen
// with. fields is the only thing the cases vary.
func projectRunContext(fields map[string]string) RenderContext {
	return RenderContext{
		Task: TaskContext{
			ID: 42, Title: "modernize the workflows", Description: "Bring them up to date.",
			Fields: fields, BaseBranch: "master",
		},
		Project: ProjectContext{ID: 7, Name: "vincent", Path: "/repo/root"},
		Steps: map[string]StepResult{
			"inventory": {Status: "succeeded", Result: ".vincent/workflows/feature-pr.yaml"},
			"modernize": {Status: "succeeded", Result: "PROPOSAL SUMMARY"},
		},
	}
}

func stepByID(t *testing.T, steps []Step, id string) Step {
	t.Helper()
	for _, s := range steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("update-workflows has no step %q", id)
	return Step{}
}

// A project run — global false, or never set — renders every step that
// existed before task 123 byte for byte as it did (task 123 decision 2). The
// goldens were written from the pre-123 built-in, and only its two new
// decisions — the trailing condition, and the global branch — may differ.
func TestUpdateWorkflowsProjectRunRendersAsBefore(t *testing.T) {
	steps := builtins()[UpdateWorkflowsName].Workflow.Steps
	skill := skillInstructions(skills.VincentWorkflows)
	cases := map[string]map[string]string{
		"unset":     nil,
		"false":     {"global": "false"},
		"empty-map": {},
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			rc := projectRunContext(fields)
			for _, g := range []struct{ file, id, kind, text string }{
				{"update-workflows-inventory.golden", "inventory", "run", stepByID(t, steps, "inventory").Run},
				{"update-workflows-modernize.golden", "modernize", "prompt", stepByID(t, steps, "modernize").Prompt},
				{"update-workflows-relist.golden", "relist", "run", stepByID(t, steps, "relist").Run},
			} {
				got, err := Render(g.kind, g.text, rc)
				if err != nil {
					t.Fatalf("Render(%s) error = %v", g.id, err)
				}
				if strings.Count(got, skill) > 1 {
					t.Fatalf("%s carries the skill more than once", g.id)
				}
				got = strings.Replace(got, skill, skillMarker, 1)
				path := filepath.Join("testdata", g.file)
				if *updateGolden && name == "unset" {
					if err := os.MkdirAll("testdata", 0o750); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
					t.Errorf("%s renders differently from the pre-task-123 built-in:\n--- got ---\n%s\n--- want ---\n%s", g.id, got, want)
				}
			}
		})
	}
}

// A global run (task 123) renders the global branch of every shared step and
// a gate that says what approving does. Each string is one of the decisions
// the prompt carries, so a missing one is a decision the agent never hears.
func TestUpdateWorkflowsGlobalRunRenders(t *testing.T) {
	steps := builtins()[UpdateWorkflowsName].Workflow.Steps
	rc := projectRunContext(map[string]string{"global": "true"})
	render := func(kind, text string) string {
		t.Helper()
		got, err := Render(kind, text, rc)
		if err != nil {
			t.Fatalf("Render(%s) error = %v", kind, err)
		}
		return got
	}

	if got := render("run", stepByID(t, steps, "inventory").Run); got != "vincent workflow ls --global" {
		t.Errorf("inventory = %q, want the daemon-free global listing", got)
	}
	if got := render("run", stepByID(t, steps, "relist").Run); got != "vincent workflow apply --proposal 42 --check" {
		t.Errorf("relist = %q, want apply --check on this task's proposal", got)
	}
	if got := render("run", stepByID(t, steps, "apply").Run); got != "vincent workflow apply --proposal 42" {
		t.Errorf("apply = %q, want this task's proposal applied", got)
	}

	prompt := render("prompt", stepByID(t, steps, "modernize").Prompt)
	for _, want := range []string{
		"workflow-proposals and\n42",            // the staging directory names this task
		"manifest.json",                         // the manifest
		`"vincent workflow ls --global --json"`, // where the version tokens come from
		`"absent"`,                              // a new file's record
		"remove it if it exists",                // the retry clears its own staging
		"never a fragment or a patch",           // whole files only
		"Never write into paths.config_dir joined with workflows", // the live registry is apply's
		"These files have no git history",                         // the evidence there is
		"two of the global workflows",                             // checklist item 2, read for global
		"before/after diff of every change",                       // the report the gate reads
		".vincent/workflows/feature-pr.yaml",                      // the inventory, quoted
		"## The bar",                                              // the checklist, shared
		"## What you may not change",                              // the rules, shared
		"Choose the cheapest correct primitive",                   // the skill, shared
		"vincent status",                                          // the status line
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("global prompt lacks %q", want)
		}
	}
	for _, never := range []string{"on this task's branch", "The diff is the conversation", "its git history"} {
		if strings.Contains(prompt, never) {
			t.Errorf("global prompt still says the project run's %q", never)
		}
	}

	gate := render("instructions", stepByID(t, steps, "approve").Instructions)
	for _, want := range []string{
		"{data_dir}/workflow-proposals/42/",
		"{config_dir}/workflows/",
		"goes live in every project",
		"Rejecting ends the task with the\nglobal registry untouched",
		"PROPOSAL SUMMARY",
	} {
		if !strings.Contains(gate, want) {
			t.Errorf("approve instructions lack %q:\n%s", want, gate)
		}
	}

	// The guard is the one thing that separates the two runs' endings.
	guard := stepByID(t, steps, "global-only").If
	for fields, want := range map[string]string{"true": "true", "false": "false", "": "false"} {
		got := renderGuard(t, guard, projectRunContext(map[string]string{"global": fields}))
		if got != want {
			t.Errorf("global-only with global=%q renders %q, want %q", fields, got, want)
		}
	}
	if got := renderGuard(t, guard, projectRunContext(nil)); got != "false" {
		t.Errorf("global-only with no fields renders %q, want false", got)
	}
}

func renderGuard(t *testing.T, guard string, rc RenderContext) string {
	t.Helper()
	got, err := Render("if", guard, rc)
	if err != nil {
		t.Fatalf("Render(if) error = %v", err)
	}
	return got
}
