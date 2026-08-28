package cursor

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestDetectVersionVerdict drives all three verdicts through the real probe.
// The fake agent's default version is deliberately *not* one of the verified
// builds — it is a fake — so the tested leg names a real one, which is also
// the proof that the comparison is exact string equality: calver plus a
// commit sha has no ordering to range over (§9.7).
func TestDetectVersionVerdict(t *testing.T) {
	tests := []struct {
		name         string
		version      string
		incompatible []string
		want         agent.VersionVerdict
	}{
		{name: "verified calver+sha is tested", version: testedVersions[0], want: agent.VersionTested},
		{
			name:    "same calver, different sha is untested",
			version: "2026.08.04-ffffff0",
			want:    agent.VersionUntested,
		},
		{name: "fake build is untested", want: agent.VersionUntested},
		{
			name:         "known-bad build is incompatible",
			version:      "2026.09.01-deadbee",
			incompatible: []string{"2026.09.01-deadbee"},
			want:         agent.VersionIncompatible,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FAKEAGENT_DIALECT", "cursor")
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

// TestCuratedRestrictedSupportFollowsSandbox exercises both platform legs on
// every OS CI runs, which is the whole reason SetSandboxAvailable exists: the
// Windows answer would otherwise be executed on Windows alone, and it is the
// one that refuses task creation.
func TestCuratedRestrictedSupportFollowsSandbox(t *testing.T) {
	for _, tt := range []struct {
		name      string
		available bool
		want      agent.RestrictedSupport
		possible  bool
	}{
		{name: "sandbox available", available: true, want: agent.RestrictedAlways, possible: true},
		{name: "sandbox unavailable", available: false, want: agent.RestrictedNever},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withSandbox(t, tt.available)
			opts := New(nil).Curated()
			if opts.RestrictedSupport != tt.want {
				t.Errorf("RestrictedSupport = %q, want %q", opts.RestrictedSupport, tt.want)
			}
			if opts.RestrictedEverPossible() != tt.possible {
				t.Errorf("RestrictedEverPossible = %v, want %v", opts.RestrictedEverPossible(), tt.possible)
			}
			// The catalog level and buildArgs must answer the same question:
			// a gate that said "yes" where Start says ErrRestrictedUnsupported
			// would refuse the wrong tasks and admit the wrong ones.
			_, err := buildArgs(agent.RunSpec{PermissionMode: agent.Restricted})
			if (err == nil) != tt.possible {
				t.Errorf("buildArgs error = %v, want refusal = %v", err, !tt.possible)
			}
		})
	}
}
