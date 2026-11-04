package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issues"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The issue state write-back drain (task 130.10, spec §12.3, §12.4). A
// human's close or reopen of an issue with a live remote leaves a pending
// row in issue_sync_outbox, committed with the change itself; this worker
// sends it. It is one goroutine, so writes are serialized, and it is woken
// by an enqueue, by each reconciler tick and by a "sync now".
//
// It talks to an issuesource.Source, never to a provider's client (task 142
// decision 2): each row is dispatched on its issue's remote provider to the
// source registered for it (task 142.4), still one row at a time, so every
// provider shares the one serialized writer and its pacing. Each provider's
// rows wait on that provider's gates alone (task 142.7).
//
// Every send is a compare-and-set: the provider is read first, and
//
//   - the provider already at the desired value is done with no write — the
//     echo of a write that landed before a crash, or a human who made the
//     same change on both sides;
//   - the provider still at the base (what vincent last saw) is written;
//   - anything else is a true conflict, which the provider wins (task 130
//     decision 9): its value is adopted locally, by sync, and the row ends
//     conflict so the issue says so.
//
// The write is idempotent and preflighted, so a crash between the send and
// the row being settled is harmless: the restarted drain finds the provider
// at the desired value and settles it done. Rows survive a restart because
// they are rows.
//
// An add_comment row (task 137.7) is a granted handoff's comment. It has no
// compare-and-set — a comment is appended, not a value the provider could
// already hold — so it is posted, and the provider's comment key is recorded
// on the local comment, which the comment sync then updates in place rather
// than importing a copy. Its failures follow set_state's classes. A crash
// between the post and the settle posts the comment again on restart: the
// one write here that is at-least-once, and visible rather than lost.
//
// A row whose provider has no enabled source waits, with the reason it has
// none. Those are the provider's importer gates: for GitHub, with
// `github.enabled: false` or `github.poll_interval: 0` nothing is called and
// every pending GitHub row stays pending with reason `disabled`, drained
// when the switch comes back on; with no client wired, `no_client`. Jira's
// rows read `jira.enabled` and `jira.poll_interval` the same way, and wait
// with `no_client` while no Jira source is wired (#908).

const (
	// outboxIdle is the heartbeat on which the drain re-reads the config
	// and the due rows when nothing wakes it.
	outboxIdle = time.Minute
	// outboxPace is the least gap between two mutative calls, GitHub's own
	// guidance for content-creating requests.
	outboxPace = time.Second
	// outboxBackoffBase and outboxBackoffMax bound the retry of a write
	// GitHub could not be reached for.
	outboxBackoffBase = 30 * time.Second
	outboxBackoffMax  = 30 * time.Minute
	// outboxCredentialWait is the slow heartbeat for a credential problem:
	// a human fixes it, and nothing retried sooner would find it fixed.
	outboxCredentialWait = 15 * time.Minute
)

// Write-back reasons the drain records on top of the seam's vocabulary.
const (
	outboxReasonDisabled      = issuesource.ReasonDisabled // the provider's enabled false or poll_interval 0
	outboxReasonNoClient      = syncReasonNoClient         // no source wired for the row's provider
	outboxReasonRemoteChanged = "remote_changed"           // a conflict: the provider moved off the base first
	outboxReasonGone          = "gone"                     // the remote is missing
)

// sourceFor resolves a provider to its enabled source, or reports why it
// has none: the reason a row of that provider waits with.
type sourceFor func(provider string) (src issuesource.Source, holdReason string)

// IssueOutbox drains issue_sync_outbox.
type IssueOutbox struct {
	store *store.Store
	cfg   func() config.Config
	// gh is the GitHub source, nil when no client is wired; jira the Jira
	// source, nil until the Jira client is wired (#908).
	gh, jira issuesource.Source
	// sources is the provider→source registry, read per row; seamed for
	// tests. The default is keyed by provider (issuesources.go).
	sources sourceFor
	logger  *slog.Logger
	// now is the clock and sleep the pacing wait, seamed for tests.
	now   func() time.Time
	sleep func(context.Context, time.Duration) bool
	// lastWrite is when the last mutative call was made; only the drain's
	// goroutine touches it.
	lastWrite time.Time
	wake      chan struct{}
}

// Kick wakes Run. It never blocks: a kick arriving while one is pending is
// the same kick. It is the store's OnIssueOutboxEnqueued callback.
func (o *IssueOutbox) Kick() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Run drains until ctx is done: at start — rows a crash left pending go
// first — on each kick, and when the next row falls due.
func (o *IssueOutbox) Run(ctx context.Context) {
	for {
		wait := outboxIdle
		if next := o.Drain(ctx); !next.IsZero() {
			if d := next.Sub(o.now()); d < wait {
				wait = max(d, outboxPace)
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-o.wake:
			timer.Stop()
		}
	}
}

// Drain sends every pending row that is due, one at a time, each through
// the source its issue's provider resolves to, and reports when the next
// pending row falls due (zero when none is pending, or when every pending
// row's provider is held). A row whose provider has no enabled source is
// kept pending with the reason, making no call, and its due time is kept so
// it goes as soon as the source is enabled.
func (o *IssueOutbox) Drain(ctx context.Context) time.Time {
	rows, err := o.store.PendingIssueWrites(ctx, time.Time{})
	if err != nil {
		o.logger.Warn("issue write-back: pending writes not listed", "error", err)
		return time.Time{}
	}
	now := o.now()
	// held is the providers this drain found no source for; provider is
	// each row's issue's, so the next due time skips held rows unread.
	held := map[string]bool{}
	provider := map[int64]string{}
	for _, row := range rows {
		if ctx.Err() != nil {
			return time.Time{}
		}
		iss, ok := o.issue(ctx, row)
		if !ok {
			continue
		}
		name := providerOf(iss)
		provider[row.ID] = name
		src, hold := o.sources(name)
		if src == nil {
			held[name] = true
			if err := o.store.DeferIssueWrite(ctx, row.ID, hold, row.NextAttemptAt, false); err != nil {
				o.logger.Warn("issue write-back: write not deferred", "write", row.ID, "error", err)
			}
			continue
		}
		if row.NextAttemptAt.After(now) {
			continue
		}
		o.send(ctx, src, row, iss)
	}
	rest, err := o.store.PendingIssueWrites(ctx, time.Time{})
	if err != nil {
		return time.Time{}
	}
	for _, row := range rest {
		name, seen := provider[row.ID]
		if !seen {
			// Enqueued during this drain: the next drain reads it.
			return row.NextAttemptAt
		}
		if !held[name] {
			return row.NextAttemptAt
		}
	}
	return time.Time{}
}

// issue is row's issue; false when it is gone (the row went with it) or
// could not be read.
func (o *IssueOutbox) issue(ctx context.Context, row *store.IssueOutbox) (*store.Issue, bool) {
	iss, err := o.store.GetIssue(ctx, row.IssueID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false // deleted under us; the row went with it
	}
	if err != nil {
		o.logger.Warn("issue write-back: issue not read", "issue", row.IssueID, "error", err)
		return nil, false
	}
	return iss, true
}

// providerOf is the provider a row of iss dispatches on: its remote's. An
// issue with no remote has none, and its row fails gone once a source runs
// it — the one source a remote-less GitHub-era row ever had is GitHub's.
func providerOf(iss *store.Issue) string {
	if iss.Remote == nil {
		return issuesource.ProviderGitHub
	}
	return iss.Remote.Provider
}

// writesBack is issues.WritesBack without its provider clause: a live
// remote. Which provider writes back is the dispatch's question — the row
// reached send because its provider resolved to an enabled source — so for
// GitHub, the only source today, the two agree.
func writesBack(iss *store.Issue) bool {
	return issues.Mirrored(iss) && iss.Remote.Status == store.RemoteStatusLive
}

// send is one row's compare-and-set through src.
func (o *IssueOutbox) send(ctx context.Context, src issuesource.Source, row *store.IssueOutbox, iss *store.Issue) {
	if !writesBack(iss) {
		reason := issuesource.ReasonMoved
		if iss.Remote == nil || iss.Remote.IssueID == nil || iss.Remote.Status == store.RemoteStatusMissing {
			reason = outboxReasonGone
		}
		o.settle(ctx, row, store.OutboxFailed, reason, nil)
		return
	}
	rem := *iss.Remote
	b, ok := src.Locate(rem)
	if !ok {
		o.settle(ctx, row, store.OutboxFailed, issuesource.ReasonBadRequest, nil)
		return
	}
	if row.Op == store.OutboxOpAddComment {
		o.sendComment(ctx, src, row, b, rem)
		return
	}
	cur, err := src.GetIssue(ctx, b, rem)
	if err != nil {
		o.retryAs(ctx, row, src.Classify(err), err)
		return
	}
	switch {
	case cur.StateValue.Same(row.Desired):
		remote := cur.StateValue
		o.settle(ctx, row, store.OutboxDone, "", &remote)
		return
	case !cur.StateValue.Same(row.Base):
		o.adopt(ctx, row, &cur)
		return
	}
	o.pace(ctx)
	err = src.SetIssueState(ctx, b, rem, row.Desired)
	o.lastWrite = o.now()
	if err != nil {
		o.retryAs(ctx, row, src.Classify(err), err)
		return
	}
	written := row.Desired
	o.settle(ctx, row, store.OutboxDone, "", &written)
	o.logger.Info("issue state written to its provider", "issue", row.IssueID, "provider", src.Provider(),
		"repo", rem.Repo, "number", rem.Number, "state", written.State, "state_reason", written.Reason)
}

// sendComment posts an add_comment row's comment and records its key.
func (o *IssueOutbox) sendComment(ctx context.Context, src issuesource.Source, row *store.IssueOutbox, b issuesource.Binding, rem store.IssueRemote) {
	if row.CommentID == nil {
		o.settle(ctx, row, store.OutboxFailed, issuesource.ReasonBadRequest, nil)
		return
	}
	c, err := o.store.GetIssueComment(ctx, *row.CommentID)
	if errors.Is(err, store.ErrNotFound) {
		o.settle(ctx, row, store.OutboxFailed, outboxReasonGone, nil)
		return
	}
	if err != nil {
		o.logger.Warn("issue write-back: comment not read", "comment", *row.CommentID, "error", err)
		return
	}
	if c.RemoteKey != "" {
		// Already on the provider: a settle that did not land last time.
		if _, err := o.store.SettleIssueCommentWrite(ctx, row.ID, c.RemoteKey); err != nil {
			o.logger.Warn("issue write-back: comment write not settled", "write", row.ID, "error", err)
		}
		return
	}
	o.pace(ctx)
	key, err := src.CreateComment(ctx, b, rem, c.Body)
	o.lastWrite = o.now()
	if err != nil {
		o.retryAs(ctx, row, src.Classify(err), err)
		return
	}
	if _, err := o.store.SettleIssueCommentWrite(ctx, row.ID, key); err != nil {
		o.logger.Warn("issue write-back: comment write not settled", "write", row.ID, "error", err)
		return
	}
	o.logger.Info("issue comment written to its provider", "issue", row.IssueID, "provider", src.Provider(),
		"repo", rem.Repo, "number", rem.Number, "comment", c.ID, "remote_key", key)
}

// adopt is the provider winning a true conflict: the row ends conflict, so
// the issue says so and the importer is no longer held off it, and the
// provider's value becomes the local state, by sync. A state that moved
// goes through the one transition path, so it announces
// issue.state_changed; the refresh after it carries the close reason, which
// a closed→closed transition cannot.
func (o *IssueOutbox) adopt(ctx context.Context, row *store.IssueOutbox, cur *issuesource.Issue) {
	remote := cur.StateValue
	o.settle(ctx, row, store.OutboxConflict, outboxReasonRemoteChanged, &remote)
	action := issuestate.RemoteReopened
	var reason issuestate.Reason
	if remote.State == string(issuestate.Closed) {
		action, reason = issuestate.RemoteClosed, issuestate.Reason(remote.Reason)
	}
	if _, err := issues.New(o.store).Transition(ctx, issuestate.Sync, row.IssueID, action, reason); err != nil {
		o.logger.Warn("issue write-back: the provider's state not adopted", "issue", row.IssueID, "error", err)
	}
	ri := cur.Remote
	ri.ProjectID = row.ProjectID
	if _, _, err := o.store.UpsertRemoteIssue(ctx, ri, issuestate.Sync); err != nil {
		o.logger.Warn("issue write-back: the provider's issue not refreshed", "issue", row.IssueID, "error", err)
	}
	o.logger.Warn("issue state changed on its provider first; the provider's kept", "issue", row.IssueID,
		"wanted", row.Desired.State, "remote", remote.State)
}

// retryAs settles a failed call by its classification on the seam's reason
// vocabulary: a terminal reason fails the row, keeping the local state;
// everything else keeps it pending until its class says to try again.
func (o *IssueOutbox) retryAs(ctx context.Context, row *store.IssueOutbox, f issuesource.Failure, err error) {
	reason := f.Reason
	now := o.now()
	var next time.Time
	switch reason {
	case issuesource.ReasonNoWriteScope, issuesource.ReasonNotFound, issuesource.ReasonGone,
		issuesource.ReasonBadRequest, issuesource.ReasonMoved, issuesource.ReasonForbidden:
		o.settle(ctx, row, store.OutboxFailed, reason, nil)
		o.logger.Warn("issue state not written to its provider", "issue", row.IssueID, "reason", reason, "error", err)
		return
	case issuesource.ReasonRateLimited:
		next = now.Add(outboxBackoffBase)
		if f.ResetAt.After(now) {
			next = f.ResetAt
		}
	case issuesource.ReasonNoCredential, issuesource.ReasonUnauthorized:
		next = now.Add(outboxCredentialWait)
	default: // unreachable, timeout, bad_response, and anything unforeseen
		next = now.Add(backoff(row.Attempts))
	}
	if derr := o.store.DeferIssueWrite(ctx, row.ID, reason, next, true); derr != nil {
		o.logger.Warn("issue write-back: write not deferred", "write", row.ID, "error", derr)
	}
	o.logger.Debug("issue write-back deferred", "issue", row.IssueID, "reason", reason, "until", next)
}

func (o *IssueOutbox) settle(ctx context.Context, row *store.IssueOutbox, status, reason string, written *store.IssueStateValue) {
	if _, err := o.store.SettleIssueWrite(ctx, row.ID, status, reason, written); err != nil {
		o.logger.Warn("issue write-back: write not settled", "write", row.ID, "status", status, "error", err)
	}
}

// pace holds the drain until outboxPace has passed since the last
// mutative call.
func (o *IssueOutbox) pace(ctx context.Context) {
	if o.lastWrite.IsZero() {
		return
	}
	if d := outboxPace - o.now().Sub(o.lastWrite); d > 0 {
		o.sleep(ctx, d)
	}
}

// backoff is the wait after the attempts-th failed attempt: doubling from
// outboxBackoffBase, capped at outboxBackoffMax.
func backoff(attempts int) time.Duration {
	d := outboxBackoffBase
	for range attempts {
		if d >= outboxBackoffMax {
			return outboxBackoffMax
		}
		d *= 2
	}
	return min(d, outboxBackoffMax)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
