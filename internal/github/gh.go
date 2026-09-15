package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ghFields is the `--json` field set both `gh issue list` and `gh issue view`
// are asked for. One list, so the two calls cannot drift into producing
// different Issues for the same issue.
const ghFields = "number,title,body,url,state,labels,author,assignees,milestone,createdAt,updatedAt"

// ghIssue is `gh --json`'s shape. It is deliberately its own type rather than
// json tags on Issue: `gh` and the REST API disagree about almost every name
// (`url` vs `html_url`, `author` vs `user`, `createdAt` vs `created_at`), and
// a single struct carrying both spellings would silently accept a half-parsed
// answer from either.
type ghIssue struct {
	Number int         `json:"number"`
	Title  string      `json:"title"`
	Body   string      `json:"body"`
	URL    string      `json:"url"`
	State  string      `json:"state"`
	Labels []wireLabel `json:"labels"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Assignees []wireAccount `json:"assignees"`
	Milestone *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"milestone"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (g ghIssue) normalize(repo Repo, now time.Time) Issue {
	issue := Issue{
		Repo:      repo.String(),
		Number:    g.Number,
		Title:     g.Title,
		Body:      g.Body,
		URL:       g.URL,
		State:     normalizeState(g.State),
		Author:    normalizeLogin(g.Author.Login),
		CreatedAt: g.CreatedAt,
		UpdatedAt: g.UpdatedAt,
		FetchedAt: now,
	}
	issue.Labels = labelNames(g.Labels)
	issue.Assignees = logins(g.Assignees)
	// Assignee is the first assignee only. §8.1.2 field values are single
	// strings, and a joined list under a field named `assignee` would read as
	// one login.
	if len(issue.Assignees) > 0 {
		issue.Assignee = issue.Assignees[0]
	}
	if g.Milestone != nil {
		issue.Milestone, issue.MilestoneNumber = g.Milestone.Title, g.Milestone.Number
	}
	return issue
}

// ghAuthenticated reports whether `gh` can act for the user. `gh auth status`
// exits non-zero when no host is logged in, which is the whole question —
// its output is not parsed, because that text is not a contract.
func (c *Client) ghAuthenticated(ctx context.Context, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	_, err := c.runGH(ctx, path, "auth", "status")
	return err == nil
}

func (c *Client) ghList(ctx context.Context, cred credential, repo Repo, opts ListOptions) ([]Issue, error) {
	out, err := c.runGH(ctx, cred.ghPath, ghListArgs("issue", repo, opts, ghFields)...)
	if err != nil {
		return nil, err
	}
	return parseGHList(out, repo, c.now())
}

// ghListArgs is the whole `gh issue list` / `gh pr list` argv, in one place so
// a test can assert on it (ghCreateArgs's precedent). noun is "issue" or "pr".
//
// Neither subcommand has a since flag, so Since becomes a `--search`
// qualifier — `updated:>=` with a UTC RFC 3339 instant, which GitHub's search
// syntax accepts. `sort:updated-desc` rides with it because a search is
// otherwise returned in creation order (observed against gh 2.100.0), and
// with a Limit that would keep the newest rows instead of the recently
// changed ones ListOptions.Since promises. `--state` still carries the state
// rather than an `is:` qualifier in the query: gh honours the flag alongside
// a search (observed: `--state all` returned open, closed and merged rows).
func ghListArgs(noun string, repo Repo, opts ListOptions, fields string) []string {
	args := []string{
		noun, "list",
		"--repo", repo.String(),
		"--state", opts.state(),
		"--limit", strconv.Itoa(opts.limit()),
	}
	if since := opts.since(); !since.IsZero() {
		args = append(args, "--search", "updated:>="+since.Format(time.RFC3339)+" sort:updated-desc")
	}
	return append(args, "--json", fields)
}

// parseGHList is the `gh issue list --json` half of the leg, split from the
// exec so the table tests can drive it straight from captured output.
func parseGHList(out []byte, repo Repo, now time.Time) ([]Issue, error) {
	var raw []ghIssue
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, newError(ReasonBadResponse, "decode gh issue list: %v", err)
	}
	issues := make([]Issue, 0, len(raw))
	for _, g := range raw {
		issues = append(issues, g.normalize(repo, now))
	}
	return issues, nil
}

func (c *Client) ghGet(ctx context.Context, cred credential, repo Repo, number int) (Issue, error) {
	out, err := c.runGH(ctx, cred.ghPath,
		"issue", "view", strconv.Itoa(number),
		"--repo", repo.String(),
		"--json", ghFields)
	if err != nil {
		return Issue{}, err
	}
	return parseGHIssue(out, repo, c.now())
}

func parseGHIssue(out []byte, repo Repo, now time.Time) (Issue, error) {
	var raw ghIssue
	if err := json.Unmarshal(out, &raw); err != nil {
		return Issue{}, newError(ReasonBadResponse, "decode gh issue view: %v", err)
	}
	if raw.Number == 0 {
		return Issue{}, newError(ReasonBadResponse, "gh issue view returned no issue number")
	}
	return raw.normalize(repo, now), nil
}

// runGH executes gh and returns stdout. Every failure becomes an *Error
// carrying a named reason plus the stderr as *detail* — which reaches the
// daemon log and nothing else (decision 1).
func (c *Client) runGH(ctx context.Context, path string, args ...string) ([]byte, error) {
	out, stderr, err := execGH(ctx, path, nil, args...)
	if err != nil {
		return nil, ghError(err, stderr, ctx.Err())
	}
	return out, nil
}

// execGH is the one place `gh` is executed. stdin is nil for every call but
// the two that take a body on `--body-file -`.
func execGH(ctx context.Context, path string, stdin io.Reader, args ...string) (stdout []byte, stderr string, err error) {
	// G204: path is the configured or PATH-resolved `gh`, args are an argument
	// slice built by this package. Never a shell string.
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // G204: see above
	hideConsole(cmd)
	// GH_PAGER and NO_COLOR are not set here: `--json` output is neither
	// paged nor colored by gh, and inheriting the user's environment is the
	// point (§2).
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, errOut.String(), err
	}
	return out.Bytes(), errOut.String(), nil
}

// ghError maps a failed `gh` invocation onto the reason vocabulary. The
// mapping reads stderr because that is the only channel gh reports a cause
// on — its exit code is 1 for everything — but no part of that text escapes
// this package's Detail field.
func ghError(runErr error, stderr string, ctxErr error) *Error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = runErr.Error()
	}
	if ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return &Error{Reason: ReasonTimeout, Detail: detail}
		}
		return &Error{Reason: ReasonUnreachable, Detail: detail}
	}
	var exit *exec.ExitError
	if !errors.As(runErr, &exit) {
		// gh could not be executed at all — deleted between the probe and
		// the call, or not executable.
		return &Error{Reason: ReasonNoCredential, Detail: detail}
	}
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "rate limit"):
		return &Error{Reason: ReasonRateLimited, Detail: detail}
	case strings.Contains(lower, "bad credentials"),
		strings.Contains(lower, "http 401"),
		strings.Contains(lower, "authentication token"):
		return &Error{Reason: ReasonUnauthorized, Detail: detail}
	case strings.Contains(lower, "not logged"), strings.Contains(lower, "gh auth login"):
		return &Error{Reason: ReasonNoCredential, Detail: detail}
	case strings.Contains(lower, "http 403"), strings.Contains(lower, "must have admin"),
		strings.Contains(lower, "resource not accessible"):
		return &Error{Reason: ReasonForbidden, Detail: detail}
	case strings.Contains(lower, "could not resolve to"),
		strings.Contains(lower, "not found"),
		strings.Contains(lower, "http 404"):
		return &Error{Reason: ReasonNotFound, Detail: detail}
	default:
		return &Error{Reason: ReasonUnreachable, Detail: fmt.Sprintf("exit %d: %s", exit.ExitCode(), detail)}
	}
}

// ghVersion returns `gh --version`'s first line, or "" when it cannot be
// asked. It is reported verbatim for the same reason gitx reports git's:
// nothing in vincent branches on a `gh` version, and a parsed one would be a
// number somebody later gates on.
func (c *Client) ghVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	out, err := c.runGH(ctx, path, "--version")
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

// ghPullFields is the `--json` field set both `gh pr list` and `gh pr view`
// are asked for, for the reason ghFields is one list: the two calls cannot
// drift into producing different PullRequests for the same pull request.
//
// It carries `statusCheckRollup` because that is the only way to ask `gh` for
// a pull request's checks at all (task 068): there is no `gh pr checks
// --json` shape both legs could be normalized from, and splitting the rollup
// into a second field list would reintroduce exactly the drift one list
// exists to prevent.
//
// `labels` and `reviewRequests` are what a listing diff reads (task 096
// decisions D and E), and they ride on the view as well for the same
// one-list reason.
const ghPullFields = "number,title,body,url,state,isDraft,headRefName,headRefOid,headRepositoryOwner,headRepository,baseRefName,author,createdAt,updatedAt,mergedAt,statusCheckRollup,labels,reviewRequests"

// ghPull is `gh pr --json`'s shape. Its own type, for the reason ghIssue is:
// `gh` and the REST API disagree about almost every name (`isDraft` vs
// `draft`, `headRefName` vs `head.ref`, `mergedAt` vs `merged_at`).
type ghPull struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	URL     string `json:"url"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
	Head    string `json:"headRefName"`
	HeadOid string `json:"headRefOid"`
	// `gh` splits the head repository across two fields and neither is
	// `owner/name`: `headRepository` is the bare name and
	// `headRepositoryOwner` is the login. Both are null for a pull request
	// whose fork has been deleted, which normalizes to an empty HeadRepo —
	// read as "same repository", per PullRequest.Fork.
	HeadRepo struct {
		Name string `json:"name"`
	} `json:"headRepository"`
	HeadRepoOwner struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	Base   string `json:"baseRefName"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
	MergedAt  *time.Time  `json:"mergedAt"`
	Labels    []wireLabel `json:"labels"`
	// ReviewRequests is one entry per outstanding request, discriminated by
	// `__typename`: an account is `{"__typename":"User","login":…}` (captured
	// from gh 2.100.0), and a Team entry is skipped — see
	// PullRequest.RequestedReviewers.
	ReviewRequests []struct {
		TypeName string `json:"__typename"`
		Login    string `json:"login"`
	} `json:"reviewRequests"`
	// StatusCheckRollup is GitHub's own folding of check runs and legacy
	// commit statuses into one array, discriminated by `__typename`. The two
	// shapes share almost no field names, which is why both sets are declared
	// here and read according to the typename rather than by hoping one is
	// empty.
	StatusCheckRollup []ghCheck `json:"statusCheckRollup"`
}

// ghCheck is one row of `statusCheckRollup`. `CheckRun` and `StatusContext`
// arrive in the same array with disjoint fields; `__typename` is what says
// which one this is.
type ghCheck struct {
	TypeName string `json:"__typename"`
	// CheckRun fields.
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	DetailsURL  string    `json:"detailsUrl"`
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"completedAt"`
	// StatusContext fields.
	Context   string    `json:"context"`
	State     string    `json:"state"`
	TargetURL string    `json:"targetUrl"`
	CreatedAt time.Time `json:"createdAt"`
}

// normalize folds one rollup row onto a CheckRun. A row with no name at all
// is dropped by the caller rather than rendered as a blank line: `gh` returns
// one for a check suite that has been requested and not yet named.
func (g ghCheck) normalize() CheckRun {
	if strings.EqualFold(g.TypeName, "StatusContext") {
		return CheckRun{
			Name:  g.Context,
			State: normalizeStatusState(g.State),
			URL:   g.TargetURL,
			// A legacy commit status is never Actions-backed: it predates
			// check runs entirely, so no run id is looked for.
			StartedAt: g.CreatedAt,
		}
	}
	return CheckRun{
		Name:        g.Name,
		State:       normalizeCheckState(g.Status, g.Conclusion),
		URL:         g.DetailsURL,
		RunID:       actionsRunID(g.DetailsURL),
		StartedAt:   g.StartedAt,
		CompletedAt: g.CompletedAt,
	}
}

func (g ghPull) normalize(repo Repo, now time.Time) PullRequest {
	pull := PullRequest{
		Repo:       repo.String(),
		Number:     g.Number,
		Title:      g.Title,
		Body:       g.Body,
		URL:        g.URL,
		State:      normalizeState(g.State),
		Draft:      g.IsDraft,
		HeadBranch: g.Head,
		HeadSHA:    g.HeadOid,
		HeadRepo:   joinRepo(g.HeadRepoOwner.Login, g.HeadRepo.Name),
		BaseBranch: g.Base,
		Author:     normalizeLogin(g.Author.Login),
		Labels:     labelNames(g.Labels),
		CreatedAt:  g.CreatedAt,
		UpdatedAt:  g.UpdatedAt,
		FetchedAt:  now,
	}
	for _, r := range g.ReviewRequests {
		if strings.EqualFold(r.TypeName, "Team") {
			continue
		}
		if login := normalizeLogin(r.Login); login != "" {
			pull.RequestedReviewers = append(pull.RequestedReviewers, login)
		}
	}
	// `gh` reports a third state, MERGED, that the REST API does not have.
	// Folding it onto State+Merged is what makes the two legs agree: a merged
	// pull request is closed everywhere in vincent, and carries Merged.
	if pull.State == "merged" || (g.MergedAt != nil && !g.MergedAt.IsZero()) {
		pull.Merged, pull.State = true, StateClosed
	}
	return pull
}

func (c *Client) ghListPulls(ctx context.Context, cred credential, repo Repo, opts ListOptions) ([]PullRequest, error) {
	out, err := c.runGH(ctx, cred.ghPath, ghListArgs("pr", repo, opts, ghPullFields)...)
	if err != nil {
		return nil, err
	}
	return parseGHPullList(out, repo, c.now())
}

// parseGHPullList is the decoding half of the leg, split from the exec so the
// table tests can drive it straight from captured output.
func parseGHPullList(out []byte, repo Repo, now time.Time) ([]PullRequest, error) {
	var raw []ghPull
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, newError(ReasonBadResponse, "decode gh pr list: %v", err)
	}
	pulls := make([]PullRequest, 0, len(raw))
	for _, g := range raw {
		pulls = append(pulls, g.normalize(repo, now))
	}
	return pulls, nil
}

func (c *Client) ghGetPull(ctx context.Context, cred credential, repo Repo, number int) (PullRequest, error) {
	out, err := c.runGH(ctx, cred.ghPath,
		"pr", "view", strconv.Itoa(number),
		"--repo", repo.String(),
		"--json", ghPullFields)
	if err != nil {
		return PullRequest{}, err
	}
	return parseGHPull(out, repo, c.now())
}

func parseGHPull(out []byte, repo Repo, now time.Time) (PullRequest, error) {
	var raw ghPull
	if err := json.Unmarshal(out, &raw); err != nil {
		return PullRequest{}, newError(ReasonBadResponse, "decode gh pr view: %v", err)
	}
	if raw.Number == 0 {
		return PullRequest{}, newError(ReasonBadResponse, "gh pr view returned no pull request number")
	}
	return raw.normalize(repo, now), nil
}

// ghChecks reads a pull request's checks. It is the same `gh pr view` call
// GetPull makes, with the same field list, so the rollup a client renders and
// the pull request it is rendered under cannot come from two different
// answers about different heads.
func (c *Client) ghChecks(ctx context.Context, cred credential, repo Repo, number int) (CheckRollup, error) {
	out, err := c.runGH(ctx, cred.ghPath,
		"pr", "view", strconv.Itoa(number),
		"--repo", repo.String(),
		"--json", ghPullFields)
	if err != nil {
		return CheckRollup{}, err
	}
	return parseGHChecks(out, c.now())
}

// parseGHChecks is the decoding half of the leg, split from the exec so the
// table tests can drive it straight from captured output.
func parseGHChecks(out []byte, now time.Time) (CheckRollup, error) {
	var raw ghPull
	if err := json.Unmarshal(out, &raw); err != nil {
		return CheckRollup{}, newError(ReasonBadResponse, "decode gh pr view checks: %v", err)
	}
	if raw.Number == 0 {
		return CheckRollup{}, newError(ReasonBadResponse, "gh pr view returned no pull request number")
	}
	runs := make([]CheckRun, 0, len(raw.StatusCheckRollup))
	for _, g := range raw.StatusCheckRollup {
		run := g.normalize()
		if run.Name == "" {
			continue
		}
		runs = append(runs, run)
	}
	return newRollup(raw.HeadOid, runs, now), nil
}

// ghCreatePull is the `gh` half of pull-request creation (task 069).
//
// `gh pr create` prints the new pull request's web URL on stdout and nothing
// else — there is no `--json` on it — so the number is parsed out of that
// URL and the pull request is then *read back* through the existing
// ghGetPull. Parsing `gh`'s human output into a PullRequest was rejected: it
// would be a second normalizer for a shape this package already has one of,
// and it would drift the moment `gh` changed a word.
//
// The body arrives on stdin via `--body-file -`. A body is prose a human just
// typed and can be any length; putting it in argv would put it under the
// platform's command-line limit, which is 32 KiB on Windows.
func (c *Client) ghCreatePull(ctx context.Context, cred credential, repo Repo, opts CreateOptions) (PullRequest, error) {
	args := ghCreateArgs(repo, opts)
	out, err := runGHWrite(ctx, cred.ghPath, strings.NewReader(opts.Body), classifyGHCreate, args...)
	if err != nil {
		return PullRequest{}, err
	}
	number, ok := pullNumberFromURL(string(out))
	if !ok {
		return PullRequest{}, newError(ReasonBadResponse,
			"gh pr create printed no pull request URL")
	}
	return c.ghGetPull(ctx, cred, repo, number)
}

// ghCreateArgs is the whole argv, in one place so a test can assert on it.
// `--draft` is present exactly when the human asked for it: `gh` has no
// `--ready` counterpart, and a pull request created without `--draft` is
// ready by definition.
func ghCreateArgs(repo Repo, opts CreateOptions) []string {
	args := []string{
		"pr", "create",
		"--repo", repo.String(),
		"--base", opts.Base,
		"--head", opts.Head,
		"--title", opts.Title,
		"--body-file", "-",
	}
	if opts.Draft {
		args = append(args, "--draft")
	}
	return args
}

// pullNumberFromURL reads the trailing number out of a
// https://github.com/owner/name/pull/N line. `gh` may print a warning line
// first, so the last non-empty line is the one read.
func pullNumberFromURL(out string) (int, bool) {
	var line string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if t := strings.TrimSpace(l); t != "" {
			line = t
		}
	}
	_, tail, ok := strings.Cut(line, "/pull/")
	if !ok {
		return 0, false
	}
	tail, _, _ = strings.Cut(tail, "/")
	tail, _, _ = strings.Cut(tail, "?")
	n, err := strconv.Atoi(strings.TrimSpace(tail))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// runGHWrite runs a mutating `gh` subcommand (task 069, task 068.4). stdin
// is nil unless the subcommand takes `--body-file -`.
//
// Failures map through ghWriteError rather than ghError: classify names the
// refusals only this write can have, and a 403 is ReasonNoWriteScope.
func runGHWrite(ctx context.Context, path string, stdin io.Reader, classify func(lowerStderr string) string, args ...string) ([]byte, error) {
	out, stderr, err := execGH(ctx, path, stdin, args...)
	if err != nil {
		return nil, ghWriteError(err, stderr, ctx.Err(), classify)
	}
	return out, nil
}

// ghWriteError is ghError for a write. `gh` reports every refusal as an
// ordinary exit 1 with the API's own sentence on stderr, so a write's own
// refusals are recognized first — they must never surface as
// "unreachable" — and a 403 becomes no_write_scope: a credential that could
// read the pull request a moment ago and cannot write to it is one condition,
// and it gets one spelling on every write (task 068.4 decision 4).
func ghWriteError(runErr error, stderr string, ctxErr error, classify func(string) string) *Error {
	var exit *exec.ExitError
	if ctxErr == nil && classify != nil && errors.As(runErr, &exit) {
		if reason := classify(strings.ToLower(stderr)); reason != "" {
			return &Error{Reason: reason, Detail: strings.TrimSpace(stderr)}
		}
	}
	e := ghError(runErr, stderr, ctxErr)
	if e.Reason == ReasonForbidden {
		e.Reason = ReasonNoWriteScope
	}
	return e
}

// classifyGHCreate names the one failure only a create can have: a pull
// request already exists for this head and base.
func classifyGHCreate(lower string) string {
	if strings.Contains(lower, "already exists") {
		return ReasonPullExists
	}
	return ""
}
