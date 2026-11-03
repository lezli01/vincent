package daemon

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The issue importer (task 130.8, spec §5.6): on the reconciler's tick it
// imports and refreshes each bound project's issues into the project's own
// issue set, and records how the last attempt went on the project's
// issue_sync_state row.
//
// It talks to an issuesource.Source, never to a provider's client (task 142
// decision 2, which supersedes task 130 decision 15.1): the source lists,
// reads and maps, and this file keeps the policy — watermarks, the initial
// import, incremental passes, the sweep's cadence, tombstones and failure
// recording. GitHub is the only source today. Its posture is the opposite of
// the pull request half's on one point — a failure is never quiet. It is
// logged and recorded with its reason, because a sync that silently stopped
// is an issue list that silently lies; issue.sync_changed announces only the
// transitions, so a failing project is one event, not one per tick.
//
// One remote signal deletes a local row: an issue the provider answers gone
// for (GitHub's 410) was deleted there, and is deleted here, leaving a
// tombstone (task 130 decision 6, amended). Every other one keeps it: a
// closed issue is closed, a transferred one is marked moved, an unreadable
// one missing.
//
// The comment pass (task 130 decision 24, 130.16) follows the issue pass on
// the same tick. On a source with a repository-wide listing
// (issuesource.RepoCommentLister — GitHub) one conditional listing mirrors
// each comment onto the imported issue it belongs to; an issue's thread
// older than the comment watermark is filled in once, by a per-issue read,
// when the issue is first imported or a backfill placeholder is adopted
// (decision 24.2). On a source without one, the pass reads the thread of
// each imported issue the issue pass listed. Either way it is read-only, and
// a comment deleted on the provider is never detected, so its row stays
// (decision 24.1).

const (
	// issueProvider is the GitHub source's provider column value, which
	// the GitHub-only paths (triggers, tests) still name.
	issueProvider = issuesource.ProviderGitHub
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

// Sync reasons the importer writes on top of the seam's reason vocabulary.
// The API lane reads these strings, so they are fixed.
const (
	syncReasonNotGitHub     = "not_github"
	syncReasonNoClient      = "no_client"
	syncReasonOriginChanged = "origin_changed"
	// syncReasonSourceNotGitHub is a project whose explicit issue-source
	// binding is none or jira (task 142.6 decision 3): the GitHub importer
	// makes no call for it, whatever its origin says. A jira binding
	// carries it until Jira sync is scheduled (#903).
	syncReasonSourceNotGitHub = "source_not_github"
)

// syncIssues runs one import attempt for project through the reconciler's
// issue source. b and bound are the project's binding, derived by the
// caller — once per project per tick, so the importer and the pull request
// half act on one reading of origin (review F1). haveClient is whether a
// client is wired. The two gates config owns (github.enabled,
// poll_interval) are the caller's and are already passed.
func (r *PullReconciler) syncIssues(ctx context.Context, project store.Project, b issuesource.Binding, bound, haveClient bool) {
	src := r.issueSrc
	now := r.clock()
	st, err := r.store.GetIssueSyncState(ctx, project.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		st = &store.IssueSyncState{ProjectID: project.ID, Provider: src.Provider(), OK: true}
	case err != nil:
		r.logger.Warn("issue sync: state not read", "project", project.ID, "error", err)
		return
	}
	boundElsewhere := project.IssueSource.Provider == store.IssueProviderNone ||
		project.IssueSource.Provider == store.IssueProviderJira
	if st.RateLimitedUntil != nil && now.Before(*st.RateLimitedUntil) && bound && haveClient && !boundElsewhere {
		// Backing off: no call, no attempt, and the row keeps saying
		// rate_limited until the provider's reset. A "sync now" waits too.
		return
	}
	next := *st
	next.Provider = src.Provider()
	next.LastAttemptAt = &now
	next.RequestedAt = nil
	// The gates, in order, before any call: a project that fails one makes
	// none.
	// Without a git runner the origin cannot be read, which is a missing
	// client rather than a verdict about the origin.
	// The binding is first: a project bound to none or jira is not the
	// GitHub importer's, whatever origin says.
	switch {
	case boundElsewhere:
		next.OK, next.Reason = false, syncReasonSourceNotGitHub
	case !bound && r.git != nil:
		next.OK, next.Reason = false, syncReasonNotGitHub
	case !haveClient:
		next.OK, next.Reason = false, syncReasonNoClient
	case st.Repo != "" && !strings.EqualFold(st.Repo, b.String()):
		// The binding is never re-keyed: issues imported from one repository
		// are not refreshed from another because origin now points there. A
		// rename GitHub redirects is not this — the node id matches through
		// the upsert — so this is a human re-pointing origin, and a human
		// decides what it means.
		next.OK, next.Reason = false, syncReasonOriginChanged
	default:
		p := &issuePass{r: r, src: src, project: project, b: b, st: &next}
		err = p.importIssues(ctx, now, st.RequestedAt != nil)
		if err == nil {
			// Only after the issue pass: a comment is filed under an
			// imported issue, so the issues it may belong to come first. Its
			// failure is recorded like the issue pass's, and the issue
			// pass's progress, already in next, is kept.
			err = p.syncComments(ctx)
		}
		if err == nil {
			next.OK, next.Reason, next.LastOKAt, next.RateLimitedUntil = true, "", &now, nil
			break
		}
		f := src.Classify(err)
		next.OK, next.Reason = false, f.Reason
		if f.Reason == issuesource.ReasonRateLimited && !f.ResetAt.IsZero() {
			reset := f.ResetAt.UTC()
			next.RateLimitedUntil = &reset
		}
	}
	switch {
	case err != nil:
		r.logger.Warn("issue sync failed", "project", project.ID, "repo", b.String(),
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

// issuePass is one import attempt for one bound project: the source, the
// binding, and the sync row it advances as it goes, so progress made before
// a failure is kept.
type issuePass struct {
	r       *PullReconciler
	src     issuesource.Source
	project store.Project
	b       issuesource.Binding
	st      *store.IssueSyncState
	// listed is every issue the issue pass listed, for the per-issue comment
	// pass of a source with no repository-wide listing.
	listed []issuesource.Issue
}

func (p *issuePass) provider() string { return p.src.Provider() }

// adopts is whether the source's rows may be backfill placeholders.
func (p *issuePass) adopts() bool {
	_, ok := p.src.(issuesource.PlaceholderAdopter)
	return ok
}

// importIssues is one pass: the initial import until it completes, an
// incremental poll after, and the daily open-set sweep — at once when a
// human asked for the sync, who is likelier than the clock to know an
// issue just went.
func (p *issuePass) importIssues(ctx context.Context, now time.Time, requested bool) error {
	st := p.st
	if !st.ImportComplete {
		if err := p.initialImport(ctx); err != nil {
			return err
		}
	} else if err := p.incremental(ctx); err != nil {
		return err
	}
	if !st.ImportComplete {
		// The sweep needs the import's open set to compare against.
		return nil
	}
	if !requested && st.LastFullScanAt != nil && now.Sub(*st.LastFullScanAt) < fullScanEvery {
		return nil
	}
	if err := p.sweepOpenSet(ctx); err != nil {
		return err
	}
	st.LastFullScanAt = &now
	return nil
}

// initialImport walks the open issues from the watermark, importPageCap
// pages at a time. Closed history is not imported: an issue closed before
// the project was synced is not work anybody here will pick up.
func (p *issuePass) initialImport(ctx context.Context) error {
	st := p.st
	var since time.Time
	if st.Watermark != nil {
		since = *st.Watermark
	}
	page, err := p.src.ListIssues(ctx, p.b, issuesource.ListOptions{
		OpenOnly: true, Since: since, PageCap: importPageCap,
	})
	if err != nil {
		return err
	}
	st.Repo = p.b.String()
	if err := p.applyIssues(ctx, page); err != nil {
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
func (p *issuePass) incremental(ctx context.Context) error {
	st := p.st
	if st.Since == nil {
		st.Since = sinceFrom(st.Watermark)
	}
	var since time.Time
	if st.Since != nil {
		since = *st.Since
	}
	page, err := p.src.ListIssues(ctx, p.b, issuesource.ListOptions{Since: since, ETag: st.ETag})
	if err != nil {
		return err
	}
	st.Repo = p.b.String()
	if page.Unchanged {
		return nil
	}
	before := st.Watermark
	if err := p.applyIssues(ctx, page); err != nil {
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
// inclusive `since` repeats the boundary rows: within a page a remote key
// is written once, and across passes the repeat is a refresh of an
// unchanged row, which writes no event.
func (p *issuePass) applyIssues(ctx context.Context, page issuesource.Page) error {
	st := p.st
	seen := map[string]bool{}
	for i := range page.Issues {
		is := &page.Issues[i]
		key := is.Remote.RemoteKey
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if err := p.applyIssue(ctx, is, needsBackfill(st)); err != nil {
			return err
		}
		p.listed = append(p.listed, *is)
		st.Watermark = laterOf(st.Watermark, is.UpdatedAt())
	}
	// ResumeSince counts pull request rows too, so a window holding only
	// pull requests still moves the cursor.
	st.Watermark = laterOf(st.Watermark, page.ResumeSince)
	return nil
}

// applyIssue writes one provider issue. A backfill placeholder with its
// number is adopted first (task 130 decision 22.3). A closed issue nothing
// here knows is skipped — closed history is not imported — and a tombstoned
// one is never resurrected: the upsert refuses it. An issue that arrives
// here for the first time brings its existing thread with it (task 130
// decision 24.2) when backfill says the comment pass would not.
func (p *issuePass) applyIssue(ctx context.Context, is *issuesource.Issue, backfill bool) error {
	r := p.r
	var thread []issuesource.Comment
	var err error
	if backfill {
		thread, err = p.threadToBackfill(ctx, is)
	}
	if err != nil {
		return err
	}
	if adopted, err := p.adoptPlaceholder(ctx, is, thread); err != nil || adopted {
		return err
	}
	if is.Closed() {
		known, err := r.store.RemoteIssueKnown(ctx, p.project.ID, p.provider(), is.Remote.RemoteKey)
		if err != nil || !known {
			return err
		}
	}
	iss, created, err := r.store.UpsertRemoteIssue(ctx, p.remoteIssue(is), issuestate.Sync)
	if err != nil {
		return err
	}
	if created {
		r.logger.Debug("imported issue", "project", p.project.ID, "provider", p.provider(),
			"repo", is.Remote.Repo, "issue", is.Remote.Number, "key", is.Remote.RemoteKey)
		return p.writeComments(ctx, iss.ID, thread)
	}
	return nil
}

// remoteIssue is is as the store imports it into this pass's project.
func (p *issuePass) remoteIssue(is *issuesource.Issue) store.RemoteIssue {
	ri := is.Remote
	ri.ProjectID = p.project.ID
	return ri
}

// adoptPlaceholder re-keys the never-synced backfill placeholder with
// is's repository and number onto is's remote key, adopting the provider's
// content and state (task 130 decision 22.3), and writes thread onto it —
// the placeholder never had one mirrored (decision 24.2). It reports false
// when there is none, when the remote key is already known, or when the
// source has no placeholders — then the usual upsert applies.
func (p *issuePass) adoptPlaceholder(ctx context.Context, is *issuesource.Issue, thread []issuesource.Comment) (bool, error) {
	if !p.adopts() {
		return false, nil
	}
	r := p.r
	adopted, err := r.store.AdoptPlaceholderRemote(ctx, p.remoteIssue(is), issuestate.Sync)
	if err != nil || !adopted {
		return false, err
	}
	r.logger.Debug("adopted backfilled issue", "project", p.project.ID, "repo", is.Remote.Repo, "issue", is.Remote.Number)
	if len(thread) == 0 {
		return true, nil
	}
	id, ok, err := r.store.IssueIDByRemoteNumber(ctx, p.project.ID, p.provider(), is.Remote.Repo, is.Remote.Number)
	if err != nil || !ok {
		return true, err
	}
	return true, p.writeComments(ctx, id, thread)
}

// threadToBackfill reads is's whole thread when this write is about to
// bring is in for the first time — imported, or a backfill placeholder
// adopted — and the provider counts comments on it (task 130 decision
// 24.2). The comment listing is bounded by its watermark, so without this
// an issue imported after the watermark passed its comments would show half
// a thread. A count of zero costs nothing, and an issue already known is the
// comment pass's to keep current.
//
// The read comes before the write on purpose: were it after, a read failing
// on a rate limit would leave the issue imported and the next tick no longer
// seeing it as new, so its thread would never be backfilled. Read first, a
// failure writes nothing and the next tick asks again.
func (p *issuePass) threadToBackfill(ctx context.Context, is *issuesource.Issue) ([]issuesource.Comment, error) {
	if is.Comments <= 0 {
		return nil, nil
	}
	r := p.r
	known, err := r.store.RemoteIssueKnown(ctx, p.project.ID, p.provider(), is.Remote.RemoteKey)
	if err != nil || known {
		return nil, err
	}
	if is.Closed() {
		// A closed issue nothing here knows is skipped unless it adopts a
		// placeholder — which, its remote key being unknown, is the live
		// row with its number.
		if !p.adopts() {
			return nil, nil
		}
		_, ok, err := r.store.IssueIDByRemoteNumber(ctx, p.project.ID, p.provider(), is.Remote.Repo, is.Remote.Number)
		if err != nil || !ok {
			return nil, err
		}
	}
	return p.src.ListComments(ctx, p.b, is)
}

// needsBackfill reports whether an issue arriving now needs its existing
// thread read on its own (task 130 decision 24.2): only once the comment
// pass has a bound. Until then its next listing walks the repository's
// comments from the beginning — this tick, after the issue pass — and
// reaches every comment of every issue imported before it, so a per-issue
// read would list the same comments twice: on a project's first import,
// one request per discussed issue for nothing. A source with no
// repository-wide listing never sets the bound, and its comment pass reads
// every listed thread anyway.
func needsBackfill(st *store.IssueSyncState) bool {
	return st.CommentSince != nil
}

// sweepOpenSet finds the open issues here the provider no longer lists as
// open, and asks after each one: closed is written, transferred is moved,
// deleted is deleted, unreadable is missing.
func (p *issuePass) sweepOpenSet(ctx context.Context) error {
	r := p.r
	open := map[string]bool{}
	var since time.Time
	for calls := 0; ; calls++ {
		if calls == fullScanCalls {
			r.logger.Warn("issue sync: open set too large to sweep", "project", p.project.ID, "repo", p.b.String())
			return nil
		}
		page, err := p.src.ListIssues(ctx, p.b, issuesource.ListOptions{OpenOnly: true, Since: since})
		if err != nil {
			return err
		}
		for i := range page.Issues {
			open[page.Issues[i].Remote.RemoteKey] = true
		}
		if !page.Truncated {
			break
		}
		since = page.ResumeSince
	}
	local, err := r.store.ListOpenRemoteIssues(ctx, p.project.ID, p.provider())
	if err != nil {
		return err
	}
	for _, rem := range local {
		if open[rem.RemoteKey] {
			continue
		}
		if _, ok := p.src.Locate(rem); !ok {
			// Not addressable on the provider (for GitHub: no number), so
			// there is nothing to ask.
			continue
		}
		if store.IsPlaceholderRemote(rem) && !strings.EqualFold(rem.Repo, p.b.String()) {
			// A backfill placeholder from another repository (task 130
			// decision 22.3): its number means nothing in this one, so it
			// is never probed and stays as the backfill left it.
			continue
		}
		if err := p.probeIssue(ctx, rem, needsBackfill(p.st)); err != nil {
			return err
		}
	}
	return nil
}

// probeIssue is one single-issue read for a remote the open listing lacked.
func (p *issuePass) probeIssue(ctx context.Context, rem store.IssueRemote, backfill bool) error {
	r := p.r
	projectID := p.project.ID
	is, err := p.src.GetIssue(ctx, p.b, rem)
	status, location := "", ""
	switch f := p.src.Classify(err); {
	case err == nil:
		// Usually closed; open is the listing lagging, and the refresh is
		// right either way. The answer's remote key is the one asked about —
		// an answer about another issue is ReasonMoved. A backfill
		// placeholder has no remote key to compare, so its answer adopts it
		// (task 130 decision 22.3): a backfilled issue closed on GitHub is
		// re-keyed and closed here.
		if store.IsPlaceholderRemote(rem) {
			var thread []issuesource.Comment
			if backfill {
				if thread, err = p.threadToBackfill(ctx, &is); err != nil {
					return err
				}
			}
			if adopted, err := p.adoptPlaceholder(ctx, &is, thread); err != nil || adopted {
				return err
			}
		}
		_, _, err := r.store.UpsertRemoteIssue(ctx, p.remoteIssue(&is), issuestate.Sync)
		return err
	case f.Reason == issuesource.ReasonMoved:
		status, location = store.RemoteStatusMoved, f.Location
		if strings.Contains(location, "/discussions/") {
			// Converted to a discussion: it is no longer an issue anywhere.
			status, location = store.RemoteStatusMissing, ""
		}
	case f.Reason == issuesource.ReasonGone:
		// Deleted on the provider: the issue goes here too, leaving the
		// remote row as a tombstone so no later import brings it back. A
		// 404 is not this — it is also what a lost grant answers — and an
		// issue a live task works in keeps its row, marked missing, until
		// that task settles and a human deletes it.
		if rem.IssueID != nil {
			err := r.store.DeleteIssue(ctx, *rem.IssueID, issuestate.Sync)
			if err == nil {
				r.logger.Info("issue deleted on its provider, deleted here", "project", projectID,
					"provider", p.provider(), "repo", p.b.String(), "issue", rem.Number)
				return nil
			}
			if _, live := store.AsIssueHasLiveMainTask(err); !live {
				return err
			}
		}
		status = store.RemoteStatusMissing
	case f.Reason == issuesource.ReasonNotFound:
		status = store.RemoteStatusMissing
	default:
		return err
	}
	if err := r.store.SetIssueRemoteStatus(ctx, projectID, p.provider(), rem.RemoteKey, status, location); err != nil {
		return err
	}
	r.logger.Info("issue no longer on its provider where it was", "project", projectID,
		"provider", p.provider(), "repo", p.b.String(), "issue", rem.Number, "status", status, "location", location)
	return nil
}

// syncComments is the comment pass (task 130 decision 24, 130.16), through
// the source's repository-wide listing when it has one and each listed
// issue's thread when it does not.
func (p *issuePass) syncComments(ctx context.Context) error {
	if lister, ok := p.src.(issuesource.RepoCommentLister); ok {
		return p.syncRepoComments(ctx, lister)
	}
	return p.syncListedThreads(ctx)
}

// syncRepoComments is one conditional listing of the repository's issue
// comments from the stored bound, with incremental's rules — the bound
// moves only with the watermark, so an idle repository answers 304. The
// first pass has no bound and walks from the beginning; a walk cut short at
// the page cap resumes on the next tick from exactly where it stopped.
func (p *issuePass) syncRepoComments(ctx context.Context, lister issuesource.RepoCommentLister) error {
	st := p.st
	var since time.Time
	if st.CommentSince != nil {
		since = *st.CommentSince
	}
	page, err := lister.ListRepoComments(ctx, p.b, issuesource.CommentQuery{Since: since, ETag: st.CommentETag})
	if err != nil {
		return err
	}
	if page.Unchanged {
		return nil
	}
	before := st.CommentWatermark
	seen := map[string]bool{}
	for i := range page.Comments {
		c := &page.Comments[i]
		if seen[c.Remote.RemoteKey] {
			continue
		}
		seen[c.Remote.RemoteKey] = true
		if err := p.applyComment(ctx, c); err != nil {
			return err
		}
		st.CommentWatermark = laterOf(st.CommentWatermark, c.Remote.UpdatedAt)
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
// number in the binding. GitHub's listing carries a pull request's
// conversation comments under the same numbers, and comments on issues
// never imported (closed history, an import still under way); neither has
// an issue here, and both are ignored (task 130 decision 24.4) — the
// backfill brings the earlier thread of an issue imported later.
func (p *issuePass) applyComment(ctx context.Context, c *issuesource.Comment) error {
	r := p.r
	id, ok, err := r.store.IssueIDByRemoteNumber(ctx, p.project.ID, p.provider(), p.b.String(), c.IssueNumber)
	if err != nil || !ok {
		return err
	}
	_, err = r.store.UpsertRemoteIssueComment(ctx, id, c.Remote)
	return err
}

// syncListedThreads is the comment pass of a source with no
// repository-wide listing (task 142.4): the thread of every issue the issue
// pass listed and imported, attached by the issue's remote key. An issue's
// comments moving moves its update time, so an issue not listed this pass
// has no new comment to read; one listed but not imported (closed history)
// is not asked about.
func (p *issuePass) syncListedThreads(ctx context.Context) error {
	r := p.r
	for i := range p.listed {
		is := &p.listed[i]
		if is.Comments <= 0 {
			continue
		}
		id, ok, err := r.store.IssueIDByRemoteKey(ctx, p.project.ID, p.provider(), is.Remote.RemoteKey)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		thread, err := p.src.ListComments(ctx, p.b, is)
		if err != nil {
			return err
		}
		if err := p.writeComments(ctx, id, thread); err != nil {
			return err
		}
	}
	return nil
}

// writeComments mirrors a thread onto issueID.
func (p *issuePass) writeComments(ctx context.Context, issueID int64, thread []issuesource.Comment) error {
	for i := range thread {
		if _, err := p.r.store.UpsertRemoteIssueComment(ctx, issueID, thread[i].Remote); err != nil {
			return err
		}
	}
	return nil
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
