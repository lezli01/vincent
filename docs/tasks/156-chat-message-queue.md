# 156 — Queue chat messages while a turn runs

**Status:** 🔄 in progress (3/5) · **Opened:** 2026-11-19 · **Issue:** #1010 (part of #1009)

A chat used to take `send` only from `idle`: a message typed while a turn ran
was refused, and the draft kept until the turn ended. This adds a durable,
daemon-owned queue of follow-up messages for every adapter. A message sent
while a turn runs — or waits on the agent's question — is queued, and runs as
the next turn once the live one ends `done`, without leaving the chat's
`max_parallel_chats` slot. Steering a message *into* the running turn is
gated on a verified claude capture (decision 4) and is not shipped.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **156.1** The state machine and the store: `send` legal from `running`
  and `awaiting_input` in all three tables, `steer` from `running`; migration
  `0059_chat_queue.sql` (`chat_queued_messages`, `chats.queue_paused`,
  `queue_pause_reason`); enqueue, list, edit, delete, clear, resume, and
  `FinishChatTurn`, which records a turn's ending and pops the next message or
  pauses the queue in one transaction; archive, close and hand-off delete the
  queue; `chat.queue_changed`. ✓ 2026-11-19
- [x] **156.2** The runner, API, client and CLI: `chatrun.Runner.Submit`
  queues on a live chat, one actor goroutine runs every turn its `done`
  endings pop, recovery pauses the queue; `GET /v1/chats/{id}/queue` and its
  `PATCH`, `DELETE`, `resume` and `clear` routes (off the MCP surface); the
  chat DTO's `queued_count`, `queue_paused`, `queue_pause_reason`, `can_steer`
  and `steer_reason`; `vincent chat send` queues and says so, and `vincent
  chat queue list|edit|rm|resume|clear`. ✓ 2026-11-19
- [x] **156.3** The TUI: `enter` queues on a live chat, queued messages as
  faded `You` cards, `↑` to edit the newest (`ctrl+d` deletes), the paused
  card with `alt+r` resume and `alt+c` clear, and `ctrl+s` as the rebindable
  `steer` operation that names why it is unavailable. Spec §5.5, §9.x, §11,
  §12.4, §13.2, §13.3, §14 and §15 amended; API, CLI, TUI and configuration
  docs. ✓ 2026-11-19
- [ ] **156.4** Re-capture the chat screenshot with `scripts/screenshots.sh`,
  seeding a chat with a queued message and one with a paused queue (new tapes
  at the end of `do_capture`), and extend the `m14` gate with a queue
  scenario: send twice while a turn runs, see both turns finish in order.
- [ ] **156.5** The steering capture (decision 4): claude 2.1.x stream-json
  fixtures writing a second `user` line mid-tool-use and mid-text. If claude
  folds it into the live turn, steering ships on claude in input mode as a
  follow-up issue; if not, it stays unavailable.

## Decisions

Confirmed by the author on 2026-11-19 (the brief for issue #1010).

1. **The slot is handed over; task 063 decision 1 stands unamended.** A `send`
   to an idle chat over `max_parallel_chats` is still `409 chat_cap_reached`.
   A queued message exists only behind a live turn, and a `done` ending pops
   the next one straight into a new turn without the chat leaving its slot —
   `running → running` — so no other chat can take it in between and a chat
   never waits on the cap. Nothing goes through `internal/scheduler`.
2. **`awaiting_input`: queued messages wait behind the question.** A message
   sent there queues; it never answers the request. `answer` stays the
   deliberate action.
3. **Failed, interrupted and cancelled turns pause the queue.** Only a `done`
   turn drains. The pause is a durable chat column set in the transaction that
   records the ending; the chat is still `idle` and usable, and the queue waits
   for `resume` or `clear`.
4. **Steering is gated on a verified capture.** It ships only if claude 2.1.x
   folds a second stream-json `user` line into the running turn before that
   turn's `result`, as an optional adapter capability found by type assertion.
   Until then the steer key is unavailable on every adapter, with the reason,
   and is never emulated by cancel-and-resend. No capture was taken in this
   piece of work (156.5), so steering is not shipped.
5. **Keys.** `enter` queues while a turn runs; `ctrl+s` is the rebindable
   `steer` operation, with a recorded keymap exception beside the forms' fixed
   `ctrl+s`; `↑` on an empty composer edits the newest queued message.
6. **Terminal transitions discard the queue.** `archive`, `hand_off` and
   `close` stay legal from `idle` only and delete the chat's queued messages
   in the same transaction; cancelling a task with a linked chat closes it the
   same way.
