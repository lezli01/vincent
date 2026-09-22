# 119 — Chat on a stopped task: a conversation in the task's own worktree

**Status:** ✅ done (10/10)
**Issue:** [#472](https://github.com/lezli01/vincent/issues/472)
**Renumbered:** opened as 115; 115, 117 and 118 reached `master` first
([115](115-scheduled-daemon-backups.md), [117](117-single-task-import.md),
[118](118-user-configurable-tui-keymap.md)), so this record, its task IDs and
every citation of them are 119.
**Spec:** amends §5.5, §6, §7.3, §9.3, §10, §11, §12.4, §13.2, §13.3, §13.4, §14,
§15, §16, §17, §18
**Builds on, without relitigating:** [025](025-ad-hoc-repair-agent.md) decisions
1, 2, 4, 5 and 6; [027](027-follow-up-runs.md); [063](063-free-chat.md) decisions
1, 2, 5 and 9; [074](074-chat-handoff.md) decisions 2 and 5
**Amends deliberately:** §5.5's "a chat gets its own git worktree and its own
branch", which now holds for **free** chats only; and §5.5's and
`internal/chatstate`'s two terminal chat states, which are now three

## Problem

When a task stops for a human, the only way to get an agent to change its
worktree is a **repair** (task 025): one prompt, one agent run, no
back-and-forth, and only from `blocked`. Many fixes need a conversation: look
at the failure, try something, check the result, adjust. Before this, that
meant leaving vincent and running an agent CLI by hand in the task's worktree,
with no transcript, no cost record, and nothing stopping a `retry` from racing
that session.

Chats (task 063, §5.5) already are conversations with an agent — session
resume, mid-run input, a transcript per turn, a workspace in the TUI. But a chat
always got its **own** worktree and branch, and archiving it removed them, so a
chat could not be used on a task's worktree.

## What it is

A new §6 human action, `chat`, opens a §5.5 chat **linked to a task**. It is
offered from `blocked`, `awaiting_gate`, `done` and `aborted`, moves nothing
(a self-loop), and returns the chat in `idle`. The chat works in the task's
existing worktree and on its branch.

While the chat is open the task is **locked**: every §6 action except `cancel`
is `409 task_locked_by_chat`, and the task stays exactly as it was. Closing the
chat (`POST /v1/chats/{id}/close`) is terminal for the chat and never touches
the worktree or the branch. A later stop can open a new chat, and closed chats
stay listed on the task as its history.

## Settled decisions

These came out of questions put to the author, and are binding.

### 1. The task keeps sole ownership of the worktree claim

The chat row stores `linked_task_id`, the one authoritative foreign key (074
decision 2's shape). It copies `branch`, `base_branch` and `base_sha` as
display history, the way a `handed_off` chat keeps them, and its own
`worktree_path` is left **empty**. `internal/chatrun` resolves the task's
`worktree_path` through the store at the start of every turn. So:

- §10's "two owners resolving to one path is a collision" (063 decision 9)
  still holds, and gc's claim sets need no change: the chat claims no worktree.
- Chat-side code that removes a worktree or deletes a branch — `archive`, the
  `RemoveAndRelease` path, `DELETE ?delete_branch=true` — has nothing to act on.
- The branch copy is never trusted for a delete: `delete_branch=true` on a
  linked chat is refused `409 chat_linked_to_task`, naming the task.

*Beaten:* storing the path on both rows and having the claim sets union them.
That would amend §10's one-owner rule and need a linked-chat guard on every
chat-side removal.

### 2. Closing lands in a third terminal chat state, `closed`

`POST /v1/chats/{id}/close` is legal from `idle` only, like `archive` and
`hand_off`; the handler cancels a live turn first and waits for it to end.
`internal/chatstate` gains a **second transition table for linked chats**,
following `taskstate`'s held table (task 096):

- `idle → {send, close}`
- `running` and `awaiting_input` exactly as in the free table
- `closed` terminal

`archive` and `hand_off` are simply not in the linked table, so the refusal is
structural, as 074 decision 5 made `handed_off`'s. `Terminal` covers three
states. `TestTheTwoTerminalStates` is rewritten as `TestTheThreeTerminalStates`
with the amendment cited, not quietly patched (the 074 precedent). The
refusals name the owning task — "this chat works in task N's worktree, which
that task owns" — rather than the generic message. For `hand_off` that
replaces 074's "nothing to hand over", which the empty `worktree_path` would
otherwise produce.

*Beaten:* reusing `archived`. `chatstate` documents `archived` as "the worktree
is gone", and the TUI's "remove its worktree?" prompt would need a special case.

### 3. On a containerized task, linked-chat turns run inside that task's container

Task 062.2 already runs the task's agent steps there, and its repair with them.
A linked chat must not hand an unconfined agent a worktree the operator chose to
confine.

- `internal/chatrun` takes an injected launcher source (`Deps.Launchers`, wired
  in `daemon.Run`) and never imports `taskrun`.
- For a linked chat, that source is `taskrun.Runner.ChatLauncher`: the task's
  container launcher when its workflow runs in one, `HostLauncher` otherwise. A
  containerized task whose container is gone fails the turn rather than running
  it on the host.
- Free chats keep the host launcher, so §16's "chats still run on the host"
  stays true of them.
- Chat recovery (§12.4) adds the container-aware orphan kill for such a turn
  (`Deps.StopOrphan`, 061 decision 9's pid file, keyed `chat-{turn_id}` so it
  cannot collide with a step's).
- Resume works inside the container because `mount_agent_config` persists the
  CLI's session store across turns. With it off, the store still lives in the
  container, and the container lives as long as the task.
- The m12 gate (Linux leg) gains a scenario proving a linked-chat turn ran in
  the container.

### 4. The fan-out retry cascade (task 090) skips locked lanes

The cascade re-admits every other `blocked` descendant and leaves a lane with
an open linked chat blocked and untouched. `retried_descendants` counts only
what moved. The parent stays parked in `awaiting_children` until that lane is
retried by hand after its chat closes.

The parent's `cancel` cascade (task 014) is `cancel`, so it applies the rule
for `cancel` on a locked task (below) to every locked lane: kill the turn, close
the chat, abort the lane.

## Decisions taken without a question

These follow the issue text or an existing precedent, and are recorded so the
implementation does not reopen them.

- **The open action is a §6 human action named `chat`**, valid from the four
  states and moving nothing (a self-loop in `internal/taskstate`). It must be a
  real action so `available_actions` can gate the TUI key (§15, task 092).
  `POST /v1/tasks/{id}/chat` takes an optional `title` (defaulting to the
  task's) and optional `agent`, `model` and `effort`, and answers `201` with
  the chat in `idle`. From any other state it is `409` with `details.state`,
  like every other action.
- **Agent, model and effort resolve as 025 decision 6 does:** request > task
  override > workflow `defaults` > adapter default, with an unset agent falling
  to the first registered adapter that can resume, as `POST /v1/chats` does. An
  adapter that cannot resume is `400 agent_cannot_resume` (063 decision 3).
  Permission mode resolves from the workflow's `defaults:` with full-auto as the
  fallback, as a repair's does under 025 decision 7, and the task's `restricted`
  clamp applies on top: a task whose workflow says `restricted` does not get a
  full-auto chat.
- **A task with no worktree** — blocked on `branch_exists` or
  `base_branch_missing`, or aborted before admission — is refused
  `409 task_has_no_worktree`. The daemon never creates the task's worktree on a
  chat's behalf, because the engine owns worktree preparation.
  `available_actions` stays state-shaped under 025 decision 8, so `chat` is
  still listed. A git operation in progress is **not** a refusal (unlike 074
  decision 4): a half-finished rebase is exactly what a conversation is for.
- **The lock is one fact, checked inside the writing transaction.** A task is
  locked when a non-terminal chat has `linked_task_id` pointing at it. The
  store checks it inside the same transaction as the §6 compare-and-swap
  (`transitionTaskTx`), which every path goes through: the #127 re-apply, the
  task 090 cascade and the held actions of task 096. Actions with a side effect
  ahead of their swap — `skip`/`approve`/`reject`'s decision row, `archive`'s
  worktree removal, `retry`'s `branch_override` rename — check it before that
  side effect too (`store.RefuseLocked`), and the swap's own check is still
  what makes the refusal race-free. Opening checks, in one transaction, that the
  state is still the one read, that no open linked chat exists and that the task
  has a worktree, and then inserts the chat, so a second open is `409` and a
  racing open and retry lose exactly one side.
  - A lock refusal is `409 task_locked_by_chat` with `details.chat_id`.
  - Triggers and the MCP `task_*` tools replay through the same handlers, so
    they get the same `409`.
  - **Scope:** the §6 action vocabulary, meaning everything that can appear in
    `available_actions`. `PATCH /v1/tasks/{id}` and the pull-request routes
    (tasks 068 and 069) are not §6 actions and run nothing in the worktree, so
    they stay available. `DELETE` needs `archived`, which the lock prevents.
- **`available_actions` reflects the lock.** While a chat is open it is
  `[cancel]` where `cancel` is legal (`blocked`, `awaiting_gate`) and `[]`
  otherwise; `chat` is removed, because a second chat is refused. The task body
  gains `open_chat_id`, read backwards from `chats.linked_task_id` with one
  indexed query per list built into a map (074 decision 2). The TUI key opens
  the existing chat when `open_chat_id` is set.
- **`cancel` on a locked task** stops the chat's live turn and waits for it,
  then in **one** transaction moves the chat to `closed` and the task to
  `aborted`. That is 025 decision 2's reading of `cancel`, so no second meaning
  is invented. `taskrun` reaches `chatrun` through an injected interface
  (`ChatTurnStopper`), which the cascade path needs too, and the dependency
  stays one-way. A crash between the stop and the transaction leaves an idle
  open chat on a task that is still locked; the operator repeats the `cancel`.
- **The first turn's context is assembled by `internal/taskrun` when the chat
  is opened** (`Runner.ChatContext`) and stored on the chat row
  (`opening_context`). `chatrun` prepends it to turn 1's prompt and to no later
  one. Because the task cannot move while it is locked, a snapshot at open is
  exactly the context at the first send, and `chatrun` never reads a step
  ledger. `repair.go`'s bounded failure block (025 decision 4) is extracted and
  shared, not copied, and so is its follow-up handling (fields from the round,
  027 decision 14). What each state gets:
  - every state: title, description and fields;
  - `blocked`: that failure block, including the last 200 lines of the
    transcript and its absolute path;
  - `awaiting_gate`: the gate step's id and rendered text;
  - `done` and `aborted`: the last step run's summary, plus the abort reason for
    `aborted`.

  The operator's message is literal, never a template (025 decision 5).
- **Cost and turns stay on the chat** (§5.5's `chat_turns`). Linked-chat turns
  therefore do **not** count toward task 033's `max_task_cost_usd`. The issue's
  "Alternatives considered" accepted that trade, and §17 and the cost-cap
  reference say so in one sentence.
- **`max_parallel_chats` applies unchanged** (063 decision 1), and a linked
  chat uses `awaiting_input`/`answer` and `input_timeout` exactly as any chat.
- **Restart.** A linked-chat turn interrupted by a restart is finalized
  `interrupted` and not re-run (063 decision 5). The chat returns to `idle` and
  stays open, so the lock holds with no extra code.
- **`notify:` stays silent on chats** (063 decision 7). Opening or closing a
  linked chat moves no §6 state, so `notify.on` does not fire either.
- **Listing and history.**
  - `GET /v1/chats` gains `?task_id=`.
  - `closed` joins the terminal states `?archived=false` hides by default (079
    decision 1). The task workspace asks for `?archived=all`.
  - `TerminalChatIDsBefore` widens to `closed` for transcript retention (074
    decision 6).
  - Permanently deleting a task (from `archived`) cascades its closed linked
    chats and removes their transcripts. The FK is `ON DELETE CASCADE`: the
    chats are the task's history, unlike 092's `handed_off` case, which is
    `SET NULL`.
  - `DELETE /v1/chats/{id}` is legal from `closed`, and `delete_branch=true` on a
    linked chat is refused.
- **MCP:** both new routes join the literal exclusion list in
  `internal/mcp/tools.go` (thirty-four in all). 063 decision 2 is extended, not
  excepted, and the "tool surface = `Routes()` minus exclusions" test covers
  them.
- **Events:** every event of a linked chat carries `linked_task_id`, and there
  is a new durable `chat.closed` (§13.3). No task event is emitted for open or
  close, because the task's state does not change. Clients learn about the lock
  from `open_chat_id` on the re-fetch the chat event prompts.

## Beaten alternatives from the issue

- **Multi-turn repair** — each turn another `__repair` step run resuming the
  previous one's session. It would keep cost on the task and under task 033's
  cap, but it breaks §7.3's rule that no step resumes a session, pays one
  scheduler admission per turn, and rebuilds the conversation UI, answer flow and
  resume handling chats already have.
- **Locking the worktree only while a turn runs.** `retry` or `skip` could fire
  between turns while the conversation is still changing the worktree.
- **A combined "close and retry" action.** Not needed for a first cut; closing
  and then choosing keeps 025 decision 1's posture — the human sees the diff
  before anything re-runs.
- **One chat per task that reopens across stops.** A fresh chat per stop, with
  closed chats kept as history, was chosen instead.

## Work

- [x] **119.1 — Store and state machines**: migration
  `0032_linked_chats.sql` (`linked_task_id … ON DELETE CASCADE`,
  `opening_context`, the `(linked_task_id, state)` index); `chatstate`'s
  `closed`, `Close`, the linked table and three-state `Terminal`; `taskstate`'s
  `chat` self-loop, `Lockable` and `LockedActionsFrom`; the lock inside
  `transitionTaskTx`, `RefuseLocked`, `OpenLinkedChat`, `CloseChat`, the close
  inside `cancel`'s transaction, `OpenLinkedChatIDs`, the `task_id` filter,
  retention and delete widened to `closed`. ✓ 2026-09-17
- [x] **119.2 — Engine**: `taskrun.Runner.ChatContext` over the failure block
  extracted from `repair.go`; `ChatLauncher` and `StopChatOrphan`;
  `cancelLocked` through the injected `ChatTurnStopper`; the lock pre-check
  ahead of `skip`, `approve`, `reject` and `archive`; the retry cascade skipping
  locked lanes. ✓ 2026-09-17
- [x] **119.3 — Chat runner and wiring**: per-turn worktree resolution,
  `opening_context` on turn 1, the injected launcher source, `StopTurn` and
  `Close`, container-aware recovery, and the two-way injection in
  `daemon.Run`. ✓ 2026-09-17
- [x] **119.4 — API and MCP**: `POST /v1/tasks/{id}/chat` and
  `POST /v1/chats/{id}/close`; lock-aware `available_actions`, `open_chat_id`
  and `linked_task_id`; `GET /v1/chats?task_id=`; the linked refusals on
  archive, hand-off and `DELETE ?delete_branch=true`; `task_locked_by_chat`,
  `task_has_no_worktree` and `chat_linked_to_task`; the `branch_override`
  pre-check; both routes on the MCP exclusion list. ✓ 2026-09-17
- [x] **119.5 — Client library and CLI**: `apiclient` wire types, `ActionChat`,
  `OpenTaskChat`, `CloseChat`; `vincent task chat TASK_ID [--title] [--agent]
  [--model] [--effort] [--json]` and `vincent chat close CHAT_ID [--json]`, for
  parity with task 048, round-tripped against the real handlers.
- [x] **119.6 — TUI**: a task-action binding in `bindings.go` chosen against task
  093's vocabulary, offered when the daemon lists `chat` and reopening the
  chat named by `open_chat_id`; the task on the chats board; the task
  workspace's list of linked chats, closed ones included; close in the chat
  workspace, and the linked refusals for archive and hand-off. The keys are
  `T` ("talk") on a task and `ctrl+q` to close in the chat workspace. `chat`
  is a §6 action, so on rebasing onto task 118's keymap it became a `tui.keys`
  operation with `T` as its default and an exception on `T` for the triggers
  takeover's dry run; `ctrl+q` is recorded as a fixed key. Two
  rough edges remain: `esc` from a linked chat returns to the chats board
  rather than to the task it was opened from, and the archived chats board
  still offers delete-with-branch on a closed linked chat, which the daemon
  refuses with `chat_linked_to_task`.
- [x] **119.7 — m14 gate scenario**: a check-failing task blocks, a linked chat
  opens, the fake agent writes the file, `retry` while it is open is `409`,
  close, `retry` reaches `done`, the worktree survives close. Bodies in the
  sh∩pwsh intersection, assertions on here-strings rather than `grep -q`.
- [x] **119.8 — m12 gate scenario (Linux leg)**: a linked-chat turn on a
  containerized task runs inside the task's container, and a free chat still
  runs on the host. `scripts/m12-gate.sh`'s scenario 10, landed in `0804bb5e`.
  It cannot be walked on a macOS host — `docker info` fails there and the gate
  skips itself with exit 0 — so the evidence is the Linux leg of CI, read
  rather than re-run: run
  [35728680712](https://github.com/lezli01/vincent/actions/runs/35728680712)
  on `master` (head `330bbccd`, 2026-09-22T12:41Z, conclusion `success`),
  job 106748541596 — `gates (ubuntu-latest)` — step `M12 gate (containers)`,
  which logged `== scenario 10: a chat opened on a containerized task runs its
  turn in that task's container` at 12:45:52, `ok: linked turn ran inside the
  task's container, free turn on the host` at 12:46:00, and
  `GATE PASS: m12 (container step execution)` at 12:46:03. The scenario ran
  there; it was not skipped. ✓ 2026-09-22
- [x] **119.9 — Documentation**: the spec amendments above, dated; this
  document and its index row; `docs/reference/api.md`, `files.md`,
  `task-lifecycle.md` and `configuration.md`; `docs/features.md`;
  `docs/guides/agents.md`; `CHANGELOG.md`. The CLI reference and the TUI guide's
  keys and chats sections land with 119.5 and 119.6.
- [x] **119.10 — Screenshots**: re-run `scripts/screenshots.sh` for the chats
  board and the task workspace once 119.6 has changed them. The seed had no
  linked chat in it at all, so a bare re-run would have photographed the same
  frames again: it now opens **two chats on `$T_GATE`** (`awaiting_gate`, one
  of the four states `chat` is offered from) with `{"agent":"cursor"}`, last
  in the chats phase so every existing chat id is unmoved, and **closes
  both** — an open linked chat locks its task to `cancel` alone, which would
  have stripped the action keys from the footers of `tui-task-steps`,
  `tui-task-workflow` and `tui-task-step-details`, three committed frames of
  this same task that are not being re-captured. Closing is terminal (§5.5),
  which moved the board half of the deliverable: the **live** chats board
  excludes terminal chats (`ArchivedExclude`), so the `task #N · ` prefix is
  photographed on the **archived** chats board, not `tui-chats.png`. Run on
  macOS 2026-09-22 with a vhs **0.11.0** binary first on `PATH` (the 0.12.0
  brew build renders nothing): `screenshots.sh seed`, then
  `VINCENT_SHOTS_ONLY` for `tui-task-details`, `tui-chats` and
  `tui-archived-chats`, then `clean`. Two PNGs written and committed —
  `docs/assets/tui-task-details.png`, now on the **Chats** section (the tape's
  `Down 1` became `Down 3`: the pane renders one section at a time and `Chats`
  sits between `Execution` and `Fields`), showing `#8` and `#7` `closed`
  `cursor` newest-first with the action keys still in the footer, which is the
  proof the task is not locked; and `docs/assets/tui-archived-chats.png`,
  four rows in two project groups, the two closed ones reading
  `task #1 · ` ahead of their titles. `git status` after the run showed those
  two and nothing else under `docs/assets/`. Accepted drift and gaps:
  `tui-chats.png` was captured, differed only in its clock and relative-time
  cells, and was **reverted to stay byte-identical** — the live board cannot
  show a closed linked chat, and the seed's four live chats are unchanged; and
  because neither chat is left open, the `chat #N holds this task` hint
  (`taskchat.go:187`) and the locked `c`-only action bar go unphotographed.
  `docs/guides/tui.md` carries both refreshed pictures with alt text that
  matches what they now show, and its linked-chat section says which board a
  closed chat is listed on. ✓ 2026-09-22

## Open questions

- **A codex chat on a restricted task — closed during implementation.** The
  permission mode resolves as `restricted`, but `codex exec resume` has no
  `--sandbox` (§9.3, task 072 decision 1), so every later turn would have run
  full-auto. Task 072 called that combination unreachable because a free chat
  is always full-auto; a linked chat makes it reachable. It fails closed at both
  ends: the codex adapter reports `SupportsRestrictedResume() == false` and
  `Start` returns `agent.ErrRestrictedUnsupported` for a resumed restricted run, and
  `POST /v1/tasks/{id}/chat` refuses such an adapter with a 400 before
  anything is written.
