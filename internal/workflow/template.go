package workflow

import (
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"
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
	// Issue is the issue this task was created from (§8.4, task 035, task
	// 130 decision 8).
	// Its zero value — `Number: 0` — is what every task created without one
	// renders with, exactly the way `.Loop`'s `Index: 0` works, so
	// `{{ if .Issue.Number }}` tells the two apart and a template shared
	// between linked and unlinked tasks renders on both (decision 8).
	//
	// It is read from the task's snapshot, never from the network: rendering
	// stays pure and offline, and an issue edited after creation is
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
	// IssueEnv is the input to §8.5's VINCENT_ISSUE_* variables (task 130,
	// 130.14). It is not part of §8.4's documented template context: it is
	// carried here so Env stays a pure function of one value, with the
	// file's write done by internal/taskrun before the context reaches Env.
	IssueEnv IssueEnv
}

// IssueEnv is what Env needs to export a linked task's issue (§8.5, 130.14).
// Its zero value — no File — is an unlinked task, which gets none of the
// variables.
type IssueEnv struct {
	// File is the absolute path of the already-written snapshot file.
	File string
	// ID is the vincent issue id, 0 for a task carrying only a legacy
	// GitHub snapshot (which has none).
	ID int64
	// Number and URL are the GitHub reference of an imported issue or a
	// legacy snapshot, zero for a local issue.
	Number int
	URL    string
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
	// ID is the project's numeric id — what a trigger's source.project names
	// and `vincent trigger ls --project` takes (task 098 decision 4).
	ID            int64
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

// IssueContext is `.Issue` — the issue a task was created from (§8.4, task
// 035, reshaped by task 130 decision 8).
//
// Number is the **vincent** issue id. The provider reference of an imported
// issue is Source, zero for a local one, so a template that hands a number to
// `gh` reads `.Issue.Source.Number` and never `.Issue.Number`. A task that
// still carries only a legacy GitHub snapshot (`github_issue` on create,
// until 130.11 removes it) renders that snapshot's GitHub number as Number,
// exactly as it did before the reshape, and fills Source from it as well.
//
// Labels is a real list, not a joined string: it is the one piece of issue
// metadata a template genuinely wants to range over, and the comma-joined
// spelling is what a declared `labels` task field gets instead (decision 7).
// Comments is the issue's discussion thread as it stood at task creation,
// oldest first and untruncated (task 130 decision 24, 130.16): local
// comments and the mirrored GitHub ones alike. It is a list for the reason
// Labels is one — a template ranges over it. A later comment never changes
// an existing task's thread, and a task with no snapshot, or only a legacy
// GitHub one, has none.
// Everything else is a plain string for the reason `.Loop.Item` is one —
// every other value in §8.4 is.
//
// This package imports neither internal/github nor internal/store: the
// snapshot is mapped into this shape by internal/taskrun, which keeps the
// render context free of the fetching machinery and keeps `.Issue`
// renderable from a row alone.
type IssueContext struct {
	// Number is 0 when no issue is linked; it is the field a template tests.
	Number          int
	Title           string
	Body            string
	State           string
	Labels          []string
	Kind            string
	Priority        int
	Author          string
	Assignee        string
	Milestone       string
	MilestoneNumber int
	Comments        []IssueComment
	// Source is where an imported issue came from; its zero value means a
	// local issue (or none).
	Source IssueSource
	// Repo and URL are deprecated aliases of Source.Repo and Source.URL,
	// kept so a template written before task 130 renders unchanged.
	Repo string
	URL  string
}

// IssueComment is one element of `.Issue.Comments` (130.16). Author is the
// GitHub login of a mirrored comment, or the author a local comment was
// attributed at write time (§5.6's create rule).
type IssueComment struct {
	Author    string
	Body      string
	CreatedAt time.Time
}

// IssueSource is `.Issue.Source` — an imported issue's provider reference.
type IssueSource struct {
	// Provider is "github" for every source vincent imports today.
	Provider string
	// Repo is `owner/name`.
	Repo   string
	Number int
	URL    string
	// State is the issue's state on the provider, as captured.
	State string
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
	return renderAgainst(name, text, rc)
}

// RenderWith renders text against data with Render's engine and options. It
// exists for internal/trigger, whose templates see `.Event` rather than the
// §8.4 context (task 096 decision 11), so one engine — and one
// `missingkey=error` rule — serves both.
func RenderWith(name, text string, data any) (string, error) {
	return renderAgainst(name, text, data)
}

// renderAgainst is Render's body, over whatever context the caller has. The
// only other context is LaneContext, which embeds RenderContext and adds
// `.Item` (§7.6, task 080 decision 1).
func renderAgainst(name, text string, data any) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse %s template: %w", name, err)
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("render %s template: %w", name, err)
	}
	return sb.String(), nil
}

// LaneContext is the render context of a derived `fan_out`'s `lane:` template
// (§8.4, §7.6, task 080 decision 1). It is the ordinary §8.4 context plus the
// one item the lane is being rendered for.
//
// Item is a **parsed JSON object**, and it is the one place §8.4's "every
// template value is a plain string" rule gives way. A DAG item must carry both
// an identity and its edges — `{{ .Item.id }}` and `{{ .Item.needs }}` — and
// one string cannot say both. `.Issue.Labels` is the existing precedent for
// structure in the render context.
//
// The widening is scoped here and goes no further: `.Loop.Item` is unchanged
// and still a string (task 016 decision 9), because a loop iteration is driven
// by a line, not by a node of a graph.
type LaneContext struct {
	RenderContext
	Item map[string]any
}

// RenderLane renders one field of a derived fan-out's `lane:` template against
// rc and the item this lane is being derived from.
func RenderLane(name, text string, rc RenderContext, item map[string]any) (string, error) {
	return renderAgainst(name, text, LaneContext{RenderContext: rc, Item: item})
}

// SplitNeeds turns a rendered `needs:` entry into lane ids. A hand-written
// entry is already one id; a derived one is whatever `{{ .Item.needs }}`
// rendered a JSON array to, which for Go's text/template is `[api db]`. Both
// end up as the same list.
func SplitNeeds(rendered string) []string {
	fields := strings.FieldsFunc(rendered, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == ' ' || r == '[' || r == ']' ||
			r == '"' || r == '\''
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
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

// Env returns the vincent variables added to the environment of command,
// check and agent steps (spec §8.5). They are appended to the daemon's own
// environment by the caller, along with any step-declared `env`.
//
// The VINCENT_ISSUE_* variables are present only for a task carrying an
// issue snapshot, and each only when it has a value: an unlinked task gets
// none, a local issue no NUMBER or URL, a legacy snapshot no ID.
func Env(rc RenderContext) []string {
	out := []string{
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
	ie := rc.IssueEnv
	if ie.File == "" {
		return out
	}
	out = append(out, "VINCENT_ISSUE_FILE="+ie.File)
	if ie.ID != 0 {
		out = append(out, "VINCENT_ISSUE_ID="+strconv.FormatInt(ie.ID, 10))
	}
	if ie.Number != 0 {
		out = append(out, "VINCENT_ISSUE_NUMBER="+strconv.Itoa(ie.Number))
	}
	if ie.URL != "" {
		out = append(out, "VINCENT_ISSUE_URL="+ie.URL)
	}
	return out
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
