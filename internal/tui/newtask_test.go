package tui

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// keys turns a string into the keystrokes a human would produce typing it.
func keys(s string) []tea.KeyPressMsg {
	out := make([]tea.KeyPressMsg, 0, len(s))
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

// press sends one named key.
func press(n *newTask, name string) tea.Cmd {
	var msg tea.KeyPressMsg
	switch name {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		msg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+s":
		msg = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	default:
		msg = tea.KeyPressMsg{Code: rune(name[0]), Text: name}
	}
	_, cmd := n.update(msg)
	return cmd
}

func typeText(n *newTask, s string) {
	for _, k := range keys(s) {
		n.update(k)
	}
}

// moveTo walks the cursor to a row from wherever it is.
func moveTo(n *newTask, row ntRow) {
	for n.cursor < row {
		press(n, "down")
	}
	for n.cursor > row {
		press(n, "up")
	}
}

// addField drives the fields editor the way a human does: a, key, enter,
// value, enter.
func addField(n *newTask, key, value string) {
	press(n, "a")
	typeText(n, key)
	press(n, "enter")
	typeText(n, value)
	press(n, "enter")
}

// loadedForm is a form with catalogs already in hand: two projects, three
// workflows (one broken, one needing an unavailable agent) and two adapters
// of which codex is missing.
func loadedForm(t *testing.T) *newTask {
	t.Helper()
	n := newNewTask()
	n.update(tea.WindowSizeMsg{Width: 140, Height: 40})
	badErr := "steps: at least one step is required"
	n.update(ntLoadedMsg{
		projects: []apiclient.Project{
			{ID: 1, Name: "vincent", Path: "/src/vincent", DefaultBranch: "main"},
			{
				ID: 2, Name: "other", Path: "/src/other", DefaultBranch: "trunk",
				DefaultWorkflow: ptr("two-step"),
			},
		},
		workflows: []apiclient.WorkflowEntry{
			{
				Name: "adhoc", Scope: "builtin", Description: "One agent step.",
				Steps: []apiclient.WorkflowEntryStep{{ID: "run", Name: "Run", Type: "agent", Agent: "claude"}},
			},
			{
				Name: "two-step", Scope: "global", Description: "Implement then review.",
				Steps: []apiclient.WorkflowEntryStep{
					{ID: "implement", Name: "Implement", Type: "agent", Agent: "claude"},
					{ID: "review", Name: "Review", Type: "agent", Agent: "codex"},
					{ID: "unset", Name: "Whatever the adapter says", Type: "agent"},
					{ID: "publish", Name: "Publish", Type: "command"},
				},
			},
			{
				Name: "busted", Scope: "project",
				Errors: []apiclient.WorkflowFinding{{Message: badErr}},
				Error:  &badErr,
			},
		},
		agents: apiclient.Agents{
			{
				Name: "claude", Available: true, Version: "2.1.0",
				Models:  []apiclient.AgentOption{{Value: "sonnet", Source: apiclient.OptionSourceCLI}},
				Efforts: []apiclient.AgentOption{{Value: "high", Source: apiclient.OptionSourceCurated}},
			},
			{
				Name: "codex", Available: false, Error: "not found in PATH",
				Models: []apiclient.AgentOption{{Value: "gpt-5", Source: apiclient.OptionSourceCurated}},
			},
		},
	})
	return n
}

func TestNewTaskGuidedLayoutShowsOnlyTheActiveStage(t *testing.T) {
	n := loadedForm(t)
	n.projectID = 1
	n.titleIn.SetValue("Improve takeover UX")
	moveTo(n, ntTitle)
	out := n.render(140, 40)
	for _, want := range []string{"Plan", "3 of 6 · Task details", "title", "description", "fields"} {
		if !strings.Contains(out, want) {
			t.Errorf("guided render is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "base branch") {
		t.Errorf("guided task-details stage also rendered the Git fields:\n%s", out)
	}
}

func TestNewTaskCompactFallbackKeepsTheWholeForm(t *testing.T) {
	n := loadedForm(t)
	out := n.render(120, 40)
	for _, want := range []string{"project", "description", "base branch", "agent", "create task"} {
		if !strings.Contains(out, want) {
			t.Errorf("compact render is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "1 of 6") {
		t.Errorf("compact render contains guided-stage chrome:\n%s", out)
	}
}

func TestNewTaskReviewSummarizesTheRequest(t *testing.T) {
	n := loadedForm(t)
	n.projectID = 1
	n.workflow = "two-step"
	n.titleIn.SetValue("Improve takeover UX")
	n.desc.SetValue("Give each decision room to breathe")
	n.fields = []kv{{key: "ticket", value: "UX-20"}}
	n.branch.SetValue("main")
	n.agent = "claude"
	moveTo(n, ntCreate)
	out := n.render(140, 40)
	for _, want := range []string{
		"6 of 6 · Review", "vincent", "two-step", "Improve takeover UX",
		"ticket=UX-20", "base branch", "claude", "create task",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review is missing %q:\n%s", want, out)
		}
	}
}

func TestNewTaskResizeDoesNotResetTheDraftStage(t *testing.T) {
	n := loadedForm(t)
	n.titleIn.SetValue("kept across resize")
	moveTo(n, ntDescription)
	n.render(140, 40)
	n.render(120, 40)
	if n.cursor != ntDescription || n.titleText() != "kept across resize" {
		t.Errorf("resize changed the draft: cursor=%v title=%q", n.cursor, n.titleText())
	}
}

func TestNewTaskResizeKeepsAnOpenPicker(t *testing.T) {
	n := loadedForm(t)
	moveTo(n, ntWorkflow)
	press(n, "enter")
	pick := n.pick
	if n.mode != ntPicking || pick == nil {
		t.Fatal("workflow picker did not open")
	}
	n.render(140, 40)
	n.render(120, 40)
	if n.mode != ntPicking || n.pick != pick || n.cursor != ntWorkflow {
		t.Errorf("resize changed the picker: mode=%v cursor=%v pick=%p", n.mode, n.cursor, n.pick)
	}
}

func TestNewTaskPrefillsFromTheHintedProject(t *testing.T) {
	n := newNewTask()
	n.hintProject = 2
	n.update(ntLoadedMsg{
		projects: []apiclient.Project{
			{ID: 1, Name: "vincent", DefaultBranch: "main"},
			{ID: 2, Name: "other", DefaultBranch: "trunk", DefaultWorkflow: ptr("two-step")},
		},
		workflows: []apiclient.WorkflowEntry{{Name: "adhoc"}, {Name: "two-step"}},
	})
	if n.projectID != 2 {
		t.Errorf("projectID = %d, want the hinted project 2", n.projectID)
	}
	if got := strings.TrimSpace(n.branch.Value()); got != "trunk" {
		t.Errorf("branch = %q, want that project's default branch", got)
	}
	if n.workflow != "two-step" {
		t.Errorf("workflow = %q, want the project default", n.workflow)
	}
}

func TestNewTaskFallsBackToAdhocWithoutAProjectDefault(t *testing.T) {
	n := loadedForm(t)
	// Only one project is hinted-at; with no hint and two projects the form
	// declines to guess.
	if n.projectID != 0 {
		t.Fatalf("projectID = %d, want no guess with two projects and no hint", n.projectID)
	}
	if n.workflow != apiclient.AdhocWorkflow {
		t.Errorf("workflow = %q, want adhoc", n.workflow)
	}
}

// resolveField is a resolved §8.6 value with its source, spelled short.
func resolveField(value, source string) *apiclient.ResolvedField {
	return &apiclient.ResolvedField{Value: value, Source: source}
}

// feedResolution delivers a daemon resolution for the form's current draft,
// exactly as the command's reply arrives.
func feedResolution(n *newTask, steps ...apiclient.ResolvedStep) {
	n.update(ntResolvedMsg{
		key:        n.resolveKey(),
		resolution: apiclient.Resolution{Workflow: n.workflow, Steps: steps},
	})
}

// twoStepResolution is what POST /v1/resolve says about the two-step
// fixture: the third step names no agent and resolves to claude, and no
// adapter reports a default model, so that field comes back empty.
func twoStepResolution() []apiclient.ResolvedStep {
	return []apiclient.ResolvedStep{
		{
			ID: "implement", Type: "agent",
			Agent: resolveField("claude", apiclient.SourceStep),
			Model: resolveField("sonnet", apiclient.SourceWorkflow),
		},
		{
			ID: "review", Type: "agent",
			Agent: resolveField("codex", apiclient.SourceStep),
			Model: resolveField("", apiclient.SourceAdapter),
		},
		{
			ID: "unset", Type: "agent",
			Agent: resolveField("claude", apiclient.SourceAdapter),
			Model: resolveField("sonnet", apiclient.SourceWorkflow),
		},
		{ID: "publish", Type: "command"},
	}
}

// TestNewTaskNamesTheWorkflowDefaultAgent is a T3.8 finding closed by T4.7:
// "(workflow default)" told nobody which agent would run, and that is a
// spend decision. The names come from the daemon's resolution, so even a
// step naming no agent is reported by name rather than as "adapter default".
func TestNewTaskNamesTheWorkflowDefaultAgent(t *testing.T) {
	n := loadedForm(t)
	n.workflow = "two-step"

	// Before the resolution lands the form says only what it knows.
	if got := n.agentSummary(); strings.Contains(got, "→") {
		t.Errorf("agent summary = %q, want no resolved names before the reply", got)
	}
	feedResolution(n, twoStepResolution()...)

	summary := n.agentSummary()
	if !strings.Contains(summary, "workflow default") {
		t.Fatalf("agent summary = %q, want it to still say the workflow decides", summary)
	}
	for _, want := range []string{"claude", "codex"} {
		if !strings.Contains(summary, want) {
			t.Errorf("agent summary = %q, want it to name %q", summary, want)
		}
	}
	if strings.Contains(summary, "adapter default") {
		t.Errorf("agent summary = %q, want the resolved adapter named, not described", summary)
	}
	// The picker's own default row carries the same names.
	opts := n.agentOptions()
	if len(opts) == 0 || opts[0].value != "" {
		t.Fatal("the first agent option is not the workflow default")
	}
	if !strings.Contains(opts[0].note, "codex") {
		t.Errorf("default option note = %q, want the workflow's agents", opts[0].note)
	}

	// Model names what it resolves to, including the step whose adapter
	// reports no default of its own — that one is the CLI's call at run time.
	model := n.overrideSummary(n.model, apiclient.ModelOf)
	for _, want := range []string{"workflow default", "sonnet", "CLI default"} {
		if !strings.Contains(model, want) {
			t.Errorf("model summary = %q, want it to name %q", model, want)
		}
	}

	// An explicit override replaces it outright — no "(default)" noise.
	n.agent = "claude"
	if got := n.agentSummary(); got != "claude" {
		t.Errorf("agent summary with an override = %q, want just the agent", got)
	}
}

// TestNewTaskDropsAStaleResolution proves the key guard: a reply for a draft
// the user has already moved past must not be rendered, or the form names
// the model of a workflow nobody selected.
func TestNewTaskDropsAStaleResolution(t *testing.T) {
	n := loadedForm(t)
	n.workflow = "two-step"
	stale := ntResolvedMsg{
		key: n.resolveKey(),
		resolution: apiclient.Resolution{Workflow: "two-step", Steps: []apiclient.ResolvedStep{
			{ID: "implement", Type: "agent", Agent: resolveField("codex", apiclient.SourceWorkflow)},
		}},
	}
	n.workflow = "adhoc" // the user moves on while the request is in flight
	n.update(stale)
	if got := n.agentSummary(); strings.Contains(got, "codex") {
		t.Errorf("agent summary = %q, want the stale resolution ignored", got)
	}
}

// TestNewTaskForgetsAFailedResolution: a resolution that errors clears what
// was shown. Keeping the old triple would name a model for a workflow the
// user has since changed.
func TestNewTaskForgetsAFailedResolution(t *testing.T) {
	n := loadedForm(t)
	n.workflow = "two-step"
	feedResolution(n, twoStepResolution()...)
	if !strings.Contains(n.agentSummary(), "codex") {
		t.Fatal("fixture did not take")
	}
	n.update(ntResolvedMsg{key: n.resolveKey(), err: errors.New("daemon said no")})
	if got := n.agentSummary(); strings.Contains(got, "→") {
		t.Errorf("agent summary = %q, want no names after a failed resolution", got)
	}
}

func TestNewTaskFlagsStepsNeedingAnUnavailableAgent(t *testing.T) {
	n := loadedForm(t)
	e := n.workflowEntry("two-step")
	bad := n.unavailableSteps(*e)
	if len(bad) != 1 || !strings.Contains(bad[0], "codex") {
		t.Fatalf("unavailableSteps = %v, want exactly the codex step", bad)
	}
	out := strings.Join(n.renderWorkflowDetail("two-step"), "\n")
	if !strings.Contains(out, "⚠ unavailable") {
		t.Errorf("workflow detail does not flag the codex step:\n%s", out)
	}
	// Without a resolution the step that names no agent is reported, never
	// accused: the registry cannot say what §8.6 level 4 resolves to.
	if !strings.Contains(out, "adapter default") {
		t.Errorf("unset agent not reported as the adapter default:\n%s", out)
	}
	if strings.Count(out, "⚠ unavailable") != 1 {
		t.Errorf("more than one step flagged:\n%s", out)
	}

	// With one, the same step is named — and stays unflagged, because the
	// adapter it resolves to is installed.
	n.workflow = "two-step"
	feedResolution(n, twoStepResolution()...)
	out = strings.Join(n.renderWorkflowDetail("two-step"), "\n")
	if strings.Contains(out, "adapter default") {
		t.Errorf("resolved step still described instead of named:\n%s", out)
	}
	if strings.Count(out, "⚠ unavailable") != 1 {
		t.Errorf("resolution changed which steps are flagged:\n%s", out)
	}
}

// TestNewTaskFlagsAnUnavailableAdapterDefault is what the resolution buys:
// a workflow naming no agent anywhere is now checkable, because the daemon
// says which adapter would run it.
func TestNewTaskFlagsAnUnavailableAdapterDefault(t *testing.T) {
	n := loadedForm(t)
	n.workflow = "two-step"
	feedResolution(n,
		apiclient.ResolvedStep{ID: "implement", Type: "agent", Agent: resolveField("claude", apiclient.SourceStep)},
		apiclient.ResolvedStep{ID: "review", Type: "agent", Agent: resolveField("claude", apiclient.SourceStep)},
		// The step naming no agent falls to an adapter that is not installed.
		apiclient.ResolvedStep{ID: "unset", Type: "agent", Agent: resolveField("codex", apiclient.SourceAdapter)},
		apiclient.ResolvedStep{ID: "publish", Type: "command"},
	)
	bad := n.unavailableSteps(*n.workflowEntry("two-step"))
	if len(bad) != 1 || !strings.Contains(bad[0], "codex") {
		t.Fatalf("unavailableSteps = %v, want the step whose adapter default is missing", bad)
	}
	if !strings.Contains(bad[0], "Whatever the adapter says") {
		t.Errorf("flagged step = %q, want the one that names no agent", bad[0])
	}
}

func TestNewTaskRefusesToSelectAnInvalidWorkflow(t *testing.T) {
	n := loadedForm(t)
	moveTo(n, ntWorkflow)
	press(n, "enter")
	if n.mode != ntPicking {
		t.Fatalf("mode = %v, want the picker open", n.mode)
	}
	// Walk to "busted" and try to take it.
	for i, o := range n.pick.options {
		if o.value == "busted" {
			n.pick.cursor = i
		}
	}
	press(n, "enter")
	if n.mode != ntPicking {
		t.Error("the picker closed on an invalid workflow")
	}
	if n.workflow == "busted" {
		t.Error("an invalid workflow was selected; create would 400")
	}
	if !strings.Contains(n.pick.err, "at least one step") {
		t.Errorf("picker error = %q, want the workflow's own finding", n.pick.err)
	}
}

// A workflow restricted to other platforms (§8.1, task 010) is listed with
// the reason and refused, the way an invalid one is: the daemon would 400 it,
// and "it vanished" is a worse answer than "it needs linux".
func TestNewTaskRefusesAWorkflowThatDoesNotRunHere(t *testing.T) {
	n := newNewTask()
	no := false
	n.update(ntLoadedMsg{
		projects: []apiclient.Project{{ID: 1, Name: "vincent", DefaultBranch: "main"}},
		workflows: []apiclient.WorkflowEntry{
			{Name: "adhoc", Scope: "builtin"},
			{
				Name: "posix-tools", Scope: "global",
				Platforms: []string{"linux", "darwin"}, PlatformSupported: &no,
			},
		},
	})
	// The default lands on a workflow that can actually run.
	if n.workflow != "adhoc" {
		t.Errorf("default workflow = %q, want the one that runs here", n.workflow)
	}

	var restricted pickerOption
	for _, o := range n.workflowOptions() {
		if o.value == "posix-tools" {
			restricted = o
		}
	}
	if restricted.value == "" {
		t.Fatal("the restricted workflow is not listed at all")
	}
	if !restricted.disabled {
		t.Error("the restricted workflow is selectable; create would 400")
	}
	if !strings.Contains(restricted.note, "linux, darwin") {
		t.Errorf("note = %q, want the platforms it needs", restricted.note)
	}

	// Reaching it another way (a project default, say) still fails locally
	// rather than travelling to the daemon.
	n.workflow = "posix-tools"
	n.titleIn.SetValue("something")
	if cmd := n.submit(); cmd != nil {
		t.Error("submit posted a task the daemon would refuse")
	}
	if got := n.rowErr[ntWorkflow]; !strings.Contains(got, "platform") {
		t.Errorf("row error = %q, want it to name the platform restriction", got)
	}
}

func TestNewTaskAgentSwitchResetsModelAndEffort(t *testing.T) {
	n := loadedForm(t)
	n.applyPick(ntAgent, "claude")
	n.applyPick(ntModel, "sonnet")
	n.applyPick(ntEffort, "high")
	n.applyPick(ntAgent, "codex")
	if n.model != "" || n.effort != "" {
		t.Errorf("model/effort = %q/%q after switching agent; §8.6 forbids the carry-over",
			n.model, n.effort)
	}
	// And the catalog the picker now offers is codex's, not claude's.
	n.openPicker(ntModel)
	var values []string
	for _, o := range n.pick.options {
		values = append(values, o.value)
	}
	if !contains(values, "gpt-5") || contains(values, "sonnet") {
		t.Errorf("model options = %v, want codex's catalog", values)
	}
}

func TestNewTaskModelPickerTakesFreeText(t *testing.T) {
	n := loadedForm(t)
	n.applyPick(ntAgent, "claude")
	moveTo(n, ntModel)
	press(n, "enter")
	if !n.pick.allowFree {
		t.Fatal("the model picker offers no free text; a catalog is a suggestion")
	}
	n.pick.startFree()
	typeText(n, "opus-tomorrow")
	press(n, "enter")
	if n.model != "opus-tomorrow" {
		t.Errorf("model = %q, want the typed value", n.model)
	}
}

func TestNewTaskRequestOmitsWhatWasNeverTouched(t *testing.T) {
	n := loadedForm(t)
	n.applyPick(ntProject, "1")
	moveTo(n, ntTitle)
	press(n, "enter")
	typeText(n, "ship it")
	press(n, "enter")
	req := n.request()
	if req.ProjectID != 1 || req.Title != "ship it" {
		t.Fatalf("req = %+v", req)
	}
	for name, p := range map[string]any{
		"description": req.Description, "priority": req.Priority,
		"agent": req.Agent, "model": req.Model, "effort": req.Effort,
	} {
		if !isNil(p) {
			t.Errorf("%s was sent though nobody set it; the daemon's own default is lost", name)
		}
	}
	if req.Fields != nil {
		t.Error("empty fields map sent")
	}
	// The branch is prefilled from the project, so it *is* sent — as the
	// value the human saw and accepted.
	if req.BaseBranch == nil || *req.BaseBranch != "main" {
		t.Errorf("base_branch = %v, want the prefilled main", req.BaseBranch)
	}
}

func isNil(v any) bool {
	switch p := v.(type) {
	case *string:
		return p == nil
	case *int:
		return p == nil
	}
	return v == nil
}

func TestNewTaskBlocksCreateOnWhatItCanDecideAlone(t *testing.T) {
	n := loadedForm(t)
	if cmd := n.submit(); cmd != nil {
		t.Fatal("submit fired with no project and no title")
	}
	if n.rowErr[ntProject] == "" || n.rowErr[ntTitle] == "" {
		t.Errorf("rowErr = %v, want both the project and the title flagged", n.rowErr)
	}
	// A base branch that does not exist is *not* blocked here: only the
	// daemon can know, and a second implementation would drift from it.
	n.applyPick(ntProject, "1")
	moveTo(n, ntTitle)
	press(n, "enter")
	typeText(n, "t")
	press(n, "enter")
	n.branch.SetValue("no-such-branch")
	n.client = nil // stop short of the network
	n.submit()
	if n.rowErr[ntBranch] != "" {
		t.Errorf("branch pre-checked client-side: %q", n.rowErr[ntBranch])
	}
	if n.err != "not connected" {
		t.Errorf("err = %q, want the submit to have got past validation", n.err)
	}
}

func TestNewTaskParksTheDaemonsComplaintOnTheRowItNames(t *testing.T) {
	n := loadedForm(t)
	n.update(ntFailedMsg{err: &apiclient.Error{
		Status: 400, Code: "validation_failed",
		Message: `base_branch "nope" does not resolve to a local branch in /src/vincent`,
	}})
	if n.rowErr[ntBranch] == "" {
		t.Fatalf("rowErr = %v, want the message on the branch row", n.rowErr)
	}
	if n.cursor != ntBranch {
		t.Errorf("cursor = %v, want it moved to the offending row", n.cursor)
	}
	if n.submitting {
		t.Error("still submitting after a rejection")
	}
	out := n.render(140, 60)
	if !strings.Contains(out, "does not resolve") {
		t.Errorf("the daemon's message is not on screen:\n%s", out)
	}
}

func TestNewTaskUnroutableFailureBecomesAFormError(t *testing.T) {
	n := loadedForm(t)
	n.update(ntFailedMsg{err: errors.New("dial tcp: connection refused")})
	if len(n.rowErr) != 0 {
		t.Errorf("rowErr = %v, want nothing pinned to a row", n.rowErr)
	}
	if !strings.Contains(n.err, "connection refused") {
		t.Errorf("err = %q", n.err)
	}
}

func TestNewTaskFieldsEditorOverwritesADuplicateKey(t *testing.T) {
	n := loadedForm(t)
	moveTo(n, ntFields)
	press(n, "enter") // open the editor
	if n.mode != ntFieldsOpen {
		t.Fatalf("mode = %v, want the fields editor", n.mode)
	}
	addField(n, "ticket", "OPS-1")
	addField(n, "owner", "ana")
	addField(n, "ticket", "OPS-2")
	if !strings.Contains(n.fieldsEd.err, "already set") {
		t.Errorf("no warning on the duplicate: %q", n.fieldsEd.err)
	}
	press(n, "esc")
	got := n.fieldMap()
	want := map[string]string{"ticket": "OPS-2", "owner": "ana"}
	if len(got) != len(want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("fields[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestNewTaskFieldsEditorDropsAnAbandonedRow(t *testing.T) {
	n := loadedForm(t)
	moveTo(n, ntFields)
	press(n, "enter")
	press(n, "a") // starts a row, then walks away without naming it
	press(n, "esc")
	press(n, "esc")
	if len(n.fields) != 0 {
		t.Errorf("fields = %v, want the keyless row dropped", n.fields)
	}
}

func TestNewTaskCapturesInputOnlyWhileTyping(t *testing.T) {
	n := loadedForm(t)
	if n.capturesInput() {
		t.Error("capturing while merely navigating; the global keys would be dead")
	}
	moveTo(n, ntTitle)
	press(n, "enter")
	if !n.capturesInput() {
		t.Error("not capturing while a text row is in edit mode; typing q would quit")
	}
	press(n, "esc")
	if n.capturesInput() {
		t.Error("still capturing after leaving the field")
	}
	if n.mode != ntNavigating {
		t.Errorf("mode = %v, want navigating", n.mode)
	}
}

func TestNewTaskEscOnAnUntouchedDraftJustLeaves(t *testing.T) {
	n := loadedForm(t)
	cmd := press(n, "esc")
	if cmd == nil {
		t.Fatal("esc on a clean form did nothing")
	}
	if msg, ok := cmd().(selectViewMsg); !ok || msg.id != viewHome {
		t.Errorf("esc = %#v, want a switch back to the board", cmd())
	}
	if n.mode == ntConfirming {
		t.Error("an untouched draft asked for confirmation")
	}
}

func TestNewTaskEscOnATouchedDraftAsksFirst(t *testing.T) {
	n := loadedForm(t)
	moveTo(n, ntTitle)
	press(n, "enter")
	typeText(n, "half a thought")
	press(n, "esc") // leave the field
	press(n, "esc") // leave the form
	if n.mode != ntConfirming {
		t.Fatalf("mode = %v, want the discard confirmation", n.mode)
	}
	if !strings.Contains(n.render(140, 60), "discard this draft?") {
		t.Error("the confirmation is not on screen")
	}
	press(n, "n")
	if n.mode != ntNavigating || n.titleText() != "half a thought" {
		t.Errorf("declining the discard lost the draft: mode=%v title=%q", n.mode, n.titleText())
	}
	press(n, "esc")
	cmd := press(n, "y")
	if cmd == nil {
		t.Fatal("confirming the discard did not leave")
	}
	if n.titleText() != "" {
		t.Errorf("title = %q after discarding", n.titleText())
	}
}

func TestNewTaskPriorityNudges(t *testing.T) {
	n := loadedForm(t)
	moveTo(n, ntPriority)
	press(n, "+")
	press(n, "+")
	press(n, "-")
	if got := n.priorityValue(); got != 1 {
		t.Errorf("priority = %d, want 1", got)
	}
	// Zero is the default and carries no meaning on the wire, so it is not
	// sent; a set priority is.
	n.priority.SetValue("0")
	if n.request().Priority != nil {
		t.Error("priority 0 sent")
	}
	n.priority.SetValue("5")
	if p := n.request().Priority; p == nil || *p != 5 {
		t.Errorf("priority = %v, want 5", p)
	}
}

// fakeExec drives the $EDITOR path without a terminal: it runs the callback
// with the error the editor would have returned, after mutating the file the
// way an editor would.
func fakeExec(t *testing.T, edit func(path string) error, runErr error) execFunc {
	t.Helper()
	return func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		path := c.Args[len(c.Args)-1]
		if edit != nil {
			if err := edit(path); err != nil {
				t.Fatalf("edit %s: %v", path, err)
			}
		}
		return func() tea.Msg { return fn(runErr) }
	}
}

func TestNewTaskDescriptionThroughEditor(t *testing.T) {
	n := loadedForm(t)
	n.exec = fakeExec(t, func(path string) error {
		return os.WriteFile(path, []byte("# written in vi\n\nbody"), 0o600)
	}, nil)
	moveTo(n, ntDescription)
	cmd := press(n, "e")
	if cmd == nil {
		t.Fatal("e did not open an editor")
	}
	n.update(cmd())
	if !strings.Contains(n.desc.Value(), "written in vi") {
		t.Errorf("description = %q, want the edited text", n.desc.Value())
	}
	if d := n.request().Description; d == nil || !strings.Contains(*d, "written in vi") {
		t.Errorf("description not on the wire: %v", d)
	}
}

func TestNewTaskEditorFailureLeavesTheDraftAlone(t *testing.T) {
	n := loadedForm(t)
	n.desc.SetValue("typed by hand")
	n.exec = fakeExec(t, nil, errors.New("exec: \"vi\": executable file not found"))
	moveTo(n, ntDescription)
	cmd := press(n, "e")
	n.update(cmd())
	if n.desc.Value() != "typed by hand" {
		t.Errorf("description = %q, want it untouched by the failed editor", n.desc.Value())
	}
	if !strings.Contains(n.err, "not found") {
		t.Errorf("err = %q, want the editor failure reported", n.err)
	}
}

func TestNewTaskEditorSeedsTheFileWithWhatIsTyped(t *testing.T) {
	n := loadedForm(t)
	var seen string
	n.exec = fakeExec(t, func(path string) error {
		b, err := os.ReadFile(path) //nolint:gosec // the test's own temp file
		seen = string(b)
		return err
	}, nil)
	n.desc.SetValue("first draft")
	moveTo(n, ntDescription)
	n.update(press(n, "e")())
	if seen != "first draft" {
		t.Errorf("editor opened on %q, want the current draft", seen)
	}
}

func TestNewTaskOpeningResetsTheDraft(t *testing.T) {
	n := loadedForm(t)
	moveTo(n, ntTitle)
	press(n, "enter")
	typeText(n, "yesterday's idea")
	press(n, "enter")
	n.fields = []kv{{key: "ticket", value: "OPS-1"}}
	n.open(0)
	if n.titleText() != "" || len(n.fields) != 0 || n.loaded {
		t.Errorf("draft survived a reopen: title=%q fields=%v loaded=%v",
			n.titleText(), n.fields, n.loaded)
	}
}

func TestNewTaskWarningsReachTheDetailView(t *testing.T) {
	d := newTestDetail(t)
	d.update(taskCreatedMsg{task: apiclient.TaskDetail{
		Task:     apiclient.Task{ID: 7},
		Warnings: []string{`model "opus-tomorrow" is not in claude's catalog`},
	}})
	if !strings.Contains(d.actions.status, "opus-tomorrow") {
		t.Errorf("status = %q, want the 201's warning", d.actions.status)
	}
	if d.actions.statusBad {
		t.Error("the warning renders as an error; the task exists and will run")
	}
}

// An agent that cannot stop and ask is listed but not selectable while the
// chosen workflow requires one (§7.4, task 013) — the same posture as a
// foreign-platform workflow, and for the same reason: the daemon would 400 it,
// and "codex vanished" is a worse answer than "codex cannot answer questions".
func TestNewTaskRefusesAnAgentThatCannotTakeInput(t *testing.T) {
	n := newNewTask()
	n.update(ntLoadedMsg{
		projects: []apiclient.Project{{ID: 1, Name: "vincent", DefaultBranch: "main"}},
		workflows: []apiclient.WorkflowEntry{
			{Name: "interactive", Scope: "global", RequiresInput: true},
		},
		agents: apiclient.Agents{
			{Name: "claude", Available: true, InputVerdict: apiclient.InputVerdictSupported},
			{Name: "codex", Available: true, InputVerdict: apiclient.InputVerdictUnsupported},
			{Name: "cursor", InputVerdict: apiclient.InputVerdictUnknown},
		},
	})
	n.workflow = "interactive"

	opts := map[string]pickerOption{}
	for _, o := range n.agentOptions() {
		opts[o.value] = o
	}
	if opts["codex"].value == "" {
		t.Fatal("the incapable agent is not listed at all")
	}
	if !opts["codex"].disabled {
		t.Error("the incapable agent is selectable; create would 400")
	}
	if !strings.Contains(opts["codex"].note, "questions") {
		t.Errorf("note = %q, want the capability it lacks", opts["codex"].note)
	}
	if opts["claude"].disabled {
		t.Error("the capable agent is disabled")
	}
	// Decision 5's asymmetry reaches the picker too: an unknown verdict is not
	// evidence, so an uninstalled adapter stays selectable.
	if opts["cursor"].disabled {
		t.Error("an unknown verdict disabled an agent; only a positive no refuses")
	}

	// Submitting with the incapable agent reports on the agent row rather than
	// waiting for the daemon's 400.
	n.agent = "codex"
	n.titleIn.SetValue("t")
	n.projectID = 1
	n.submit()
	if !strings.Contains(n.rowErr[ntAgent], "codex") {
		t.Errorf("agent row error = %q, want one naming codex", n.rowErr[ntAgent])
	}
}

// With a workflow that requires nothing, the same agent list is unconstrained.
func TestNewTaskAgentPickerUnconstrainedWithoutRequire(t *testing.T) {
	n := newNewTask()
	n.update(ntLoadedMsg{
		projects:  []apiclient.Project{{ID: 1, Name: "vincent", DefaultBranch: "main"}},
		workflows: []apiclient.WorkflowEntry{{Name: "adhoc", Scope: "builtin"}},
		agents: apiclient.Agents{
			{Name: "codex", Available: true, InputVerdict: apiclient.InputVerdictUnsupported},
		},
	})
	for _, o := range n.agentOptions() {
		if o.value == "codex" && o.disabled {
			t.Error("codex disabled for a workflow that never asks a question")
		}
	}
}
