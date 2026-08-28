package apiclient

import "testing"

// The four shapes a task's workflow origin renders as (task 043). A client
// never composes these itself: the nil case is a real one — a task created
// before migration 0017 — and it must read as "not recorded", never as a
// guessed scope.
func TestWorkflowOriginRendering(t *testing.T) {
	parent := int64(41)
	digest := "sha256:0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e0f4a1c1e"
	for _, tc := range []struct {
		name          string
		origin        *WorkflowOrigin
		source, whole string
	}{
		{
			name:   "unrecorded",
			origin: nil,
			source: "unknown", whole: "unknown",
		},
		{
			name:   "builtin",
			origin: &WorkflowOrigin{Scope: "builtin", Digest: digest},
			source: "built-in", whole: "built-in " + digest,
		},
		{
			name:   "project",
			origin: &WorkflowOrigin{Scope: "project", File: ".vincent/workflows/adhoc.yaml", Digest: digest},
			source: "project .vincent/workflows/adhoc.yaml",
			whole:  "project .vincent/workflows/adhoc.yaml " + digest,
		},
		{
			name:   "global",
			origin: &WorkflowOrigin{Scope: "global", File: "workflows/release.yaml", Digest: digest},
			source: "global workflows/release.yaml",
			whole:  "global workflows/release.yaml " + digest,
		},
		{
			name:   "derived",
			origin: &WorkflowOrigin{Scope: "derived", ParentTaskID: &parent},
			source: "derived from task 41", whole: "derived from task 41",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.origin.Source(); got != tc.source {
				t.Errorf("Source() = %q, want %q", got, tc.source)
			}
			if got := tc.origin.Display(); got != tc.whole {
				t.Errorf("Display() = %q, want %q", got, tc.whole)
			}
		})
	}
}
