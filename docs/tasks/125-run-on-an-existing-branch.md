# 125 — Run a task or chat on an existing branch

Issue [#538](https://github.com/lezli01/vincent/issues/538). Spec §10 (worktree
management, creation modes), §5.3 (`base_sha`), §18 (block reasons), §13.2 (the
routes). Related: task 001 (configurable branch names), task 064 (a task from a
pull request), task 099 (base fast-forward in the human's checkout).

## What this is

A **third worktree-creation mode**. §10 had two: the ordinary one, which cuts
`-b {branch} --no-track` from a fetched base and refuses a pre-existing branch
(`branch_exists`), and task 064's pull-request one, which adopts a pull
request's head branch. The third adopts an *arbitrary* existing local branch,
and — where that branch is already checked out in the project's own main
checkout — runs the task in the project path itself instead of in a worktree.

It is selected by a request field, `existing_branch`, on `POST /v1/tasks` and
`POST /v1/chats`. Without it nothing changes.

## Decisions

Settled with the author on 2026-09-21, each with the alternative it beat.

1. **Adoption is explicit, not inferred.** A request field selects the mode.
   The inferred reading ("the branch exists, so adopt it") was rejected because
   it reopens task 001's binding decision: a template with no discriminator —
   `feat/{{ index .Fields "ticket" }}` — collides on the second task for the
   same input, and under inference that second task would silently run on the
   first one's branch. That is a worse version of the auto-dedupe task 001
   rejected, and it would retire `branch_exists` and `retry { branch_override }`
   together. The field also keeps §10's naming chain intact: adoption chooses a
   *mode*, it does not add a level above the per-task literal the way a pull
   request does.

2. **A claimed working directory makes the next task wait, it does not block
   it.** At most one unarchived owner — task or chat — may be working in one
   directory. A queued task whose directory is taken is skipped by
   `internal/scheduler` and reconsidered on the next walk, the way a project at
   its cap is: the condition clears on its own, and burning a `blocked` state on
   it would cost a human a retry for nothing. The claim cannot come from
   `store.ListAdmissible`'s caps SQL as a tally, because it is a path; it is a
   second predicate in the Go walk beside `ProjectCap`, fed by a count the same
   query carries. Chats are created synchronously and cannot wait, so
   `POST /v1/chats` answers **409** against the same claim set. Refusing at
   creation only was rejected for the reason task 001 made admission the
   authority for `branch_exists`: the creation check is racy, and two drafts
   confirmed together would both reach git and one would die of a bare
   `git_error`.

   The claim is held by a *slot-holding state or a recorded `worktree_path`*,
   whichever comes first. Only the second was not enough: the scheduler marks a
   task `running` before its actor creates the worktree, and a peer admitted in
   that window reached git and blocked `adopt_branch_checked_out` — found by
   `scripts/125-gate.sh` scenario 4 on its first run.

3. **The pull-request mode keeps `pull_branch_checked_out`, and §10 says why.**
   Task 064 decision 4 is not reopened. A pull-request task *fast-forwards* the
   head onto whatever holds it, so adopting the human's checkout would move
   their working tree under them; the adopt mode leaves a branch that is ahead
   exactly where it is and so has nothing to push into their tree.

4. **The diff of a main-checkout task is unchanged, and documented.**
   `GET /v1/tasks/{id}/diff` keeps computing the diff in the working directory,
   so uncommitted work the human already had at admission reads as part of the
   task's diff. Recording a dirty-at-admission baseline (a `git write-tree`
   snapshot subtracted later) was rejected as a migration, a write into the
   user's object store and an unanswered question about files both authors
   touched. Refusing the diff outright was rejected: it removes the tab exactly
   where it is most wanted.

5. **An adopted branch with no upstream gets none.** No fetch, no
   `branch.{name}.remote`, no `.merge` — the same answer §10's base fetch gives
   to "no remote at all". Falling back to `origin` like `worktree.pullRemote`
   does was rejected: it writes remote-tracking configuration into the user's
   repository for a branch they deliberately kept local, and a later push would
   create a remote branch nobody asked for. A workflow's push on such a branch
   fails loudly, which §26's rule already prefers over a quiet guess.

6. **The adopted-branch marker is persisted, because archive needs it.** Task
   064 decision 3 — vincent deletes only branches it cut — is the whole reason
   adoption may keep an upstream at all, so `DeleteEmptyBranch` and archive's
   `push --delete` leg both read `not_ours` on an adopted branch. "Adopted" is
   not derivable from the row after the fact, so it is a column on `tasks` and
   on `chats` (migration `0034_adopted_branch.sql`), written in the creating
   transaction.

7. **An adopted task refreshes no base and records no `base_refresh`.** Same
   shape as the pull-request mode: the fetch it runs is the adopted branch's,
   not the base's, and no base fast-forward is attempted. `base_sha` is the
   adopted branch's tip at admission (§5.3), so the diff answers "what did this
   task change" rather than re-rendering the branch's history. `base_branch`
   stays on the row for the fields that already read it.

8. **The picker's shape decides the request, and the row says which shape it
   holds.** *(2026-09-21, 125.9, issue #542.)* On the new-task form a branch
   chosen off the listing commits `branch_name` + `existing_branch: true`; the
   picker's free-text row commits `branch_name` alone, as it always did. No new
   row, no new key, nothing extra to set. It is lossless because the one shape
   it takes away — cut a new branch under a name that already exists — is the
   shape §10 refuses anyway. It does not reopen decision 1: that is about the
   *daemon* never inferring the mode from a branch existing, and a client
   offering two named shapes to choose between leaves the wire contract exactly
   as decision 1 left it. The rejected alternative was a separate
   enter-toggled `existing branch` row modelled on the start row: more literal,
   but it adds a row to §15's order, lets a draft hold the toggle on against a
   name no branch has, and breaks the `Down N` counts in
   `scripts/screenshots.sh`'s `tui-new-task` tape for no behavioural gain.

9. **The new-chat form's branch row is adopt-only.** *(2026-09-21, 125.9.)* A
   chat cannot cut a branch under a name you chose — `branch_name` without
   `existing_branch` is a 400 (§13.2) — so the row has exactly one meaning and
   empty leaves today's behaviour untouched. This matches
   `vincent chat start --branch --existing-branch`, where `--branch` alone is
   refused. Widening the chat API so `branch_name` alone names a cut branch was
   rejected as a daemon-and-spec change well outside 125.

10. **A branch one of vincent's own worktrees holds is listed, noted and still
    selectable.** *(2026-09-21, 125.9.)* `branchListResponse`'s own comment says
    the listing is a listing and not a validator; the worktree can be removed
    between picking and admission, and free text can name such a branch anyway.
    The row carries a note saying it would block. `pickerOption.disabled` was
    rejected: it makes the client a validator over state that changes
    underneath it.

11. **Scope is the two creation forms.** *(2026-09-21, 125.9.)* `adopted_branch`
    is served on every task and chat representation and is rendered nowhere in
    the TUI; badging the task detail and the chat workspace header ("running in
    your checkout") is real, but it is past what 125.9 says and belongs to its
    own issue.

## Accepted hazard

Nothing here can stop the human switching the main checkout to another branch,
or starting a rebase there, **while** a task is running in it. Vincent holds no
lock on someone else's repository. It is the same shape as §10's isolation
caveat — documented rather than solved — and it is why the mode is opt-in per
task rather than a default anyone can fall into.

## Tasks

- [x] 125.1 `worktree.CreateAdoptAndClaim`, its three §18 reasons, the branch
      listing, and `Remove`'s early return for the project path.
- [x] 125.2 Migration `0034_adopted_branch.sql`, the marker on both rows, the
      working-directory claim spanning both tables, and `DirClaimants` on
      `store.Candidate`.
- [x] 125.3 `GET /v1/projects/{id}/branches`, `existing_branch` on
      `POST /v1/tasks` and `POST /v1/chats`, and the chat's 409.
- [x] 125.4 The scheduler's directory predicate beside `ProjectCap`.
- [x] 125.5 `taskrun`'s third `ensureWorktree` branch, the `refreshOf` nil
      case, and `not_ours` on every archive leg.
- [x] 125.6 `internal/apiclient`, `vincent task add --existing-branch`,
      `vincent chat start --branch --existing-branch`, and the MCP
      `project_branches` tool.
- [x] 125.7 `scripts/125-gate.sh`, on all three platforms.
- [x] 125.8 Spec §10, §5.3 and §18; `docs/reference/api.md`, `cli.md`,
      `task-lifecycle.md` and `guides/troubleshooting.md`.
- [x] 125.9 The TUI branch picker on the new-task and new-chat forms, over the
      listing, with `allowFree` true and a `note` on rows that are checked out.
      Not in the first pull request — the daemon, the API, the CLI and the gate
      land first, and the picker is a form change with no behaviour behind it
      that is not already reachable.
