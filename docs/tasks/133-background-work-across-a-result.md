# 133 — Background work across a result: keep the claude run open until it finishes

**Status:** ✅ done (2/2)
**Opened:** 2026-10-06
**Spec:** §9.2 ("Background work across a result"), §12.3 (`defaults.background_wait`)

## Problem

A chat sometimes stopped on an agent message like "the full test suite is
running in the background; once it finishes I'll seed the screenshot tree".
The turn read `done`, and the human had to type "continue" to get the rest.

The cause was in the claude adapter. In input mode, `readLoop` closed stdin at
the first `result` line ("single-turn semantics"). The CLI then exited and
killed every background task it was running. Claude Code's tools tell the model
it will be woken when background work finishes: Bash's `run_in_background`, a
backgrounded `Agent`, `Monitor`. So the model ended its turn deliberately,
expecting a wake-up that the exit made impossible. When the turn resumed, the
CLI's own transcript said so: `"Background shell command didn't finish before
the previous session ended"`.

Measured on one installation's history on 2026-10-06: 7 chat turns had
background work killed after their last `result`, and 5 of them are this
report (chats 30 turns 3–5, 53 turns 1–2). Steps hit it too: 49 of 6,843 step
runs, mostly "Run full test suite" and "Watch PR checks until done".
Their step outcome was the agent's "waiting on it" message.

## What the CLI does

Captured against claude 2.1.289 (`internal/agent/claude/testdata/
stream_background_2.1.289.jsonl`), in `-p --input-format stream-json` mode
with stdin held open:

- `task_started` (with `task_id`, `task_type: local_bash`, `is_backgrounded`)
  for each background command, then the turn's `result`.
- When a task finishes: `task_updated` (`patch.status: completed`),
  `task_notification` (`status: completed`), a fresh `system/init`, a woken
  model turn, and a **second** `result`.
- When stdin closes with a task still running: `task_updated`
  (`patch.status: killed`), `task_notification` (`status: stopped`), and
  exit 0.
- `usage` on each `result` covers that model turn alone. `total_cost_usd`
  and `modelUsage` are the process's running totals.

## Decisions

1. **Keep stdin open while background work is out.** The read loop tracks
   `system` task lines by `task_id`. At a `result` it closes stdin only when no
   started task is still open. *Beat:* disallowing the background tools
   (`--disallowedTools`). The model would run the same work in the foreground,
   into Bash's own time cap, and lose `Monitor` and parallel subagents. *Beat:*
   telling the model in a preamble not to background work. That is advice the
   model can ignore, and it doesn't help the step runs, which have no preamble.
2. **Track every task line, not just shells.** Shells, subagents and monitors
   announce themselves the same way. A synchronous subagent's entry opens and
   closes before its turn's `result`, so it never holds a run open.
3. **Bounded by a new `defaults.background_wait`, default 30m, `0` to opt
   out.** The window restarts at each `result`, and pauses while the main loop
   writes output or tool calls. A background subagent's own lines are the work
   being waited on, so they don't pause it. *Beat:* bounding by
   `agent_timeout` alone. A dev server the agent left up would then hold a chat
   for the whole hour and end as a `timeout` failure. *Beat:* a constant, which
   leaves no opt-out for anyone who prefers the old behavior. 30m covers this
   repository's full suite and a CI watch, which are the cases measured.
4. **A lapsed window is a success on the last answer.** The agent did answer.
   Stdin closes, the CLI stops what is left and exits 0, and the transcript
   keeps the `stopped` notification. *Beat:* a new failure reason. That would
   fail a turn whose agent did everything asked of it except stop a server.
5. **Tokens summed, cost from the last `result`.** This matches what each
   field means on the wire (above). Taking the last `result`'s tokens would
   undercount a held run.
6. **Steps get it too.** The 49 step runs are the same bug. A step's result is
   whatever its agent said last, and "waiting on the suite" is not a result.
   `taskrun` passes the same config value.
7. **claude only.** codex and cursor have no background work in their
   headless modes. `RunSpec.BackgroundWait` is documented as ignorable, like
   `OnInput`.

## Open questions

- `ScheduleWakeup` and `CronCreate` are offered to a vincent run, and they
  schedule a wake-up minutes to hours away. Holding a process for those is not
  what this wait is for, and none of the 56 hits used them. If one shows up,
  disallow them for vincent runs rather than stretching the window.
- While a run is held, a chat turn shows `running` with the agent's last
  message already on screen. A client could say "waiting on background work"
  instead. That would need a normalized event for the hold, which nothing
  emits yet.

## Tasks

- [x] **133.1** The adapter: `background.go`'s tracker, `endTurn`'s hold in
  `readLoop`, summed tokens in `Wait`, `RunSpec.BackgroundWait`, the 2.1.289
  capture, and the fake agent's `background` scenario (stdin-sensitive, like
  the real CLI). ✓ 2026-10-06
- [x] **133.2** `defaults.background_wait` (config, bootstrap, API, client,
  CLI `config` keys, TUI config block), passed by `chatrun` and `taskrun`, with
  turn- and step-level tests, the spec's §9.2 and §12.3 amendments, and the
  configuration reference. ✓ 2026-10-06
