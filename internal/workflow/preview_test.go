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

// TestPreviewStepsWalksDerivedLane is issue #370's first half: a derived
// fan-out (§7.6, task 080) carries its lane as a single `lane:` template
// beside `for_each:`, and that template's inline steps carry prompts and
// `run:` bodies that fail at run time exactly like any other step's. The walk
// must reach them — and bind their ids in `.Steps` — or the dry run shows
// nothing for the part of the workflow most likely to need one. A named
// `lane:` template cannot be looked up offline, so it is reported unresolved
// the way a named declared lane is.
func TestPreviewStepsWalksDerivedLane(t *testing.T) {
	wf := &Workflow{
		Name: "wf",
		Steps: []Step{
			{ID: "plan", Type: StepAgent, Prompt: "x"},
			{
				ID: "build", Type: StepFanOut, MaxLanes: intp(8), Schedule: ScheduleEager,
				ForEach: ForEach{"{{ .Steps.plan.Result }}"},
				Lane: &Lane{
					ID: "{{ .Item.id }}", Needs: LaneNeeds{"{{ .Item.needs }}"},
					Steps: []Step{{ID: "implement", Type: StepCommand, Run: "make {{ .Task.Title }}"}},
				},
			},
			{
				ID: "named", Type: StepFanOut,
				ForEach: ForEach{"{{ .Steps.plan.Result }}"},
				Lane:    &Lane{ID: "{{ .Item.id }}", Workflow: "implement-module"},
			},
		},
	}

	var implement, named *PreviewStep
	steps := PreviewSteps(wf)
	for i := range steps {
		switch steps[i].Path {
		case "steps[1].lane.steps[0]":
			implement = &steps[i]
		case "steps[2].lane":
			named = &steps[i]
		}
	}
	if implement == nil || implement.Step.ID != "implement" {
		t.Errorf("the derived lane template's inline step was not walked: %+v", steps)
	} else if implement.Unresolved != "" {
		t.Errorf("an inline lane template step is present in the file, yet unresolved: %q", implement.Unresolved)
	}
	if named == nil || named.Unresolved == "" {
		t.Errorf("a named lane template was not reported unresolved: %+v", steps)
	}

	rc := NewPreviewContext(wf, PreviewInput{})
	if _, ok := rc.Steps["implement"]; !ok {
		t.Errorf(".Steps has no entry for the derived lane's step: %v", rc.Steps)
	}
}

// TestPreviewItemBindsItemReads: a derived lane template's own fields read
// `.Item`, a JSON object only the run has. Every key chain they spell out —
// nested, or through `$` — binds to a sentinel, so they render; a typo in
// `.Task` beside them still fails, because only `.Item` is stood in for.
func TestPreviewItemBindsItemReads(t *testing.T) {
	lane := Lane{
		ID:     "{{ .Item.id }}",
		If:     `{{ if $.Item.enabled }}true{{ else }}false{{ end }}`,
		Needs:  LaneNeeds{"{{ .Item.needs }}"},
		Fields: map[string]string{"owner": "{{ .Item.meta.owner }}"},
	}
	item := PreviewItem(lane)
	rc := NewPreviewContext(&Workflow{Name: "wf"}, PreviewInput{})

	for text, want := range map[string]string{
		lane.ID:              SentinelItem("id"),
		lane.If:              "true",
		lane.Needs[0]:        SentinelItem("needs"),
		lane.Fields["owner"]: SentinelItem("meta.owner"),
	} {
		got, err := RenderLane("lane", text, rc, item)
		if err != nil {
			t.Errorf("%s: %v", text, err)
		} else if got != want {
			t.Errorf("%s rendered %q, want %q", text, got, want)
		}
	}

	if _, err := RenderLane("lane", "{{ .Item.id }} {{ .Task.Titel }}", rc, item); err == nil {
		t.Error("a .Task typo beside a bound .Item read rendered clean")
	}
}

func intp(v int) *int { return &v }
