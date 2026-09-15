package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The pull-request writes (task 068.4, decision record rows 11 and 27 as
// rewritten): merge, close, reopen, comment and re-run the failed jobs of an
// Actions run, beside task 069's CreatePull.
//
// Every one of them is a human's act. Nothing on the step path reaches this
// file, and the routes in front of it are excluded from the MCP tool surface
// (§13.4): "the keypress is the consent" only holds while a human is the one
// pressing it. An agent step still has a full-auto shell in its own worktree
// and can run `gh pr merge` there, which is row 11's original path and stays
// open.
//
// Both legs answer into the same normalized PullRequest the read side
// produces, and neither leg's own error text escapes as anything but Detail.
// Where GitHub's refusal can be predicted — a merge — it is predicted from a
// preflight read and refused before anything is sent, so the refusal a client
// sees is one of this package's named reasons rather than a translation of a
// sentence GitHub wrote.

// Merge methods. There is no default and no config key (task 068 decision
// 4): the human names the method in the confirmation, and a repository that
// forbids it refuses in the reason vocabulary rather than being pre-guessed
// wrongly.
const (
	MergeMethodMerge  = "merge"
	MergeMethodSquash = "squash"
	MergeMethodRebase = "rebase"
)

// ValidMergeMethod reports that m is one of the three merge methods.
func ValidMergeMethod(m string) bool {
	switch m {
	case MergeMethodMerge, MergeMethodSquash, MergeMethodRebase:
		return true
	default:
		return false
	}
}

// MergeOptions is exactly what the human confirmed. Both fields are required.
type MergeOptions struct {
	// Method is MergeMethodMerge, MergeMethodSquash or MergeMethodRebase.
	Method string
	// HeadSHA is the head commit the human was shown when they confirmed. The
	// merge is refused when the live head differs, and the send itself is
	// pinned to it, so a push landing between the preflight and the merge is
	// refused by GitHub rather than merged.
	HeadSHA string
}

// MergePull merges a pull request and returns it, re-read after the merge.
//
// It reads before it writes: the pull request's merge state and, when the
// merge is blocked, the live check rollup (068.1) decide whether anything is
// sent at all. See mergeRefusal for the table.
func (c *Client) MergePull(ctx context.Context, repo Repo, number int, opts MergeOptions) (PullRequest, error) {
	if !ValidMergeMethod(opts.Method) {
		return PullRequest{}, newError(ReasonBadRequest, "merge method must be merge, squash or rebase, got %q", opts.Method)
	}
	if strings.TrimSpace(opts.HeadSHA) == "" {
		return PullRequest{}, newError(ReasonBadRequest, "a merge must name the head commit it was confirmed for")
	}
	if number < 1 {
		return PullRequest{}, newError(ReasonBadRequest, "pull request number must be positive, got %d", number)
	}
	cred, err := c.credential(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	pull, err := c.mergePull(ctx, cred, repo, number, opts)
	if err != nil {
		c.logf("github pull request merge failed", "repo", repo.String(), "pull", number,
			"method", opts.Method, "via", cred.via, "reason", ReasonOf(err), "detail", err)
		return PullRequest{}, err
	}
	return pull, nil
}

func (c *Client) mergePull(ctx context.Context, cred credential, repo Repo, number int, opts MergeOptions) (PullRequest, error) {
	pre, err := c.preflight(ctx, cred, repo, number)
	if err != nil {
		return PullRequest{}, err
	}
	running := false
	if strings.EqualFold(pre.mergeState, "blocked") {
		// Only a blocked merge needs the rollup: it is what tells "wait for
		// the checks" from "something else is in the way", and on the REST leg
		// it costs two more calls nothing else here needs.
		rollup, err := c.preflightChecks(ctx, cred, repo, pre)
		if err != nil {
			return PullRequest{}, err
		}
		running = anyRunning(rollup.Runs)
	}
	if reason := mergeRefusal(pre.pull, pre.mergeState, opts.HeadSHA, running); reason != "" {
		return PullRequest{}, newError(reason,
			"preflight refused #%d: state %s, merged %v, draft %v, merge state %q, head %s, confirmed %s",
			number, pre.pull.State, pre.pull.Merged, pre.pull.Draft, pre.mergeState, pre.pull.HeadSHA, opts.HeadSHA)
	}
	if cred.via == ViaGH {
		_, err = runGHWrite(ctx, cred.ghPath, nil, classifyGHMerge, ghMergeArgs(repo, number, opts)...)
	} else {
		_, err = c.restWrite(ctx, cred, http.MethodPut, restPullPath(repo, number)+"/merge", map[string]any{
			"merge_method": opts.Method,
			"sha":          opts.HeadSHA,
		}, classifyRESTMerge)
	}
	if err != nil {
		return PullRequest{}, err
	}
	merged := pre.pull
	merged.State, merged.Merged = StateClosed, true
	return c.readBack(ctx, cred, repo, number, merged), nil
}

// mergeRefusal is the preflight table (task 068.4 decision 3), and "" means
// send. mergeState is `gh`'s `mergeStateStatus` or REST's `mergeable_state`,
// which are the same enumeration in two cases.
//
//   - a closed or merged pull request → not_mergeable, which is also what
//     refuses a double merge
//   - a head that is not the confirmed commit → head_changed
//   - BEHIND → branch_behind
//   - BLOCKED with an unfinished check → checks_running; BLOCKED otherwise →
//     not_mergeable
//   - DIRTY, DRAFT, or a draft pull request → not_mergeable
//   - CLEAN, UNSTABLE (a non-required check failing), HAS_HOOKS → send
//   - UNKNOWN, or a value GitHub adds later → send: GitHub is still computing,
//     the send is pinned to the head, and GitHub's own refusal is the
//     fallback, mapped to not_mergeable
func mergeRefusal(pull PullRequest, mergeState, confirmedSHA string, checksRunning bool) string {
	if pull.Merged || pull.State == StateClosed {
		return ReasonNotMergeable
	}
	if !strings.EqualFold(pull.HeadSHA, strings.TrimSpace(confirmedSHA)) {
		return ReasonHeadChanged
	}
	switch strings.ToLower(strings.TrimSpace(mergeState)) {
	case "behind":
		return ReasonBranchBehind
	case "blocked":
		if checksRunning {
			return ReasonChecksRunning
		}
		return ReasonNotMergeable
	case "dirty", "draft":
		return ReasonNotMergeable
	}
	if pull.Draft {
		return ReasonNotMergeable
	}
	return ""
}

func anyRunning(runs []CheckRun) bool {
	for _, r := range runs {
		if r.Running() {
			return true
		}
	}
	return false
}

// ghMergeArgs is the whole `gh pr merge` argv, in one place so a test can
// assert on it. Never `--delete-branch` — branch deletion is §10's archive
// rule, not a merge side effect — never `--auto`, and never `--admin`, which
// would override exactly the protections the preflight just read.
func ghMergeArgs(repo Repo, number int, opts MergeOptions) []string {
	return []string{
		"pr", "merge", strconv.Itoa(number),
		"-R", repo.String(),
		"--" + opts.Method,
		"--match-head-commit", opts.HeadSHA,
	}
}

// classifyGHMerge reads the refusals `gh pr merge` reports after the
// preflight passed: a head that moved in between (the pin firing), and
// GitHub's own "not mergeable".
func classifyGHMerge(lower string) string {
	switch {
	case strings.Contains(lower, "head branch was modified"):
		return ReasonHeadChanged
	case strings.Contains(lower, "not mergeable"), strings.Contains(lower, "is not in a mergeable state"):
		return ReasonNotMergeable
	default:
		return ""
	}
}

// classifyRESTMerge reads `PUT /pulls/{n}/merge`'s two refusal statuses: 405
// is "not mergeable" and 409 is a `sha` that is no longer the head.
func classifyRESTMerge(status int, _ string) string {
	switch status {
	case http.StatusMethodNotAllowed:
		return ReasonNotMergeable
	case http.StatusConflict:
		return ReasonHeadChanged
	default:
		return ""
	}
}

// ClosePull closes a pull request without merging it and returns it.
func (c *Client) ClosePull(ctx context.Context, repo Repo, number int) (PullRequest, error) {
	return c.setPullState(ctx, repo, number, StateClosed)
}

// ReopenPull reopens a closed, unmerged pull request and returns it.
func (c *Client) ReopenPull(ctx context.Context, repo Repo, number int) (PullRequest, error) {
	return c.setPullState(ctx, repo, number, StateOpen)
}

func (c *Client) setPullState(ctx context.Context, repo Repo, number int, state string) (PullRequest, error) {
	verb := "close"
	if state == StateOpen {
		verb = "reopen"
	}
	if number < 1 {
		return PullRequest{}, newError(ReasonBadRequest, "pull request number must be positive, got %d", number)
	}
	cred, err := c.credential(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	// What the write made true, for the read-back to fall back on.
	fallback := PullRequest{
		Repo: repo.String(), Number: number, URL: PullURL(repo, number), State: state, FetchedAt: c.now(),
	}
	var pull PullRequest
	if cred.via == ViaGH {
		_, err = runGHWrite(ctx, cred.ghPath, nil, nil, "pr", verb, strconv.Itoa(number), "-R", repo.String())
		if err == nil {
			pull = c.readBack(ctx, cred, repo, number, fallback)
		}
	} else {
		var body []byte
		body, err = c.restWrite(ctx, cred, http.MethodPatch, restPullPath(repo, number),
			map[string]any{"state": state}, nil)
		if err == nil {
			// PATCH answers with the pull request itself, so there is nothing
			// to read back — and a body that does not parse must not turn a
			// write that happened into a failure a human retries.
			var parseErr error
			if pull, parseErr = parseRESTPull(body, repo, c.now()); parseErr != nil {
				c.logf("github pull request "+verb+" response unreadable", "repo", repo.String(),
					"pull", number, "detail", parseErr)
				pull = fallback
			}
		}
	}
	if err != nil {
		c.logf("github pull request "+verb+" failed", "repo", repo.String(), "pull", number,
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return PullRequest{}, err
	}
	return pull, nil
}

// CommentPull posts a comment on a pull request and returns the comment's
// web URL.
//
// There is no idempotency key (task 069 decision 7 carries over): a second
// call posts a second comment, and the client's submit-disable is the defence.
func (c *Client) CommentPull(ctx context.Context, repo Repo, number int, body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", newError(ReasonBadRequest, "a comment needs a body")
	}
	if number < 1 {
		return "", newError(ReasonBadRequest, "pull request number must be positive, got %d", number)
	}
	cred, err := c.credential(ctx)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	var link string
	if cred.via == ViaGH {
		// The body on stdin, for the reason `pr create` takes it there: it is
		// prose a human typed, and argv is under Windows' 32 KiB limit.
		var out []byte
		out, err = runGHWrite(ctx, cred.ghPath, strings.NewReader(body), nil,
			"pr", "comment", strconv.Itoa(number), "-R", repo.String(), "--body-file", "-")
		link = lastURLLine(string(out))
	} else {
		var resp []byte
		resp, err = c.restWrite(ctx, cred, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/comments",
			url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number), map[string]any{"body": body}, nil)
		if err == nil {
			var created struct {
				HTMLURL string `json:"html_url"`
			}
			if json.Unmarshal(resp, &created) == nil {
				link = created.HTMLURL
			}
		}
	}
	if err != nil {
		c.logf("github pull request comment failed", "repo", repo.String(), "pull", number,
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return "", err
	}
	if link == "" {
		// The comment exists; failing here would invite a human to post it
		// twice. The pull request's own page is where it is.
		link = PullURL(repo, number)
	}
	return link, nil
}

// lastURLLine is the last non-empty stdout line when it is a web URL, which
// is what `gh pr comment` prints.
func lastURLLine(out string) string {
	var line string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if t := strings.TrimSpace(l); t != "" {
			line = t
		}
	}
	if !strings.HasPrefix(line, "https://") {
		return ""
	}
	return line
}

// RerunFailedJobs re-runs the failed jobs of one GitHub Actions workflow run
// on a pull request's head commit.
//
// The run id is validated against the live rollup before anything is sent:
// it must belong to an Actions-backed row (task 068 decision 3) that failed,
// on the head the pull request has now. Without that, this would re-run any
// run id in the repository, and 068.1's host-and-path check on the check's
// URL would count for nothing.
func (c *Client) RerunFailedJobs(ctx context.Context, repo Repo, number int, runID int64) error {
	if runID < 1 {
		return newError(ReasonBadRequest, "run id must be positive, got %d", runID)
	}
	if number < 1 {
		return newError(ReasonBadRequest, "pull request number must be positive, got %d", number)
	}
	cred, err := c.credential(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	if err := c.rerunFailedJobs(ctx, cred, repo, number, runID); err != nil {
		c.logf("github check re-run failed", "repo", repo.String(), "pull", number, "run", runID,
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return err
	}
	return nil
}

func (c *Client) rerunFailedJobs(ctx context.Context, cred credential, repo Repo, number int, runID int64) error {
	pre, err := c.preflight(ctx, cred, repo, number)
	if err != nil {
		return err
	}
	rollup, err := c.preflightChecks(ctx, cred, repo, pre)
	if err != nil {
		return err
	}
	if !rerunnable(rollup, runID) {
		return newError(ReasonBadRequest, "run %d is not a failed Actions run on #%d's head %s", runID, number, rollup.Ref)
	}
	id := strconv.FormatInt(runID, 10)
	if cred.via == ViaGH {
		_, err = runGHWrite(ctx, cred.ghPath, nil, nil, "run", "rerun", id, "--failed", "-R", repo.String())
		return err
	}
	_, err = c.restWrite(ctx, cred, http.MethodPost, fmt.Sprintf("/repos/%s/%s/actions/runs/%s/rerun-failed-jobs",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), id), nil, nil)
	return err
}

// rerunnable reports that runID backs a failed, Actions-backed row of the
// rollup. A run is several jobs, so one failed job is enough.
func rerunnable(rollup CheckRollup, runID int64) bool {
	for _, r := range rollup.Runs {
		if r.Actions() && r.RunID == runID && r.Failed() {
			return true
		}
	}
	return false
}

// preflight is what a write reads before it sends: the pull request, its
// merge state, and — on the `gh` leg, where it arrives in the same answer —
// its check rollup.
//
// The merge state is deliberately **not** a field on PullRequest. The REST
// listing does not return `mergeable_state`, so it would be a field only one
// route could fill, which is the reasoning 068.1 applied to a check's
// workflow name.
type preflight struct {
	pull       PullRequest
	mergeState string
	rollup     *CheckRollup
}

// ghPreflightFields is ghPullFields plus the merge state, so one `gh pr view`
// answers the pull request, its merge state and its rollup about one head.
const ghPreflightFields = ghPullFields + ",mergeStateStatus"

func (c *Client) preflight(ctx context.Context, cred credential, repo Repo, number int) (preflight, error) {
	if cred.via == ViaGH {
		out, err := c.runGH(ctx, cred.ghPath,
			"pr", "view", strconv.Itoa(number),
			"--repo", repo.String(),
			"--json", ghPreflightFields)
		if err != nil {
			return preflight{}, err
		}
		return parseGHPreflight(out, repo, c)
	}
	body, err := c.restGET(ctx, cred, restPullPath(repo, number))
	if err != nil {
		return preflight{}, err
	}
	return parseRESTPreflight(body, repo, c)
}

func parseGHPreflight(out []byte, repo Repo, c *Client) (preflight, error) {
	var raw struct {
		ghPull
		MergeStateStatus string `json:"mergeStateStatus"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return preflight{}, newError(ReasonBadResponse, "decode gh pr view preflight: %v", err)
	}
	if raw.Number == 0 {
		return preflight{}, newError(ReasonBadResponse, "gh pr view returned no pull request number")
	}
	rollup, err := parseGHChecks(out, c.now())
	if err != nil {
		return preflight{}, err
	}
	return preflight{
		pull:       raw.normalize(repo, c.now()),
		mergeState: raw.MergeStateStatus,
		rollup:     &rollup,
	}, nil
}

func parseRESTPreflight(body []byte, repo Repo, c *Client) (preflight, error) {
	var raw struct {
		restPull
		MergeableState string `json:"mergeable_state"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return preflight{}, newError(ReasonBadResponse, "decode pull request preflight: %v", err)
	}
	if raw.Number == 0 {
		return preflight{}, newError(ReasonBadResponse, "pull request response carried no number")
	}
	return preflight{pull: raw.normalize(repo, c.now()), mergeState: raw.MergeableState}, nil
}

// preflightChecks is the rollup for the preflight's head: already in hand on
// the `gh` leg, and read for that exact commit on the REST leg.
func (c *Client) preflightChecks(ctx context.Context, cred credential, repo Repo, pre preflight) (CheckRollup, error) {
	if pre.rollup != nil {
		return *pre.rollup, nil
	}
	if pre.pull.HeadSHA == "" {
		return CheckRollup{}, newError(ReasonBadResponse, "pull request #%d carries no head commit", pre.pull.Number)
	}
	return c.restChecks(ctx, cred, repo, pre.pull.HeadSHA)
}

// readBack re-reads a pull request after a write that succeeded. A failed
// read must not fail the call — a caller told "failed" presses the key again,
// and the write already happened — so it falls back to what the write itself
// made true, and logs.
func (c *Client) readBack(ctx context.Context, cred credential, repo Repo, number int, fallback PullRequest) PullRequest {
	var (
		pull PullRequest
		err  error
	)
	if cred.via == ViaGH {
		pull, err = c.ghGetPull(ctx, cred, repo, number)
	} else {
		pull, err = c.restGetPull(ctx, cred, repo, number)
	}
	if err != nil {
		c.logf("github pull request read-back failed", "repo", repo.String(), "pull", number,
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return fallback
	}
	return pull
}

func restPullPath(repo Repo, number int) string {
	return fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
}
