package copilot

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestDetectVersionVerdict drives the verdicts through the real probe. The
// comparison is exact strings (task 041): 1.0.84, the documented minimum, is
// as untested as any other build, and nothing judges incompatible unless the
// known-bad list names it.
func TestDetectVersionVerdict(t *testing.T) {
	tests := []struct {
		name         string
		version      string
		incompatible []string
		want         agent.VersionVerdict
	}{
		{name: "the pinned build is tested", want: agent.VersionTested},
		{name: "the documented minimum is untested", version: "1.0.84", want: agent.VersionUntested},
		{name: "a newer build is untested", version: "1.0.106", want: agent.VersionUntested},
		{name: "an older build is untested, never incompatible", version: "1.0.61", want: agent.VersionUntested},
		{
			name:         "a known-bad build is incompatible",
			version:      "1.0.200",
			incompatible: []string{"1.0.200"},
			want:         agent.VersionIncompatible,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FAKEAGENT_DIALECT", "copilot")
			if tt.version != "" {
				t.Setenv("FAKEAGENT_VERSION", tt.version)
			}
			if tt.incompatible != nil {
				t.Cleanup(SetIncompatibleVersions(tt.incompatible))
			}
			av, err := fakeAdapter(t).Detect(t.Context())
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if av.VersionVerdict != tt.want {
				t.Errorf("VersionVerdict = %q, want %q (version %q)", av.VersionVerdict, tt.want, av.Version)
			}
		})
	}
}

func TestIncompatibleVersionsShipEmpty(t *testing.T) {
	if len(incompatibleVersions) != 0 {
		t.Errorf("incompatibleVersions = %q, want empty", incompatibleVersions)
	}
}
