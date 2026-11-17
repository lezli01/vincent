package issuearchive

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDocsClaimsHeldIsNotOutOfScope holds the published pages to the worker's
// retry rule. The worker re-asks an issue on any later event about it
// (issuesOf), so a pre-upgrade or key-off close, and a dirty refusal, are held
// until something on the issue moves — never out of scope for good. The
// configuration reference said pre-upgrade issues were "never archived", and
// the guide listed the held cases under "never touches" with "nothing retries
// it" (review F1 and F2 on #999).
func TestDocsClaimsHeldIsNotOutOfScope(t *testing.T) {
	root := filepath.Join("..", "..")
	collapse := regexp.MustCompile(`\s+`)
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}

	// No sentence may put a pre-upgrade or key-off close under "never".
	never := regexp.MustCompile(`(?i)\bnever (archived|touche[sd])`)
	upgrade := regexp.MustCompile(`(?i)before (the|you) upgrade|while (this|it|the key|` +
		"`archive_tasks_on_issue_close`" + `) was off`)
	for _, name := range []string{"docs/reference/configuration.md", "docs/guides/issues.md", "docs/reference/task-lifecycle.md", "docs/guides/troubleshooting.md"} {
		text := collapse.ReplaceAllString(read(name), " ")
		for _, s := range regexp.MustCompile(`[^.]+\.`).FindAllString(text, -1) {
			if never.MatchString(s) && upgrade.MatchString(s) {
				t.Errorf("%s says a pre-upgrade or key-off close is never archived, but a later "+
					"event on the issue archives it:\n  %s", name, strings.TrimSpace(s))
			}
		}
		if strings.Contains(text, "nothing retries it") {
			t.Errorf("%s says nothing retries a held task, but any later event on its issue does", name)
		}
	}

	// The guide's out-of-scope section lists only what is never archived.
	guide := read("docs/guides/issues.md")
	const head = "### What auto-archive never touches"
	i := strings.Index(guide, head)
	if i < 0 {
		t.Fatalf("docs/guides/issues.md has no %q section; configuration.md, "+
			"troubleshooting.md and task-lifecycle.md link to it", head)
	}
	section := guide[i+len(head):]
	if j := strings.Index(section, "\n#"); j >= 0 {
		section = section[:j]
	}
	section = collapse.ReplaceAllString(section, " ")
	for _, held := range []string{"uncommitted", "upgrade", "unfinished work"} {
		if strings.Contains(section, held) {
			t.Errorf("%q lists a held case (%s), which a later event archives", head, held)
		}
	}
}
