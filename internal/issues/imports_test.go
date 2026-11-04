package issues

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/lezli01/vincent/"

// TestImportsAreTheDocumentedSet pins the package's internal imports to the
// list doc.go and CLAUDE.md's package map state (review F1): the set had
// grown by internal/issueprovider while both still named store and
// issuestate only.
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
	want := []string{"internal/issueprovider", "internal/issuestate", "internal/store"}
	if !slices.Equal(got, want) {
		t.Errorf("internal/issues imports %v; doc.go and CLAUDE.md say %v — update both with the code", got, want)
	}
}
