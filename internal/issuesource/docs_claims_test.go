package issuesource

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDocsClaimsStatusJQLMarksMissing holds the published pages to what the
// Jira source does with a binding's JQL. GetIssues drops every id the extra
// JQL no longer matches (dropExcluded), and the sync sweep marks an absent
// remote `missing`, which ends close sync and write-back for it. A page that
// suggests narrowing the binding's JQL to a status, without saying so, sends
// a reader to a binding that stops working the moment work starts — the Jira
// guide did exactly that (review F1 on #950).
//
// It is a drift check on the pairing: a paragraph that puts a `status =`
// clause beside the binding's JQL must also say `missing`.
func TestDocsClaimsStatusJQLMarksMissing(t *testing.T) {
	status := regexp.MustCompile(`(?i)\bstatus\s*(?:=|in\b)`)
	binding := regexp.MustCompile(`(?i)binding'?s? JQL|JQL of the binding`)
	var bad []string
	for name, para := range docParagraphs(t) {
		for _, p := range para {
			if status.MatchString(p) && binding.MatchString(p) && !strings.Contains(p, "missing") {
				bad = append(bad, name+": "+p)
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("a binding's JQL that stops matching an issue marks it missing, but these "+
			"paragraphs narrow it to a status without saying so:\n  %s", strings.Join(bad, "\n  "))
	}
}

// docParagraphs reads the user-facing pages — everything under docs/ except
// the maintainer records and the spec, plus the README — split into
// paragraphs with their whitespace collapsed.
func docParagraphs(t *testing.T) map[string][]string {
	t.Helper()
	root := filepath.Join("..", "..")
	names := []string{"README.md"}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		slashed := filepath.ToSlash(rel)
		if d.IsDir() {
			switch slashed {
			case "docs/tasks", "docs/history", "docs/gates", "docs/assets":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(slashed, ".md") && slashed != "docs/spec.md" {
			names = append(names, slashed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	collapse := regexp.MustCompile(`\s+`)
	out := make(map[string][]string, len(names))
	for _, name := range names {
		b, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		for _, p := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n\n") {
			out[name] = append(out[name], strings.TrimSpace(collapse.ReplaceAllString(p, " ")))
		}
	}
	return out
}
