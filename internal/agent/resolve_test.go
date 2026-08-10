package agent

import "testing"

// TestResolve walks the §8.6 precedence ladder and the agent-scoped
// inheritance rule that keeps a claude alias away from codex. Moved from
// taskrun with the resolver (T2.11); taskrun keeps a mapping test for its
// wrapper.
//
// Every case also pins the *source* of each field (T4.7): the resolution
// endpoint reports which level won, and a refactor that quietly re-attributes
// a value would otherwise ship a form that misexplains a spend decision.
func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		step     Level
		override Level
		defaults Level
		want     Selection
		wantSrc  Sources
	}{
		{
			name:    "nothing declared falls back to the daemon default agent",
			want:    Selection{Agent: DefaultAgent},
			wantSrc: Sources{SourceAdapter, SourceAdapter, SourceAdapter},
		},
		{
			name:     "workflow defaults apply",
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "low"},
			want:     Selection{Agent: "claude", Model: "sonnet", Effort: "low"},
			wantSrc:  Sources{SourceWorkflow, SourceWorkflow, SourceWorkflow},
		},
		{
			name:     "task override replaces workflow defaults",
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "low"},
			override: Level{Agent: "claude", Model: "opus", Effort: "max"},
			want:     Selection{Agent: "claude", Model: "opus", Effort: "max"},
			wantSrc:  Sources{SourceTask, SourceTask, SourceTask},
		},
		{
			name:     "explicit step fields beat the task override",
			step:     Level{Agent: "claude", Model: "haiku", Effort: "low"},
			defaults: Level{Agent: "claude", Model: "sonnet"},
			override: Level{Agent: "claude", Model: "opus", Effort: "max"},
			want:     Selection{Agent: "claude", Model: "haiku", Effort: "low"},
			wantSrc:  Sources{SourceStep, SourceStep, SourceStep},
		},
		{
			name:     "a step pinning another agent does not inherit its model",
			step:     Level{Agent: "codex"},
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "max"},
			want:     Selection{Agent: "codex"},
			wantSrc:  Sources{SourceStep, SourceAdapter, SourceAdapter},
		},
		{
			name:     "a step pinning another agent keeps its own model and effort",
			step:     Level{Agent: "codex", Effort: "high"},
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "max"},
			want:     Selection{Agent: "codex", Effort: "high"},
			wantSrc:  Sources{SourceStep, SourceAdapter, SourceStep},
		},
		{
			name:     "a task override switching agent drops the workflow model",
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "max"},
			override: Level{Agent: "codex"},
			want:     Selection{Agent: "codex"},
			wantSrc:  Sources{SourceTask, SourceAdapter, SourceAdapter},
		},
		{
			name:     "a task override switching agent carries its own model",
			defaults: Level{Agent: "claude", Model: "sonnet"},
			override: Level{Agent: "codex", Model: "gpt-5.2"},
			want:     Selection{Agent: "codex", Model: "gpt-5.2"},
			wantSrc:  Sources{SourceTask, SourceTask, SourceAdapter},
		},
		{
			name:     "a model-only task override rides the workflow agent",
			defaults: Level{Agent: "claude", Model: "sonnet"},
			override: Level{Model: "opus"},
			want:     Selection{Agent: "claude", Model: "opus"},
			wantSrc:  Sources{SourceWorkflow, SourceTask, SourceAdapter},
		},
		{
			name:     "a model-only task override does not reach a step that switched agent",
			step:     Level{Agent: "codex"},
			defaults: Level{Agent: "claude"},
			override: Level{Model: "opus"},
			want:     Selection{Agent: "codex"},
			wantSrc:  Sources{SourceStep, SourceAdapter, SourceAdapter},
		},
		{
			name:     "workflow defaults fill what the task override leaves unset",
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "low"},
			override: Level{Effort: "max"},
			want:     Selection{Agent: "claude", Model: "sonnet", Effort: "max"},
			wantSrc:  Sources{SourceWorkflow, SourceWorkflow, SourceTask},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.step, tt.override, tt.defaults)
			if got != tt.want {
				t.Errorf("Resolve = %+v, want %+v", got, tt.want)
			}
			// Resolve is the thin wrapper, so the two must never disagree.
			detailed, src := ResolveWithSources(tt.step, tt.override, tt.defaults)
			if detailed != got {
				t.Errorf("ResolveWithSources selection = %+v, Resolve = %+v", detailed, got)
			}
			if src != tt.wantSrc {
				t.Errorf("sources = %+v, want %+v", src, tt.wantSrc)
			}
		})
	}
}
