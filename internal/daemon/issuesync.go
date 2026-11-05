package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The issue importer (task 130.8, spec §5.6): on its provider's tick
// (issuesyncer.go) it imports and refreshes each bound project's issues into the project's own
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

// syncReasonOriginChanged is a project whose origin no longer names the
// repository its issues were imported from. The API lane reads it, so it is
// fixed. (not_github and source_not_github are no longer the importer's to
// write, task 142.7: a project resolving to none gets no sync row, and the
// API derives what to say about it from its binding and origin.)
const syncReasonOriginChanged = "origin_changed"

// syncIssues runs one GitHub import attempt for project through the
// syncer's GitHub source. The caller has resolved project to github on a
// github.com origin (gitHubPass), and b and bound are its binding, derived
// once per project per tick so the importer and the pull request half act
// on one reading of origin (review F1). haveClient is whether a client is
// wired. The two gates config owns (github.enabled, poll_interval) are the
// caller's and are already passed.
//
// The pass runs under the project's issue-source gate (review F1): a rebind
// that commits while it waits leaves it a stale project, so it re-reads the
// project and stands down for this tick if the binding or path moved; a
// rebind that arrives mid-pass cancels its context, and nothing more is
// written — not even the sync row the rebind deleted.
func (s *IssueSyncer) syncIssues(ctx context.Context, project store.Project, b issuesource.Binding, bound, haveClient bool) {
	src := s.issueSrc
	ctx, _, st, done, ok := s.beginPass(ctx, project, src.Provider())
	if !ok {
		return
	}
	defer done()
	if !bound {
		// A source other than GitHub's (a test's stub) that cannot bind the
		// project: like a project resolving to none, it gets no row.
		return
	}
	now := s.clock()
	if s.backingOff(st, now) && haveClient {
		// Backing off until the provider's reset.
		return
	}
	next := *st
	next.Provider = src.Provider()
	next.LastAttemptAt = &now
	next.RequestedAt = nil
	// The gates, in order, before any call: a project that fails one makes
	// none. Without a git runner the origin cannot be read, which is a
	// missing client rather than a verdict about the origin.
	var err error
	switch {
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
		err = s.runImport(ctx, src, project, b, st, &next, now)
	}
	s.finishPass(ctx, project, b, st, next, err)
}

// issuePass is one import attempt for one bound project: the source, the
// binding, and the sync row it advances as it goes, so progress made before
// a failure is kept.
type issuePass struct {
	r       *IssueSyncer
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
		if err := p.initialImport(ctx, now); err != nil {
			return err
		}
	} else if err := p.incremental(ctx, now); err != nil {
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
//
// A source whose open walk is ordered by id (Jira's) resumes from the
// cursor its truncated page returned (ImportCursor) instead of from the
// watermark. Its walk is not ordered by update time, so the watermark at
// its end says nothing about an issue read on an earlier tick and changed
// since: the import records when it began in Since — which nothing else
// reads until the import completes — and the first incremental bound is
// no later than that. A source resuming by time never sets a cursor, and
// its import is exactly as it was.
func (p *issuePass) initialImport(ctx context.Context, now time.Time) error {
	st := p.st
	var since time.Time
	if st.Watermark != nil {
		since = *st.Watermark
	}
	page, err := p.src.ListIssues(ctx, p.b, issuesource.ListOptions{
		OpenOnly: true, Since: since, PageCap: importPageCap, After: st.ImportCursor,
	})
	if err != nil {
		return err
	}
	st.Repo = p.b.String()
	if err := p.applyIssues(ctx, page); err != nil {
		return err
	}
	switch {
	case !page.Truncated:
		st.ImportComplete = true
		st.ETag = ""
		st.Since = earlierOf(sinceFrom(st.Watermark), st.Since)
		st.ImportCursor = ""
	case page.ResumeAfter != "":
		if st.Since == nil {
			begin := now.Add(-sinceOverlap)
			st.Since = &begin
		}
		st.ImportCursor = page.ResumeAfter
	}
	return nil
}

// incremental asks for everything updated since the stored bound, with the
// stored ETag. The bound moves only when the watermark does, so an idle
// repository is asked the same question every tick and answers 304: one
// request, nothing charged.
//
// A source that resumes a truncated listing by cursor (Jira's, ordered by
// id) is walked the way initialImport walks: the cursor goes in
// ImportCursor and is handed back until the walk ends, and the bound is
// held at when the walk began, because an issue the walk passed may change
// before it ends and the watermark at the end says nothing about it.
// Resuming such a window by time could not get past one holding more
// changes than a walk reads (review F1 on #943). A source resuming by time
// never sets a cursor, and its pass is exactly as it was.
func (p *issuePass) incremental(ctx context.Context, now time.Time) error {
	st := p.st
	if st.Since == nil {
		st.Since = sinceFrom(st.Watermark)
	}
	var since time.Time
	if st.Since != nil {
		since = *st.Since
	}
	page, err := p.src.ListIssues(ctx, p.b, issuesource.ListOptions{Since: since, ETag: st.ETag, After: st.ImportCursor})
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
	switch {
	case page.Truncated && page.ResumeAfter != "":
		if st.ImportCursor == "" {
			begin := now.Add(-sinceOverlap)
			st.Since = &begin
		}
		st.ImportCursor = page.ResumeAfter
		st.ETag = ""
	case st.ImportCursor != "":
		// The walk ended: the next pass asks from when it began.
		st.ImportCursor = ""
		st.Since = earlierOf(sinceFrom(st.Watermark), st.Since)
		st.ETag = ""
	case !sameTime(before, st.Watermark) || laterBound(st.Since, sinceFrom(st.Watermark)):
		// A new question: the stored ETag answered the old one. The second
		// clause is an id-ordered walk's bound (initialImport, and the
		// walk above), held at the walk's start until a pass has read
		// everything since; a source resuming by time always has its bound
		// at the watermark's.
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
	confirmed, err := p.confirmLagged(ctx, page.Issues)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range page.Issues {
		is := &page.Issues[i]
		key := is.Remote.RemoteKey
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		// The watermark advances by what the listing said, confirmed or
		// not: the listing is what the next bound is asked of.
		st.Watermark = laterOf(st.Watermark, is.UpdatedAt())
		if c, held := confirmed[key]; held {
			if c == nil {
				// The confirming read did not find it here; the sweep
				// decides about a missing or moved issue, not a listing.
				continue
			}
			is = c
		}
		if err := p.applyIssue(ctx, is, needsBackfill(st)); err != nil {
			return err
		}
		p.listed = append(p.listed, *is)
	}
	// ResumeSince counts pull request rows too, so a window holding only
	// pull requests still moves the cursor.
	st.Watermark = laterOf(st.Watermark, page.ResumeSince)
	return nil
}

// confirmLagged is task 142.13 decision 1: on a source whose listing may
// trail its own writes (issuesource.LaggingLister — Jira's search index), a
// listed state that differs from the local one, on an issue whose newest
// state write is done, may be the index still showing the state from before
// that write. Each such issue is re-read by id — a consistent read, through
// BulkGetter when the source has it — and the listing's copy is replaced by
// what the read reports; a key mapped to nil was not found at the binding,
// and the listing's copy is not applied. There is no time window and no
// clock comparison. A source that does not lag (GitHub) is untouched, and
// the store's own hold rules (pending and failed writes) still apply after.
func (p *issuePass) confirmLagged(ctx context.Context, listed []issuesource.Issue) (map[string]*issuesource.Issue, error) {
	if _, ok := p.src.(issuesource.LaggingLister); !ok {
		return nil, nil
	}
	var rems []store.IssueRemote
	for i := range listed {
		is := &listed[i]
		suspect, err := p.writtenSince(ctx, is)
		if err != nil {
			return nil, err
		}
		if suspect {
			rems = append(rems, store.IssueRemote{
				Provider: is.Remote.Provider, RemoteKey: is.Remote.RemoteKey,
				Repo: is.Remote.Repo, Number: is.Remote.Number,
			})
		}
	}
	if len(rems) == 0 {
		return nil, nil
	}
	out := make(map[string]*issuesource.Issue, len(rems))
	if bulk, ok := p.src.(issuesource.BulkGetter); ok {
		for start := 0; start < len(rems); start += issuesource.BulkGetMax {
			batch := rems[start:min(start+issuesource.BulkGetMax, len(rems))]
			got, err := bulk.GetIssues(ctx, p.b, batch)
			if err != nil {
				return nil, err
			}
			for _, rem := range batch {
				bi, found := got[rem.RemoteKey]
				if !found || bi.MovedTo != "" {
					out[rem.RemoteKey] = nil
					continue
				}
				is := bi.Issue
				out[rem.RemoteKey] = &is
			}
		}
		return out, nil
	}
	for _, rem := range rems {
		is, err := p.src.GetIssue(ctx, p.b, rem)
		if err != nil {
			if f := p.src.Classify(err); f.Reason == issuesource.ReasonNotFound || f.Reason == issuesource.ReasonMoved {
				out[rem.RemoteKey] = nil
				continue
			}
			return nil, err
		}
		out[rem.RemoteKey] = &is
	}
	return out, nil
}

// writtenSince reports whether is, as listed, differs from its local issue's
// state while that issue's newest state write is done: the one case a
// lagging listing could revert a write just made.
func (p *issuePass) writtenSince(ctx context.Context, is *issuesource.Issue) (bool, error) {
	st := p.r.store
	id, ok, err := st.IssueIDByRemoteKey(ctx, p.project.ID, p.provider(), is.Remote.RemoteKey)
	if err != nil || !ok {
		return false, err
	}
	iss, err := st.GetIssue(ctx, id)
	if err != nil {
		return false, err
	}
	if is.StateValue.Same(store.RemoteStateOf(iss.State, iss.CloseReason)) {
		return false, nil
	}
	writes, err := st.IssueWrites(ctx, id)
	if err != nil {
		return false, err
	}
	for i := len(writes) - 1; i >= 0; i-- {
		if writes[i].Op == store.OutboxOpSetState {
			return writes[i].Status == store.OutboxDone, nil
		}
	}
	return false, nil
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
// deleted is deleted, unreadable is missing. A source with a bulk read
// (issuesource.BulkGetter) is asked BulkGetMax at a time, and on it absent
// is missing — never deleted (probeBatch). An id-ordered open walk resumes
// by its cursor, a time-ordered one by its time.
func (p *issuePass) sweepOpenSet(ctx context.Context) error {
	r := p.r
	open := map[string]bool{}
	var (
		since time.Time
		after string
	)
	for calls := 0; ; calls++ {
		if calls == fullScanCalls {
			r.logger.Warn("issue sync: open set too large to sweep", "project", p.project.ID, "repo", p.b.String())
			return nil
		}
		page, err := p.src.ListIssues(ctx, p.b, issuesource.ListOptions{OpenOnly: true, Since: since, After: after})
		if err != nil {
			return err
		}
		for i := range page.Issues {
			open[page.Issues[i].Remote.RemoteKey] = true
		}
		if !page.Truncated {
			break
		}
		since, after = page.ResumeSince, page.ResumeAfter
	}
	local, err := r.store.ListOpenRemoteIssues(ctx, p.project.ID, p.provider())
	if err != nil {
		return err
	}
	bulk, _ := p.src.(issuesource.BulkGetter)
	var batch []store.IssueRemote
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
		if bulk != nil {
			if batch = append(batch, rem); len(batch) == issuesource.BulkGetMax {
				if err := p.probeBatch(ctx, bulk, batch); err != nil {
					return err
				}
				batch = nil
			}
			continue
		}
		if err := p.probeIssue(ctx, rem, needsBackfill(p.st)); err != nil {
			return err
		}
	}
	if len(batch) > 0 {
		return p.probeBatch(ctx, bulk, batch)
	}
	return nil
}

// probeBatch is the sweep's read of up to BulkGetMax remotes the open
// listing lacked, through a source that reads many in one call (Jira's
// bulkfetch). An absent one is marked missing and never deleted: the
// provider answers deleted and unreadable alike by leaving it out, so
// GitHub's "gone deletes" rule has no analogue here. A moved one is marked
// moved and its row rewritten to where it went; any other is refreshed, as
// a single probe's answer is.
func (p *issuePass) probeBatch(ctx context.Context, bulk issuesource.BulkGetter, batch []store.IssueRemote) error {
	got, err := bulk.GetIssues(ctx, p.b, batch)
	if err != nil {
		return err
	}
	for _, rem := range batch {
		bi, ok := got[rem.RemoteKey]
		switch {
		case !ok:
			err = p.markRemote(ctx, rem, store.RemoteStatusMissing, "")
		case bi.MovedTo != "":
			err = p.relocate(ctx, rem, &bi.Issue, bi.MovedTo)
		default:
			_, _, err = p.r.store.UpsertRemoteIssue(ctx, p.remoteIssue(&bi.Issue), issuestate.Sync)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// relocate marks rem moved to location and rewrites its row to is's new
// repository, number, key and URL — a source that read the issue where it
// went (task 142.12).
func (p *issuePass) relocate(ctx context.Context, rem store.IssueRemote, is *issuesource.Issue, location string) error {
	r := p.r
	if err := r.store.RelocateIssueRemote(ctx, p.project.ID, p.provider(), rem.RemoteKey, p.remoteIssue(is), location); err != nil {
		return err
	}
	r.logger.Info("issue no longer on its provider where it was", "project", p.project.ID,
		"provider", p.provider(), "repo", p.b.String(), "issue", rem.Number, "status", store.RemoteStatusMoved,
		"location", location, "to_repo", is.Remote.Repo, "to_issue", is.Remote.Number)
	return nil
}

// markRemote records what a probe learned of rem.
func (p *issuePass) markRemote(ctx context.Context, rem store.IssueRemote, status, location string) error {
	r := p.r
	if err := r.store.SetIssueRemoteStatus(ctx, p.project.ID, p.provider(), rem.RemoteKey, status, location); err != nil {
		return err
	}
	r.logger.Info("issue no longer on its provider where it was", "project", p.project.ID,
		"provider", p.provider(), "repo", p.b.String(), "issue", rem.Number, "status", status, "location", location)
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
	case f.Reason == issuesource.ReasonMoved && is.Remote.Repo != "":
		// A source that read the issue where it went (Jira): the row
		// follows it there.
		return p.relocate(ctx, rem, &is, f.Location)
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
	return p.markRemote(ctx, rem, status, location)
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

// earlierOf is the earlier of two bounds, nil when both are.
func earlierOf(a, b *time.Time) *time.Time {
	if a == nil || (b != nil && b.Before(*a)) {
		return b
	}
	return a
}

// laterBound is whether next is a bound later than cur.
func laterBound(cur, next *time.Time) bool {
	return next != nil && (cur == nil || next.After(*cur))
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
