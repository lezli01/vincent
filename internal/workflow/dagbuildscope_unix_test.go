//go:build unix

package workflow

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/internal/testrepo"
)

// The conflict resolver in github-resolve-issue-dag builds only the packages
// the resolution staged, because a whole-repo build is red by construction
// mid-join (#328, PR #336). Nothing exercised that extraction, and it misses
// two shapes of staged change (issue #341): a Go file at the repository root
// never yields a directory, and `--diff-filter=ACMR` drops pure deletions —
// which is exactly the edit most likely to break a package's remaining files.
//
// Unix-only on purpose: the workflow declares `platforms: [posix]` (§8.1.1)
// and its check bodies run under the daemon's `/bin/sh` (§8.3), so there is no
// Windows behaviour of this pipeline to assert.
func TestDAGResolverBuildScopeCoversStagedGoFiles(t *testing.T) {
	scope := stagedBuildScope(t)

	// The repository as the resolver finds it: two ordinary packages, a
	// package with two files, a package with one, a Go file at the root and a
	// file that is not Go at all.
	base := map[string]string{
		"internal/api/tasks.go":  "package api\n",
		"internal/tui/board.go":  "package tui\n",
		"internal/store/keep.go": "package store\n",
		"internal/store/drop.go": "package store\n",
		"internal/gone/only.go":  "package gone\n",
		"magefile.go":            "package main\n",
		"docs/spec.md":           "spec\n",
	}

	cases := []struct {
		name string
		// edit maps a path to its new content; an empty string deletes it.
		edit map[string]string
		want []string
	}{
		{
			name: "nested packages",
			edit: map[string]string{
				"internal/api/tasks.go": "package api\n\n// resolved\n",
				"internal/tui/board.go": "package tui\n\n// resolved\n",
			},
			want: []string{"internal/api", "internal/tui"},
		},
		{
			// mage.go and magefile.go live at the root and are Go files like
			// any other; a resolution that touches one has to build it.
			name: "root-level go file",
			edit: map[string]string{"magefile.go": "package main\n\n// resolved\n"},
			want: []string{"."},
		},
		{
			// Deleting one file out of a package is precisely the edit that
			// breaks the siblings that stayed.
			name: "deletion with siblings left behind",
			edit: map[string]string{"internal/store/drop.go": ""},
			want: []string{"internal/store"},
		},
		{
			// The other half of the deletion case, and the reason the fix
			// cannot simply add D to the filter: the directory is gone after
			// the resolution, and `go build ./internal/gone` on it would fail
			// the check for a package that no longer exists.
			name: "package emptied by the deletion",
			edit: map[string]string{"internal/gone/only.go": ""},
			want: nil,
		},
		{
			name: "no go files staged",
			edit: map[string]string{"docs/spec.md": "spec\n\n## resolved\n"},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := testrepo.Init(t, "main")
			for path, content := range base {
				testrepo.WriteFile(t, repo, path, content)
			}
			testrepo.Run(t, repo, "add", "-A")
			testrepo.Run(t, repo, "commit", "-q", "-m", "base")

			for path, content := range tc.edit {
				if content == "" {
					if err := os.Remove(filepath.Join(repo, path)); err != nil {
						t.Fatalf("delete %s: %v", path, err)
					}
					continue
				}
				testrepo.WriteFile(t, repo, path, content)
			}
			testrepo.Run(t, repo, "add", "-A")

			got := runBuildScope(t, repo, scope)
			want := append([]string(nil), tc.want...)
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("staged %v\n build scope = %v\n         want %v", stagedPaths(t, repo), got, want)
			}
		})
	}
}

// runBuildScope executes the extracted assignment in repo and returns the
// packages it would hand `go build`, split the way the unquoted `$dirs`
// expansion in the check splits them and cleaned so that the assertion is
// about which packages are built, not about how the pipeline spells them —
// `./internal/api`, `internal/api`, `.` and `./.` all name one package to
// `go build`.
func runBuildScope(t *testing.T, repo, scope string) []string {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", scope+"\nprintf '%s\\n' $dirs\n")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run build scope: %v\n%s", err, out)
	}
	var dirs []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			dirs = append(dirs, path.Clean(line))
		}
	}
	return dirs
}

func stagedPaths(t *testing.T, repo string) string {
	t.Helper()
	return strings.ReplaceAll(testrepo.Run(t, repo, "diff", "--cached", "--name-status"), "\n", ", ")
}

// stagedBuildScope pulls the `dirs=$(...)` assignment out of the shipped
// workflow rather than restating it, so the test asserts what the resolver
// actually runs.
func stagedBuildScope(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", ".vincent", "workflows", "github-resolve-issue-dag.yaml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var bodies []string
	walkYAMLStrings(doc, func(s string) {
		if strings.Contains(s, "git diff --cached --name-only") {
			bodies = append(bodies, s)
		}
	})
	if len(bodies) != 1 {
		t.Fatalf("expected exactly one staged-name pipeline in %s, found %d", path, len(bodies))
	}
	return assignment(t, bodies[0], "dirs")
}

// assignment returns `name=$( … )` from body, balancing parentheses and
// ignoring anything inside single quotes — the sed script carries `\(` and
// `\)` of its own.
func assignment(t *testing.T, body, name string) string {
	t.Helper()
	start := strings.Index(body, name+"=$(")
	if start < 0 {
		t.Fatalf("no %s=$(...) assignment in check body: %s", name, body)
	}
	depth, quoted := 0, false
	for i := start; i < len(body); i++ {
		switch c := body[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			if depth--; depth == 0 {
				return body[start : i+1]
			}
		}
	}
	t.Fatalf("unbalanced %s=$(...) in check body: %s", name, body)
	return ""
}

func walkYAMLStrings(v any, fn func(string)) {
	switch n := v.(type) {
	case string:
		fn(n)
	case []any:
		for _, e := range n {
			walkYAMLStrings(e, fn)
		}
	case map[string]any:
		for _, e := range n {
			walkYAMLStrings(e, fn)
		}
	case map[any]any:
		for _, e := range n {
			walkYAMLStrings(e, fn)
		}
	}
}
