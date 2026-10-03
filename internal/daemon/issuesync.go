package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The issue importer (task 130.8, spec §5.6): on the reconciler's tick it
// imports and refreshes each GitHub-based project's issues into the
// project's own issue set, and records how the last attempt went on the
// project's issue_sync_state row.
//
// There is no provider package: the importer is the daemon calling
// github.Client.ListIssuesSince and writing through
// store.UpsertRemoteIssue, and the row's provider column is the only seam a
// second provider would need. Its posture is the opposite of the pull
// request half's on one point — a failure is never quiet. It is logged and
// recorded with its reason, because a sync that silently stopped is an issue
// list that silently lies; issue.sync_changed announces only the
// transitions, so a failing project is one event, not one per tick.
//
// Nothing here deletes a local row on a remote signal: a closed issue is
// closed, a transferred one is marked moved, a deleted one missing.
//
// The comment pass (task 130 decision 24, 130.16) follows the issue pass on
// the same tick: one conditional listing of the repository's issue comments
// mirrors each onto the imported issue it belongs to. It is read-only —
// nothing is ever posted to GitHub — and a comment deleted there is never
// detected, so its row stays (decision 24.1). An issue's thread older than
// the comment watermark is filled in once, by a per-issue read, when the
// issue is first imported or a backfill placeholder is adopted
// (decision 24.2).

const (
	// issueProvider is the provider column's value for every GitHub row.
	issueProvider = "github"
	// importPageCap bounds one initial-import pass: 500 open issues a tick,
	// resumed from the watermark on the next.
	importPageCap = 5
	// sinceOverlap is how far behind the watermark an incremental poll asks
	// from. GitHub's `since` is on GitHub's clock and its listing is not
	// instantly consistent, so a little overlap catches a row indexed late;
	// re-reading an unchanged row writes nothing.
	sinceOverlap = 2 * time.Minute
	// fullScanEvery paces the open-set sweep for moved and missing issues,
	// which no `since` listing reports: a transferred or deleted issue is
	// simply not in it.
	fullScanEvery = 24 * time.Hour
	// fullScanCalls bounds the sweep's listing at this many capped walks
	// (10,000 open issues); a repository larger than that is swept for
	// nothing, rather than having its unscanned tail reported missing.
	fullScanCalls = 10
)

// Sync reasons the importer writes on top of github's reason vocabulary.
// The API lane reads these strings, so they are fixed.
const (
	syncReasonNotGitHub     = "not_github"
	syncReasonNoClient      = "no_client"
	syncReasonOriginChanged = "origin_changed"
)

// syncIssues runs one import attempt for project. isGitHub and repo are the
// project's origin as this tick derived it; haveClient is whether a client
// is wired. The two gates config owns (github.enabled, poll_interval) are
// the caller's and are already passed.
func (r *PullReconciler) syncIssues(ctx context.Context, project store.Project, repo github.Repo, isGitHub, haveClient bool) {
	now := r.clock()
	st, err := r.store.GetIssueSyncState(ctx, project.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		st = &store.IssueSyncState{ProjectID: project.ID, Provider: issueProvider, OK: true}
	case err != nil:
		r.logger.Warn("issue sync: state not read", "project", project.ID, "error", err)
		return
	}
	if st.RateLimitedUntil != nil && now.Before(*st.RateLimitedUntil) && isGitHub && haveClient {
		// Backing off: no call, no attempt, and the row keeps saying
		// rate_limited until GitHub's reset. A "sync now" waits too.
		return
	}
	next := *st
	next.Provider = issueProvider
	next.LastAttemptAt = &now
	next.RequestedAt = nil
	// The gates, in order, before any call: a project that fails one makes
	// none.
	// Without a git runner the origin cannot be read, which is a missing
	// client rather than a verdict about the origin.
	switch {
	case !isGitHub && r.git != nil:
		next.OK, next.Reason = false, syncReasonNotGitHub
	case !haveClient:
		next.OK, next.Reason = false, syncReasonNoClient
	case st.Repo != "" && !strings.EqualFold(st.Repo, repo.String()):
		// The binding is never re-keyed: issues imported from one repository
		// are not refreshed from another because origin now points there. A
		// rename GitHub redirects is not this — the node id matches through
		// the upsert — so this is a human re-pointing origin, and a human
		// decides what it means.
		next.OK, next.Reason = false, syncReasonOriginChanged
	default:
		err = r.importIssues(ctx, project, repo, &next, now)
		if err == nil {
			// Only after the issue pass: a comment is filed under an
			// imported issue, so the issues it may belong to come first. Its
			// failure is recorded like the issue pass's, and the issue
			// pass's progress, already in next, is kept.
			err = r.syncComments(ctx, project, repo, &next)
		}
		if err == nil {
			next.OK, next.Reason, next.LastOKAt, next.RateLimitedUntil = true, "", &now, nil
			break
		}
		next.OK, next.Reason = false, github.ReasonOf(err)
		var gerr *github.Error
		if next.Reason == github.ReasonRateLimited && errors.As(err, &gerr) && !gerr.ResetAt.IsZero() {
			reset := gerr.ResetAt.UTC()
			next.RateLimitedUntil = &reset
		}
	}
	switch {
	case err != nil:
		r.logger.Warn("issue sync failed", "project", project.ID, "repo", repo.String(),
			"reason", next.Reason, "error", err)
	case !next.OK:
		// A gate is a standing condition of the project, not a failed call:
		// the row says so, and the log says it once per transition.
		if st.OK || st.Reason != next.Reason {
			r.logger.Info("issue sync stopped", "project", project.ID, "reason", next.Reason)
		}
	}
	if err := r.store.PutIssueSyncState(ctx, next); err != nil {
		r.logger.Warn("issue sync: state not written", "project", project.ID, "error", err)
	}
}

// importIssues is one pass: the initial import until it completes, an
// incremental poll after, and the daily open-set sweep. It advances st as
// it goes, so progress made before a failure is kept.
func (r *PullReconciler) importIssues(ctx context.Context, project store.Project, repo github.Repo, st *store.IssueSyncState, now time.Time) error {
	if !st.ImportComplete {
		if err := r.initialImport(ctx, project, repo, st); err != nil {
			return err
		}
	} else if err := r.incremental(ctx, project, repo, st); err != nil {
		return err
	}
	if !st.ImportComplete {
		// The sweep needs the import's open set to compare against.
		return nil
	}
	if st.LastFullScanAt != nil && now.Sub(*st.LastFullScanAt) < fullScanEvery {
		return nil
	}
	if err := r.sweepOpenSet(ctx, project, repo, st); err != nil {
		return err
	}
	st.LastFullScanAt = &now
	return nil
}

// initialImport walks the open issues from the watermark, importPageCap
// pages at a time. Closed history is not imported: an issue closed before
// the project was synced is not work anybody here will pick up.
func (r *PullReconciler) initialImport(ctx context.Context, project store.Project, repo github.Repo, st *store.IssueSyncState) error {
	var since time.Time
	if st.Watermark != nil {
		since = *st.Watermark
	}
	page, err := r.client.ListIssuesSince(ctx, repo, github.ListSinceOptions{
		State: github.StateOpen, Since: since, PageCap: importPageCap,
	})
	if err != nil {
		return err
	}
	st.Repo = repo.String()
	if err := r.applyIssues(ctx, project, repo, st, page); err != nil {
		return err
	}
	if !page.Truncated {
		st.ImportComplete = true
		st.ETag = ""
		st.Since = sinceFrom(st.Watermark)
	}
	return nil
}

// incremental asks for everything updated since the stored bound, with the
// stored ETag. The bound moves only when the watermark does, so an idle
// repository is asked the same question every tick and answers 304: one
// request, nothing charged.
func (r *PullReconciler) incremental(ctx context.Context, project store.Project, repo github.Repo, st *store.IssueSyncState) error {
	if st.Since == nil {
		st.Since = sinceFrom(st.Watermark)
	}
	var since time.Time
	if st.Since != nil {
		since = *st.Since
	}
	page, err := r.client.ListIssuesSince(ctx, repo, github.ListSinceOptions{
		State: github.StateAll, Since: since, ETag: st.ETag,
	})
	if err != nil {
		return err
	}
	st.Repo = repo.String()
	if page.Unchanged {
		return nil
	}
	before := st.Watermark
	if err := r.applyIssues(ctx, project, repo, st, page); err != nil {
		return err
	}
	st.ETag = page.ETag
	if !sameTime(before, st.Watermark) {
		// A new question: the stored ETag answered the old one.
		st.Since = sinceFrom(st.Watermark)
		st.ETag = ""
	}
	return nil
}

// applyIssues upserts a listing's issues and advances the watermark. The
// inclusive `since` repeats the boundary rows: within a page a node id is
// written once, and across passes the repeat is a refresh of an unchanged
// row, which writes no event.
func (r *PullReconciler) applyIssues(ctx context.Context, project store.Project, repo github.Repo, st *store.IssueSyncState, page github.IssuePage) error {
	seen := map[string]bool{}
	for i := range page.Issues {
		is := &page.Issues[i]
		if is.NodeID == "" || seen[is.NodeID] {
			continue
		}
		seen[is.NodeID] = true
		if err := r.applyIssue(ctx, project.ID, repo, is, needsBackfill(st)); err != nil {
			return err
		}
		st.Watermark = laterOf(st.Watermark, is.UpdatedAt)
	}
	// ResumeSince counts pull request rows too, so a window holding only
	// pull requests still moves the cursor.
	st.Watermark = laterOf(st.Watermark, page.ResumeSince)
	return nil
}

// applyIssue writes one GitHub issue. A backfill placeholder with its
// number is adopted first (task 130 decision 22.3). A closed issue nothing
// here knows is skipped — closed history is not imported — and a tombstoned
// one is never resurrected: the upsert refuses it. An issue that arrives
// here for the first time brings its existing thread with it (task 130
// decision 24.2) when backfill says the comment pass would not.
func (r *PullReconciler) applyIssue(ctx context.Context, projectID int64, repo github.Repo, is *github.Issue, backfill bool) error {
	var thread []github.IssueComment
	var err error
	if backfill {
		thread, err = r.threadToBackfill(ctx, projectID, repo, is)
	}
	if err != nil {
		return err
	}
	if adopted, err := r.adoptPlaceholder(ctx, projectID, is, thread); err != nil || adopted {
		return err
	}
	if is.State == github.StateClosed {
		known, err := r.store.RemoteIssueKnown(ctx, projectID, issueProvider, is.NodeID)
		if err != nil || !known {
			return err
		}
	}
	iss, created, err := r.store.UpsertRemoteIssue(ctx, remoteIssue(projectID, is), issuestate.Sync)
	if err != nil {
		return err
	}
	if created {
		r.logger.Debug("imported issue", "project", projectID, "repo", is.Repo, "issue", is.Number)
		return r.writeComments(ctx, iss.ID, thread)
	}
	return nil
}

// adoptPlaceholder re-keys the never-synced backfill placeholder with
// is's repository and number onto is's node id, adopting GitHub's content
// and state (task 130 decision 22.3), and writes thread onto it — the
// placeholder never had one mirrored (decision 24.2). It reports false when
// there is none, or when the node id is already known — then the usual
// upsert applies.
func (r *PullReconciler) adoptPlaceholder(ctx context.Context, projectID int64, is *github.Issue, thread []github.IssueComment) (bool, error) {
	adopted, err := r.store.AdoptPlaceholderRemote(ctx, remoteIssue(projectID, is), issuestate.Sync)
	if err != nil || !adopted {
		return false, err
	}
	r.logger.Debug("adopted backfilled issue", "project", projectID, "repo", is.Repo, "issue", is.Number)
	if len(thread) == 0 {
		return true, nil
	}
	id, ok, err := r.store.IssueIDByRemoteNumber(ctx, projectID, issueProvider, is.Repo, is.Number)
	if err != nil || !ok {
		return true, err
	}
	return true, r.writeComments(ctx, id, thread)
}

// threadToBackfill reads is's whole thread when this write is about to
// bring is in for the first time — imported, or a backfill placeholder
// adopted — and GitHub counts comments on it (task 130 decision 24.2). The
// comment listing is bounded by its watermark, so without this an issue
// imported after the watermark passed its comments would show half a
// thread. A count of zero costs nothing, and an issue already known is the
// comment pass's to keep current.
//
// The read comes before the write on purpose: were it after, a read failing
// on a rate limit would leave the issue imported and the next tick no longer
// seeing it as new, so its thread would never be backfilled. Read first, a
// failure writes nothing and the next tick asks again.
func (r *PullReconciler) threadToBackfill(ctx context.Context, projectID int64, repo github.Repo, is *github.Issue) ([]github.IssueComment, error) {
	if is.Comments <= 0 {
		return nil, nil
	}
	known, err := r.store.RemoteIssueKnown(ctx, projectID, issueProvider, is.NodeID)
	if err != nil || known {
		return nil, err
	}
	if is.State == github.StateClosed {
		// A closed issue nothing here knows is skipped unless it adopts a
		// placeholder — which, its node id being unknown, is the live row
		// with its number.
		_, ok, err := r.store.IssueIDByRemoteNumber(ctx, projectID, issueProvider, is.Repo, is.Number)
		if err != nil || !ok {
			return nil, err
		}
	}
	return r.client.ListCommentsOfIssue(ctx, repo, is.Number)
}

// needsBackfill reports whether an issue arriving now needs its existing
// thread read on its own (task 130 decision 24.2): only once the comment
// pass has a bound. Until then its next listing walks the repository's
// comments from the beginning — this tick, after the issue pass — and
// reaches every comment of every issue imported before it, so a per-issue
// read would list the same comments twice: on a project's first import,
// one request per discussed issue for nothing.
func needsBackfill(st *store.IssueSyncState) bool {
	return st.CommentSince != nil
}

// sweepOpenSet finds the open issues here GitHub no longer lists as open,
// and asks after each one: closed is written, transferred is moved,
// deleted or unreadable is missing. Nothing is deleted.
func (r *PullReconciler) sweepOpenSet(ctx context.Context, project store.Project, repo github.Repo, st *store.IssueSyncState) error {
	open := map[string]bool{}
	var since time.Time
	for calls := 0; ; calls++ {
		if calls == fullScanCalls {
			r.logger.Warn("issue sync: open set too large to sweep", "project", project.ID, "repo", repo.String())
			return nil
		}
		page, err := r.client.ListIssuesSince(ctx, repo, github.ListSinceOptions{State: github.StateOpen, Since: since})
		if err != nil {
			return err
		}
		for i := range page.Issues {
			open[page.Issues[i].NodeID] = true
		}
		if !page.Truncated {
			break
		}
		since = page.ResumeSince
	}
	local, err := r.store.ListOpenRemoteIssues(ctx, project.ID, issueProvider)
	if err != nil {
		return err
	}
	for _, rem := range local {
		if open[rem.RemoteKey] || rem.Number < 1 {
			continue
		}
		if store.IsPlaceholderRemote(rem) && !strings.EqualFold(rem.Repo, repo.String()) {
			// A backfill placeholder from another repository (task 130
			// decision 22.3): its number means nothing in this one, so it
			// is never probed and stays as the backfill left it.
			continue
		}
		if err := r.probeIssue(ctx, project.ID, repo, rem, needsBackfill(st)); err != nil {
			return err
		}
	}
	return nil
}

// probeIssue is one single-issue read for a remote the open listing lacked.
func (r *PullReconciler) probeIssue(ctx context.Context, projectID int64, repo github.Repo, rem store.IssueRemote, backfill bool) error {
	is, err := r.client.GetIssue(ctx, repo, rem.Number)
	status, location := "", ""
	switch reason := github.ReasonOf(err); {
	case err == nil:
		// Usually closed; open is the listing lagging, and the refresh is
		// right either way. The answer's node id is the one asked about —
		// an answer about another issue is ReasonMoved. A backfill
		// placeholder has no node id to compare, so its answer adopts it
		// (task 130 decision 22.3): a backfilled issue closed on GitHub is
		// re-keyed and closed here.
		if store.IsPlaceholderRemote(rem) {
			var thread []github.IssueComment
			if backfill {
				if thread, err = r.threadToBackfill(ctx, projectID, repo, &is); err != nil {
					return err
				}
			}
			if adopted, err := r.adoptPlaceholder(ctx, projectID, &is, thread); err != nil || adopted {
				return err
			}
		}
		_, _, err := r.store.UpsertRemoteIssue(ctx, remoteIssue(projectID, &is), issuestate.Sync)
		return err
	case reason == github.ReasonMoved:
		status = store.RemoteStatusMoved
		var gerr *github.Error
		if errors.As(err, &gerr) {
			location = gerr.Location
		}
		if strings.Contains(location, "/discussions/") {
			// Converted to a discussion: it is no longer an issue anywhere.
			status, location = store.RemoteStatusMissing, ""
		}
	case reason == github.ReasonGone || reason == github.ReasonNotFound:
		status = store.RemoteStatusMissing
	default:
		return err
	}
	if err := r.store.SetIssueRemoteStatus(ctx, projectID, issueProvider, rem.RemoteKey, status, location); err != nil {
		return err
	}
	r.logger.Info("issue no longer on GitHub where it was", "project", projectID,
		"repo", repo.String(), "issue", rem.Number, "status", status, "location", location)
	return nil
}

// syncComments is the comment pass (task 130 decision 24, 130.16): one
// conditional listing of the repository's issue comments from the stored
// bound, with incremental's rules — the bound moves only with the
// watermark, so an idle repository answers 304. The first pass has no
// bound and walks from the beginning; a walk cut short at the page cap
// resumes on the next tick from exactly where it stopped. It advances st as
// it goes, so progress made before a failure is kept.
func (r *PullReconciler) syncComments(ctx context.Context, project store.Project, repo github.Repo, st *store.IssueSyncState) error {
	var since time.Time
	if st.CommentSince != nil {
		since = *st.CommentSince
	}
	page, err := r.client.ListIssueComments(ctx, repo, since, st.CommentETag)
	if err != nil {
		return err
	}
	if page.Unchanged {
		return nil
	}
	before := st.CommentWatermark
	seen := map[int64]bool{}
	for i := range page.Comments {
		c := &page.Comments[i]
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		if err := r.applyComment(ctx, project.ID, repo, c); err != nil {
			return err
		}
		st.CommentWatermark = laterOf(st.CommentWatermark, c.UpdatedAt)
	}
	st.CommentWatermark = laterOf(st.CommentWatermark, page.ResumeSince)
	st.CommentETag = page.ETag
	switch {
	case page.Truncated && st.CommentWatermark != nil:
		// GitHub's since is inclusive, so resuming from the last row read
		// repeats only the rows at that instant; without sinceOverlap the
		// resume always moves forward, however many comments share the
		// overlap window.
		resume := *st.CommentWatermark
		st.CommentSince = &resume
		st.CommentETag = ""
	case !sameTime(before, st.CommentWatermark):
		// A new question: the stored ETag answered the old one.
		st.CommentSince = sinceFrom(st.CommentWatermark)
		st.CommentETag = ""
	}
	return nil
}

// applyComment mirrors one listed comment onto the imported issue with its
// number in repo. GitHub's listing carries a pull request's conversation
// comments under the same numbers, and comments on issues never imported
// (closed history, an import still under way); neither has an issue here,
// and both are ignored (task 130 decision 24.4) — the backfill brings the
// earlier thread of an issue imported later.
func (r *PullReconciler) applyComment(ctx context.Context, projectID int64, repo github.Repo, c *github.IssueComment) error {
	id, ok, err := r.store.IssueIDByRemoteNumber(ctx, projectID, issueProvider, repo.String(), c.IssueNumber)
	if err != nil || !ok {
		return err
	}
	_, err = r.store.UpsertRemoteIssueComment(ctx, id, remoteComment(c))
	return err
}

// writeComments mirrors a backfilled thread onto issueID.
func (r *PullReconciler) writeComments(ctx context.Context, issueID int64, thread []github.IssueComment) error {
	for i := range thread {
		if _, err := r.store.UpsertRemoteIssueComment(ctx, issueID, remoteComment(&thread[i])); err != nil {
			return err
		}
	}
	return nil
}

// remoteComment maps a GitHub comment onto the store's shape. GitHub's
// integer comment id is the remote key: it is what the repository listing
// and the per-issue read agree on, so a comment both report is one row.
func remoteComment(c *github.IssueComment) store.RemoteIssueComment {
	return store.RemoteIssueComment{
		RemoteKey: strconv.FormatInt(c.ID, 10), Author: c.Author, Body: c.Body,
		CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
	}
}

// remoteJSON is what an imported issue keeps of GitHub's that the issue
// row has no column for.
type remoteJSON struct {
	Assignees       []string   `json:"assignees,omitempty"`
	Milestone       string     `json:"milestone,omitempty"`
	MilestoneNumber int        `json:"milestone_number,omitempty"`
	Author          string     `json:"author,omitempty"`
	ClosedAt        *time.Time `json:"closed_at,omitempty"`
	StateReason     string     `json:"state_reason,omitempty"`
}

// remoteIssue maps a GitHub issue onto the store's provider-neutral shape.
// Kind is left empty: kind is vincent's, and GitHub has none to give.
func remoteIssue(projectID int64, is *github.Issue) store.RemoteIssue {
	extra := remoteJSON{
		Assignees: issueAssignees(is), Milestone: is.Milestone, MilestoneNumber: is.MilestoneNumber,
		Author: is.Author, StateReason: is.StateReason,
	}
	if !is.ClosedAt.IsZero() {
		closed := is.ClosedAt.UTC()
		extra.ClosedAt = &closed
	}
	raw, _ := json.Marshal(extra)
	ri := store.RemoteIssue{
		ProjectID: projectID, Provider: issueProvider, RemoteKey: is.NodeID,
		Repo: is.Repo, Number: is.Number, URL: is.URL, RemoteJSON: string(raw),
		Title: is.Title, Body: is.Body, Author: is.Author,
		State: issuestate.Open, Labels: is.Labels,
	}
	if !is.UpdatedAt.IsZero() {
		updated := is.UpdatedAt.UTC()
		ri.RemoteUpdatedAt = &updated
	}
	if is.State == github.StateClosed {
		ri.State = issuestate.Closed
		// GitHub's state_reason is issuestate's vocabulary already; anything
		// else (none, or "reopened" on a closed row) closes as completed.
		reason, err := issuestate.ResolveReason(issuestate.RemoteClosed, issuestate.Reason(is.StateReason))
		if err != nil {
			reason = issuestate.Completed
		}
		ri.CloseReason = reason
	}
	return ri
}

// sinceFrom is the incremental bound for a watermark: nil (no bound) before
// anything was seen.
func sinceFrom(watermark *time.Time) *time.Time {
	if watermark == nil {
		return nil
	}
	since := watermark.Add(-sinceOverlap)
	return &since
}

func laterOf(cur *time.Time, t time.Time) *time.Time {
	if t.IsZero() || (cur != nil && !t.After(*cur)) {
		return cur
	}
	t = t.UTC()
	return &t
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
