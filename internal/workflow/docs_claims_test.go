package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDocsClaimsQuickstartListsEveryExample: the quickstart walks a reader
// through one example and names the rest by count ("the other three
// examples"). Adding an example to examples/ without editing that sentence
// leaves a reader believing they have seen the whole shelf (issue #137).
func TestDocsClaimsQuickstartListsEveryExample(t *testing.T) {
	root := filepath.Join("..", "..")
	entries, err := filepath.Glob(filepath.Join(root, "examples", "*.yaml"))
	if err != nil {
		t.Fatalf("glob examples: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no examples found")
	}

	page := filepath.Join(root, "docs", "getting-started", "quickstart.md")
	b, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("read quickstart: %v", err)
	}
	body := string(b)

	for _, path := range entries {
		name := filepath.Base(path)
		if !strings.Contains(body, name) {
			t.Errorf("examples/%s is never named in docs/getting-started/quickstart.md", name)
		}
	}

	// "The other N examples are …" — N counts everything but the one walked
	// through above it.
	words := map[string]int{
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
		"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	}
	counted := regexp.MustCompile(`(?i)the other (\w+) examples?`)
	for _, m := range counted.FindAllStringSubmatch(body, -1) {
		got, ok := words[strings.ToLower(m[1])]
		if !ok {
			t.Errorf("quickstart says %q; not a number this test can check", m[0])
			continue
		}
		if want := len(entries) - 1; got != want {
			t.Errorf("quickstart says %q, but examples/ holds %d workflows, so the others number %d",
				m[0], len(entries), want)
		}
	}
}
