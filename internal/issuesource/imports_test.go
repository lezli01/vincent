package issuesource

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/lezli01/vincent/"

// TestImportsAreTheDocumentedSet pins the package's internal imports to the
// list doc.go and CLAUDE.md's package map state (review F4), and proves
// the "neither imports it back" half: store, github and jira stay free of
// the seam, even transitively.
func TestImportsAreTheDocumentedSet(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var got []string
	for _, imp := range strings.Fields(string(out)) {
		if rest, ok := strings.CutPrefix(imp, module); ok {
			got = append(got, rest)
		}
	}
	slices.Sort(got)
	want := []string{
		"internal/github", "internal/gitx", "internal/issueprovider", "internal/issuestate",
		"internal/jira", "internal/jira/jiramap", "internal/jira/render", "internal/store",
	}
	if !slices.Equal(got, want) {
		t.Errorf("internal/issuesource imports %v; doc.go and CLAUDE.md say %v — update both with the code", got, want)
	}
	for _, pkg := range []string{"../store", "../github", "../jira", "../jira/jiramap", "../jira/render"} {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		if slices.Contains(strings.Fields(string(out)), module+"internal/issuesource") {
			t.Errorf("%s depends on internal/issuesource; the seam must not be imported back", pkg)
		}
	}
}
