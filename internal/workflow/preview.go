package workflow

import (
	"fmt"
	"runtime"
	"strings"
	"text/template"
	"text/template/parse"
)

// Preview is the §8.4 render context of a *dry run* — `vincent workflow
// render` (task 044). Everything a real run learns from the database, a
// worktree or a previous attempt is bound to a visible sentinel instead.
//
// The sentinels are what make the preview honest in both directions.
// Rendering with `missingkey=error` (§8.4) means an unbound `.Steps` would
// report every legitimate `{{ .Steps.plan.Result }}` as a failure a real run
// would not hit; binding it to empty would print a prompt that looks like the
// one the agent will receive and is not. A value spelled `<steps.plan.result>`
// renders, reads as a placeholder, and leaves only genuine authoring bugs —
// a typo'd struct field, an unknown step id, an unsupplied task field — as
// errors.
const (
	SentinelTitle         = "<task.title>"
	SentinelDescription   = "<task.description>"
	SentinelBranch        = "<branch>"
	SentinelBaseBranch    = "<base_branch>"
	SentinelProjectName   = "<project.name>"
	SentinelProjectPath   = "<project.path>"
	SentinelDefaultBranch = "<project.default_branch>"
	SentinelWorktree      = "<worktree>"
	SentinelFailureReason = "<last_failure.reason>"
	SentinelFailureOutput = "<last_failure.output>"
	SentinelLoopItem      = "<loop.item>"
	// SentinelLane is the placeholder a *derived* fan-out's lanes stand at
	// until the step runs (§7.6, task 080). A `lane:` template plus
	// `for_each:` has no lane list to draw before spawn — the width is a fact
	// the run discovers — so every surface that draws lanes draws one of these
	// instead of guessing a count. After the step materializes its lanes into
	// the snapshot (decision 5) there is nothing left to stand in for.
	SentinelLane     = "<derived lane>"
	SentinelConflict = "<conflicts[0]>"
)

// SentinelField is the placeholder a declared **required** task field binds
// to. Only required fields are bound: `POST /v1/tasks` guarantees a real task
// carries them (§8.1.2), while an optional or undeclared name may genuinely be
// absent — so reading one without `{{ with index .Task.Fields "x" }}` is the
// error §8.4's defensive-read rule says it is, and the preview reports it.
func SentinelField(name string) string { return "<field." + name + ">" }

// SentinelStep is the placeholder one entry of `.Steps` binds to. field is
// the struct field being stood in for, lowercased: `<steps.plan.result>`.
func SentinelStep(id, field string) string {
	return "<steps." + id + "." + field + ">"
}

// SentinelItem is the placeholder one `.Item` key of a derived lane template
// binds to. path is the key chain as the template spells it: `<item.id>`,
// `<item.meta.name>`.
func SentinelItem(path string) string { return "<item." + path + ">" }

// PreviewInput is what a caller knows about the hypothetical task a preview
// renders for. Every zero-valued field falls back to its sentinel, so the
// offline no-flag case and a `--task`-bound one go through one constructor.
type PreviewInput struct {
	Task    TaskContext
	Project ProjectContext
}

// NewPreviewContext builds the shared part of a dry run's render context: the
// parts that do not depend on which step is being rendered. The caller fills
// `.Step`, `.Loop` and `.Conflicts` per step from the PreviewStep walk below.
//
// `.Task.ID` stays 0 and `.Issue` stays its zero value, following the
// `.Loop.Index: 0` precedent — `{{ if .Issue.Number }}` takes the unlinked
// branch, which is the branch a task created without an issue takes.
//
// `.Host` is the **real** GOOS/GOARCH of the machine running the preview,
// which is the only honest offline answer: there is no daemon to ask. A guard
// reading `.Host.OS` therefore previews for this host, not for a remote
// daemon's.
func NewPreviewContext(wf *Workflow, in PreviewInput) RenderContext {
	task := in.Task
	task.Title = orSentinel(task.Title, SentinelTitle)
	task.Description = orSentinel(task.Description, SentinelDescription)
	task.BranchName = orSentinel(task.BranchName, SentinelBranch)
	task.BaseBranch = orSentinel(task.BaseBranch, SentinelBaseBranch)
	task.Fields = previewFields(wf, task.Fields)

	project := in.Project
	project.Name = orSentinel(project.Name, SentinelProjectName)
	project.Path = orSentinel(project.Path, SentinelProjectPath)
	project.DefaultBranch = orSentinel(project.DefaultBranch, SentinelDefaultBranch)

	rc := RenderContext{
		Task:        task,
		Project:     project,
		Steps:       previewSteps(wf),
		Host:        HostContext{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Worktree:    WorktreeContext{Path: SentinelWorktree},
		LastFailure: Failure{Reason: SentinelFailureReason, Output: SentinelFailureOutput},
	}
	if wf != nil {
		rc.Workflow = Info{Name: wf.Name, Description: wf.Description}
	}
	return rc
}

// PreviewLoop is the `.Loop` a step inside a `loop` body renders with: the
// first iteration. A body step is rendered once, and the first iteration is
// the one every loop actually runs, so `{{ if .Loop.IsFirst }}` previews the
// branch that will execute.
func PreviewLoop() LoopContext {
	return LoopContext{Index: 1, Item: SentinelLoopItem, IsFirst: true}
}

// previewFields binds the task fields a preview can honestly claim: every
// declared **required** field, overridden by anything the caller supplied.
//
// A required field binds, in order: its `default:` when it has one; else, for
// an enum, its first declared value; else the sentinel. The two real values
// come first because SentinelField("environment") is `<field.environment>`,
// which is by construction not a member of its own enum — a preview that
// bound it would render something the workflow could never receive (task
// 058). Optional fields stay absent, which is exactly what POST /v1/tasks
// guarantees: the preview binds only what it can honestly claim.
func previewFields(wf *Workflow, supplied map[string]string) map[string]string {
	out := make(map[string]string, len(supplied))
	if wf != nil {
		for _, f := range wf.Fields {
			if !f.Required || f.Name == "" {
				continue
			}
			switch {
			case f.Default != "":
				out[f.Name] = f.Default
			case f.Type == FieldEnum && len(f.Values) > 0:
				out[f.Name] = f.Values[0]
			default:
				out[f.Name] = SentinelField(f.Name)
			}
		}
	}
	for k, v := range supplied {
		out[k] = v
	}
	return out
}

// previewSteps binds one `.Steps` entry per step the file declares, nested
// bodies included. That catches strictly more than a blanket sentinel would:
// `{{ .Steps.plan.Reslt }}` fails on the struct field and `{{ .Steps.pln.Result }}`
// fails on the unknown step id, which is a real authoring bug validation
// cannot see.
//
// Forward references — a step reading one that has not completed at that
// point — render clean. Restricting the map to steps that would have completed
// was considered and rejected (task 044 decision 3): §8.4's (step_index,
// iteration, body position) rule interacts with `parallel` blindness, loop
// iterations and `allow_failure` in ways that produce false positives, and a
// false positive exits 1 inside a pre-commit hook.
func previewSteps(wf *Workflow) map[string]StepResult {
	steps := map[string]StepResult{}
	for _, ps := range PreviewSteps(wf) {
		// An unresolved node contributes no id: an `include` step is spliced
		// away before anything runs (§7.9), and a named lane is a child
		// task's whole snapshot rather than a step in this one.
		if ps.Step.ID == "" || ps.Unresolved != "" {
			continue
		}
		steps[ps.Step.ID] = StepResult{
			Status: SentinelStep(ps.Step.ID, "status"),
			Result: SentinelStep(ps.Step.ID, "result"),
		}
	}
	return steps
}

// PreviewStep is one step a dry run renders, with where it sits in the file.
type PreviewStep struct {
	// Path is the YAML path the step was found at, as validation reports it:
	// `steps[2].steps[0]`, `steps[1].lanes[0].steps[0]`, `steps[3].merge.agent`.
	Path string
	Step Step
	// Index is `.Step.Index`. Members of a `parallel` group and of a `loop`
	// body share their container's index, exactly as a run does — one
	// step_index per top-level step (§7.5, §7.8). A lane's inline steps get
	// their position within the lane, because a lane becomes a child task
	// with its own flat snapshot (§7.6).
	Index int
	// InLoop marks a step inside a `loop` body, which renders with
	// PreviewLoop rather than the zero `.Loop`.
	InLoop bool
	// Conflicts marks a `merge: on_conflict: agent` resolver step, the one
	// place §8.4 populates `.Conflicts`.
	Conflicts bool
	// Unresolved says why this step's own body is not in the file: an
	// `include`, or a fan-out lane naming a registry workflow. Empty for
	// every step whose content is present.
	Unresolved string
	// DerivedLane is set on everything inside a derived fan-out's `lane:`
	// template (§7.6, task 080): the `for_each` templates the lane is rendered
	// once per item of. Such a step is previewed once, but runs in as many
	// lanes as the list turns out to have.
	DerivedLane ForEach
}

// PreviewSteps flattens wf into every step a dry run renders, in declaration
// order, each container before its members.
//
// It goes deeper than the validator's allSteps, which stops at a group's
// sub-steps: a preview must reach loop bodies and fan-out lanes too, because
// those carry templates that fail at run time exactly like a top-level step's.
func PreviewSteps(wf *Workflow) []PreviewStep {
	if wf == nil {
		return nil
	}
	out := make([]PreviewStep, 0, len(wf.Steps))
	for i, step := range wf.Steps {
		out = append(out, previewWalk(step, fmt.Sprintf("steps[%d]", i), i, false)...)
	}
	return out
}

// previewWalk yields step and everything nested inside it.
func previewWalk(step Step, path string, index int, inLoop bool) []PreviewStep {
	self := PreviewStep{Path: path, Step: step, Index: index, InLoop: inLoop}
	if step.Type == StepInclude {
		self.Unresolved = fmt.Sprintf("include of workflow %q", step.Workflow)
	}
	out := []PreviewStep{self}

	// A group's members and a loop's body share the one `steps:` field.
	bodyInLoop := inLoop || step.Type == StepLoop
	for j, sub := range step.Steps {
		out = append(out, previewWalk(sub, fmt.Sprintf("%s.steps[%d]", path, j), index, bodyInLoop)...)
	}

	for j, lane := range step.Lanes {
		lanePath := fmt.Sprintf("%s.lanes[%d]", path, j)
		if lane.Workflow != "" {
			out = append(out, PreviewStep{
				Path:       lanePath,
				Step:       Step{ID: lane.ID, Type: StepFanOut},
				Index:      index,
				Unresolved: fmt.Sprintf("lane %q names workflow %q", lane.ID, lane.Workflow),
			})
			continue
		}
		// A lane's inline steps become a child task's own flat snapshot, so
		// they are indexed from 0 within the lane and are outside any loop
		// the parent is in.
		for k, sub := range lane.Steps {
			out = append(out, previewWalk(sub, fmt.Sprintf("%s.steps[%d]", lanePath, k), k, false)...)
		}
	}

	// A derived fan-out's single `lane:` template (§7.6, task 080) is still
	// live in an authored file: it becomes a lane per `for_each` item only at
	// spawn. It is walked exactly like a declared lane — its inline steps are
	// a child task's flat snapshot — and marked with the list it expands over.
	if step.Lane != nil {
		lanePath := path + ".lane"
		var lane []PreviewStep
		if step.Lane.Workflow != "" {
			lane = append(lane, PreviewStep{
				Path:       lanePath,
				Step:       Step{ID: SentinelLane, Type: StepFanOut},
				Index:      index,
				Unresolved: fmt.Sprintf("lane template names workflow %q", step.Lane.Workflow),
			})
		} else {
			for k, sub := range step.Lane.Steps {
				lane = append(lane, previewWalk(sub, fmt.Sprintf("%s.steps[%d]", lanePath, k), k, false)...)
			}
		}
		for i := range lane {
			// A derived fan-out nested inside this template has already
			// marked its own lane with its own list.
			if lane[i].DerivedLane == nil {
				lane[i].DerivedLane = step.ForEach
			}
		}
		out = append(out, lane...)
	}

	if step.Merge != nil && step.Merge.Agent != nil {
		resolver := previewWalk(*step.Merge.Agent, path+".merge.agent", index, inLoop)
		resolver[0].Conflicts = true
		out = append(out, resolver...)
	}
	return out
}

// PreviewItem is the `.Item` a derived lane template's own fields — `id`,
// `if`, `needs` and `fields` — render with in a dry run (§7.6, §8.4). The real
// item is a JSON object only the run discovers, and `.Item` renders under
// `missingkey=error`, so binding it empty would fail every legitimate
// `{{ .Item.id }}`. Instead each `.Item` key chain those fields spell out binds
// to SentinelItem, the way a required task field binds to `<field.NAME>`: a
// well-formed item read renders a placeholder, and a typo in `.Task` or
// `.Steps` beside it still fails exactly where a run would.
//
// A nested read binds its parents to objects, so `.Item.meta.name` resolves.
// A key read through a rebound dot — `{{ with .Item }}{{ .id }}` — is not
// bound, and is reported: the preview binds only what it can see.
func PreviewItem(lane Lane) map[string]any {
	item := map[string]any{}
	texts := append([]string{lane.ID, lane.If}, lane.Needs...)
	for _, v := range lane.Fields {
		texts = append(texts, v)
	}
	for _, text := range texts {
		tmpl, err := template.New("item").Parse(text)
		if err != nil {
			// Rendering the same text reports the parse error with its field.
			continue
		}
		for _, t := range tmpl.Templates() {
			if t.Tree != nil {
				bindItemRefs(item, t.Root)
			}
		}
	}
	return item
}

// bindItemRefs walks one parsed template and binds every `.Item` field chain
// it finds.
func bindItemRefs(item map[string]any, node parse.Node) {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, child := range n.Nodes {
			bindItemRefs(item, child)
		}
	case *parse.ActionNode:
		bindItemRefs(item, n.Pipe)
	case *parse.IfNode:
		bindItemBranch(item, &n.BranchNode)
	case *parse.RangeNode:
		bindItemBranch(item, &n.BranchNode)
	case *parse.WithNode:
		bindItemBranch(item, &n.BranchNode)
	case *parse.TemplateNode:
		bindItemRefs(item, n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return
		}
		for _, cmd := range n.Cmds {
			bindItemRefs(item, cmd)
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			bindItemRefs(item, arg)
		}
	case *parse.ChainNode:
		bindItemRefs(item, n.Node)
	case *parse.FieldNode:
		bindItemPath(item, n.Ident)
	case *parse.VariableNode:
		if len(n.Ident) > 0 && n.Ident[0] == "$" {
			bindItemPath(item, n.Ident[1:])
		}
	}
}

func bindItemBranch(item map[string]any, n *parse.BranchNode) {
	bindItemRefs(item, n.Pipe)
	bindItemRefs(item, n.List)
	bindItemRefs(item, n.ElseList)
}

// bindItemPath binds one field chain — `Item id`, `Item meta name` — if it
// reads `.Item`.
func bindItemPath(item map[string]any, ident []string) {
	if len(ident) < 2 || ident[0] != "Item" {
		return
	}
	keys := ident[1:]
	m := item
	for i, key := range keys {
		if i == len(keys)-1 {
			if _, ok := m[key]; !ok {
				m[key] = SentinelItem(strings.Join(keys, "."))
			}
			return
		}
		next, ok := m[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[key] = next
		}
		m = next
	}
}

func orSentinel(value, sentinel string) string {
	if value == "" {
		return sentinel
	}
	return value
}
