// Package finishedarchive is the daemon's age-based auto-archive: it archives
// a root task that has sat in `done` or `aborted` for
// `archive_finished_tasks_after` (task 155; spec §6, §10, §12.3).
//
// A finished task's worktree is disk the board no longer needs, and a board
// that only ever grows hides the work still in flight. Closing an issue
// already archives its tasks (task 143, internal/issuearchive); this covers
// every other finished task — one with no issue, one whose issue is still
// open — on a clock instead of on an event.
//
// The worker decides nothing about *which* tasks may go:
// Store.FinishedArchiveDue answers that from current rows — a done or aborted
// root finished at or before the cutoff, with an issue's main task held while
// its issue still needs the line (an unsettled root, a live issue chat, a
// handoff claim, or side work not yet archived). The worker only decides
// *when* to ask, and asks through taskrun.Runner.Archive in unattended mode,
// which never forces a dirty worktree and never deletes a remote branch.
//
// When to ask is a plain timer, not the event table: what makes a task due
// is the passing of time, which records no event. The loop sweeps once at
// start — so a daemon that was down past a task's deadline archives it as
// soon as it is back (§12.4) — and then every Interval (one minute). The
// query is derived from current rows and archive is idempotent, so a sweep
// that overlaps a human archive, or one cut short by shutdown, is harmless.
//
// The clock is the task's finished_at, which every entry into `done` or
// `aborted` restamps (Store.TransitionTask). A follow-up returns a task to
// the state it came from, so it restarts the clock (decision 4): a task
// someone is still working with stays on the board.
//
// A refusal — a dirty worktree, a linked chat open on the task, a lane still
// working — is logged to daemon.log once, at warn, and the task is left
// exactly as it was. It is asked again after RetryAfter (one hour), not on
// every sweep: a refusal costs a `git status` per worktree, and nothing that
// clears the commonest one, a dirty worktree, records anything the worker
// could watch. A task that leaves the due set — archived by hand, followed up
// — is forgotten.
//
// Its archive event carries `archived_by: "finished_after"` (ArchivedBy) on
// the task.state_changed → archived payload (§13.3), as the closed-issue
// archive carries `issue_closed`. That event is not one of the issue
// worker's own, so it wakes it, exactly as a human archive does — which is
// what releases a main task the issue worker held behind side work.
//
// The switch is `archive_finished_tasks_after`, read on every sweep so a hot
// reload governs the next one; `0` turns it off. Chats are never archived.
//
// Like internal/issuearchive the package never imports internal/api; it
// depends on narrow interfaces over the store and the runner.
package finishedarchive
