package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestStringNeverPrintsEmptyFields(t *testing.T) {
	got := String()
	if !strings.HasPrefix(got, "vincent version ") {
		t.Fatalf("unexpected prefix in version string: %q", got)
	}
	for _, forbidden := range []string{"  ", "commit ,", "built )"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("empty field leaked into version string: %q", got)
		}
	}
}

// An injected version always wins over the build-info fallback: a release
// binary must report its tag, never the module version Go happens to stamp.
func TestInjectedVersionWins(t *testing.T) {
	t.Cleanup(func(orig string) func() {
		return func() { version = orig }
	}(version))

	version = "v9.9.9"
	if got := Version(); got != "v9.9.9" {
		t.Fatalf("Version() = %q, want the injected v9.9.9", got)
	}
}

// The `go install` fallback must never surface a placeholder. Inside a test
// binary debug.ReadBuildInfo reports the main module as "" or "(devel)" — the
// two values the fallback deliberately declines — so this pins the floor:
// whatever the environment says, Version() degrades to "dev" and not to junk a
// user would see in `vincent version`.
func TestVersionFallbackNeverReportsPlaceholder(t *testing.T) {
	t.Cleanup(func(orig string) func() {
		return func() { version = orig }
	}(version))

	version = "dev"
	got := Version()
	if got == "" || got == "(devel)" {
		t.Fatalf("Version() = %q, want a real version or %q", got, "dev")
	}
	if info, ok := debug.ReadBuildInfo(); ok && (info.Main.Version == "" || info.Main.Version == "(devel)") && got != "dev" {
		t.Fatalf("Version() = %q with main module version %q, want dev", got, info.Main.Version)
	}
}
