package workflow

import (
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

// failureOutputLines is how much of the previous attempt's output the §8.4
// failure block carries back to the agent.
const failureOutputLines = 200

// RenderContext is the template context of spec §8.4. Every field is
// populated before a step runs; rendering happens before any process is
// started, so a bad reference fails the step without side effects.
type RenderContext struct {
	Task        TaskContext
	Project     ProjectContext
	Workflow    Info
	Step        StepContext
	Steps       map[string]StepResult
	Worktree    WorktreeContext
	LastFailure Failure
}

// TaskContext is `.Task`.
type TaskContext struct {
	ID          int64
	Title       string
	Description string
	// Fields is the task's free-form key/value map. Because templates render
	// with missingkey=error, an optional field must be read defensively:
	// {{ with index .Task.Fields "ticket" }}…{{ end }}.
	Fields     map[string]string
	BaseBranch string
	BranchName string
}

// ProjectContext is `.Project`.
type ProjectContext struct {
	Name          string
	Path          string
	DefaultBranch string
}

// Info is `.Workflow` — the identity of the workflow being run.
type Info struct {
	Name        string
	Description string
}

// StepContext is `.Step` — the step being rendered.
type StepContext struct {
	ID      string
	Name    string
	Index   int
	Attempt int // 1-based
}

// StepResult is one completed step in `.Steps`: the agent's final result
// text (agent steps) or the tail of stdout (command steps).
type StepResult struct {
	Status   string
	Result   string
	ExitCode int
}

// WorktreeContext is `.Worktree`.
type WorktreeContext struct {
	Path string
}

// Failure is `.LastFailure`, populated on retry attempts only.
type Failure struct {
	Reason string
	Output string
}

// Empty reports whether there is no previous failure to report.
func (f Failure) Empty() bool { return f.Reason == "" && f.Output == "" }

// Render renders one template body (a prompt, run, check, or instructions
// field) against rc. name identifies the field in error messages. Missing
// map keys and unknown fields are errors (phase 2 decision), so a typo fails
// the step instead of silently rendering a hole (§8.4).
func Render(name, text string, rc RenderContext) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse %s template: %w", name, err)
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, rc); err != nil {
		return "", fmt.Errorf("render %s template: %w", name, err)
	}
	return sb.String(), nil
}

// Env returns the vincent variables added to the environment of command and
// check steps (spec §8.5). They are appended to the daemon's own environment
// by the caller, along with any step-declared `env`.
func Env(rc RenderContext) []string {
	return []string{
		"VINCENT_TASK_ID=" + strconv.FormatInt(rc.Task.ID, 10),
		"VINCENT_TASK_TITLE=" + rc.Task.Title,
		"VINCENT_PROJECT_NAME=" + rc.Project.Name,
		"VINCENT_PROJECT_PATH=" + rc.Project.Path,
		"VINCENT_WORKTREE=" + rc.Worktree.Path,
		"VINCENT_BRANCH=" + rc.Task.BranchName,
		"VINCENT_BASE_BRANCH=" + rc.Task.BaseBranch,
		"VINCENT_STEP_ID=" + rc.Step.ID,
		"VINCENT_STEP_ATTEMPT=" + strconv.Itoa(rc.Step.Attempt),
		"VINCENT_WORKFLOW=" + rc.Workflow.Name,
	}
}

// AppendFailureBlock appends the structured previous-attempt block of §8.4
// to a rendered agent prompt. It is a no-op on the first attempt or when
// there is no recorded failure.
func AppendFailureBlock(prompt string, attempt int, failure Failure) string {
	if attempt <= 1 || failure.Empty() {
		return prompt
	}
	var sb strings.Builder
	sb.WriteString(prompt)
	if !strings.HasSuffix(prompt, "\n") {
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "\n<previous-attempt-failure attempt=\"%d\">\n", attempt-1)
	fmt.Fprintf(&sb, "reason: %s\n", failure.Reason)
	if failure.Output != "" {
		fmt.Fprintf(&sb, "--- output (last %d lines) ---\n", failureOutputLines)
		sb.WriteString(lastLines(failure.Output, failureOutputLines))
		if !strings.HasSuffix(failure.Output, "\n") {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("</previous-attempt-failure>\n")
	return sb.String()
}

// lastLines returns the final n lines of s.
func lastLines(s string, n int) string {
	if s == "" || n <= 0 {
		return ""
	}
	trimmed := strings.TrimSuffix(s, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= n {
		return trimmed
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
