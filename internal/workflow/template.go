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
	Task     TaskContext
	Project  ProjectContext
	Workflow Info
	Step     StepContext
	Steps    map[string]StepResult
	// Loop is where this step sits inside an enclosing `loop` (§7.8, task 016
	// decision 9). Its zero value — `Index: 0` — is what every step outside a
	// loop renders with, so a template shared between the two can tell.
	Loop LoopContext
	// Issue is the GitHub issue this task was created from (§8.4, task 035).
	// Its zero value — `Number: 0` — is what every task created without one
	// renders with, exactly the way `.Loop`'s `Index: 0` works, so
	// `{{ if .Issue.Number }}` tells the two apart and a template shared
	// between linked and unlinked tasks renders on both (decision 8).
	//
	// It is read from the task's snapshot, never from the network: rendering
	// stays pure and offline, and an issue edited on GitHub after creation is
	// deliberately not reflected.
	Issue       IssueContext
	Worktree    WorktreeContext
	LastFailure Failure
	// Host is the daemon's own platform (§8.4, task 015 decision 12). The
	// daemon is what runs the steps, so it is the daemon's GOOS a guard must
	// judge — the same reasoning §8.1.1 gives for `platforms:`.
	//
	// It is what closes §8.1.1's deferred per-step `platforms:` with no new
	// schema: `if: '{{ ne .Host.OS "windows" }}'` says the same thing using
	// a guard whose skip semantics §7.7 defines. `.Now` was deliberately not
	// added beside it — a guard reading wall-clock makes a run
	// non-reproducible, which is the property §7.6 chose declared lane order
	// to preserve.
	Host HostContext
	// Conflicts are the files a fan_out join is asking an `on_conflict:
	// agent` resolver to fix (§7.6, task 014 decision 24). Empty for every
	// other step, so a prompt that reads it defensively works anywhere.
	Conflicts []string
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

// LoopContext is `.Loop` — the current iteration of the enclosing `loop`
// step (§7.8).
//
// Item is a **string** rather than anything structured: `Task.Fields` is
// map[string]string and every other value in §8.4 is a string, so a
// structured item would push a new type through the render context, the API
// DTOs and the TUI for a case nobody has yet (decision 9).
type LoopContext struct {
	// Index is the 1-based iteration, and 0 outside any loop.
	Index int
	// Item is the `for_each` item this iteration runs on, empty for a
	// `count:` loop.
	Item    string
	IsFirst bool
	IsLast  bool
}

// Driver names for a `loop` step's single item source (§7.8, decision 2).
// There is deliberately no `while`: a guard can read only `.Steps`, which on
// the first iteration has no row for the body that would fill it, so every
// spelling of a useful `while` is either loud-and-unwritable or silently
// false. `count:` plus `break` writes the same loop correctly, post-test by
// construction, with the condition in the body where it can see the body.
const (
	DriverCount   = "count"
	DriverForEach = "for_each"
)

// Driver reports which item source a loop step carries. Validation
// guarantees exactly one, so this is total for a step that parsed.
func (s Step) Driver() string {
	if len(s.ForEach) > 0 {
		return DriverForEach
	}
	return DriverCount
}

// IssueContext is `.Issue` — the GitHub issue snapshot a task carries (§8.4,
// task 035).
//
// Labels is a real list, not a joined string: it is the one piece of issue
// metadata a template genuinely wants to range over, and the comma-joined
// spelling is what a declared `labels` task field gets instead (decision 7).
// Everything else is a plain string for the reason `.Loop.Item` is one —
// every other value in §8.4 is.
//
// This package does not import internal/github: the snapshot is mapped into
// this shape by internal/taskrun, which keeps the render context free of the
// fetching machinery and keeps `.Issue` renderable from a row alone.
type IssueContext struct {
	// Number is 0 when no issue is linked; it is the field a template tests.
	Number int
	// Repo is `owner/name`.
	Repo            string
	Title           string
	Body            string
	URL             string
	State           string
	Labels          []string
	Author          string
	Assignee        string
	Milestone       string
	MilestoneNumber int
}

// HostContext is `.Host` — the daemon's GOOS and GOARCH.
type HostContext struct {
	OS   string
	Arch string
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

// EscapeTemplate neutralizes the one sequence that would make embedded prose
// execute as part of a step's template. Only "{{" opens an action, so a lone
// "}}" needs nothing. The replacement is itself an action rendering the two
// literal characters, which is why this must run before the text reaches
// Render and not after.
//
// Two callers need it, for the same reason: text that was written as prose
// reaches a field Render will parse. The built-in `create-workflow` prompt
// splices in an embedded skill (task 024), and a `repair` prompt is what an
// operator typed at a form (task 025). Neither is a workflow-authoring
// surface, and §8.4 renders with `missingkey=error` — so an unescaped `{{`
// would fail the step before anything ran.
func EscapeTemplate(s string) string {
	return strings.ReplaceAll(s, "{{", `{{"{{"}}`)
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
		// lastLines never carries a trailing newline, so add exactly one to
		// keep the closing tag on its own line.
		sb.WriteString(lastLines(failure.Output, failureOutputLines))
		sb.WriteString("\n")
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
