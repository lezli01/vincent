// Package issuearchive is the daemon's auto-archive worker for a closed
// issue's settled tasks (task 143, issue #928; spec §5.6, §6, §10, §12.4).
//
// Closing an issue says its work is finished, so the tasks that did that work
// should leave the board without a person archiving each one. The worker
// decides nothing about *which* tasks may go: Store.ArchivableIssueTasks
// (143.2) answers that from current rows — a closed issue, every root
// settled, no live issue chat, created at or before the close, side work
// before main tasks. The worker only decides *when* to ask, and does the
// asking through taskrun.Runner.Archive in unattended mode (143.1), which
// never forces a dirty worktree and never deletes a remote branch.
//
// When to ask is driven by the durable event table, not by the broker's
// in-memory fan-out: the broker only wakes the loop (a non-blocking,
// level-triggered signal into a one-slot channel, so a slow archive never
// stalls the store's writing goroutine), and the loop reads every event after
// its cursor in pages. The cursor is the one row of issue_archive_cursor
// (migration 0053), persisted after each page, so a restart replays what the
// previous daemon had not finished (§12.4). Replay is safe because the query
// is derived from current rows and archive is idempotent — an already
// archived task is an InvalidActionError, silently ignored. The migration
// seeds the cursor at the newest event, so an upgrade archives no history
// (task 143 decision 7).
//
// An issue is looked at when an event says something that could make it
// archivable: it closed (issue.state_changed, or a sync's issue.updated,
// read through store.IssueStateMove); one of its tasks settled, a lane
// included, because the root's UnfinishedDescendantsError is retried then; a
// chat on it or linked to one of its tasks reached a terminal state, which
// releases a link lock or a live issue chat; or a handoff item on it changed
// or its handoff settled. A refusal is logged to daemon.log and never blocks
// the cursor: the retry comes from a later event on the issue. A chat close
// or a lane settle is one; cleaning a dirty worktree records no event, so a
// dirty refusal waits for an unrelated event or a human archive.
//
// While a handoff item on the issue still claims it (HandoffOutcome.Claims),
// none of its tasks is archived (task 143 decision 10): a handoff `return`
// after a reopen follows up the issue's latest main task, which an archive
// would refuse. Chats are never archived (decision 4).
//
// The switch is `archive_tasks_on_issue_close`, read once per page so a hot
// reload governs the next page. Events read while it is off are passed over
// for good — turning it back on processes later events, never a backfill.
//
// Like internal/trigger (its decision 30) the package never imports
// internal/api; it depends on narrow interfaces over the store and the runner.
package issuearchive
