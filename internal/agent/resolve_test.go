package agent

import "testing"

// TestResolve walks the §8.6 precedence ladder and the agent-scoped
// inheritance rule that keeps a claude alias away from codex. Moved from
// taskrun with the resolver (T2.11); taskrun keeps a mapping test for its
// wrapper.
func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		step     Level
		override Level
		defaults Level
		want     Selection
	}{
		{
			name: "nothing declared falls back to the daemon default agent",
			want: Selection{Agent: DefaultAgent},
		},
		{
			name:     "workflow defaults apply",
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "low"},
			want:     Selection{Agent: "claude", Model: "sonnet", Effort: "low"},
		},
		{
			name:     "task override replaces workflow defaults",
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "low"},
			override: Level{Agent: "claude", Model: "opus", Effort: "max"},
			want:     Selection{Agent: "claude", Model: "opus", Effort: "max"},
		},
		{
			name:     "explicit step fields beat the task override",
			step:     Level{Agent: "claude", Model: "haiku", Effort: "low"},
			defaults: Level{Agent: "claude", Model: "sonnet"},
			override: Level{Agent: "claude", Model: "opus", Effort: "max"},
			want:     Selection{Agent: "claude", Model: "haiku", Effort: "low"},
		},
		{
			name:     "a step pinning another agent does not inherit its model",
			step:     Level{Agent: "codex"},
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "max"},
			want:     Selection{Agent: "codex"},
		},
		{
			name:     "a step pinning another agent keeps its own model and effort",
			step:     Level{Agent: "codex", Effort: "high"},
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "max"},
			want:     Selection{Agent: "codex", Effort: "high"},
		},
		{
			name:     "a task override switching agent drops the workflow model",
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "max"},
			override: Level{Agent: "codex"},
			want:     Selection{Agent: "codex"},
		},
		{
			name:     "a task override switching agent carries its own model",
			defaults: Level{Agent: "claude", Model: "sonnet"},
			override: Level{Agent: "codex", Model: "gpt-5.2"},
			want:     Selection{Agent: "codex", Model: "gpt-5.2"},
		},
		{
			name:     "a model-only task override rides the workflow agent",
			defaults: Level{Agent: "claude", Model: "sonnet"},
			override: Level{Model: "opus"},
			want:     Selection{Agent: "claude", Model: "opus"},
		},
		{
			name:     "a model-only task override does not reach a step that switched agent",
			step:     Level{Agent: "codex"},
			defaults: Level{Agent: "claude"},
			override: Level{Model: "opus"},
			want:     Selection{Agent: "codex"},
		},
		{
			name:     "workflow defaults fill what the task override leaves unset",
			defaults: Level{Agent: "claude", Model: "sonnet", Effort: "low"},
			override: Level{Effort: "max"},
			want:     Selection{Agent: "claude", Model: "sonnet", Effort: "max"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.step, tt.override, tt.defaults)
			if got != tt.want {
				t.Errorf("Resolve = %+v, want %+v", got, tt.want)
			}
		})
	}
}
