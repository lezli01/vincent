package daemon

import (
	"context"
	"encoding/json"
	"errors"
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
	if err := r.sweepOpenSet(ctx, project, repo); err != nil {
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
	if err := r.applyIssues(ctx, project, st, page); err != nil {
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
	if err := r.applyIssues(ctx, project, st, page); err != nil {
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
func (r *PullReconciler) applyIssues(ctx context.Context, project store.Project, st *store.IssueSyncState, page github.IssuePage) error {
	seen := map[string]bool{}
	for i := range page.Issues {
		is := &page.Issues[i]
		if is.NodeID == "" || seen[is.NodeID] {
			continue
		}
		seen[is.NodeID] = true
		if err := r.applyIssue(ctx, project.ID, is); err != nil {
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
// number is adopted first (task 130 decision 21.3). A closed issue nothing
// here knows is skipped — closed history is not imported — and a tombstoned
// one is never resurrected: the upsert refuses it.
func (r *PullReconciler) applyIssue(ctx context.Context, projectID int64, is *github.Issue) error {
	if adopted, err := r.adoptPlaceholder(ctx, projectID, is); err != nil || adopted {
		return err
	}
	if is.State == github.StateClosed {
		known, err := r.store.RemoteIssueKnown(ctx, projectID, issueProvider, is.NodeID)
		if err != nil || !known {
			return err
		}
	}
	_, created, err := r.store.UpsertRemoteIssue(ctx, remoteIssue(projectID, is), issuestate.Sync)
	if err != nil {
		return err
	}
	if created {
		r.logger.Debug("imported issue", "project", projectID, "repo", is.Repo, "issue", is.Number)
	}
	return nil
}

// adoptPlaceholder re-keys the never-synced backfill placeholder with
// is's repository and number onto is's node id, adopting GitHub's content
// and state (task 130 decision 21.3). It reports false when there is none,
// or when the node id is already known — then the usual upsert applies.
func (r *PullReconciler) adoptPlaceholder(ctx context.Context, projectID int64, is *github.Issue) (bool, error) {
	adopted, err := r.store.AdoptPlaceholderRemote(ctx, remoteIssue(projectID, is), issuestate.Sync)
	if err == nil && adopted {
		r.logger.Debug("adopted backfilled issue", "project", projectID, "repo", is.Repo, "issue", is.Number)
	}
	return adopted, err
}

// sweepOpenSet finds the open issues here GitHub no longer lists as open,
// and asks after each one: closed is written, transferred is moved,
// deleted or unreadable is missing. Nothing is deleted.
func (r *PullReconciler) sweepOpenSet(ctx context.Context, project store.Project, repo github.Repo) error {
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
			// decision 21.3): its number means nothing in this one, so it
			// is never probed and stays as the backfill left it.
			continue
		}
		if err := r.probeIssue(ctx, project.ID, repo, rem); err != nil {
			return err
		}
	}
	return nil
}

// probeIssue is one single-issue read for a remote the open listing lacked.
func (r *PullReconciler) probeIssue(ctx context.Context, projectID int64, repo github.Repo, rem store.IssueRemote) error {
	is, err := r.client.GetIssue(ctx, repo, rem.Number)
	status, location := "", ""
	switch reason := github.ReasonOf(err); {
	case err == nil:
		// Usually closed; open is the listing lagging, and the refresh is
		// right either way. The answer's node id is the one asked about —
		// an answer about another issue is ReasonMoved. A backfill
		// placeholder has no node id to compare, so its answer adopts it
		// (task 130 decision 21.3): a backfilled issue closed on GitHub is
		// re-keyed and closed here.
		if store.IsPlaceholderRemote(rem) {
			if adopted, err := r.adoptPlaceholder(ctx, projectID, &is); err != nil || adopted {
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
