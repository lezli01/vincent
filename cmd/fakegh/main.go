// fakegh is a scenario-driven stand-in for the `gh` CLI (task 035), the same
// shape cmd/fakeagent takes for the agent CLIs: the command is read from
// argv, the behaviour from the environment, so the argv internal/github
// builds stays faithful to the real tool and no test ever calls GitHub.
//
// It answers the three invocations the daemon makes and nothing else:
//
//	gh auth status
//	gh issue list --repo owner/name --state S --limit N --json FIELDS
//	gh issue view N --repo owner/name --json FIELDS
//	gh pr list --repo owner/name --state S --limit N --json FIELDS
//	gh pr view N --repo owner/name --json FIELDS
//
// Scenario selection is environment-driven:
//
//	FAKEGH_SCENARIO  success (default) | logged-out | empty | not-found |
//	                 unauthorized | rate-limited | forbidden | bad-json |
//	                 hang
//	FAKEGH_PR_BRANCH when set, the head branch of the first pull request in
//	                 the corpus, so a gate or a test can point one at the
//	                 branch a real task was given.
//	FAKEGH_ARGV_FILE when set, each invocation appends its argv (one
//	                 space-joined line) to this file, so a test can assert
//	                 the flags the adapter passed — and, for a disabled
//	                 integration, that it made no call at all.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	recordArgv(args)
	scenario := os.Getenv("FAKEGH_SCENARIO")
	if scenario == "hang" {
		// Long enough to outlive any timeout a test sets; the parent kills it.
		time.Sleep(10 * time.Minute)
		return
	}
	switch {
	case len(args) >= 2 && args[0] == "auth" && args[1] == "status":
		authStatus(scenario)
	case len(args) >= 2 && args[0] == "issue" && args[1] == "list":
		issueList(scenario)
	case len(args) >= 3 && args[0] == "issue" && args[1] == "view":
		issueView(scenario, args[2])
	case len(args) >= 2 && args[0] == "pr" && args[1] == "list":
		pullList(scenario, flagValue(args, "--state"))
	case len(args) >= 3 && args[0] == "pr" && args[1] == "view":
		pullView(scenario, args[2])
	default:
		fmt.Fprintf(os.Stderr, "fakegh: unsupported invocation %q\n", strings.Join(args, " "))
		os.Exit(2)
	}
}

// flagValue reads `--name value` out of argv, which is how the real CLI takes
// every one of the flags this fake honours.
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func recordArgv(args []string) {
	path := os.Getenv("FAKEGH_ARGV_FILE")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintln(f, strings.Join(args, " "))
}

func authStatus(scenario string) {
	if scenario == "logged-out" {
		// The real CLI's wording, so a mapping that reads stderr is exercised
		// against something recognizable.
		fmt.Fprintln(os.Stderr, "You are not logged into any GitHub hosts. To log in, run: gh auth login")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "github.com\n  ✓ Logged in to github.com account octocat (keyring)")
}

// failures are the stderr lines the real gh prints, keyed by scenario. Each
// one is what internal/github's mapping reads to pick a reason constant.
var failures = map[string]string{
	"not-found":    "GraphQL: Could not resolve to a Repository with the name 'octo/missing'. (repository)",
	"unauthorized": "gh: Bad credentials (HTTP 401)",
	"rate-limited": "gh: API rate limit exceeded for user ID 1. (HTTP 403)",
	"forbidden":    "gh: Resource not accessible by integration (HTTP 403)",
}

func fail(scenario string) bool {
	msg, ok := failures[scenario]
	if !ok {
		return false
	}
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
	return true
}

func issueList(scenario string) {
	if fail(scenario) {
		return
	}
	if scenario == "bad-json" {
		fmt.Println("not json at all")
		return
	}
	issues := corpus()
	if scenario == "empty" {
		issues = nil
	}
	emit(issues)
}

func issueView(scenario, number string) {
	if fail(scenario) {
		return
	}
	if scenario == "bad-json" {
		fmt.Println("{")
		return
	}
	n, err := strconv.Atoi(number)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh: invalid issue number %q\n", number)
		os.Exit(1)
	}
	for _, issue := range corpus() {
		if issue["number"] == n {
			emit(issue)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "gh: Could not resolve to an Issue with the number of %d. (HTTP 404)\n", n)
	os.Exit(1)
}

func pullList(scenario, state string) {
	if fail(scenario) {
		return
	}
	if scenario == "bad-json" {
		fmt.Println("not json at all")
		return
	}
	pulls := pullCorpus()
	if scenario == "empty" {
		pulls = nil
	}
	// `--state` is honoured rather than ignored, the way the real CLI does
	// it (task 064 decision 9): the daemon now passes closed and all, and a
	// fake that answered "open" to every one of them would make a test that
	// asserts a merged pull request is listable pass for the wrong reason.
	if state == "" {
		state = "open"
	}
	rows := make([]map[string]any, 0, len(pulls))
	for _, pull := range pulls {
		st, _ := pull["state"].(string)
		switch state {
		case "all":
			rows = append(rows, pull)
		case "closed":
			// MERGED is a closed pull request everywhere in vincent, and `gh`
			// lists it under --state closed too.
			if st != "OPEN" {
				rows = append(rows, pull)
			}
		default:
			if st == "OPEN" {
				rows = append(rows, pull)
			}
		}
	}
	if pulls == nil {
		rows = nil
	}
	emit(rows)
}

func pullView(scenario, number string) {
	if fail(scenario) {
		return
	}
	if scenario == "bad-json" {
		fmt.Println("{")
		return
	}
	n, err := strconv.Atoi(number)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh: invalid pull request number %q\n", number)
		os.Exit(1)
	}
	for _, pull := range pullCorpus() {
		if pull["number"] == n {
			emit(pull)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "gh: Could not resolve to a PullRequest with the number of %d. (HTTP 404)\n", n)
	os.Exit(1)
}

// pullCorpus is the fixed pull-request set, shaped exactly like
// `gh pr list --json`. Three rows on purpose: an open one whose head branch a
// test or gate can point at a real task, an open draft, and a **merged** one,
// which is the case the durable link exists to serve and the one an open-only
// listing can never answer, and a **fork**, whose head lives in another
// repository entirely.
func pullCorpus() []map[string]any {
	branch := os.Getenv("FAKEGH_PR_BRANCH")
	if branch == "" {
		branch = "vincent/1-add-a-thing"
	}
	return []map[string]any{
		{
			"number":              412,
			"title":               "Add a thing",
			"body":                "Adds the thing, and a test for the thing.",
			"url":                 "https://github.com/octo/repo/pull/412",
			"state":               "OPEN",
			"isDraft":             false,
			"headRefName":         branch,
			"headRepository":      map[string]any{"name": "repo"},
			"headRepositoryOwner": map[string]any{"login": "octo"},
			"baseRefName":         "main",
			"author":              map[string]any{"login": "octocat"},
			"createdAt":           "2026-08-26T19:21:29Z",
			"updatedAt":           "2026-08-26T19:30:00Z",
			"mergedAt":            nil,
			"headRefOid":          "d3adb33fd3adb33fd3adb33fd3adb33fd3adb33f",
			// The rollup carries all three kinds on purpose (task 068): an
			// Actions-backed check run, a third-party one, and a legacy
			// commit status. They are what tells an offered re-run from an
			// absent one, and the gate has to be able to drive that.
			"statusCheckRollup": []map[string]any{
				{
					"__typename":  "CheckRun",
					"name":        "build",
					"status":      "COMPLETED",
					"conclusion":  "FAILURE",
					"detailsUrl":  "https://github.com/octo/repo/actions/runs/5150/job/71",
					"startedAt":   "2026-08-26T19:22:00Z",
					"completedAt": "2026-08-26T19:26:00Z",
				},
				{
					"__typename":  "CheckRun",
					"name":        "test",
					"status":      "IN_PROGRESS",
					"conclusion":  "",
					"detailsUrl":  "https://github.com/octo/repo/actions/runs/5150/job/72",
					"startedAt":   "2026-08-26T19:22:01Z",
					"completedAt": nil,
				},
				{
					"__typename":  "CheckRun",
					"name":        "license/cla",
					"status":      "COMPLETED",
					"conclusion":  "SUCCESS",
					"detailsUrl":  "https://cla.example.test/octo/repo/pull/412",
					"startedAt":   "2026-08-26T19:22:02Z",
					"completedAt": "2026-08-26T19:22:09Z",
				},
				{
					"__typename": "StatusContext",
					"context":    "ci/legacy-builder",
					"state":      "SUCCESS",
					"targetUrl":  "https://legacy.example.test/build/9",
					"createdAt":  "2026-08-26T19:23:00Z",
				},
			},
		},
		{
			"number":              401,
			"title":               "Draft: rework the board header",
			"body":                "",
			"url":                 "https://github.com/octo/repo/pull/401",
			"state":               "OPEN",
			"isDraft":             true,
			"headRefName":         "vincent/9-rework-the-board-header",
			"headRepository":      map[string]any{"name": "repo"},
			"headRepositoryOwner": map[string]any{"login": "octo"},
			"baseRefName":         "main",
			"author":              map[string]any{"login": "hubot"},
			"createdAt":           "2026-07-01T08:00:00Z",
			"updatedAt":           "2026-07-02T08:00:00Z",
			"mergedAt":            nil,
		},
		{
			"number":              377,
			"title":               "Ship the thing that already merged",
			"body":                "The merged one.",
			"url":                 "https://github.com/octo/repo/pull/377",
			"state":               "MERGED",
			"isDraft":             false,
			"headRefName":         "vincent/3-ship-the-thing",
			"headRepository":      map[string]any{"name": "repo"},
			"headRepositoryOwner": map[string]any{"login": "octo"},
			"baseRefName":         "main",
			"author":              map[string]any{"login": "octocat"},
			"createdAt":           "2026-06-01T08:00:00Z",
			"updatedAt":           "2026-06-02T08:00:00Z",
			"mergedAt":            "2026-06-02T08:00:00Z",
		},
		{
			// The fork row (task 064 decision 5). Its head repository is a
			// different `owner/name`, which is the only thing that makes a
			// fork detectable at all — and the only reason a task created
			// from it fetches `refs/pull/{n}/head` and gets no upstream.
			"number":              355,
			"title":               "Fix a typo from a fork",
			"body":                "One character.",
			"url":                 "https://github.com/octo/repo/pull/355",
			"state":               "OPEN",
			"isDraft":             false,
			"headRefName":         "typo-fix",
			"headRepository":      map[string]any{"name": "repo"},
			"headRepositoryOwner": map[string]any{"login": "contributor"},
			"baseRefName":         "main",
			"author":              map[string]any{"login": "contributor"},
			"createdAt":           "2026-05-01T08:00:00Z",
			"updatedAt":           "2026-05-02T08:00:00Z",
			"mergedAt":            nil,
		},
	}
}

func emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakegh:", err)
		os.Exit(3)
	}
	fmt.Println(string(b))
}

// corpus is the fixed issue set every scenario serves, shaped exactly like
// `gh issue list --json`. Two issues with different metadata coverage: one
// carrying labels, an assignee and a milestone, one carrying none, so a
// prefill test can assert both the filled and the empty mapping.
func corpus() []map[string]any {
	return []map[string]any{
		{
			"number": 200,
			"title":  "GitHub integration: select a GitHub issue when creating a task",
			"body":   "Most work in a GitHub-hosted repo starts life as a GitHub issue.",
			"url":    "https://github.com/octo/repo/issues/200",
			"state":  "OPEN",
			"labels": []map[string]any{
				{"name": "enhancement"},
				{"name": "area/api"},
			},
			"author":    map[string]any{"login": "octocat"},
			"assignees": []map[string]any{{"login": "hubot"}},
			"milestone": map[string]any{"number": 4, "title": "v0.2.0"},
			"createdAt": "2026-08-26T19:21:29Z",
			"updatedAt": "2026-08-26T19:30:00Z",
		},
		{
			"number":    41,
			"title":     "Board header truncates on narrow terminals",
			"body":      "",
			"url":       "https://github.com/octo/repo/issues/41",
			"state":     "OPEN",
			"labels":    []map[string]any{},
			"author":    map[string]any{"login": "hubot"},
			"assignees": []map[string]any{},
			"milestone": nil,
			"createdAt": "2026-07-01T08:00:00Z",
			"updatedAt": "2026-07-02T08:00:00Z",
		},
	}
}
