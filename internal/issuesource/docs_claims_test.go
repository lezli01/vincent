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

// TestDocsClaimsMirrorRulesNameJira holds the pages to the mirror and
// forge-write rules being provider-agnostic. issues.ErrMirrored and
// ErrForgeWriteNeedsHuman refuse the same writes on a Jira-imported issue as
// on a GitHub one, so a paragraph stating one of them for GitHub alone tells a
// reader binding a project to Jira that the rule does not apply to them
// (review F2 on #950). A paragraph that names Jira too is taken to cover it,
// and an image's alt text is skipped: it describes one captured issue, which
// may well be a GitHub one.
func TestDocsClaimsMirrorRulesNameJira(t *testing.T) {
	githubOnly := regexp.MustCompile(`mirrors? GitHub\b|GitHub remote is live|` +
		`thread is GitHub's|posted to GitHub|mirrored from GitHub`)
	var bad []string
	for name, para := range docParagraphs(t) {
		for _, p := range para {
			if strings.HasPrefix(p, "![") {
				continue
			}
			if githubOnly.MatchString(p) && !strings.Contains(p, "Jira") {
				bad = append(bad, name+": "+p)
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("the mirror and forge-write rules hold for Jira too, but these paragraphs "+
			"state them for GitHub alone:\n  %s", strings.Join(bad, "\n  "))
	}
}

// TestDocsClaimsTokenNoRestartNamesEnvironmentSet holds the pages to where a
// Jira credential is read from. The daemon resolves it per request, but over
// its own os.Environ(), fixed when it started (newJiraSource in
// internal/daemon); only `environment.set`, hot-reloaded from config.yaml,
// changes without a restart. A paragraph promising a new token "with no
// restart" must say that it means `environment.set` (review F3 on #950).
func TestDocsClaimsTokenNoRestartNamesEnvironmentSet(t *testing.T) {
	noRestart := regexp.MustCompile(`(?i)(?:\bno|\bwithout(?: a)?) restart`)
	credential := regexp.MustCompile(`(?i)\btoken\b|credential`)
	var bad []string
	for name, para := range docParagraphs(t) {
		for _, p := range para {
			aboutJira := strings.Contains(name, "jira") ||
				strings.Contains(p, "Jira") || strings.Contains(p, "JIRA_")
			jiraCredential := aboutJira && credential.MatchString(p)
			if jiraCredential && noRestart.MatchString(p) && !strings.Contains(p, "environment.set") {
				bad = append(bad, name+": "+p)
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("only an environment.set credential changes without a restart, but these "+
			"paragraphs promise a new Jira credential takes effect without one:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

// TestDocsClaimsIssueTasksShareWorktreeNamesSideTasks holds the pages to an
// issue's tasks not all sharing its main worktree: a side task runs in a
// worktree of its own and is merged back (docs/guides/issues.md). A paragraph
// saying an issue's tasks share one main branch or worktree must name the side
// task too (review F4 on #950).
func TestDocsClaimsIssueTasksShareWorktreeNamesSideTasks(t *testing.T) {
	share := regexp.MustCompile(`(?i)tasks (?:share|build on) (?:\*\*)?one main`)
	var bad []string
	for name, para := range docParagraphs(t) {
		for _, p := range para {
			if share.MatchString(p) && !strings.Contains(strings.ToLower(p), "side task") {
				bad = append(bad, name+": "+p)
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("a side task runs apart from the issue's main worktree, but these paragraphs "+
			"say every task of an issue shares it:\n  %s", strings.Join(bad, "\n  "))
	}
}
