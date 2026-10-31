# Task 140 gate — a chat on an issue

**Acceptance (task 140.11):** a chat opened on an issue holds the issue's
main worktree as a main task does — it cuts the issue's main branch when
there is none, moves the issue to `in_progress`, keeps a second chat and a
running main task out, makes a main task created behind it wait — and hands
the directory on: by close to the queued main task, uncommitted work
included, and by handoff into the issue's next main task. In the TUI
(140.9), `T` on either issue screen opens that chat, or reopens the one
holding the worktree.

## Scripted: `scripts/140-gate.sh`

One chain over curl against `cmd/fakeagent` configured as `claude`, run in
`ci.yml`'s `gate-group` on Linux, macOS and Windows:

| # | Step | Asserts |
|---|---|---|
| 1 | open | `POST /v1/chats` with `issue_id` cuts a `vincent/…` branch that becomes the issue's main branch; the issue reads `in_progress`, `main_worktree.occupant_chat_id` the chat |
| 2 | chat | a turn leaves an uncommitted edit to `notes.txt` in the chat's worktree; a second chat is `409 issue_has_live_chat` with `details.chat_id` |
| 3 | queued main task | its 201 names the chat in `main_worktree_occupant_chat_id`, its branch is the chat's, and it stays `queued` |
| 4 | close | the chat is `closed` with `end_sha` the branch tip |
| 5 | admission | the task runs in the chat's directory and its `git commit -a` commits the chat's edit; a chat meanwhile is `409 issue_worktree_occupied` |
| 6 | handoff | a new chat takes the same branch and directory, its handoff with no `issue_id` makes a `main` task of the issue there, the chat is `handed_off`, and the task commits the chat's second edit |

The TUI half is asserted in Go against the real handlers:
`TestIssueChatKeyAgainstTheRealAPI` (`internal/tui`) drives `T`, the occupant
row and section, `enter` on the section, the chat-event refresh and the stale
row's 409; `TestIssueChatOverTheWire` (`internal/apiclient`) the wire fields
and the `IssueHasLiveChat` decoder.

## Walkthrough: the TUI

Judged by eye, for the reason [134's board](134-issues-board.md) is: whether
the screens read.

| # | Do | Expect |
|---|---|---|
| 1 | `:issues`, select an open issue with no task, `T` | The chat workspace opens, its header reading `on issue #N`; the footer offers both `ctrl+q close` and `ctrl+t` |
| 2 | `esc`, `:issues` | The issue sits in `in progress`, its row ending `● chat #C` |
| 3 | `enter` on it | The Main worktree section names the branch and `● chat #C`; `enter` on the section reopens the chat, and so does `T` |
| 4 | Back on the issue, `a` | The new-task form preselects `separate`, naming `main worktree held by chat #C` |
| 5 | `esc`, open the chat, `ctrl+q`, `y` | The note reads `chat closed — issue #N's main worktree is free for its next main task`; the issue's row drops `chat #C` with no keypress |

## Runs

| Date | Where | Result | By |
|---|---|---|---|
| 2026-10-31 | `scripts/140-gate.sh` on macOS (darwin/arm64) | PASS, all six steps | task 140.11 |
| — | TUI walkthrough | not yet walked | |
