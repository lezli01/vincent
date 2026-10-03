package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issues"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The issue state write-back drain (task 130.10, spec §12.3, §12.4). A
// human's close or reopen of an issue with a live GitHub remote leaves a
// pending row in issue_sync_outbox, committed with the change itself; this
// worker sends it. It is one goroutine, so writes are serialized, and it
// is woken by an enqueue, by each reconciler tick and by a "sync now".
//
// Every send is a compare-and-set: GitHub is read first, and
//
//   - GitHub already at the desired value is done with no write — the
//     echo of a write that landed before a crash, or a human who made the
//     same change on both sides;
//   - GitHub still at the base (what vincent last saw) is written;
//   - anything else is a true conflict, which GitHub wins (task 130
//     decision 9): its value is adopted locally, by sync, and the row ends
//     conflict so the issue says so.
//
// The PATCH is idempotent and preflighted, so a crash between the send and
// the row being settled is harmless: the restarted drain finds GitHub at
// the desired value and settles it done. Rows survive a restart because
// they are rows.
//
// The config gates are the importer's: with `github.enabled: false` or
// `poll_interval: 0` nothing is called and every pending row stays pending
// with reason `disabled`, drained when the switch comes back on.

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

// Write-back reasons the drain records on top of github's vocabulary.
const (
	outboxReasonDisabled      = github.ReasonDisabled // github.enabled false or poll_interval 0
	outboxReasonNoClient      = syncReasonNoClient
	outboxReasonRemoteChanged = "remote_changed" // a conflict: GitHub moved off the base first
	outboxReasonGone          = "gone"           // the remote is missing
)

// IssueOutbox drains issue_sync_outbox.
type IssueOutbox struct {
	store  *store.Store
	cfg    func() config.Config
	client *github.Client
	logger *slog.Logger
	// now is the clock and sleep the pacing wait, seamed for tests.
	now   func() time.Time
	sleep func(context.Context, time.Duration) bool
	// lastWrite is when the last mutative call was made; only the drain's
	// goroutine touches it.
	lastWrite time.Time
	wake      chan struct{}
}

// NewIssueOutbox builds the drain. It performs no I/O.
func NewIssueOutbox(st *store.Store, cfg func() config.Config, client *github.Client, logger *slog.Logger) *IssueOutbox {
	return &IssueOutbox{
		store: st, cfg: cfg, client: client, logger: logger,
		now: time.Now, sleep: sleepCtx,
		wake: make(chan struct{}, 1),
	}
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

// Drain sends every pending row that is due, one at a time, and reports
// when the next pending row falls due (zero when none is pending, or when
// the gates hold them all).
func (o *IssueOutbox) Drain(ctx context.Context) time.Time {
	gh := o.cfg().GitHub
	switch {
	case !gh.Polls():
		o.holdAll(ctx, outboxReasonDisabled)
		return time.Time{}
	case o.client == nil:
		o.holdAll(ctx, outboxReasonNoClient)
		return time.Time{}
	}
	due, err := o.store.PendingIssueWrites(ctx, o.now())
	if err != nil {
		o.logger.Warn("issue write-back: pending writes not listed", "error", err)
		return time.Time{}
	}
	for _, row := range due {
		if ctx.Err() != nil {
			return time.Time{}
		}
		o.send(ctx, row)
	}
	rest, err := o.store.PendingIssueWrites(ctx, time.Time{})
	if err != nil || len(rest) == 0 {
		return time.Time{}
	}
	return rest[0].NextAttemptAt
}

// holdAll keeps every pending row pending with reason, making no call. The
// due time is kept, so the rows go as soon as the gate opens.
func (o *IssueOutbox) holdAll(ctx context.Context, reason string) {
	rows, err := o.store.PendingIssueWrites(ctx, time.Time{})
	if err != nil {
		o.logger.Warn("issue write-back: pending writes not listed", "error", err)
		return
	}
	for _, row := range rows {
		if err := o.store.DeferIssueWrite(ctx, row.ID, reason, row.NextAttemptAt, false); err != nil {
			o.logger.Warn("issue write-back: write not deferred", "write", row.ID, "error", err)
		}
	}
}

// send is one row's compare-and-set.
func (o *IssueOutbox) send(ctx context.Context, row *store.IssueOutbox) {
	iss, err := o.store.GetIssue(ctx, row.IssueID)
	if errors.Is(err, store.ErrNotFound) {
		return // deleted under us; the row went with it
	}
	if err != nil {
		o.logger.Warn("issue write-back: issue not read", "issue", row.IssueID, "error", err)
		return
	}
	if !issues.WritesBack(iss) {
		reason := github.ReasonMoved
		if iss.Remote == nil || iss.Remote.IssueID == nil || iss.Remote.Status == store.RemoteStatusMissing {
			reason = outboxReasonGone
		}
		o.settle(ctx, row, store.OutboxFailed, reason, nil)
		return
	}
	rem := iss.Remote
	repo, ok := splitRepo(rem.Repo)
	if !ok || rem.Number < 1 {
		o.settle(ctx, row, store.OutboxFailed, github.ReasonBadRequest, nil)
		return
	}
	cur, err := o.client.GetIssue(ctx, repo, rem.Number)
	if err != nil {
		o.retry(ctx, row, err)
		return
	}
	remote := remoteStateValue(cur)
	switch {
	case remote.Same(row.Desired):
		o.settle(ctx, row, store.OutboxDone, "", &remote)
		return
	case !remote.Same(row.Base):
		o.adopt(ctx, row, &cur)
		return
	}
	o.pace(ctx)
	_, err = o.client.SetIssueState(ctx, repo, rem.Number, row.Desired.State, row.Desired.Reason, row.Desired.DuplicateOf)
	o.lastWrite = o.now()
	if err != nil {
		o.retry(ctx, row, err)
		return
	}
	written := row.Desired
	o.settle(ctx, row, store.OutboxDone, "", &written)
	o.logger.Info("issue state written to GitHub", "issue", row.IssueID, "repo", rem.Repo,
		"number", rem.Number, "state", written.State, "state_reason", written.Reason)
}

// adopt is GitHub winning a true conflict: the row ends conflict, so the
// issue says so and the importer is no longer held off it, and GitHub's
// value becomes the local state, by sync. A state that moved goes through
// the one transition path, so it announces issue.state_changed; the
// refresh after it carries the close reason, which a closed→closed
// transition cannot.
func (o *IssueOutbox) adopt(ctx context.Context, row *store.IssueOutbox, cur *github.Issue) {
	remote := remoteStateValue(*cur)
	o.settle(ctx, row, store.OutboxConflict, outboxReasonRemoteChanged, &remote)
	action := issuestate.RemoteReopened
	var reason issuestate.Reason
	if remote.State == string(issuestate.Closed) {
		action, reason = issuestate.RemoteClosed, issuestate.Reason(remote.Reason)
	}
	if _, err := issues.New(o.store).Transition(ctx, issuestate.Sync, row.IssueID, action, reason); err != nil {
		o.logger.Warn("issue write-back: GitHub's state not adopted", "issue", row.IssueID, "error", err)
	}
	if _, _, err := o.store.UpsertRemoteIssue(ctx, remoteIssue(row.ProjectID, cur), issuestate.Sync); err != nil {
		o.logger.Warn("issue write-back: GitHub's issue not refreshed", "issue", row.IssueID, "error", err)
	}
	o.logger.Warn("issue state changed on GitHub first; GitHub's kept", "issue", row.IssueID,
		"wanted", row.Desired.State, "github", remote.State)
}

// retry classifies a failed call on github's reason vocabulary: a
// terminal reason fails the row, keeping the local state; everything else
// keeps it pending until its class says to try again.
func (o *IssueOutbox) retry(ctx context.Context, row *store.IssueOutbox, err error) {
	reason := github.ReasonOf(err)
	now := o.now()
	var next time.Time
	switch reason {
	case github.ReasonNoWriteScope, github.ReasonNotFound, github.ReasonGone, github.ReasonBadRequest,
		github.ReasonMoved, github.ReasonForbidden:
		o.settle(ctx, row, store.OutboxFailed, reason, nil)
		o.logger.Warn("issue state not written to GitHub", "issue", row.IssueID, "reason", reason, "error", err)
		return
	case github.ReasonRateLimited:
		next = now.Add(outboxBackoffBase)
		var gerr *github.Error
		if errors.As(err, &gerr) && gerr.ResetAt.After(now) {
			next = gerr.ResetAt
		}
	case github.ReasonNoCredential, github.ReasonUnauthorized:
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

// remoteStateValue is GitHub's issue as the outbox compares it.
func remoteStateValue(is github.Issue) store.IssueStateValue {
	if is.State != github.StateClosed {
		return store.IssueStateValue{State: string(issuestate.Open)}
	}
	reason := is.StateReason
	if !issuestate.ValidReason(issuestate.Reason(reason)) {
		reason = string(issuestate.Completed)
	}
	return store.IssueStateValue{State: string(issuestate.Closed), Reason: reason}
}

// splitRepo parses a remote row's "owner/name".
func splitRepo(s string) (github.Repo, bool) {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return github.Repo{}, false
	}
	return github.Repo{Owner: owner, Name: name}, true
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
