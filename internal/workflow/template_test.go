package workflow

import (
	"strconv"
	"strings"
	"testing"
)

func testContext() RenderContext {
	return RenderContext{
		Task: TaskContext{
			ID:          42,
			Title:       "Add rate limiting",
			Description: "Cap the API at 100 rps.",
			Fields:      map[string]string{"ticket": "OPS-123"},
			BaseBranch:  "main",
			BranchName:  "vincent/42-add-rate-limiting",
		},
		Project:  ProjectContext{Name: "vincent", Path: "/repos/vincent", DefaultBranch: "main"},
		Workflow: Info{Name: "feature-pr", Description: "Implement and publish"},
		Step:     StepContext{ID: "implement", Name: "Implement the change", Index: 0, Attempt: 2},
		Steps: map[string]StepResult{
			"plan": {Status: "succeeded", Result: "planned it", ExitCode: 0},
		},
		Worktree:    WorktreeContext{Path: "/data/worktrees/42"},
		LastFailure: Failure{Reason: "check command failed (exit 1)", Output: "FAIL ./pkg"},
	}
}

// TestRenderEveryContextVariable walks every §8.4 variable, so a field
// renamed on one of the context structs breaks a test rather than a workflow.
func TestRenderEveryContextVariable(t *testing.T) {
	rc := testContext()
	tests := []struct{ tmpl, want string }{
		{"{{.Task.ID}}", "42"},
		{"{{.Task.Title}}", "Add rate limiting"},
		{"{{.Task.Description}}", "Cap the API at 100 rps."},
		{`{{index .Task.Fields "ticket"}}`, "OPS-123"},
		{"{{.Task.BaseBranch}}", "main"},
		{"{{.Task.BranchName}}", "vincent/42-add-rate-limiting"},
		{"{{.Project.Name}}", "vincent"},
		{"{{.Project.Path}}", "/repos/vincent"},
		{"{{.Project.DefaultBranch}}", "main"},
		{"{{.Workflow.Name}}", "feature-pr"},
		{"{{.Workflow.Description}}", "Implement and publish"},
		{"{{.Step.ID}}", "implement"},
		{"{{.Step.Name}}", "Implement the change"},
		{"{{.Step.Index}}", "0"},
		{"{{.Step.Attempt}}", "2"},
		{`{{(index .Steps "plan").Result}}`, "planned it"},
		{`{{(index .Steps "plan").Status}}`, "succeeded"},
		{`{{(index .Steps "plan").ExitCode}}`, "0"},
		{"{{.Worktree.Path}}", "/data/worktrees/42"},
		{"{{.LastFailure.Reason}}", "check command failed (exit 1)"},
		{"{{.LastFailure.Output}}", "FAIL ./pkg"},
	}
	for _, tt := range tests {
		got, err := Render("prompt", tt.tmpl, rc)
		if err != nil {
			t.Errorf("Render(%s): %v", tt.tmpl, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Render(%s) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
}

// TestRenderStrictness pins the phase 2 decision: a typo fails the step
// before any process starts, instead of rendering a hole into the prompt.
func TestRenderStrictness(t *testing.T) {
	rc := testContext()
	badTemplates := []string{
		"{{.Task.Titel}}",           // struct field typo
		"{{.Task.Fields.tickte}}",   // map key typo
		"{{.Nonexistent.Whatever}}", // unknown root
	}
	for _, tmpl := range badTemplates {
		if got, err := Render("prompt", tmpl, rc); err == nil {
			t.Errorf("Render(%s) = %q, want an error", tmpl, got)
		}
	}

	// An optional field read defensively renders empty without failing.
	got, err := Render("prompt", `{{ with index .Task.Fields "absent" }}ticket {{.}}{{ end }}ok`, rc)
	if err != nil {
		t.Fatalf("defensive optional field failed: %v", err)
	}
	if got != "ok" {
		t.Errorf("optional field render = %q, want %q", got, "ok")
	}
}

func TestRenderUnparsableTemplate(t *testing.T) {
	if _, err := Render("run", "{{.Task.Title", testContext()); err == nil {
		t.Error("Render with a broken template = nil error, want a parse failure")
	}
}

func TestEnv(t *testing.T) {
	rc := testContext()
	env := map[string]string{}
	for _, kv := range Env(rc) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("env entry %q is not k=v", kv)
		}
		env[k] = v
	}
	want := map[string]string{
		"VINCENT_TASK_ID":      strconv.FormatInt(rc.Task.ID, 10),
		"VINCENT_TASK_TITLE":   rc.Task.Title,
		"VINCENT_PROJECT_NAME": rc.Project.Name,
		"VINCENT_PROJECT_PATH": rc.Project.Path,
		"VINCENT_WORKTREE":     rc.Worktree.Path,
		"VINCENT_BRANCH":       rc.Task.BranchName,
		"VINCENT_BASE_BRANCH":  rc.Task.BaseBranch,
		"VINCENT_STEP_ID":      rc.Step.ID,
		"VINCENT_STEP_ATTEMPT": "2",
		"VINCENT_WORKFLOW":     rc.Workflow.Name,
	}
	if len(env) != len(want) {
		t.Errorf("env has %d entries, want %d: %v", len(env), len(want), env)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestAppendFailureBlock(t *testing.T) {
	failure := Failure{Reason: "check command failed (exit 1)", Output: "line1\nline2\nline3"}

	if got := AppendFailureBlock("prompt", 1, failure); got != "prompt" {
		t.Errorf("first attempt = %q, want the prompt unchanged", got)
	}
	if got := AppendFailureBlock("prompt", 3, Failure{}); got != "prompt" {
		t.Errorf("no recorded failure = %q, want the prompt unchanged", got)
	}

	got := AppendFailureBlock("prompt", 2, failure)
	for _, want := range []string{
		"prompt",
		`<previous-attempt-failure attempt="1">`,
		"reason: check command failed (exit 1)",
		"--- output (last 200 lines) ---",
		"line3",
		"</previous-attempt-failure>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block %q missing %q", got, want)
		}
	}
	if !strings.Contains(got, "line3\n</previous-attempt-failure>\n") {
		t.Errorf("block %q: closing tag is not on its own line", got)
	}

	// Output ending in a newline (the usual case) must not glue the closing
	// tag onto the last line, and must not open a blank line before it.
	got = AppendFailureBlock("prompt", 2, Failure{Reason: "boom", Output: "line1\nline2\n"})
	if !strings.Contains(got, "line2\n</previous-attempt-failure>\n") {
		t.Errorf("block %q: closing tag is not on its own line after newline-terminated output", got)
	}
	if strings.Contains(got, "\n\n</previous-attempt-failure>") {
		t.Errorf("block %q: blank line before the closing tag", got)
	}
}

func TestAppendFailureBlockTruncatesOutput(t *testing.T) {
	var sb strings.Builder
	for i := range 500 {
		sb.WriteString("line" + strconv.Itoa(i) + "\n")
	}
	got := AppendFailureBlock("p", 2, Failure{Reason: "boom", Output: sb.String()})
	if strings.Contains(got, "line299\n") {
		t.Error("block carries output older than the last 200 lines")
	}
	if !strings.Contains(got, "line300\n") || !strings.Contains(got, "line499") {
		t.Error("block is missing the most recent output lines")
	}
	if !strings.Contains(got, "line499\n</previous-attempt-failure>\n") {
		t.Error("closing tag is not on its own line after truncated newline-terminated output")
	}
}
