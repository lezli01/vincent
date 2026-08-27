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
//
// Scenario selection is environment-driven:
//
//	FAKEGH_SCENARIO  success (default) | logged-out | empty | not-found |
//	                 unauthorized | rate-limited | forbidden | bad-json |
//	                 hang
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
	default:
		fmt.Fprintf(os.Stderr, "fakegh: unsupported invocation %q\n", strings.Join(args, " "))
		os.Exit(2)
	}
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
