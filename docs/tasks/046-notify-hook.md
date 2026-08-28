# 046 — A notify hook: letting the daemon signal a human outside the TUI

**Status:** ✅ done (6/6)
**Issue:** [#90](https://github.com/lezli01/vincent/issues/90)
**Spec:** amends §2, §3 row 22, §12.3, §13.3, §15, §16, §17, §20

## Problem

vincent's premise is that you start work and walk away. `awaiting_gate`,
`awaiting_input` and `blocked` are first-class states (§6, §7.4) precisely
because the system stops and waits for a person. It stopped and waited
**silently** unless someone had a TUI open.

The only alert in the tree was the terminal bell in the TUI board
(`internal/tui/board.go`, `ringsFor`), which rings on a transition **into
`awaiting_input`** and nothing else. Nothing rang for `blocked`, nothing for
`awaiting_gate`, nothing for `done`. There was no webhook, no exec hook, no file
drop — no mechanism of any kind that survived the TUI being closed. A task could
sit in `awaiting_input` for the full 24-hour `input_timeout`, fail on expiry, and
the first anyone knew was the next time they opened the board.

That is a hole in the core loop rather than a missing nicety: §2's third goal is
a daemon that runs with **zero clients attached**, and the one thing it could not
do with zero clients attached was say it needed a human.

## What shipped

One new top-level `config.yaml` block, global and hot-reloading, and one new
package that subscribes to the existing post-commit event fan-out:

```yaml
notify:
  on: [blocked, awaiting_gate, awaiting_input, done]
  command: ["/usr/local/bin/notify-me"]   # the envelope arrives on stdin
```

`internal/notify` is a broker subscriber (`broker.OnEvent`) that reads the `to`
field of `task.state_changed`, and — when it names a listed state and a command
is configured — enqueues. Workers do the database reads, assemble an enriched
JSON envelope and spawn the command through `procx.Start`, feeding the envelope
to its stdin. Off by default; the generated `config.yaml` documents it commented
out.

## Tasks

- [x] **046.1** `config.Notify` — the block, its validation against §6's state
  vocabulary, and the two `Warnings()` entries. ✓ 2026-08-28
- [x] **046.2** `internal/notify` — the subscriber, the bounded queue, the worker
  pool, envelope assembly and the child spawn. ✓ 2026-08-28
- [x] **046.3** Daemon wiring: construct, register on the broker, stop before
  `broker.Close()`. ✓ 2026-08-28
- [x] **046.4** Tests: config validation and warnings; the notify package against
  a real child process (a re-exec of the test binary); a live test over a real
  store, a real broker and the daemon's own wiring. ✓ 2026-08-28
- [x] **046.5** Gate: a notify leg on `scripts/m1-gate.sh`, asserting one fire
  per matching transition against a daemon with **no client attached**, and that
  editing `notify.on` changes what fires with no restart. ✓ 2026-08-28
- [x] **046.6** Spec amendments and the derived documentation pages. ✓ 2026-08-28

## Decisions

**1 (2026-08-28). Decision record row 22 and the §2/§20 deferral are amended,
not worked around.** Row 22 fixed agent input requests as "TUI-level alerts only
(§6, §7.4, §13.2, §15)"; §2 lists OS desktop notifications as a non-goal and §20
calls them "natural M4+1".

M4's acceptance was met 2026-08-11 and v1 shipped as `v0.1.0`, so the §20 line is
a **spent scope boundary rather than a decision against** — the same reading task
035 applied when it promoted GitHub issue reading out of §20. Row 22 stays true
about what it actually decided: how one agent question is normalized, surfaced
and answered *inside a client*. It is narrowed in place to say it does not bind
the daemon against signalling outward, and it never spoke to `blocked` or
`awaiting_gate` at all — both of which have the same problem and were never
covered by it.

*Beaten:* leaving the deferral untouched and shipping nothing. That leaves the
walk-away premise unbacked for two states no recorded decision ever considered.

The **platform-native** notification stack stays deferred in §2 and §20, with the
exec hook recorded as the reason it is now cheaper to leave deferred:
`terminal-notifier`, `notify-send` and `msg` are all reachable through `command`.

**2 (2026-08-28). Only root tasks notify.** A `fan_out` lane is an ordinary task
row (§7.6, task 014 decision 1), so a twenty-lane tree reaching `done` produces
twenty child transitions on top of the parent's. A task whose `parent_task_id`
is non-null is skipped.

The parent's own `awaiting_children` → `running` → `done` is the human-meaningful
signal, a lane that blocks blocks its parent's join, and a lane finishing is
machinery.

*Beaten:* an `include_children` key. That is the same "new key for a case nobody
has hit" the issue itself rejects for per-project routing. The trigger for
revisiting is a user who wants per-lane signalling.

**3 (2026-08-28). Bounded queue and four workers — not four-in-flight-then-drop.**
The issue proposed "at most 4 in flight, everything else dropped". That loses a
notification whenever five tasks block at once, which is precisely when the
feature is supposed to earn its keep.

What shipped is a fixed-size FIFO (64) drained by at most 4 concurrent children.
Drop-and-log happens only when the **queue** is full. This keeps every property
the issue asked for — the publishing goroutine never blocks, the backlog is
bounded, nothing is persisted or replayed — and makes the ordinary burst
lossless. It also absorbs the fan-out case cleanly: lanes are enqueued and
discarded by a worker after one indexed read each.

**4 (2026-08-28). `internal/config` imports `internal/taskstate` to validate
`on:`.** This is the one internal import `config` has, and it is a deliberate
narrowing of CLAUDE.md's "leaf packages depend on nothing internal".

`taskstate` is itself a leaf — it imports only the standard library — so there is
no cycle, and it already exposes `All` and `Valid`. It gives the acceptance the
issue asked for literally: an unknown state name fails `config.Load`, so a bad
edit is rejected whole and the last good configuration stays active
(`internal/config/watch.go`).

*Beaten:* the `branch_template` precedent of validating in `internal/daemon`.
That exists because the branch-template *context* lives in `internal/worktree`, a
package with real dependencies, and that reason does not apply to a ten-element
string set. The alternative was a second copy of §6's vocabulary in `config`,
free to drift from the first.

**5 (2026-08-28). The payload is an enriched envelope, assembled by the daemon.**
A notifier handed `{task_id, to}` cannot write a message without calling back
into the API with a bearer token, which defeats a one-line shell script. One JSON
object on the child's stdin: `event_id`, `ts`, `type`; `task_id`, `title`,
`from`, `to`, `block_reason`, `queued_reason`, `current_step`, `steps_total`,
`worktree_path`, `branch`; `project_id`, `project`, `workflow`; and `input`
(`{kind, summary}`) on a transition into `awaiting_input`, taken from what that
§7.4 transition already carries in its event payload.

`steps_total` comes from the task's own `workflow_snapshot`, which is the honest
*n* for that run — more honest than the registry-derived count the TUI shows.
`internal/notify` does not import `internal/workflow` for it; the daemon injects
a `func(snapshot string) int` the way `taskrun.Deps` injects its functions.

**6 (2026-08-28). Filtering is split across the two goroutines by cost.** The
`OnEvent` callback runs on the store's writing goroutine and must not block
(`internal/events/broker.go`, `store.SetEventHook`). It does only in-memory
checks — the event type, `to` against `on:`, a configured command — and enqueues.
Every database read (task, project, snapshot) and the root-task check happen on
a worker.

**7 (2026-08-28). Delivery posture, as the issue specified it.** Fire-and-forget.
A fixed **10 s** timeout per child, not configurable — this is the pruner's
posture (`internal/taskrun/prune.go`), and a daemon that stops serving because a
notifier hung has its priorities backwards. On expiry the process **tree** is
killed via `procx.Start` / `Proc.Kill`, not just the direct child, because that
is how every other process the daemon spawns is handled; `procx.Start` also
applies `NoWindow` on Windows, for the reason `internal/agent/probe.go` applies
it — the daemon is normally console-less, and a console-subsystem child of a
console-less parent is handed a window unless its creator says otherwise.
`command` is argv, never a shell string: there is no portable shell to assume. No
replay on restart and no persisted cursor — a weekend of downtime must not
produce a notification storm.

The constant is a `Notifier` field initialised from `perChildTimeout` so tests
can watch the kill happen without spending ten real seconds; nothing outside the
package can reach it, which is what keeps it un-configurable.

**8 (2026-08-28). Children inherit the §12.3 `environment` policy.** A notifier
is a process the daemon spawns, and §12.3 governs every one of them. It gets no
`VINCENT_*` variables — those are §8.5's contract for command steps and their
checks, and the envelope on stdin is this hook's contract.

**9 (2026-08-28). `notify` is not exposed on `GET /v1/config`.**
`configResponse` (`internal/api/server.go`) is a curated DTO, no client needs
this, and `command` can reasonably carry a webhook URL with a token in its argv.
Adding a read path for that buys nothing.

**10 (2026-08-28). Two `Warnings()` entries, not validation errors.** A `command`
with an empty `on:`, and a non-empty `on:` with no `command`, both load and take
effect and both can never fire — exactly the shape `Warnings()` exists for
(`delete_remote_branch_on_archive` is its sibling). Neither refuses the file: a
user commenting `command` out for an afternoon should not have the same save
revert an unrelated `log_level` edit.

**11 (2026-08-28). The TUI bell is untouched.** It keeps its current
`awaiting_input`-only behaviour as the in-client alert. No coupling in either
direction, as the issue scoped it.

## Testing

`internal/config` covers the validation the issue asked for by name: an unknown
state fails `Load` with an error naming the offending value, every §6 state is
accepted, a duplicate and an empty argv element are refused, an absent block is
inert and moves no other default, both warnings fire on exactly their conditions,
and the generated `config.yaml` still loads to a disabled hook with no warnings.

`internal/notify`'s child is a **re-exec of the test binary**, selected with
`-test.run` and told what to do by the argv after a `--` sentinel. That is why
every case runs identically on Windows, macOS and Linux with no shell involved
and no environment variable that has to survive the environment policy: the
guard is the sentinel, which a normal `go test` invocation never carries.

Covered: argv byte-identical to the configured list and the stdin JSON decoding
to the documented envelope; an unlisted state, a foreign event type, a missing
task id and a configured `on:` with no command all spawning nothing; a
`parent_task_id` task skipped while its root parent still fires; a hung child
killed and logged; a non-zero exit logged with its code and a truncated stderr
tail and never retried; the concurrency cap and the queue drop, with `OnEvent`
staying prompt across a burst well past both.

`notify_live_test.go` drives a real SQLite store, the real broker and the
daemon's own hook wiring through a real `TransitionTask`, with no client
attached — the acceptance sentence the issue wrote.

The gate leg rides `scripts/m1-gate.sh` rather than a new script: that gate
already has a daemon, a project and tasks that finish, and every one of its legs
is already "a daemon with no client attached". The notifier is `git config -f
"$out" --add vincent.notify.fired yes` — argv only, no shell, identical on all
three platforms, and the same trick `m6`/`m7` use to write a file from a step
body. `notify.on` starts on a state nothing in the script reaches, so the legs
above must fire nothing; the leg then rewrites `config.yaml` and gets exactly one
fire for the next task's `done`, which is the hot-reload half. The timeout-kill
and queue-drop legs stay in Go, where a portable "hang forever" child is
available and `sleep` is not.

## Deliberately not included

- **A native HTTP webhook.** `command: ["curl", "-fsS", "-XPOST", ...]` reaches
  it with no request builder, retry policy, TLS trust decision or secret handling
  in the daemon. The exec hook does not block one later.
- **Per-project routing.** Projects are database rows, not YAML; it would need a
  column and API surface for a case nobody has asked for.
- **Replay of events missed while the daemon was down.** No cursor is persisted,
  by design.
- **A `vincent doctor` check that `notify.command[0]` resolves.** Genuinely
  useful and genuinely not this issue. The trigger is the first report of a
  notifier that silently never ran because the path was wrong.
