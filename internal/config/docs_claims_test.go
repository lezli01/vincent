package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDocsClaimsArchiveKeepsBranch holds the published pages to this
// package's archive defaults. `delete_empty_branch_on_archive` has defaulted
// to true since task 008 (spec §10, amended 2026-08-16), so a page telling a
// reader that archiving keeps the branch — or that vincent never deletes one
// — is telling them a branch is safe that the daemon deletes (issue #137).
//
// It is a drift check on the pairing, not on the default: flipping the
// default or qualifying the sentence both satisfy it.
func TestDocsClaimsArchiveKeepsBranch(t *testing.T) {
	if !Default().DeleteEmptyBranchOnArchive {
		t.Skip("archive keeps every branch by default; the unqualified claim is true")
	}

	// A survival claim: the branch outlives the archive.
	keeps := regexp.MustCompile(`(?i)keeps the branch|the branch is kept|` +
		`never deletes? (?:a |your |any )?branch|delete my branches\?`)
	// The exception task 008 introduced, in any of the spellings the pages use.
	qualified := regexp.MustCompile(`(?i)delete_empty_branch_on_archive|` +
		`no commits|carries a commit|carrying a commit|has commits|with commits|` +
		`commits past|holds a commit`)
	collapse := regexp.MustCompile(`\s+`)

	var bad []string
	for name, body := range docPagesForClaims(t) {
		for _, para := range strings.Split(body, "\n\n") {
			flat := collapse.ReplaceAllString(para, " ")
			// Only paragraphs about archiving: `vincent gc` never deletes a
			// branch, unqualified and true.
			if !strings.Contains(strings.ToLower(flat), "archiv") {
				continue
			}
			if keeps.MatchString(flat) && !qualified.MatchString(flat) {
				bad = append(bad, name+": "+strings.TrimSpace(flat))
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("delete_empty_branch_on_archive defaults to true, but these pages promise "+
			"the branch survives archiving with no exception:\n  %s", strings.Join(bad, "\n  "))
	}
}

// docPagesForClaims reads the user-facing pages: everything under docs/ except
// the maintainer records (`docs/tasks/`, `docs/history/`, `docs/gates/`) and
// the spec itself, plus the README.
func docPagesForClaims(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	pages := map[string]string{"README.md": ""}
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
			pages[slashed] = ""
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	for name := range pages {
		b, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		pages[name] = string(b)
	}
	return pages
}
