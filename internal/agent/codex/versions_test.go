package codex

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestDetectVersionVerdict drives all three verdicts through the real probe:
// the fake agent's default is a fixture-verified build, any other string is
// untested, and an injected known-bad entry is incompatible. The injection is
// what keeps the third leg from being a branch nothing has ever executed —
// the list ships empty because no codex release has been observed to break.
func TestDetectVersionVerdict(t *testing.T) {
	tests := []struct {
		name         string
		version      string
		incompatible []string
		want         agent.VersionVerdict
	}{
		{name: "fixture build is tested", want: agent.VersionTested},
		{name: "newer build is untested", version: "codex-cli 9.9.9", want: agent.VersionUntested},
		{
			name:         "known-bad build is incompatible",
			version:      "codex-cli 9.9.9",
			incompatible: []string{"9.9.9"},
			want:         agent.VersionIncompatible,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FAKEAGENT_DIALECT", "codex")
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
			if av.TestedVersions == "" {
				t.Error("TestedVersions is empty; an untested row has nothing to name")
			}
		})
	}
}

// TestDetectMissingBinaryHasNoVerdict pins the rule that silence is not
// evidence: nothing installed means no judgement, not "untested".
func TestDetectMissingBinaryHasNoVerdict(t *testing.T) {
	av, err := New(func() string { return "/nonexistent/codex-not-here" }).Detect(t.Context())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if av.VersionVerdict != agent.VersionUnknown {
		t.Errorf("VersionVerdict = %q, want unknown for a missing binary", av.VersionVerdict)
	}
}

// TestCuratedRestrictedSupport pins codex as an adapter that can always
// restrict: its sandbox flags exist on every platform it runs on, on every platform.
func TestCuratedRestrictedSupport(t *testing.T) {
	opts := New(nil).Curated()
	if opts.RestrictedSupport != agent.RestrictedAlways {
		t.Errorf("RestrictedSupport = %q, want %q", opts.RestrictedSupport, agent.RestrictedAlways)
	}
	if !opts.RestrictedEverPossible() {
		t.Error("RestrictedEverPossible = false; codex restricts on every platform")
	}
}
