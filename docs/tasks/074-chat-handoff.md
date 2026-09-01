# 074 — Hand off a chat's worktree and branch to a task

**Status:** ✅ done (1/1)
**Issue:** #288
**Follows:** [063](063-free-chat.md) (free chat), and is a sibling of
[064](064-task-from-pull-request.md) — the existing precedent for "a task whose
workspace was not cut the ordinary way"

Chats ended only by being archived. A chat could explore a change for as long
as it took, in its own worktree and on its own branch, and there was no
supported way to turn that exploration into a structured task without
abandoning the workspace or coordinating the branch by hand.

An idle chat now has one more human action, `hand_off`. It creates a task in
the chat's project that adopts the chat's worktree path, branch, base branch
and base SHA verbatim, links the two records, and moves the chat to a second
terminal state, `handed_off`.

## Why it needed no new machinery

Three facts made this cheap rather than invasive, and the design is built on
them:

- `internal/taskrun/engine.go:ensureWorktree` opens with
  `if task.WorktreePath != "" { return nil }`. A task created with its worktree
  already set adopts it and runs no git at admission, so there is **no third
  worktree-creation mode** — unlike 064, which had to add `CreatePullAndClaim`.
- `worktree.Manager.Path` has no non-test callers, so nothing derives a task's
  directory from its id. The task can live in `{root}/chat-7` forever, which is
  what makes "no worktree rename" free rather than a fight with
  `worktree.Owner` (063 decision 9), whose directory name that decision already
  calls informational — the claim decides, not the name.
- `internal/taskrun/reclaim.go:claimSets` claims worktrees **by path**, so the
  task's claim covers the inherited directory with no change to gc at all. Only
  the transcript half is name-keyed, and it stays keyed to the chat.

## Decisions

1. **Handoff is a chats-family route: `POST /v1/chats/{id}/handoff`**, taking
   `POST /v1/tasks`' body. A `source_chat_id` field on `POST /v1/tasks` — 064's
   shape for `github_pull` — was rejected: that route **is** the `task_create`
   MCP tool, and 063 decision 2 excluded the whole chat family from MCP
   precisely so an agent cannot start agent processes outside the
   `created_by_task_id` chain `mcp.max_depth`/`mcp.max_tasks` walk. A field
   would need a field-level MCP guard, a shape the exclusion list does not
   have. 063 decision 2 is therefore **extended, not excepted**: the route
   joins the literal list in `internal/mcp/tools.go`. The cost is that
   task-create validation is shared rather than duplicated — see Risks.
2. **The authoritative foreign key is `chats.handoff_task_id`**; a task's
   `source_chat_id` is the reverse lookup. A stored `tasks.source_chat_id` was
   rejected: 063's posture is that no existing task query changes meaning when
   chats exist, and the chat is the entity whose lifecycle this changes. The
   reverse direction is one indexed query per list call, built into a map,
   never one per rendered task.
3. **Conversational context reaches the workflow through `tasks.description`.**
   No `handoff_note` column and no new §8.4 template key. The handoff form asks
   the human for the objective the way the new-task form does, so the issue's
   "not injected wholesale into workflow prompts" holds by there being nothing
   to inject.
4. **Handoff refuses a worktree with a git operation in progress**, with a
   typed 409 naming which. `worktree.InMerge` generalised to
   `worktree.InProgressOp`, covering `MERGE_HEAD`, `rebase-merge/`,
   `rebase-apply/`, `CHERRY_PICK_HEAD`, `REVERT_HEAD` and `BISECT_LOG`, reusing
   `merge.go`'s resolution of a linked worktree's real git dir. New reason
   `ReasonRepoOperationInProgress`. Gating on merge alone was rejected: a
   half-finished rebase would be inherited silently and surface later as an
   unexplained `git_error` inside a step. **Ordinary dirty state is not a
   refusal** — the acceptance criterion is met by the worktree not being
   touched.
5. **`handed_off` is a second terminal chat state**, and §5.5's "archived is
   the only terminal state" is amended. Reusing `archived` was rejected on
   mechanism: `handleChatArchive` removes the worktree and may delete the
   branch, exactly the state transfer moves. The chat's `worktree_path` is
   cleared in the handoff transaction — that is what transferring the claim
   means — while `branch`, `base_branch` and `base_sha` stay as history.
   `archive` is not legal from `handed_off`, so "archiving a handed-off chat
   must never remove task-owned workspace state" is satisfied structurally
   rather than by a guard.
6. **Transcript retention treats `handed_off` as terminal.**
   `ArchivedChatIDsBefore` widened to both states and renamed
   `TerminalChatIDsBefore`. It already measures from `updated_at`, and the
   handoff transition is the last write the chat row takes, so its existing
   justification transfers verbatim and no `archived_at` column is needed. The
   chat's transcripts live under `{transcripts}/chat-{id}`, which the task
   never claims, so pruning cannot reach task-owned state.
7. **The branch-name chain is not touched.** 064 grew it with a `pull` level
   because a PR task is created through `POST /v1/tasks`, whose form previews
   the branch through `/v1/resolve`. A handoff has its own route and its own
   read-only field, so the branch is passed verbatim and `ResolveBranchName`
   gains nothing. `claimBranchTx` still runs on the inherited name inside the
   transaction, so a live task already holding that branch collides here
   exactly as any other create does.
8. **§7.3's fresh-session rule is untouched** and the chat's `session_id` is
   not transferred. §5.5's chat-only amendment stays chat-only.

**§17 needed no amendment**: its aggregates are over `step_runs`, whose
`task_id` stays `NOT NULL`, and a handed-off task's step runs are ordinary
ones. **§11 needed none either**: `handed_off` holds no process, so the
`max_parallel_chats` tally is unchanged.

## Risks named before the pull request

The one real cost of decision 1 is that `handleTaskCreate`'s validation —
workflow resolution and snapshot, include expansion, fan-out resolution, the
GitHub prefills, declared fields, the base branch, the agent/model/effort
override, MCP provenance and the four capability gates — had to be **factored
out and shared**, not copied. A second copy would drift, and the drift would
show up as a task the handoff route accepts and the create route rejects. That
extraction (`prepareTaskCreate`) is the largest part of the diff and landed as
its own commit ahead of the feature, so a regression in it is bisectable
separately.

`chatstate`'s `TestArchivedIsTheOnlyTerminal` was not patched quietly — it was
063's lifecycle shape written down. It was rewritten as
`TestTheTwoTerminalStates`, with the amendment cited, in the same commit as the
spec edit.
