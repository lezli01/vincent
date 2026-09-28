package reasons

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// docsDir is the repository's docs/, relative to this package.
var docsDir = filepath.Join("..", "..", "docs")

func TestExplainUnknownFallsBackToTheRawString(t *testing.T) {
	got := Explain("no_such_reason")
	if got.Title != "no_such_reason" || got.Meaning != "" || got.Actions != nil || got.DocAnchor != "" {
		t.Errorf("Explain(no_such_reason) = %+v, want only Title set to the raw string", got)
	}
}

func TestEveryEntryIsFilledIn(t *testing.T) {
	for _, r := range Known() {
		e := Explain(r)
		if e.Title == "" || e.Meaning == "" || e.DocAnchor == "" {
			t.Errorf("%s: Title, Meaning and DocAnchor must all be set: %+v", r, e)
		}
		if e.Title != strings.ToLower(e.Title) {
			t.Errorf("%s: Title %q is not lowercase", r, e.Title)
		}
		if strings.Contains(e.Title, "_") {
			t.Errorf("%s: Title %q is snake_case, not plain language", r, e.Title)
		}
	}
}

func TestExplainDoesNotShareActions(t *testing.T) {
	a := Explain("check_failed")
	a.Actions[0] = "mutated"
	if got := Explain("check_failed").Actions[0]; got != "retry" {
		t.Errorf("a caller's edit leaked into the catalogue: Actions[0] = %q", got)
	}
}

func TestActionsFollowTheRepairOrdering(t *testing.T) {
	// Task 025 decision 8's order, with task 119's chat after it.
	order := []string{"retry", "skip", "repair", "chat"}
	for _, r := range Known() {
		last := -1
		for _, a := range Explain(r).Actions {
			i := slices.Index(order, a)
			if i < 0 {
				continue
			}
			if i < last {
				t.Errorf("%s: actions %v are out of retry, skip, repair, chat order", r, Explain(r).Actions)
			}
			last = i
		}
	}
}

// slug is GitHub's heading anchor: lowercased, punctuation dropped, each
// space a hyphen. Underscores and hyphens survive.
func slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestSlug(t *testing.T) {
	tests := map[string]string{
		"Failure reasons":  "failure-reasons",
		"`worktree_dirty`": "worktree_dirty",
		"`branch_exists` / `worktree_path_occupied`":   "branch_exists--worktree_path_occupied",
		"`cost_limit` — raise the cap and retry":       "cost_limit--raise-the-cap-and-retry",
		"`agent_protocol_error` — vincent's reader, x": "agent_protocol_error--vincents-reader-x",
	}
	for in, want := range tests {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

var headingRE = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*$`)

// headingSlugs returns the anchor of every heading in a markdown file,
// skipping fenced code blocks.
func headingSlugs(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	fenced := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if m := headingRE.FindStringSubmatch(line); !fenced && m != nil {
			slugs[slug(m[1])] = true
		}
	}
	return slugs
}

func TestDocAnchorsResolve(t *testing.T) {
	cache := map[string]map[string]bool{}
	for _, r := range Known() {
		anchor := Explain(r).DocAnchor
		file, fragment, ok := strings.Cut(anchor, "#")
		if !ok || file == "" || fragment == "" {
			t.Errorf("%s: DocAnchor %q is not path#fragment", r, anchor)
			continue
		}
		slugs, seen := cache[file]
		if !seen {
			slugs = headingSlugs(t, filepath.Join(docsDir, filepath.FromSlash(file)))
			cache[file] = slugs
		}
		if !slugs[fragment] {
			t.Errorf("%s: docs/%s has no heading whose anchor is #%s", r, file, fragment)
		}
	}
}

// TestLifecycleDocListsExactlyTheCatalogue holds the "Failure reasons"
// section of the lifecycle reference to the catalogue's key set (task 127
// decision 2): the first column of every table in it, together, is exactly
// Known(). The wording is the doc's own; only the set is checked.
func TestLifecycleDocListsExactlyTheCatalogue(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(docsDir, "reference", "task-lifecycle.md"))
	if err != nil {
		t.Fatal(err)
	}
	var documented []string
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "## ") {
			in = line == "## Failure reasons"
			continue
		}
		if !in || !strings.HasPrefix(line, "| `") {
			continue
		}
		cell, _, _ := strings.Cut(strings.TrimPrefix(line, "| "), " |")
		documented = append(documented, strings.Trim(cell, "`"))
	}
	if len(documented) == 0 {
		t.Fatal(`found no table rows under "## Failure reasons"`)
	}
	slices.Sort(documented)
	if dup := slices.Compact(slices.Clone(documented)); len(dup) != len(documented) {
		t.Errorf("a reason is listed twice: %v", documented)
	}
	documented = slices.Compact(documented)
	known := Known()
	for _, r := range known {
		if !slices.Contains(documented, r) {
			t.Errorf("%s is in the catalogue but not in the lifecycle doc's tables", r)
		}
	}
	for _, r := range documented {
		if !slices.Contains(known, r) {
			t.Errorf("%s is in the lifecycle doc's tables but not in the catalogue", r)
		}
	}
}

// TestIsALeaf: the package imports nothing internal (task 127 decision 3),
// so a client can use it without depending on the engine.
func TestIsALeaf(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, "github.com/lezli01/vincent/") {
				t.Errorf("%s imports %s; internal/reasons must stay a leaf", f, path)
			}
		}
	}
}
