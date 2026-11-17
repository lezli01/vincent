# 149 — A global pause: pause all work, continue all work

**Status:** 🔄 in progress (3/4) · **Opened:** 2026-11-17

The author asked for one command that pauses everything and one that continues
everything, so maintenance — upgrading vincent, rebooting, rotating a token,
taking a backup with nothing in flight — does not mean pausing tasks one by
one, and for the TUI to show clearly whether the system is active, pausing or
paused.

Conventions for this file are in [the tasks README](README.md).

## What this changes

- **§6** gains no state and no action. The global pause is built out of §6's
  own soft `pause` (finish the step, then park) and `resume`; what it adds is
  a daemon-wide admission hold and a per-task mark saying which pauses were
  its own.
- **§11** gains a third thing that keeps a queued task waiting, beside the caps
  and the usage hold (task 003).
- **§13.2** gains `GET`/`POST /v1/daemon/pause` and `POST /v1/daemon/continue`;
  **§13.3** gains `daemon.pause_changed`; **§13.4** excludes the two writes.
- **§12.1** gains `vincent daemon pause` and `vincent daemon continue`.
- **§15** gains the global key `Z`, a header badge and a status line.

## Decisions

### 1. Three displayed states, one stored fact

*2026-11-17.* Only "a pause is recorded" is stored (the `global_pause`
singleton, migration 0057). `pausing` and `paused` are that one fact told
apart by whether any task is still `running` — still executing a step that
will park at its boundary. Storing `pausing` would need a writer to notice the
last task parking; deriving it cannot go stale.

### 2. It is §6's soft pause, never a kill

*2026-11-17.* A running task finishes the step it is on and parks as `paused`,
exactly as `vincent task pause` would. Cutting a step short would turn a
maintenance pause into lost agent work and a retry. A human who needs work
stopped *now* still has `cancel`, and `daemon stop` re-queues in-flight steps
(§12.4).

### 3. The engine reads the pause at every step boundary

*2026-11-17.* The scheduler holds the queue, and taking the pause marks every
running task `pause_requested` so clients see "pausing" on each — but the mark
is not what makes a task stop. `runSteps` reads the pause itself at each
boundary, so a task admitted in the instant before the pause committed parks
too, and no in-memory mirror has to be kept in step with the database.

### 4. Queued tasks stay queued

*2026-11-17.* The pause is an admission hold, not a mass transition: a queued
task is simply not admitted. Moving every queued task to `paused` would write
a transition per task on pause and again on continue, and would make a task
created during the pause (by a human, a trigger or a handoff) need special
handling. Held tasks keep their place in §11's order.

### 5. `continue` resumes exactly the tasks the pause parked

*2026-11-17.* `tasks.global_pause` marks a pause — pending or taken — that the
global pause made. `continue` resumes those through §6's `resume`, so each gets
its own transition and event, and withdraws the requests that had not taken
effect yet. A task a human paused by hand, before the pause or during it,
stays paused: lifting a global pause is not a human resuming every task. Any
human action on a task clears its mark, the way every human action but
`pause` clears `pause_requested`, and a human `pause` on a marked task takes
the pause over.

### 6. Tasks waiting on a human are left alone

*2026-11-17.* `awaiting_gate`, `blocked` and `awaiting_children` were not going
to move without a human. `awaiting_input` holds a live agent idle on a
question; the pause does not wait on it (it would wait for as long as the
human takes) and reports it separately, and the task parks at the step
boundary after the answer.

### 7. Chats are not paused

*2026-11-17.* A chat turn is a foreground conversation a human is watching
(§5.5), not unattended work. Triggers keep firing and their tasks queue behind
the hold — that is the point of a hold rather than a stop.

### 8. Durable, idempotent, reasoned

*2026-11-17.* The pause survives a daemon restart: the most common maintenance
is restarting the daemon, which must come back paused rather than admit the
whole queue on its first walk. Pausing a paused daemon keeps the original time
and optional one-line reason; continuing an active one resumes nothing.

### 9. Not an MCP tool, except the read

*2026-11-17.* `POST /v1/daemon/pause` and `/continue` join `daemon/stop` in
§13.4's exclusion: an agent must not halt, or restart, every piece of work the
daemon supervising it runs — its own step included. `GET /v1/daemon/pause` is
the tool `daemon_pause_status`.

### 10. The TUI draws it only while it is news, and asks first

*2026-11-17.* The header badge (`⏸ pausing (N finishing)`, `⏸ PAUSED`) and the
status line follow task 129.15's rule for the connection badge: drawn only
while not active, never shed. `active` is shown on the daemon view, which
answers "is this daemon working?". `Z` is global and asks y/n on the status
line before it acts, since it changes what every project is doing.

## Tasks

- [x] **149.1** Store, engine and scheduler: migration 0057, the hold, the
  boundary park, the mark, `PauseAll`/`ContinueAll`.
- [x] **149.2** API, client, MCP exclusion, `vincent daemon pause` /
  `continue`, and the pause line in `vincent daemon status`.
- [x] **149.3** TUI: `Z`, the header badge, the status line, the daemon view's
  `work` row.
- [ ] **149.4** Screenshots: a capture of the paused header in
  `scripts/screenshots.sh`.
