package workflow

import (
	"runtime"
	"sort"
	"strings"
	"testing"
)

// renderAll executes every template body a workflow declares against the
// preview context, the way `vincent workflow render` does, and returns the
// first failure. It is the assertion behind the corpus tests below.
func renderAll(t *testing.T, wf *Workflow) {
	t.Helper()
	base := NewPreviewContext(wf, PreviewInput{})
	for _, ps := range PreviewSteps(wf) {
		if ps.Unresolved != "" {
			continue
		}
		rc := base
		rc.Step = StepContext{ID: ps.Step.ID, Name: ps.Step.DisplayName(), Index: ps.Index, Attempt: 1}
		if ps.InLoop {
			rc.Loop = PreviewLoop()
		}
		if ps.Conflicts {
			rc.Conflicts = []string{SentinelConflict}
		}
		bodies := map[string]string{
			"if": ps.Step.If, "prompt": ps.Step.Prompt, "run": ps.Step.Run,
			"instructions": ps.Step.Instructions, "check": ps.Step.Check,
		}
		for i, item := range ps.Step.ForEach {
			bodies["for_each"+string(rune('0'+i))] = item
		}
		for _, lane := range ps.Step.Lanes {
			bodies["lane.if:"+lane.ID] = lane.If
		}
		for field, text := range bodies {
			if text == "" {
				continue
			}
			if _, err := Render(field, text, rc); err != nil {
				t.Errorf("%s (%s): %s does not render: %v", ps.Path, ps.Step.ID, field, err)
			}
		}
	}
}

// TestPreviewContextSentinels: every run-only value binds to a visible
// placeholder rather than to empty, so a preview never reads as the literal
// prompt an agent will receive.
func TestPreviewContextSentinels(t *testing.T) {
	rc := NewPreviewContext(&Workflow{Name: "wf"}, PreviewInput{})
	for name, got := range map[string]string{
		"title":       rc.Task.Title,
		"description": rc.Task.Description,
		"branch":      rc.Task.BranchName,
		"base branch": rc.Task.BaseBranch,
		"project":     rc.Project.Name,
		"worktree":    rc.Worktree.Path,
		"failure":     rc.LastFailure.Reason,
	} {
		if !strings.HasPrefix(got, "<") || !strings.HasSuffix(got, ">") {
			t.Errorf("%s bound to %q, want a <sentinel>", name, got)
		}
	}
	if rc.Task.ID != 0 {
		t.Errorf("Task.ID = %d, want 0", rc.Task.ID)
	}
	if rc.Issue.Number != 0 {
		t.Errorf("Issue.Number = %d, want 0 so {{ if .Issue.Number }} takes the unlinked branch", rc.Issue.Number)
	}
	if rc.Loop.Index != 0 {
		t.Errorf("Loop.Index = %d, want 0 outside a loop", rc.Loop.Index)
	}
	if len(rc.Conflicts) != 0 {
		t.Errorf("Conflicts = %v, want empty outside a merge resolver", rc.Conflicts)
	}
	// .Host is the real host: there is no daemon to ask offline, so the
	// running machine is the only honest answer.
	if rc.Host.OS != runtime.GOOS || rc.Host.Arch != runtime.GOARCH {
		t.Errorf("Host = %+v, want the running host", rc.Host)
	}
}

// TestPreviewFieldsBindWhenRequired: a declared required field binds, because
// POST /v1/tasks guarantees a real task carries it; an optional or undeclared
// one stays absent so a non-defensive read is the error §8.4 says it is.
func TestPreviewFieldsBindWhenRequired(t *testing.T) {
	wf := &Workflow{Fields: []FieldDefinition{
		{Name: "ticket", Required: true},
		{Name: "note"},
	}}
	rc := NewPreviewContext(wf, PreviewInput{})
	if got := rc.Task.Fields["ticket"]; got != SentinelField("ticket") {
		t.Errorf("required field bound to %q, want %q", got, SentinelField("ticket"))
	}
	if _, ok := rc.Task.Fields["note"]; ok {
		t.Error("optional declared field is bound; it must stay absent")
	}

	// A supplied value wins over the sentinel.
	rc = NewPreviewContext(wf, PreviewInput{
		Task: TaskContext{Fields: map[string]string{"ticket": "ABC-1"}},
	})
	if got := rc.Task.Fields["ticket"]; got != "ABC-1" {
		t.Errorf("supplied field = %q, want ABC-1", got)
	}
}

// TestPreviewStepsWalksNestedBodies: `.Steps` carries one entry per declared
// step id, including a group's members, a loop body and an inline lane's
// steps — so a typo'd id fails the preview the way a typo'd field does.
func TestPreviewStepsWalksNestedBodies(t *testing.T) {
	wf := &Workflow{
		Name: "wf",
		Steps: []Step{
			{ID: "plan", Type: StepAgent, Prompt: "x"},
			{ID: "group", Type: StepParallel, Steps: []Step{
				{ID: "lint", Type: StepCommand, Run: "x"},
				{ID: "test", Type: StepCommand, Run: "x"},
			}},
			{ID: "again", Type: StepLoop, Count: intp(2), Steps: []Step{
				{ID: "body", Type: StepCommand, Run: "x"},
			}},
			{ID: "spread", Type: StepFanOut, Lanes: []Lane{
				{ID: "one", Steps: []Step{{ID: "lane-step", Type: StepCommand, Run: "x"}}},
				{ID: "two", Workflow: "checks"},
			}},
		},
	}
	rc := NewPreviewContext(wf, PreviewInput{})
	var ids []string
	for id := range rc.Steps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	want := []string{"again", "body", "group", "lane-step", "lint", "plan", "spread", "test"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf(".Steps ids = %v, want %v", ids, want)
	}
	if got := rc.Steps["plan"].Result; got != SentinelStep("plan", "result") {
		t.Errorf("Steps.plan.Result = %q, want %q", got, SentinelStep("plan", "result"))
	}
	// The named lane resolved to nothing offline, so it contributes no id and
	// is reported instead.
	var unresolved int
	for _, ps := range PreviewSteps(wf) {
		if ps.Unresolved != "" {
			unresolved++
		}
	}
	if unresolved != 1 {
		t.Errorf("unresolved nodes = %d, want 1 (the named lane)", unresolved)
	}
}

// TestPreviewLoopAndConflicts: a loop body renders as the first iteration and
// a merge resolver sees one conflict, which is what makes those two prompts
// previewable at all.
func TestPreviewLoopAndConflicts(t *testing.T) {
	wf := &Workflow{
		Name: "wf",
		Steps: []Step{
			{ID: "again", Type: StepLoop, Count: intp(2), Steps: []Step{
				{ID: "body", Type: StepAgent, Prompt: "{{ .Loop.Item }} {{ .Loop.Index }}"},
			}},
			{
				ID: "spread", Type: StepFanOut,
				Lanes: []Lane{{ID: "one", Steps: []Step{{ID: "l", Type: StepCommand, Run: "x"}}}},
				Merge: &Merge{OnConflict: ConflictAgent, Agent: &Step{
					ID: "fix", Type: StepAgent, Prompt: "{{ range .Conflicts }}{{ . }}{{ end }}",
				}},
			},
		},
	}
	var body, resolver PreviewStep
	for _, ps := range PreviewSteps(wf) {
		switch ps.Step.ID {
		case "body":
			body = ps
		case "fix":
			resolver = ps
		}
	}
	if !body.InLoop {
		t.Error("loop body step is not marked InLoop")
	}
	if !resolver.Conflicts {
		t.Error("merge resolver step is not marked Conflicts")
	}
	renderAll(t, wf)
}

// TestBuiltinsRender holds the three built-in workflows to the bar CLAUDE.md
// sets for them: their own templates must execute, not merely parse.
func TestBuiltinsRender(t *testing.T) {
	for name, src := range builtinSources {
		t.Run(name, func(t *testing.T) {
			wf, _, err := Parse([]byte(src), curatedOptions())
			if err != nil {
				t.Fatalf("built-in does not parse: %v", err)
			}
			renderAll(t, wf)
		})
	}
}

func intp(v int) *int { return &v }
