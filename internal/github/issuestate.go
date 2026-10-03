package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The issue state write (task 130.5): close with a reason, or reopen. It
// follows the pull-request writes in write.go exactly — a preflight read, the
// write, and a read-back that never turns a write that happened into a
// failure — and, like them, is a human's act: nothing on the step path
// reaches it (task 130 decisions 9 and 10, task 068 decision 1).
//
// Both legs send the same REST PATCH, `gh api -X PATCH … --input -` on the
// `gh` leg and restAPI on the token leg, so the two legs agree on what was
// sent as well as on what came back.

// Issue state reasons a write may carry: GitHub's `state_reason` values.
const (
	IssueReasonCompleted  = "completed"
	IssueReasonNotPlanned = "not_planned"
	IssueReasonDuplicate  = "duplicate"
	IssueReasonReopened   = "reopened"
)

// IssueStateChange is what SetIssueState read before it wrote, and the issue
// after. Before is the preflight read, returned so a caller can compare it
// against the state it believed the issue was in.
type IssueStateChange struct {
	Before Issue
	After  Issue
}

// SetIssueState closes or reopens an issue.
//
// state is StateClosed or StateOpen. Closing takes reason completed,
// not_planned or duplicate; reopening takes reopened, or "" for it.
// duplicateOf is the *number* of the issue this one duplicates, and is
// accepted only with duplicate; 0 closes as duplicate without naming one.
// GitHub's `duplicate_issue_id` takes the other issue's integer id, not its
// number (observed against lezli01/vincent-test, 2026-10-02: a number is a
// 422 "Issue not found for duplicate_issue_id"), so the other issue is read
// first to learn it. Any other combination is refused before anything is
// sent.
//
// Closing a closed issue is not refused: GitHub accepts it, and updates
// state_reason (observed, same date).
func (c *Client) SetIssueState(ctx context.Context, repo Repo, number int, state, reason string, duplicateOf int) (IssueStateChange, error) {
	reason, err := validIssueState(number, state, reason, duplicateOf)
	if err != nil {
		return IssueStateChange{}, err
	}
	cred, err := c.credential(ctx)
	if err != nil {
		return IssueStateChange{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	change, err := c.setIssueState(ctx, cred, repo, number, state, reason, duplicateOf)
	if err != nil {
		c.logf("github issue state write failed", "repo", repo.String(), "issue", number,
			"state", state, "reason", reason, "via", cred.via, "error_reason", ReasonOf(err), "detail", err)
		return IssueStateChange{}, err
	}
	return change, nil
}

func validIssueState(number int, state, reason string, duplicateOf int) (string, error) {
	if number < 1 {
		return "", newError(ReasonBadRequest, "issue number must be positive, got %d", number)
	}
	switch state {
	case StateClosed:
		switch reason {
		case IssueReasonCompleted, IssueReasonNotPlanned, IssueReasonDuplicate:
		default:
			return "", newError(ReasonBadRequest, "closing takes reason completed, not_planned or duplicate, got %q", reason)
		}
	case StateOpen:
		switch reason {
		case "", IssueReasonReopened:
			reason = IssueReasonReopened
		default:
			return "", newError(ReasonBadRequest, "reopening takes reason reopened, got %q", reason)
		}
	default:
		return "", newError(ReasonBadRequest, "issue state must be open or closed, got %q", state)
	}
	switch {
	case duplicateOf < 0:
		return "", newError(ReasonBadRequest, "duplicate-of must be positive, got %d", duplicateOf)
	case duplicateOf > 0 && reason != IssueReasonDuplicate:
		return "", newError(ReasonBadRequest, "duplicate-of is only for reason duplicate, got %q", reason)
	case duplicateOf == number:
		return "", newError(ReasonBadRequest, "#%d cannot duplicate itself", number)
	}
	return reason, nil
}

func (c *Client) setIssueState(ctx context.Context, cred credential, repo Repo, number int, state, reason string, duplicateOf int) (IssueStateChange, error) {
	before, err := c.apiGetIssue(ctx, cred, repo, number)
	if err != nil {
		return IssueStateChange{}, err
	}
	patch := map[string]any{"state": state, "state_reason": reason}
	if duplicateOf > 0 {
		dup, err := c.apiGetIssue(ctx, cred, repo, duplicateOf)
		if err != nil {
			// The other issue's absence is a value the human chose, not
			// this issue's trouble: one reason for it whatever GitHub said.
			return IssueStateChange{}, newError(ReasonBadRequest, "duplicate-of #%d: %v", duplicateOf, err)
		}
		if dup.ID == 0 {
			return IssueStateChange{}, newError(ReasonBadResponse, "#%d carried no id", duplicateOf)
		}
		patch["duplicate_issue_id"] = dup.ID
	}
	payload, err := json.Marshal(patch)
	if err != nil {
		return IssueStateChange{}, newError(ReasonBadRequest, "encode request: %v", err)
	}
	resp, err := c.apiCall(ctx, cred, http.MethodPatch, issuePath(repo, number), nil, payload)
	if err != nil {
		return IssueStateChange{}, err
	}
	// What the write made true, for a read-back that does not parse.
	fallback := before
	fallback.State, fallback.StateReason, fallback.FetchedAt = state, reason, c.now()
	after, err := parseAPIIssue(resp.body, repo, number, c.now())
	if err != nil {
		if ReasonOf(err) == ReasonMoved {
			// `gh api` turned the PATCH into a GET of the transfer's target
			// (observed with gh 2.100.0: exit 0, 200, the other issue, and
			// nothing written). The preflight normally catches a transfer
			// first; this is the race where it lands in between.
			return IssueStateChange{}, err
		}
		// PATCH answers with the issue itself, so this is the read-back — and
		// a body that does not parse must not turn a write that happened into
		// a failure a human retries.
		c.logf("github issue state response unreadable", "repo", repo.String(),
			"issue", number, "detail", err)
		after = fallback
	}
	return IssueStateChange{Before: before, After: after}, nil
}

// GetIssue reads one issue over REST on both legs and follows no redirect:
// a transferred issue is ReasonMoved with Error.Location, a deleted one
// ReasonGone, an absent one (or a pull request) ReasonNotFound — the same
// on the `gh` leg as on the token leg. It is the issue sync's per-issue
// probe (task 130.8), for an open issue the open listing no longer
// carries; Get is the picker's, through `gh issue view`, and reports none
// of the three.
func (c *Client) GetIssue(ctx context.Context, repo Repo, number int) (Issue, error) {
	if number < 1 {
		return Issue{}, newError(ReasonBadRequest, "issue number must be positive, got %d", number)
	}
	cred, err := c.credential(ctx)
	if err != nil {
		return Issue{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	issue, err := c.apiGetIssue(ctx, cred, repo, number)
	if err != nil {
		c.logf("github issue read failed", "repo", repo.String(), "issue", number,
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return Issue{}, err
	}
	return issue, nil
}

// apiGetIssue reads one issue through apiCall, so it reports gone, moved and
// not_found the same way on both legs.
func (c *Client) apiGetIssue(ctx context.Context, cred credential, repo Repo, number int) (Issue, error) {
	resp, err := c.apiCall(ctx, cred, http.MethodGet, issuePath(repo, number), nil, nil)
	if err != nil {
		return Issue{}, err
	}
	return parseAPIIssue(resp.body, repo, number, c.now())
}

// parseAPIIssue is parseRESTIssue plus the check a followed redirect needs:
// `gh api` follows a transfer's 301 by itself (observed with gh 2.100.0,
// captured: gh_2.100.0_api_issue_transferred.txt) and answers 200 with the
// issue in its new repository, so an answer about a different repository or
// number than the one asked for is `moved`, located at the answer's own API
// URL — the same URL the REST leg's 301 names.
func parseAPIIssue(body []byte, repo Repo, number int, now time.Time) (Issue, error) {
	var raw restIssue
	if err := json.Unmarshal(body, &raw); err != nil {
		return Issue{}, newError(ReasonBadResponse, "decode issue: %v", err)
	}
	if raw.Number != 0 && !sameIssue(raw, repo, number) {
		return Issue{}, &Error{
			Reason:   ReasonMoved,
			Detail:   fmt.Sprintf("asked for %s#%d, answered %s", repo, number, raw.URL),
			Location: raw.URL,
		}
	}
	return parseRESTIssue(body, repo, number, now)
}

// sameIssue reports that raw is repo's issue number. A row without a
// repository_url is taken at its number: the check exists to catch a
// redirect, and a fixture or a fake that omits the field is not one.
func sameIssue(raw restIssue, repo Repo, number int) bool {
	if raw.Number != number {
		return false
	}
	if raw.RepositoryURL == "" {
		return true
	}
	u, err := url.Parse(raw.RepositoryURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSuffix(u.Path, "/"), "/repos/"+repo.Owner+"/"+repo.Name)
}

func issuePath(repo Repo, number int) string {
	return fmt.Sprintf("repos/%s/%s/issues/%d", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
}
