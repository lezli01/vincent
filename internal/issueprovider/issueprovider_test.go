package issueprovider

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestRegistry(t *testing.T) {
	for _, tc := range []struct {
		id                string
		known, writesBack bool
	}{
		{GitHub, true, true},
		{Jira, true, true},
		{"gitlab", false, false},
		{"", false, false},
		{"GitHub", false, false},
	} {
		if got := Known(tc.id); got != tc.known {
			t.Errorf("Known(%q) = %v, want %v", tc.id, got, tc.known)
		}
		if got := WritesBack(tc.id); got != tc.writesBack {
			t.Errorf("WritesBack(%q) = %v, want %v", tc.id, got, tc.writesBack)
		}
	}
	if got, want := All(), []string{"github", "jira"}; !slices.Equal(got, want) {
		t.Errorf("All() = %v, want %v", got, want)
	}
	a := All()
	a[0] = "mutated"
	if All()[0] != GitHub {
		t.Error("All() shares its backing array")
	}
}

// TestIsALeaf: the package depends on the standard library alone, so the
// store and the issue sources can both import it without a cycle.
func TestIsALeaf(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/lezli01/vincent/") && !strings.HasSuffix(dep, "/internal/issueprovider") {
			t.Errorf("internal/issueprovider depends on %s; it may import the standard library only", dep)
		}
	}
}
