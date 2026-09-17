package cli

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/workflow"
)

// renderResult is the --json shape of `workflow render`. Like validateResult
// it is the command's own type: nothing here crosses the API, so borrowing a
// wire DTO would tie a local command to a remote contract it never speaks.
type renderResult struct {
	File  string       `json:"file"`
	Name  string       `json:"name,omitempty"`
	OK    bool         `json:"ok"`
	Steps []renderStep `json:"steps"`
	// Errors are template failures — the reason this command exists. Each
	// names the step and the field, and any one of them exits 1.
	Errors []renderIssue `json:"errors"`
	// Warnings never affect the exit code. A guard rendering to something
	// other than true/false is the main one: a sentinel can legitimately make
	// a guard non-boolean under preview, so it is reported, not judged.
	Warnings []renderIssue `json:"warnings"`
}

// renderStep is one step's preview: what each of its template fields renders
// to, and the §8.6 triple an agent step resolves to.
type renderStep struct {
	ID     string        `json:"id,omitempty"`
	Path   string        `json:"path"`
	Type   string        `json:"type"`
	Fields []renderField `json:"fields"`
	// Selection is present for agent steps only. Nothing beyond the §8.6
	// triple is reported: `timeout`, `permission_mode`, `on_input` and the
	// rest resolve on their own defaults→step precedence, which is not what
	// this command previews.
	Selection *renderSelection `json:"selection,omitempty"`
	// Unresolved says why a step's content is not in the file — an `include`
	// or a fan-out lane naming a registry workflow, neither of which can be
	// looked up without a daemon. It is reported, never fatal.
	Unresolved string `json:"unresolved,omitempty"`
	// DerivedLane marks a step inside a derived fan-out's `lane:` template
	// (§7.6, task 080) with the `for_each` templates the lane expands over:
	// it is previewed once, and runs once per item.
	DerivedLane []string `json:"derived_lane,omitempty"`
	// Schedule, MaxLanes and Lanes are a `fan_out` step's lane graph (§7.6,
	// issue #407), present only on a step that declares `lanes:` or a `lane:`
	// template. Schedule is the resolved mode, so an unnamed one reads
	// `barrier`; the human output badges `eager` alone (task 084 decision 9).
	Schedule string       `json:"schedule,omitempty"`
	MaxLanes *int         `json:"max_lanes,omitempty"`
	Lanes    []renderLane `json:"lanes,omitempty"`
}

// renderLane is one lane of a fan-out's graph. A declared lane carries its
// `needs:` edges and the wave they put it in, numbered from 1 as the TUI's
// diagram numbers them. A derived template is one entry marked Derived, with
// the `for_each` templates it expands over and no wave: its width, and so its
// graph, is a fact only the run discovers.
type renderLane struct {
	ID    string   `json:"id"`
	Needs []string `json:"needs,omitempty"`
	Wave  int      `json:"wave,omitempty"`
	// Guarded marks a lane carrying `if:`. A guarded-off lane imposes no
	// ordering (§7.6, task 080 decision 8), so its edges may not hold; the
	// guard itself is shown as the step's `lanes[N].if` field, never judged.
	Guarded bool     `json:"guarded,omitempty"`
	Derived bool     `json:"derived,omitempty"`
	ForEach []string `json:"for_each,omitempty"`
}

// renderField is one rendered template body. Output and Error are mutually
// exclusive.
type renderField struct {
	Field  string `json:"field"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// renderSelection is the §8.6 triple with the level that supplied each field
// — the same {value, source} shape POST /v1/resolve returns, from the same
// resolver.
type renderSelection struct {
	Agent  renderValue `json:"agent"`
	Model  renderValue `json:"model"`
	Effort renderValue `json:"effort"`
}

type renderValue struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

type renderIssue struct {
	Step    string `json:"step,omitempty"`
	Field   string `json:"field,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// renderFlags are the command's inputs, gathered so the offline and
// daemon-backed paths share one body.
type renderFlags struct {
	taskID      int64
	projectID   int64
	title       string
	description string
	fields      []string
	agent       string
	model       string
	effort      string
}

func newWorkflowRenderCmd() *cobra.Command {
	var f renderFlags
	cmd := &cobra.Command{
		Use:   "render <file>",
		Short: "Render a workflow's templates against a preview context (no daemon required)",
		Long: "Execute every template a workflow file declares — prompt, run, check,\n" +
			"instructions, if and for_each — against a synthetic §8.4 render context,\n" +
			"and print what each step would send alongside the §8.6 agent/model/effort\n" +
			"triple it resolves to.\n\n" +
			"`validate` parses templates; this executes them, which is where\n" +
			"missingkey=error catches a typo'd field or an unsupplied task field.\n" +
			"With no flags it runs entirely locally, so it belongs in the same\n" +
			"pre-commit hook as `validate`. Run-only values bind to visible\n" +
			"placeholders (<worktree>, <steps.plan.result>), so the output reads as a\n" +
			"preview and never as the literal prompt an agent will receive.\n\n" +
			"--task and --project reach the daemon: for a real task's title,\n" +
			"description, fields, branch and override triple, and to resolve `include`\n" +
			"steps and named fan-out lanes through the registry.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowRender(cmd, args[0], f)
		},
	}
	cmd.Flags().Int64Var(&f.taskID, "task", 0, "Bind a real task's title, description, fields and overrides")
	cmd.Flags().Int64Var(&f.projectID, "project", 0, "Bind this project's facts and resolve includes and named lanes")
	cmd.Flags().StringVar(&f.title, "title", "", "Title of the hypothetical task")
	cmd.Flags().StringVar(&f.description, "description", "", "Description of the hypothetical task")
	cmd.Flags().StringArrayVar(&f.fields, "field", nil, "Task field as key=value (repeatable)")
	cmd.Flags().StringVar(&f.agent, "agent", "", "Task-level agent override (§8.6 level 2)")
	cmd.Flags().StringVar(&f.model, "model", "", "Task-level model override (§8.6 level 2)")
	cmd.Flags().StringVar(&f.effort, "effort", "", "Task-level effort override (§8.6 level 2)")
	jsonFlag(cmd)
	return cmd
}

func runWorkflowRender(cmd *cobra.Command, file string, f renderFlags) error {
	// G304: the path is this command's own argument — the file the user asked
	// to preview, exactly as `workflow validate` reads it.
	src, err := os.ReadFile(file) //nolint:gosec // G304: see above
	if err != nil {
		return err
	}
	supplied, err := parseFieldFlags(f.fields)
	if err != nil {
		return err
	}

	wf, _, parseErr := workflow.Parse(src, localValidateOptions())
	if parseErr != nil {
		// A file that does not parse has no templates to execute. Report the
		// §8.2 findings and exit 1, the same verdict `validate` gives them.
		return emitRender(cmd, renderResult{
			File:     file,
			Steps:    []renderStep{},
			Errors:   parseIssues(parseErr),
			Warnings: []renderIssue{},
		})
	}

	in := workflow.PreviewInput{
		Task: workflow.TaskContext{
			Title: f.title, Description: f.description, Fields: supplied,
		},
	}
	override := agent.Level{Agent: f.agent, Model: f.model, Effort: f.effort}

	body := func(ctx context.Context, c *apiclient.Client) error {
		resolved := wf
		if c != nil {
			var err error
			resolved, err = bindRemote(ctx, cmd, c, f, wf, &in, &override)
			if err != nil {
				return err
			}
		}
		return emitRender(cmd, renderWorkflow(file, resolved, in, override))
	}
	if f.taskID != 0 || f.projectID != 0 {
		return withClient(cmd, body)
	}
	return body(cmd.Context(), nil)
}

// bindRemote fills the preview from the daemon: a real task's facts and
// override triple, the project's own, and the registry lookups that let
// `include` steps and named fan-out lanes resolve. Flags win over the task,
// so `--task 7 --title x` previews that task with a different title.
//
// The returned workflow is the expanded and lane-resolved one; on failure the
// daemon answered and said no, which is exit 1.
func bindRemote(ctx context.Context, cmd *cobra.Command, c *apiclient.Client, f renderFlags,
	wf *workflow.Workflow, in *workflow.PreviewInput, override *agent.Level,
) (*workflow.Workflow, error) {
	projectID := f.projectID
	if f.taskID != 0 {
		task, err := c.GetTask(ctx, f.taskID)
		if err != nil {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
			return nil, exitError{code: 1}
		}
		in.Task = workflow.TaskContext{
			Title:       firstNonBlank(f.title, task.Title),
			Description: firstNonBlank(f.description, task.Description),
			Fields:      mergeFields(task.Fields, in.Task.Fields),
			BaseBranch:  task.BaseBranch,
			BranchName:  task.BranchName,
		}
		*override = agent.Level{
			Agent:  firstNonBlank(f.agent, deref(task.AgentOverride)),
			Model:  firstNonBlank(f.model, deref(task.ModelOverride)),
			Effort: firstNonBlank(f.effort, deref(task.EffortOverride)),
		}
		if projectID == 0 {
			projectID = task.ProjectID
		}
	}
	if projectID != 0 {
		projects, err := c.ListProjects(ctx)
		if err != nil {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
			return nil, exitError{code: 1}
		}
		for _, p := range projects {
			if p.ID == projectID {
				in.Project = workflow.ProjectContext{
					ID: p.ID, Name: p.Name, Path: p.Path, DefaultBranch: p.DefaultBranch,
				}
				break
			}
		}
	}
	return resolveComposition(ctx, cmd, c, projectID, wf, *override)
}

// resolveComposition splices `include` steps and resolves named fan-out lanes
// through the registry, exactly as task creation does — the daemon answers
// the "which scope wins" question PR U put there, and this client asks it
// rather than re-deriving it from sibling files, which would answer
// differently from a real run whenever shadowing applies.
func resolveComposition(ctx context.Context, cmd *cobra.Command, c *apiclient.Client, projectID int64,
	wf *workflow.Workflow, override agent.Level,
) (*workflow.Workflow, error) {
	lookup := registryLookup(ctx, c, projectID)
	defaults := config.Default()
	expanded, err := workflow.Expand(wf, workflow.ExpandOptions{
		Lookup:   lookup,
		Limits:   workflow.IncludeLimits{MaxDepth: defaults.Include.MaxDepth},
		Override: override,
	})
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		return nil, exitError{code: 1}
	}
	resolved, _, err := workflow.ResolveTree(expanded, lookup, workflow.Limits{
		MaxDepth: defaults.FanOut.MaxDepth, MaxTasks: defaults.FanOut.MaxTasks,
	})
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		return nil, exitError{code: 1}
	}
	return resolved, nil
}

// registryLookup resolves a workflow name through the daemon's registry, with
// §5.2 shadowing already applied server-side. A name the registry does not
// know, or a file that does not parse, is reported as "not found" — the
// caller turns that into the same error task creation would give.
func registryLookup(ctx context.Context, c *apiclient.Client, projectID int64) workflow.LookupFunc {
	return func(name string) (*workflow.Workflow, bool) {
		def, err := c.GetWorkflowDefinition(ctx, projectID, name)
		if err != nil || !def.Valid() {
			return nil, false
		}
		return workflowFromDefinition(def.Definition), true
	}
}

// workflowFromDefinition maps the §13.2 definition DTO back to the parser's
// model, for the one caller that needs a callee's *body* rather than a
// description of it.
//
// Durations are deliberately dropped: `timeout`, `check_timeout`,
// `input_timeout` and `retry_backoff` are not part of the §8.6 triple and are
// not previewed, and nothing in the render path reads them. Everything a
// template or the triple can see is carried across.
func workflowFromDefinition(body *apiclient.WorkflowBody) *workflow.Workflow {
	if body == nil {
		return nil
	}
	wf := &workflow.Workflow{
		Name:        body.Name,
		Description: body.Description,
		Platforms:   body.Platforms,
		Defaults: workflow.Defaults{
			Agent:          body.Defaults.Agent,
			Model:          body.Defaults.Model,
			Effort:         body.Defaults.Effort,
			PermissionMode: body.Defaults.PermissionMode,
			OnInput:        body.Defaults.OnInput,
			MaxRetries:     body.Defaults.MaxRetries,
		},
		Steps: stepsFromDefinition(body.Steps),
	}
	for _, f := range body.Fields {
		wf.Fields = append(wf.Fields, workflow.FieldDefinition{
			Name: f.Name, Label: f.Label, Description: f.Description,
			Type: f.Type, Required: f.Required, Pattern: f.Pattern,
			Values: f.Values, Multiple: f.Multiple, Default: f.Default,
		})
	}
	return wf
}

func stepsFromDefinition(in []apiclient.WorkflowStepDef) []workflow.Step {
	if len(in) == 0 {
		return nil
	}
	out := make([]workflow.Step, 0, len(in))
	for _, s := range in {
		step := workflow.Step{
			ID: s.ID, Name: s.Name, Type: s.Type,
			MaxRetries: s.MaxRetries, If: s.If, AllowFailure: s.AllowFailure,
			Prompt: s.Prompt, Agent: s.Agent, Model: s.Model, Effort: s.Effort,
			PermissionMode: s.PermissionMode, OnInput: s.OnInput,
			Check: s.Check, Run: s.Run, Shell: s.Shell, Env: s.Env,
			Instructions: s.Instructions,
			Steps:        stepsFromDefinition(s.Steps), MaxParallel: s.MaxParallel,
			MaxLanes: s.MaxLanes, Schedule: s.Schedule,
			Count: s.Count, ForEach: workflow.ForEach(s.ForEach),
			MaxIterations: s.MaxIterations,
			Workflow:      s.Workflow, ResolvedFrom: s.ResolvedFrom,
		}
		for _, lane := range s.Lanes {
			step.Lanes = append(step.Lanes, laneFromDefinition(lane))
		}
		if s.Lane != nil {
			lane := laneFromDefinition(*s.Lane)
			step.Lane = &lane
		}
		if s.Merge != nil {
			merge := &workflow.Merge{OnConflict: s.Merge.OnConflict}
			if s.Merge.Agent != nil {
				if agentStep := stepsFromDefinition([]apiclient.WorkflowStepDef{*s.Merge.Agent}); len(agentStep) == 1 {
					merge.Agent = &agentStep[0]
				}
			}
			step.Merge = merge
		}
		out = append(out, step)
	}
	return out
}

// laneFromDefinition maps one lane back, for a declared list and a derived
// step's `lane:` template alike — one mapping, so the two cannot drift apart.
// `derived_from` is not carried: §7.6 refuses it in an authored document, and
// a registry callee is one.
func laneFromDefinition(lane apiclient.WorkflowLaneDef) workflow.Lane {
	return workflow.Lane{
		ID: lane.ID, If: lane.If, Needs: workflow.LaneNeeds(lane.Needs), Workflow: lane.Workflow,
		ResolvedFrom: lane.ResolvedFrom, Steps: stepsFromDefinition(lane.Steps),
		Fields: lane.Fields, Agent: lane.Agent, Model: lane.Model,
		Effort: lane.Effort, Priority: lane.Priority,
	}
}

// renderWorkflow executes every template the file declares and collects the
// verdict. It is pure: given the same workflow and preview input it produces
// the same result, which is what makes the whole command testable without a
// daemon, a worktree or an agent CLI.
func renderWorkflow(file string, wf *workflow.Workflow, in workflow.PreviewInput, override agent.Level) renderResult {
	res := renderResult{
		File: file, Name: wf.Name, OK: true,
		Steps: []renderStep{}, Errors: []renderIssue{}, Warnings: []renderIssue{},
	}
	base := workflow.NewPreviewContext(wf, in)
	defaults := agent.Level{
		Agent: wf.Defaults.Agent, Model: wf.Defaults.Model, Effort: wf.Defaults.Effort,
	}

	for _, ps := range workflow.PreviewSteps(wf) {
		out := renderStep{
			ID: ps.Step.ID, Path: ps.Path, Type: ps.Step.Type,
			Fields: []renderField{}, Unresolved: ps.Unresolved, DerivedLane: ps.DerivedLane,
		}
		if lanes := laneGraph(ps.Step); len(lanes) > 0 {
			out.Schedule = ps.Step.ScheduleMode()
			out.MaxLanes = ps.Step.MaxLanes
			out.Lanes = lanes
		}
		if ps.Unresolved != "" {
			res.Steps = append(res.Steps, out)
			continue
		}
		rc := base
		rc.Step = workflow.StepContext{
			ID: ps.Step.ID, Name: ps.Step.DisplayName(), Index: ps.Index, Attempt: 1,
		}
		if ps.InLoop {
			rc.Loop = workflow.PreviewLoop()
		}
		if ps.Conflicts {
			rc.Conflicts = []string{workflow.SentinelConflict}
		}
		var item map[string]any
		if ps.Step.Lane != nil {
			item = workflow.PreviewItem(*ps.Step.Lane)
		}
		for _, tf := range templateFields(ps.Step) {
			field := renderField{Field: tf.name}
			var text string
			var err error
			if tf.item {
				// Spawn renders these with RenderLane (task 080), so the
				// preview does too, and fails where a run would.
				text, err = workflow.RenderLane(tf.name, tf.text, rc, item)
			} else {
				text, err = workflow.Render(tf.name, tf.text, rc)
			}
			if err != nil {
				field.Error = err.Error()
				res.Errors = append(res.Errors, renderIssue{
					Step: ps.Step.ID, Field: tf.name, Path: ps.Path, Message: err.Error(),
				})
			} else {
				field.Output = text
				if tf.guard && !isBooleanOutput(text) {
					res.Warnings = append(res.Warnings, renderIssue{
						Step: ps.Step.ID, Field: tf.name, Path: ps.Path,
						Message: fmt.Sprintf("guard rendered to %q, which is neither true nor false; "+
							"a preview sentinel can do that, a real value must not", text),
					})
				}
			}
			out.Fields = append(out.Fields, field)
		}
		if ps.Step.Type == workflow.StepAgent {
			sel, src := agent.ResolveWithSources(agent.Level{
				Agent: ps.Step.Agent, Model: ps.Step.Model, Effort: ps.Step.Effort,
			}, override, defaults)
			out.Selection = &renderSelection{
				Agent:  renderValue{Value: sel.Agent, Source: string(src.Agent)},
				Model:  renderValue{Value: sel.Model, Source: string(src.Model)},
				Effort: renderValue{Value: sel.Effort, Source: string(src.Effort)},
			}
		}
		res.Steps = append(res.Steps, out)
	}
	res.OK = len(res.Errors) == 0
	return res
}

// laneGraph is a `fan_out` step's lanes as render draws them (issue #407), or
// nil for any other row — including the two fan_out rows previewWalk emits
// for a lane whose body is a registry workflow, which carry no lanes of their
// own.
//
// Waves come from workflow.LaneWaves, the engine's own derivation, and cover
// every declared lane: whether a guard selects a lane is a run-time fact the
// preview does not judge (task 044 decision 6). A derived template draws no
// waves even when its `for_each` would resolve offline — the per-item pass
// stays deferred (task 044 decision 10).
func laneGraph(step workflow.Step) []renderLane {
	if step.Type != workflow.StepFanOut {
		return nil
	}
	if step.Lane != nil {
		return []renderLane{{ID: workflow.SentinelLane, Derived: true, ForEach: step.ForEach}}
	}
	if len(step.Lanes) == 0 {
		return nil
	}
	waves := workflow.LaneWaves(step.Lanes)
	out := make([]renderLane, 0, len(step.Lanes))
	for _, lane := range step.Lanes {
		out = append(out, renderLane{
			ID: lane.ID, Needs: lane.Needs, Wave: waves[lane.ID] + 1, Guarded: lane.If != "",
		})
	}
	return out
}

// templateField is one body this command executes. guard marks the ones §7.7
// judges against true/false; item marks a derived lane template's own fields,
// which render with `.Item` bound.
type templateField struct {
	name  string
	text  string
	guard bool
	item  bool
}

// templateFields is the set of bodies a step declares as templates: exactly
// the map validation parses, plus `for_each` items and lane guards, which it
// parses at their own sites. All of them go through workflow.Render at run
// time and fail identically, so leaving the guards out would leave half the
// failure surface unreachable.
func templateFields(step workflow.Step) []templateField {
	out := make([]templateField, 0, 4)
	add := func(name, text string, guard bool) {
		if text != "" {
			out = append(out, templateField{name: name, text: text, guard: guard})
		}
	}
	add("if", step.If, true)
	add("prompt", step.Prompt, false)
	add("run", step.Run, false)
	add("instructions", step.Instructions, false)
	add("check", step.Check, false)
	for i, item := range step.ForEach {
		add(fmt.Sprintf("for_each[%d]", i), item, false)
	}
	for i, lane := range step.Lanes {
		add(fmt.Sprintf("lanes[%d].if", i), lane.If, true)
	}
	// A derived step's `lane:` template renders its guard, id, needs and
	// fields once per item at spawn (§7.6, task 080).
	if lane := step.Lane; lane != nil {
		addItem := func(name, text string, guard bool) {
			if text != "" {
				out = append(out, templateField{name: name, text: text, guard: guard, item: true})
			}
		}
		addItem("lane.if", lane.If, true)
		addItem("lane.id", lane.ID, false)
		for i, need := range lane.Needs {
			addItem(fmt.Sprintf("lane.needs[%d]", i), need, false)
		}
		for _, k := range slices.Sorted(maps.Keys(lane.Fields)) {
			addItem("lane.fields."+k, lane.Fields[k], false)
		}
	}
	return out
}

// isBooleanOutput reports whether a guard rendered to something §7.7 accepts.
func isBooleanOutput(s string) bool {
	switch strings.TrimSpace(s) {
	case "true", "false":
		return true
	}
	return false
}

func emitRender(cmd *cobra.Command, res renderResult) error {
	if wantJSON(cmd) {
		if err := emitJSON(cmd.OutOrStdout(), res); err != nil {
			return err
		}
	} else if err := printRender(cmd, res); err != nil {
		return err
	}
	if !res.OK {
		// Exit 1: the file was read and judged bad. That is a rejected input,
		// not an unreachable daemon (which is 2).
		return exitError{code: 1}
	}
	return nil
}

func printRender(cmd *cobra.Command, res renderResult) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	for _, s := range res.Steps {
		header := s.Path
		if s.ID != "" {
			header = fmt.Sprintf("%s %s", s.Path, s.ID)
		}
		if s.Type != "" {
			header += " (" + s.Type + ")"
		}
		if _, err := fmt.Fprintln(out, header); err != nil {
			return err
		}
		if len(s.DerivedLane) > 0 {
			if _, err := fmt.Fprintf(out, "  %s: a lane template, rendered once per item of %s\n",
				workflow.SentinelLane, strings.Join(s.DerivedLane, ", ")); err != nil {
				return err
			}
		}
		if s.Unresolved != "" {
			if _, err := fmt.Fprintf(out, "  unresolved: %s — pass --project <id> to resolve it through the registry\n",
				s.Unresolved); err != nil {
				return err
			}
			continue
		}
		if _, err := io.WriteString(out, laneBlock(s)); err != nil {
			return err
		}
		if s.Selection != nil {
			if _, err := fmt.Fprintf(out, "  agent: %s  model: %s  effort: %s\n",
				sourced(s.Selection.Agent), sourced(s.Selection.Model), sourced(s.Selection.Effort)); err != nil {
				return err
			}
		}
		for _, f := range s.Fields {
			if f.Error != "" {
				if _, err := fmt.Fprintf(out, "  %s: ERROR\n", f.Field); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(out, "  %s:\n%s\n", f.Field, indent(f.Output)); err != nil {
				return err
			}
		}
	}
	for _, e := range res.Errors {
		_, _ = fmt.Fprintln(errOut, "  error:", locateRender(e))
	}
	for _, w := range res.Warnings {
		_, _ = fmt.Fprintln(errOut, "  warning:", locateRender(w))
	}
	if !res.OK {
		_, err := fmt.Fprintf(out, "%s: %d render error(s)\n", res.File, len(res.Errors))
		return err
	}
	_, err := fmt.Fprintf(out, "%s: ok — %s, %d step(s) rendered, %d warning(s)\n",
		res.File, res.Name, len(res.Steps), len(res.Warnings))
	return err
}

// laneBlock draws a fan-out's lane graph on the step's own row: an optional
// `schedule: eager` line, then one `lanes:` line per wave, in wave order, each
// naming that wave's lanes in declaration order. It is plain line-oriented
// text — no box drawing, nothing that depends on terminal width — so it
// survives a pipe. Empty for a step with no graph.
func laneBlock(s renderStep) string {
	if len(s.Lanes) == 0 {
		return ""
	}
	var b strings.Builder
	derived := s.Lanes[0].Derived
	if s.Schedule == workflow.ScheduleEager {
		b.WriteString("  schedule: eager")
		// Task 081 decision 4, made visible: with no edge to wait on, eager
		// has nothing to do sooner than barrier. A derived list's flatness is
		// unknowable before spawn, so its eager prints bare.
		if !derived && slices.IndexFunc(s.Lanes, func(l renderLane) bool { return l.Wave > 1 }) < 0 {
			b.WriteString(" (runs as barrier: no lane needs another)")
		}
		b.WriteString("\n")
	}
	b.WriteString("  lanes:\n")
	if derived {
		lane := s.Lanes[0]
		fmt.Fprintf(&b, "    %s: unknown width", lane.ID)
		if s.MaxLanes != nil {
			fmt.Fprintf(&b, ", at most %d", *s.MaxLanes)
		}
		fmt.Fprintf(&b, ", one per item of %s\n", strings.Join(lane.ForEach, ", "))
		return b.String()
	}
	last := 0
	for _, lane := range s.Lanes {
		last = max(last, lane.Wave)
	}
	for wave := 1; wave <= last; wave++ {
		var names []string
		for _, lane := range s.Lanes {
			if lane.Wave == wave {
				names = append(names, laneLabel(lane))
			}
		}
		fmt.Fprintf(&b, "    wave %d: %s\n", wave, strings.Join(names, ", "))
	}
	return b.String()
}

// laneLabel is one lane in its wave line: its id, tagged `guarded` when an
// `if:` may leave it unspawned, and with the lanes it needs.
func laneLabel(lane renderLane) string {
	var tags []string
	if lane.Guarded {
		tags = append(tags, "guarded")
	}
	if len(lane.Needs) > 0 {
		tags = append(tags, "needs "+strings.Join(lane.Needs, ", "))
	}
	if len(tags) == 0 {
		return lane.ID
	}
	return lane.ID + " (" + strings.Join(tags, "; ") + ")"
}

// sourced renders one §8.6 field as "value (level)". An empty value is the
// adapter's own default, which only the CLI knows — the level still says so.
func sourced(v renderValue) string {
	return fmt.Sprintf("%s (%s)", dash(v.Value), v.Source)
}

// indent offsets a rendered body so it cannot be mistaken for the command's
// own output. Blank lines stay blank rather than gaining trailing spaces.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = "    " + line
		}
	}
	return strings.Join(lines, "\n")
}

func locateRender(i renderIssue) string {
	switch {
	case i.Step != "" && i.Field != "":
		return fmt.Sprintf("step %s: %s: %s", i.Step, i.Field, i.Message)
	case i.Path != "":
		return fmt.Sprintf("%s: %s", i.Path, i.Message)
	}
	return i.Message
}

// parseIssues turns Parse's §8.2 findings into this command's issue shape.
func parseIssues(err error) []renderIssue {
	var errs workflow.Errors
	if !asWorkflowErrors(err, &errs) {
		return []renderIssue{{Message: err.Error()}}
	}
	out := make([]renderIssue, 0, len(errs))
	for _, e := range errs {
		out = append(out, renderIssue{Path: e.Path, Message: e.Message})
	}
	return out
}

// mergeFields layers explicit --field values over a task's own.
func mergeFields(base, over map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func firstNonBlank(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
