# 140 — A chat on an issue, in the issue's main worktree

**Status:** 🔄 in progress (8/11) · **Opened:** 2026-10-31

Issue [#885](https://github.com/lezli01/vincent/issues/885). A person who
wants to talk an issue through with an agent today opens a free chat, which
cuts a branch of its own off the issue's line, and then hands off with
`--issue`, which makes a side task once the issue has a main branch. This
work lets a chat be opened **on the issue**: it holds the issue's main
worktree as a main task does, moves the issue to `in_progress`, and hands
off into a main task on the same worktree.

It amends task 134's statement that "chats carry no `issue_id`" (134's lane
section), explicitly, in that document and in spec §5.5 and §5.6.

Conventions for this file are in [the tasks README](README.md).

## Decisions (2026-10-31)

1. **Worktree and branch at open.** An issue with no main branch: the chat
   cuts `vincent/{id}-{slug}` in its create, and that becomes the issue's
   main branch. An issue with one: the chat takes the main branch — a queued
   main task's already-resolved name counts — and receives the directory by
   134.12's transfer (the predecessor's `end_sha` stamped in the same
   transaction), or revives it from the branch. `branch_name` beside
   `issue_id` may only restate the main branch (`400`).
2. **One holder at a time.** A non-terminal issue chat occupies the main
   worktree beside `issueMainOccupantSQL`'s main tasks
   (`issueChatOccupantSQL`, read by the issue row, the creation hint and the
   scheduler). An issue chat beside an occupying main task is
   `409 issue_worktree_occupied`; a second live chat is
   `409 issue_has_live_chat`, checked in the create transaction. A main task
   created while the chat holds the worktree, a merge-back included, waits
   `queued`; the issue chat's own directory is not a §10 working-directory
   claimant against the issue's main tasks. Side tasks are unchanged.
3. **Release.** `chatstate` gains an issue-chat table: the free table plus
   `close` from `idle`. `close` and `archive` both release and stamp the
   branch tip as the new `chats.end_sha`; `handoff` moves the worktree as
   today. The directory stays in place for the next holder, which receives
   it by transfer, uncommitted work included.
4. **The branch stays the issue's main branch.** The main-branch view is a
   main task's branch, else any not-handed-off issue chat's.
   `delete_branch=true` on an issue chat is `409 issue_chat_branch`.
   Archive removes the directory only when no main task is already waiting
   for it; the branch keeps the work and the next holder revives it
   (`IssueMainBranchHeld` counts an issue chat's branch).
5. **Lane.** `LaneOf` gains `chatLatest`: a not-handed-off issue chat
   created after the latest finished main-line root task finished, or with
   none finished, makes the lane `in_progress` ahead of a ready delivery.
   `active` includes a live issue chat, so `active ⇔ in_progress` holds for
   one, and a closed or archived chat reads as the `idle` marker without a
   marker change. `attention` includes an issue chat in `awaiting_input`.
6. **Seeded context.** The first turn carries the issue's title, body
   (bounded to 16 KiB) and its latest 30 comments (each bounded to 4 KiB),
   mirrored ones included, through the linked chat's `opening_context`.
7. **Handoff** defaults `issue_id` to the chat's and is always a main task;
   another issue or `merge_back` is a `400`.
8. **MCP stays off** (task 063 decision 2): `POST /v1/chats` is not an MCP
   tool; the issue row's `occupant_chat_id` is visible through the read-only
   issue tools.
9. **Closed issues** take a chat with a `warnings` entry; the lane stays
   `done`.
10. **Issue delete** is `409 issue_has_live_chat` while an issue chat is
    live; otherwise `chats.issue_id` goes `NULL` (`ON DELETE SET NULL`).

## Items

- [x] **140.1** Migration `0050_chat_issue.sql` (`chats.issue_id`,
  `chats.end_sha`), `store.Chat.IssueID`/`EndSHA`, `CreateIssueChat` with its
  in-transaction refusals and branch binding.
- [x] **140.2** `chatstate.Kind` and the issue-chat transition table.
- [x] **140.3** Occupancy, main-branch view, lane, `active`, `attention`
  SQL and `LaneOf`'s fourth fact.
- [x] **140.4** Admission: the chat as occupant, the transfer from a
  released chat (`TransferIssueWorktreeFromChat`, taskrun).
- [x] **140.5** API: `issue_id` on `POST /v1/chats`, chat `issue_id` and
  `end_sha`, issue `occupant_chat_id`, the three 409 codes, close, archive,
  delete and handoff rules, issue delete refusal.
- [x] **140.6** Opening context.
- [x] **140.7** CLI `vincent chat start --issue`; apiclient wire fields.
- [x] **140.8** Spec §5.5/§5.6, API and CLI references, CHANGELOG, the
  task 134 amendment.
- [ ] **140.9** TUI: a key on the issues list and issue view that opens a
  chat on the selected issue (`internal/keymap`, `internal/tui/bindings.go`,
  §15), and the card/view showing a chat occupant.
- [ ] **140.10** `*_live_test.go` coverage of the new wire fields through
  apiclient and the TUI.
- [ ] **140.11** A gate (`scripts/140-gate.sh` or an extension of
  `134-gate.sh`): open → chat → in_progress → queued main task → close →
  admission → handoff.
