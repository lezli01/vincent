package main

import (
	"strings"
	"testing"
)

// The corpus stays octo/repo unless asked: every test and gate reads it so.
func TestCorpusIsOctoRepoByDefault(t *testing.T) {
	t.Setenv("FAKEGH_REPO", "")
	t.Setenv("FAKEGH_PR_TITLE", "")
	pull := pullCorpus()[0]
	if pull["url"] != "https://github.com/octo/repo/pull/412" || pull["title"] != "Add a thing" {
		t.Fatalf("default corpus changed: %v %v", pull["url"], pull["title"])
	}
}

// FAKEGH_REPO moves every URL and head repository that names octo/repo, and
// nothing else; FAKEGH_PR_TITLE retitles the first pull request.
func TestRehomeMovesTheCorpus(t *testing.T) {
	t.Setenv("FAKEGH_REPO", "acme/web")
	t.Setenv("FAKEGH_PR_TITLE", "Bump the design tokens")
	pulls := pullCorpus()

	first := pulls[0]
	if first["title"] != "Bump the design tokens" {
		t.Errorf("title = %v", first["title"])
	}
	if first["url"] != "https://github.com/acme/web/pull/412" {
		t.Errorf("url = %v", first["url"])
	}
	for _, check := range first["statusCheckRollup"].([]map[string]any) {
		for _, key := range []string{"detailsUrl", "targetUrl"} {
			if u, _ := check[key].(string); strings.Contains(u, "octo/repo") {
				t.Errorf("check %s still names octo/repo: %s", key, u)
			}
		}
	}
	if got := first["headRepositoryOwner"].(map[string]any)["login"]; got != "acme" {
		t.Errorf("head owner = %v", got)
	}
	if got := first["headRepository"].(map[string]any)["name"]; got != "web" {
		t.Errorf("head repository = %v", got)
	}
	if got := first["author"].(map[string]any)["login"]; got != "octocat" {
		t.Errorf("author moved with the repository: %v", got)
	}

	// The fork keeps its owner — that is what makes it a fork — and takes
	// the name of what it forked.
	for _, pull := range pulls {
		if pull["number"] != 355 {
			continue
		}
		if got := pull["headRepositoryOwner"].(map[string]any)["login"]; got != "contributor" {
			t.Errorf("fork owner = %v", got)
		}
		if got := pull["headRepository"].(map[string]any)["name"]; got != "web" {
			t.Errorf("fork repository = %v", got)
		}
	}

	for _, issue := range corpus() {
		if u, _ := issue["url"].(string); !strings.HasPrefix(u, "https://github.com/acme/web/issues/") {
			t.Errorf("issue url = %s", u)
		}
	}
}
