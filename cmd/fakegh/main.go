// fakegh is a scenario-driven stand-in for the `gh` CLI (task 035), the same
// shape cmd/fakeagent takes for the agent CLIs: the command is read from
// argv, the behaviour from the environment, so the argv internal/github
// builds stays faithful to the real tool and no test ever calls GitHub.
//
// It answers the invocations the daemon makes and nothing else:
//
//	gh auth status
//	gh issue list --repo owner/name --state S --limit N --json FIELDS
//	gh issue view N --repo owner/name --json FIELDS
//	gh issue close N -R owner/name [--reason completed|"not planned"|duplicate] [--duplicate-of M]
//	gh issue reopen N -R owner/name
//	gh api [-i] [-X METHOD] [-H "K: V"]... [--input -] [-f k=v]... [-F k=v]... ENDPOINT
//	gh pr list --repo owner/name --state S --limit N --json FIELDS
//	gh pr view N --repo owner/name --json FIELDS
//	gh pr create --repo owner/name --base B --head H --title T --body-file - [--draft]
//	gh pr merge N -R owner/name --merge|--squash|--rebase --match-head-commit SHA
//	gh pr close N -R owner/name
//	gh pr reopen N -R owner/name
//	gh pr comment N -R owner/name --body-file -
//	gh run rerun ID --failed -R owner/name
//
// `gh api` serves internal/github/fakeissues (#661), which holds the issue
// corpus and the REST answers for both the `gh` leg and the `net/http` one:
//
//	repos/{o}/{r}/issues           state, since, sort, direction, per_page,
//	                               page; pull request rows included
//	repos/{o}/{r}/issues/{n}       GET, and PATCH with state, state_reason,
//	                               duplicate_issue_id
//	repos/{o}/{r}/issues/comments  since, sort, direction, per_page, page
//
// With -i it prints gh's `HTTP/2.0 <code> <text>` status line, the headers
// (Etag, Link, X-Ratelimit-*) and a blank line before the body. A GET whose
// If-None-Match is the page's current weak etag answers gh 2.100.0's
// observed 304: that status line, no body, `gh: HTTP 304` on stderr, exit 1.
// Any other non-2xx prints the JSON error body on stdout, `gh: <message>
// (HTTP n)` on stderr, and exits 1. A corpus row may carry a fake-only
// marker that no answer shows — `"_fake": {"transferred_to": "o/r#12"}`
// (301 with a Location) or `"_fake": {"deleted": true}` (410) — and is left
// out of every listing.
//
// Scenario selection is environment-driven:
//
//	FAKEGH_SCENARIO  success (default) | logged-out | empty | not-found |
//	                 unauthorized | rate-limited | forbidden | bad-json |
//	                 hang | pr-exists (a `pr create` refused because one
//	                 already exists for the head — task 069) |
//	                 read-only (every read answers, every write is refused
//	                 with a 403 — the scope a merge's preflight read passes
//	                 and its write does not, task 068.4) |
//	                 behind | blocked-running | blocked | dirty (the
//	                 `mergeStateStatus` #412 reports, the last with no
//	                 unfinished check in its rollup) |
//	                 head-moved (`pr merge` refused because the head moved
//	                 after the preflight — the `--match-head-commit` pin) |
//	                 unreachable (every call but `auth status` prints gh's
//	                 network-failure wording and exits 1).
//	                 Under `gh api`, `read-only` answers every GET and
//	                 refuses a PATCH with gh's 403, and `rate-limited`
//	                 answers every call 403 with X-Ratelimit-Remaining: 0,
//	                 X-Ratelimit-Reset and Retry-After.
//	FAKEGH_SCENARIO_FILE
//	                 when set and naming a non-empty file, its trimmed
//	                 content is the scenario for this invocation, over
//	                 FAKEGH_SCENARIO. It is read on every invocation, so a
//	                 gate can flip one running daemon between scenarios.
//	FAKEGH_ISSUES_FILE
//	                 when set, the issue corpus: a JSON array of REST-shaped
//	                 rows (issues; pull requests, which carry
//	                 `pull_request`; comments, which carry `issue_url`),
//	                 re-read on every invocation and written back by
//	                 `gh api -X PATCH`, `issue close` and `issue reopen`.
//	                 `issue list`/`issue view` read it too, leaving out pull
//	                 request and `_fake`-marked rows. Unset, the built-in
//	                 corpus (#200 and #41, both open) answers; set but
//	                 missing, the built-in corpus seeds it.
//	FAKEGH_STATE_FILE
//	                 when set, `pr merge`, `pr close` and `pr reopen` record
//	                 the state they left a pull request in here, and `pr view`
//	                 reads it back, so a write-then-read sequence answers the
//	                 new state.
//	FAKEGH_STDIN_FILE
//	                 when set, `pr comment` writes the body it read from stdin
//	                 here, so a test can assert the body travelled on stdin.
//	FAKEGH_CREATED_FILE
//	                 when set, `pr create` writes the pull request it made
//	                 here and `pr view`/`pr list` read it back, so the
//	                 create-then-read sequence internal/github performs
//	                 answers without a network.
//	FAKEGH_PR_BRANCH when set, the head branch of the first pull request in
//	                 the corpus, so a gate or a test can point one at the
//	                 branch a real task was given.
//	FAKEGH_PR_TITLE  when set, the title of that same pull request, so a
//	                 picture of it can read like the task it belongs to.
//	FAKEGH_REPO      when set (`owner/name`), the repository the corpus lives
//	                 in instead of octo/repo: every URL and every head
//	                 repository that names octo/repo names this one, the way
//	                 the real CLI answers for the repository it was asked
//	                 about. scripts/screenshots.sh sets both of these.
//	FAKEGH_ARGV_FILE when set, each invocation appends its argv (one
//	                 space-joined line) to this file, so a test can assert
//	                 the flags the adapter passed — and, for a disabled
//	                 integration, that it made no call at all.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/github/fakeissues"
)

func main() {
	args := os.Args[1:]
	recordArgv(args)
	scenario := fakeissues.Scenario()
	if scenario == "hang" {
		// Long enough to outlive any timeout a test sets; the parent kills it.
		time.Sleep(10 * time.Minute)
		return
	}
	authCall := len(args) >= 2 && args[0] == "auth" && args[1] == "status"
	if scenario == "unreachable" && !authCall {
		// `auth status` still answers: the token is in the keyring, and the
		// scenario is about the API call failing, which is what
		// internal/github maps to ReasonUnreachable.
		unreachable()
	}
	switch {
	case authCall:
		authStatus(scenario)
	case len(args) >= 2 && args[0] == "issue" && args[1] == "list":
		issueList(scenario, args)
	case len(args) >= 3 && args[0] == "issue" && args[1] == "view":
		issueView(scenario, args[2])
	case len(args) >= 3 && args[0] == "issue" && (args[1] == "close" || args[1] == "reopen"):
		issueSetState(scenario, args)
	case len(args) >= 2 && args[0] == "api":
		api(scenario, args[1:])
	case len(args) >= 2 && args[0] == "pr" && args[1] == "list":
		pullList(scenario, flagValue(args, "--state"))
	case len(args) >= 2 && args[0] == "pr" && args[1] == "create":
		pullCreate(scenario, args)
	case len(args) >= 3 && args[0] == "pr" && args[1] == "view":
		pullView(scenario, args[2])
	case len(args) >= 3 && args[0] == "pr" && args[1] == "merge":
		pullMerge(scenario, args)
	case len(args) >= 3 && args[0] == "pr" && (args[1] == "close" || args[1] == "reopen"):
		pullSetState(scenario, args[1], args[2])
	case len(args) >= 3 && args[0] == "pr" && args[1] == "comment":
		pullComment(scenario, args[2])
	case len(args) >= 3 && args[0] == "run" && args[1] == "rerun":
		runRerun(scenario, args[2])
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

// unreachable is gh's wording when it cannot open a connection at all.
func unreachable() {
	fmt.Fprintln(os.Stderr, "error connecting to api.github.com")
	fmt.Fprintln(os.Stderr, "check your internet connection or https://githubstatus.com")
	os.Exit(1)
}

func issueList(scenario string, args []string) {
	if fail(scenario) {
		return
	}
	if scenario == "bad-json" {
		fmt.Println("not json at all")
		return
	}
	limit, _ := strconv.Atoi(flagValue(args, "--limit"))
	// `--search` stays ignored: the corpus is small enough that a since
	// filter would only hide rows a test asked for.
	issues, err := fakeissues.FromEnv().List(flagValue(args, "--state"), limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakegh:", err)
		os.Exit(3)
	}
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
	issue, ok, err := fakeissues.FromEnv().Issue(n)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakegh:", err)
		os.Exit(3)
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "gh: Could not resolve to an Issue with the number of %d. (HTTP 404)\n", n)
		os.Exit(1)
	}
	emit(issue)
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
	pull := findPull(n)
	// The merge state a preflight reads (task 068.4). Only #412 carries one
	// that varies by scenario; every other row answers what GitHub would.
	switch {
	case n != 412:
		if draft, _ := pull["isDraft"].(bool); draft {
			pull["mergeStateStatus"] = "DRAFT"
		} else {
			pull["mergeStateStatus"] = "CLEAN"
		}
	case scenario == "behind":
		pull["mergeStateStatus"] = "BEHIND"
	case scenario == "blocked-running":
		pull["mergeStateStatus"] = "BLOCKED"
	case scenario == "blocked":
		pull["mergeStateStatus"] = "BLOCKED"
		pull["statusCheckRollup"] = withoutRunning(pull["statusCheckRollup"])
	case scenario == "dirty":
		pull["mergeStateStatus"] = "DIRTY"
	default:
		pull["mergeStateStatus"] = "CLEAN"
	}
	emit(pull)
}

// findPull is the corpus row numbered n, or a 404 exit the way the real CLI
// reports one.
func findPull(n int) map[string]any {
	for _, pull := range pullCorpus() {
		if pull["number"] == n {
			return pull
		}
	}
	fmt.Fprintf(os.Stderr, "gh: Could not resolve to a PullRequest with the number of %d. (HTTP 404)\n", n)
	os.Exit(1)
	return nil
}

// withoutRunning drops the unfinished checks, so a blocked merge has nothing
// to wait for.
func withoutRunning(rollup any) []map[string]any {
	rows, _ := rollup.([]map[string]any)
	kept := []map[string]any{}
	for _, row := range rows {
		if row["status"] == "IN_PROGRESS" || row["status"] == "QUEUED" {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}

// writeRefused is the 403 every write answers under `forbidden` and
// `read-only`, in the real CLI's words, and any other failure scenario's
// stderr. It reports whether the invocation was refused (it exits first).
func writeRefused(scenario string) bool {
	if scenario == "forbidden" || scenario == "read-only" {
		fmt.Fprintln(os.Stderr, "gh: HTTP 403: Resource not accessible by integration")
		os.Exit(1)
		return true
	}
	return fail(scenario)
}

func pullNumber(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh: invalid pull request number %q\n", raw)
		os.Exit(1)
	}
	return n
}

// pullMerge answers `gh pr merge` (task 068.4). The pin is honoured the way
// GitHub honours it: a `--match-head-commit` that is not the head refuses,
// and so does every merge under `head-moved`, which stands for a push landing
// between the daemon's preflight and its send.
func pullMerge(scenario string, args []string) {
	if writeRefused(scenario) {
		return
	}
	pull := findPull(pullNumber(args[2]))
	if scenario == "head-moved" || flagValue(args, "--match-head-commit") != pull["headRefOid"] {
		fmt.Fprintln(os.Stderr,
			"GraphQL: Head branch was modified. Review and try the merge again. (mergePullRequest)")
		os.Exit(1)
	}
	if pull["state"] != "OPEN" {
		fmt.Fprintf(os.Stderr, "X Pull request octo/repo#%v is not mergeable\n", pull["number"])
		os.Exit(1)
	}
	recordState(pull["number"], "MERGED")
	fmt.Fprintf(os.Stderr, "✓ Merged pull request octo/repo#%v\n", pull["number"])
}

func pullSetState(scenario, verb, number string) {
	if writeRefused(scenario) {
		return
	}
	pull := findPull(pullNumber(number))
	state, word := "CLOSED", "Closed"
	if verb == "reopen" {
		state, word = "OPEN", "Reopened"
	}
	recordState(pull["number"], state)
	fmt.Fprintf(os.Stderr, "✓ %s pull request octo/repo#%v\n", word, pull["number"])
}

// pullComment answers `gh pr comment`, whose body arrives on stdin because
// the adapter passes `--body-file -`. The real CLI prints the comment's URL.
func pullComment(scenario, number string) {
	if writeRefused(scenario) {
		return
	}
	pull := findPull(pullNumber(number))
	body, _ := io.ReadAll(os.Stdin)
	if path := os.Getenv("FAKEGH_STDIN_FILE"); path != "" {
		_ = os.WriteFile(path, body, 0o600)
	}
	fmt.Printf("https://github.com/octo/repo/pull/%v#issuecomment-1\n", pull["number"])
}

func runRerun(scenario, id string) {
	if writeRefused(scenario) {
		return
	}
	fmt.Fprintf(os.Stderr, "✓ Requested rerun (failed jobs) of run %s\n", id)
}

// recordState persists a write's resulting state so a later `pr view` reads
// it back.
func recordState(number any, state string) {
	path := os.Getenv("FAKEGH_STATE_FILE")
	if path == "" {
		return
	}
	states := readStates()
	states[fmt.Sprint(number)] = state
	if encoded, err := json.Marshal(states); err == nil {
		_ = os.WriteFile(path, encoded, 0o600)
	}
}

func readStates() map[string]string {
	states := map[string]string{}
	path := os.Getenv("FAKEGH_STATE_FILE")
	if path == "" {
		return states
	}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &states)
	}
	return states
}

// applyStates lays the recorded write results over the corpus.
func applyStates(rows []map[string]any) []map[string]any {
	states := readStates()
	for _, row := range rows {
		state, ok := states[fmt.Sprint(row["number"])]
		if !ok {
			continue
		}
		row["state"] = state
		if state == "MERGED" {
			row["mergedAt"] = "2026-09-15T10:00:00Z"
		}
	}
	return rows
}

// pullCorpus is the fixed pull-request set, shaped exactly like
// `gh pr list --json`. Three rows on purpose: an open one whose head branch a
// test or gate can point at a real task, an open draft, and a **merged** one,
// which is the case the durable link exists to serve and the one an open-only
// listing can never answer, and a **fork**, whose head lives in another
// repository entirely.
func pullCorpus() []map[string]any {
	// A pull request `pr create` made in an earlier invocation comes first,
	// so the create → read-back sequence internal/github performs answers
	// without a network (task 069).
	var out []map[string]any
	if row, ok := createdPull(); ok {
		out = append(out, row)
	}
	branch := os.Getenv("FAKEGH_PR_BRANCH")
	if branch == "" {
		branch = "vincent/1-add-a-thing"
	}
	title := os.Getenv("FAKEGH_PR_TITLE")
	if title == "" {
		title = "Add a thing"
	}
	return applyStates(rehome(append(out, []map[string]any{
		{
			"number":              412,
			"title":               title,
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
	}...)))
}

// corpusRepo is the repository the fixed corpora are written against.
const corpusRepo = "octo/repo"

// rehome moves rows into FAKEGH_REPO: each string naming corpusRepo as a path
// segment names that repository instead, and a head repository that is
// corpusRepo becomes it. A fork keeps its own owner and takes the new name,
// since a fork is named after what it forked. Unset, the rows are untouched,
// which is what every test and gate reads.
func rehome(rows []map[string]any) []map[string]any {
	repo := os.Getenv("FAKEGH_REPO")
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return rows
	}
	for _, row := range rows {
		rehomeStrings(row, "/"+corpusRepo+"/", "/"+repo+"/")
		if head, ok := row["headRepository"].(map[string]any); ok {
			head["name"] = name
		}
		if head, ok := row["headRepositoryOwner"].(map[string]any); ok && head["login"] == "octo" {
			head["login"] = owner
		}
	}
	return rows
}

// rehomeStrings replaces from with to in every string under v, in place.
func rehomeStrings(v map[string]any, from, to string) {
	for k, x := range v {
		switch x := x.(type) {
		case string:
			v[k] = strings.ReplaceAll(x, from, to)
		case map[string]any:
			rehomeStrings(x, from, to)
		case []map[string]any:
			for _, row := range x {
				rehomeStrings(row, from, to)
			}
		}
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

// corpus is the built-in issue set in `gh issue list --json`'s shape, moved
// into FAKEGH_REPO. It lives in internal/github/fakeissues now, in REST
// shape, so the porcelain and `gh api` answer from one stored form.
func corpus() []map[string]any {
	issues, _ := fakeissues.Store{Repo: os.Getenv("FAKEGH_REPO")}.List("all", 0)
	return issues
}

// closeReasons maps `gh issue close --reason`'s values to the REST
// state_reason each one writes.
var closeReasons = map[string]string{
	"completed":   "completed",
	"not planned": "not_planned",
	"duplicate":   "duplicate",
}

// issueSetState answers `gh issue close` and `gh issue reopen` by sending the
// PATCH gh would through the same corpus, so the write shows on `gh api` and
// on `issue view` alike. The lines it prints are gh's.
func issueSetState(scenario string, args []string) {
	if writeRefused(scenario) {
		return
	}
	verb := args[1]
	n, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh: invalid issue number %q\n", args[2])
		os.Exit(1)
	}
	repo := flagValue(args, "-R")
	if repo == "" {
		repo = flagValue(args, "--repo")
	}
	if repo == "" {
		repo = corpusRepo
	}
	store := fakeissues.FromEnv()
	issue, ok, err := store.Issue(n)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakegh:", err)
		os.Exit(3)
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "gh: Could not resolve to an Issue with the number of %d. (HTTP 404)\n", n)
		os.Exit(1)
	}
	patch := map[string]any{"state": "open"}
	word, already := "Reopened", issue["state"] == "OPEN"
	if verb == "close" {
		word, already = "Closed", issue["state"] == "CLOSED"
		reason := flagValue(args, "--reason")
		dup := flagValue(args, "--duplicate-of")
		if reason == "" && dup != "" {
			reason = "duplicate"
		}
		stateReason, valid := closeReasons[reason]
		if reason != "" && !valid {
			fmt.Fprintf(os.Stderr,
				"invalid argument %q for \"-r, --reason\" flag: valid values are {completed|not planned|duplicate}\n", reason)
			os.Exit(1)
		}
		patch = map[string]any{"state": "closed"}
		if stateReason != "" {
			patch["state_reason"] = stateReason
		}
		if d, err := strconv.Atoi(strings.TrimPrefix(dup, "#")); err == nil {
			patch["duplicate_issue_id"] = d
		}
	}
	if already {
		state := "open"
		if verb == "close" {
			state = "closed"
		}
		fmt.Fprintf(os.Stderr, "! Issue %s#%d (%s) is already %s\n", repo, n, issue["title"], state)
		return
	}
	body, _ := json.Marshal(patch)
	resp, err := store.Serve(fakeissues.Request{
		Method:   http.MethodPatch,
		Endpoint: fmt.Sprintf("repos/%s/issues/%d", repo, n),
		Body:     body,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakegh:", err)
		os.Exit(3)
	}
	if resp.Status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "gh: %s (HTTP %d)\n", resp.Message(), resp.Status)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "✓ %s issue %s#%d (%s)\n", word, repo, n, issue["title"])
}

// api answers `gh api` from internal/github/fakeissues. The argv is parsed
// the way gh parses it: fields make the method POST unless -X says
// otherwise, and on a GET they become the query string.
func api(scenario string, args []string) {
	method, endpoint, include := "", "", false
	header := http.Header{}
	fields := map[string]any{}
	input := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() string {
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "flag needs an argument: %s\n", a)
				os.Exit(1)
			}
			i++
			return args[i]
		}
		switch {
		case a == "-i" || a == "--include":
			include = true
		case a == "-X" || a == "--method":
			method = strings.ToUpper(value())
		case a == "-H" || a == "--header":
			k, v, _ := strings.Cut(value(), ":")
			header.Add(strings.TrimSpace(k), strings.TrimSpace(v))
		case a == "--input":
			input = value()
		case a == "-f" || a == "--raw-field":
			k, v, _ := strings.Cut(value(), "=")
			fields[k] = v
		case a == "-F" || a == "--field":
			k, v, _ := strings.Cut(value(), "=")
			fields[k] = typedField(v)
		case strings.HasPrefix(a, "-"):
			// A flag the fake does not need (--silent, --paginate …) carries
			// no value it would read.
		case endpoint == "":
			endpoint = a
		}
	}
	if method == "" {
		method = http.MethodGet
		if len(fields) > 0 || input != "" {
			method = http.MethodPost
		}
	}
	var body []byte
	switch {
	case input == "-":
		body, _ = io.ReadAll(os.Stdin)
	case input != "":
		var err error
		if body, err = os.ReadFile(input); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case len(fields) > 0 && method == http.MethodGet:
		q := url.Values{}
		for k, v := range fields {
			q.Set(k, fmt.Sprint(v))
		}
		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		endpoint += sep + q.Encode()
	case len(fields) > 0:
		body, _ = json.Marshal(fields)
	}
	resp, err := fakeissues.FromEnv().Serve(fakeissues.Request{
		Method: method, Endpoint: endpoint, Header: header, Body: body, Scenario: scenario,
	})
	if errors.Is(err, fakeissues.ErrUnreachable) {
		unreachable()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakegh:", err)
		os.Exit(3)
	}
	writeAPI(resp, include)
	if resp.Status < 200 || resp.Status > 299 {
		os.Exit(1)
	}
}

// writeAPI prints a response the way gh api does: under -i the status line
// (`fmt.Fprintln`), the headers sorted by name with CRLF endings and a CRLF
// blank line, then the body as received. Anything outside 2xx — a 304
// included, as observed with gh 2.100.0 — also gets gh's stderr line.
func writeAPI(resp fakeissues.Response, include bool) {
	if include {
		fmt.Println(resp.StatusLine())
		names := make([]string, 0, len(resp.Header))
		for k := range resp.Header {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Printf("%s: %s\r\n", k, strings.Join(resp.Header[k], ", "))
		}
		fmt.Print("\r\n")
	}
	_, _ = os.Stdout.Write(resp.Body)
	if resp.Status >= 200 && resp.Status <= 299 {
		return
	}
	if msg := resp.Message(); msg != "" {
		fmt.Fprintf(os.Stderr, "gh: %s (HTTP %d)\n", msg, resp.Status)
		return
	}
	fmt.Fprintf(os.Stderr, "gh: HTTP %d\n", resp.Status)
}

// typedField is -F's conversion: true, false, null and integers become JSON
// values, anything else stays a string.
func typedField(v string) any {
	switch v {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return v
}

// createdPullNumber is the number `pr create` always reports. It is outside
// the fixed corpus so a test can tell a created pull request from a listed
// one by its number alone.
const createdPullNumber = 999

// pullCreate answers `gh pr create` (task 069).
//
// The real CLI prints the new pull request's web URL on stdout and nothing
// else — there is no `--json` on it — and internal/github parses the number
// out of that URL and then reads the pull request back through `pr view`. So
// this writes the created row to FAKEGH_CREATED_FILE and pullCorpus reads it
// back in, which is what makes the two-call sequence work end to end without
// a network.
//
// The body arrives on **stdin**, because the adapter passes `--body-file -`:
// a pull request body is prose a human just typed and putting it in argv
// would put it under Windows' 32 KiB command-line limit.
func pullCreate(scenario string, args []string) {
	if scenario == "pr-exists" {
		fmt.Fprintln(os.Stderr,
			"gh: a pull request for branch \"x\" into branch \"main\" already exists:")
		os.Exit(1)
	}
	if writeRefused(scenario) {
		return
	}
	body, _ := io.ReadAll(os.Stdin)
	row := map[string]any{
		"number":              createdPullNumber,
		"title":               flagValue(args, "--title"),
		"body":                string(body),
		"url":                 "https://github.com/octo/repo/pull/999",
		"state":               "OPEN",
		"isDraft":             hasFlag(args, "--draft"),
		"headRefName":         flagValue(args, "--head"),
		"headRepository":      map[string]any{"name": "repo"},
		"headRepositoryOwner": map[string]any{"login": "octo"},
		"baseRefName":         flagValue(args, "--base"),
		"author":              map[string]any{"login": "octocat"},
		"createdAt":           "2026-08-31T10:00:00Z",
		"updatedAt":           "2026-08-31T10:00:00Z",
		"mergedAt":            nil,
	}
	recordCreated(row)
	fmt.Println(row["url"])
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// recordCreated persists the created row so a later `pr view` finds it.
func recordCreated(row map[string]any) {
	path := os.Getenv("FAKEGH_CREATED_FILE")
	if path == "" {
		return
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, encoded, 0o600)
}

// createdPull reads back what pullCreate wrote, so `pr view 999` answers.
func createdPull() (map[string]any, bool) {
	path := os.Getenv("FAKEGH_CREATED_FILE")
	if path == "" {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, false
	}
	// json.Unmarshal makes every number a float64; the lookups compare
	// against an int, so the one field they compare is put back.
	if n, ok := row["number"].(float64); ok {
		row["number"] = int(n)
	}
	return row, true
}
