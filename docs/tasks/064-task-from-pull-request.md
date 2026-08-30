# 064 — Create a task from a pull request, checked out on the PR's head branch

Issue [#262](https://github.com/lezli01/vincent/issues/262). Builds on tasks
035 (issue selection), 052 (pull requests), 001 (branch names), 008 (archive
branch deletion) and 056 (base fetch).

A pull request becomes a runnable task. `POST /v1/tasks` grows `github_pull`,
`vincent task add` grows `--github-pull <n>`, and the §15 view 7 pull-requests
takeover grows `c`, which opens the new-task form prefilled from the selected
row. All three resolve the same daemon-side prefill, the way `github_issue`
already does (035 decision 2), and title, description and declared fields stay
fully editable before the human confirms.

The task's `branch_name` **is** the pull request's head branch, and its
worktree is that branch checked out with an upstream, so the agent's commits
reach the pull request when a workflow pushes. That is the whole point of the
feature and it is also where the cost is: §10 was written as *cut a new branch,
refuse a pre-existing one*, and this inverts both halves for one class of task.

Decision record row 11 and row 27 are untouched. vincent still pushes nothing,
opens nothing and merges nothing; `internal/github` gained no write method and
no mutating `gh` subcommand. Fetching is a read.

## Decisions

1. **The pull request is a new top level of the branch-name chain.** The chain
   is `built-in < config.yaml < project < per-task literal < pull request`,
   reported by `/v1/resolve` as `BranchSourcePull` (`"pull"`).
   `ResolveBranchName` grew the level rather than being bypassed. A PR task's
   branch is not negotiable: a project template or a typed literal would put
   the commits somewhere the pull request never sees. Task 001's decision that
   the chain is resolved server-side and that a literal is used verbatim is
   honored, not reopened — the new level sits above the literal, it does not
   change what the literal means.

2. **The head is fetched at admission, and a failure is a new block reason.**
   `POST /v1/tasks` stays entirely offline (§10) — it resolves the PR over
   GitHub for the prefill, as `github_issue` already does, but runs no git.
   `worktree.CreatePullAndClaim` is a second creation mode: it skips the
   `branch_exists` refusal, fetches the head, and runs `git worktree add <path>
   <branch>` — no `-b` — with the branch's upstream configured. There is
   nothing to fall back to when that fetch fails, so unlike task 056's base
   fetch it cannot be silent: it blocks the task with `pull_fetch_failed`.

   §10's "`--no-track` is not optional" is narrowed, not reversed. Its stated
   hazard — an inherited upstream on a branch vincent cut, which archive's
   remote leg would push `--delete` against — is closed by decision 3 instead,
   and on a PR task the upstream is the deliverable.

3. **Archive never touches a branch vincent did not cut.** A task whose branch
   came from a pull request skips **both** archive branch legs, local and
   remote, and reports `BranchNotOurs`. Task 008 was designed on the premise
   that vincent only ever deletes branches it created; that premise was
   implicit and is now explicit, because a task made from a **merged** PR is
   exactly "no commits past its base" — the case
   `delete_empty_branch_on_archive` (default **true**) fires on — and with
   `delete_remote_branch_on_archive` opted in it would delete a contributor's
   head branch on the forge. The worktree is still removed and pruned.

4. **A pre-existing local head branch is fast-forwarded, or the task blocks.**
   After the fetch, a local branch of that name is fast-forwarded to the
   fetched head. A clean fast-forward proceeds; a diverged branch blocks with
   `pull_branch_diverged`. It is never `reset --hard`: the local copy may hold
   commits nobody has pushed, and discarding them silently is the same
   dishonesty §10 refuses for branch names. A branch already checked out in
   another worktree — including the user's main checkout — blocks with
   `pull_branch_checked_out`, because git cannot put one branch in two
   worktrees. That is also the honest answer to "one active task per PR":
   within vincent it is already a `400` from task 001's in-transaction claim
   check (`store.BranchClaimedError`), and outside vincent it is this block.

5. **Fork pull requests run, and cannot push back.** `github.PullRequest`
   carries the head repository, so a fork is detectable at all. A fork PR
   fetches `refs/pull/{n}/head` into a local branch with **no upstream**; the
   task runs and the agent works, but nothing can push back, and that is stated
   on the task at creation rather than discovered when a delivery step fails.
   The daemon does not `git remote add` the fork: §10 (task 056) states nothing
   local is mutated, and a remote left behind after archive is exactly the
   residue that rule exists to prevent.

6. **`base_sha` is the head commit as it stood at admission.** So
   `GET /v1/tasks/{id}/diff` answers "what did this task change", which is what
   the tab means on every other task. §5.3 already defines `base_sha` as "the
   commit `branch_name` was actually cut from"; on a PR task that is the PR
   head at admission. The pull request's own diff is a browser keystroke away.

7. **The link is written at creation, as `human`.** The create call writes the
   `PullLink` itself so the takeover reads "claimed" immediately rather than up
   to `github.poll_interval` later, with `source: human` so 052 decision 2's
   reconciler will not overwrite it. No migration: `github_pull_json` (0018)
   already exists and already carries this shape.

8. **The envelope records that the branch came from the pull request.** The
   link alone cannot carry that — a human may link any task to any PR — but
   admission (decision 2), archive (decision 3) and the retry guard (decision
   10) all need to know. `PullLink.Branch` (and `PullLink.Fork`) ride in the
   existing `github_pull_json` envelope beside `source` and `suppressed`. It is
   a JSON column, so this is a shape change and not a migration, and it does
   not make the link a snapshot: nothing renderable is stored, which is what
   052 decision 3 and row 27 actually forbid.

9. **`pull` is a declared field name, and the listing grows a state filter.**
   `internal/github` gained `FieldPull = "pull"`, matched exactly and validated
   against the declaration like every other candidate (035 decision 7), because
   a `run:` step receives §8.5's environment and not §8.4's template context —
   the same reason `issue` was added on 2026-08-27. The prefilled title carries
   the `#N` prefix for the same reason an issue's does. `ListOptions.State`
   already accepted `open|closed|all`, so making closed and merged pull
   requests selectable is a query parameter on the listing endpoint and the `s`
   key on the takeover; the default stays `open`, so 052 decision 3's
   objection — do not pull a repository's whole PR history to answer one
   question — is not reopened, only made a choice the human makes.

10. **`branch_override` is refused on a PR task.** §10 offers it as the escape
    hatch from a `branch_exists` block; on a PR task it would detach the task
    from the pull request it was created for, so
    `POST /v1/tasks/{id}/retry` rejects it with a 409 and a stated reason
    rather than quietly producing a task that can never deliver.

11. **No `.Pull` and no snapshot column.** 052's "a pull request is a pointer,
    never a snapshot" is not reversed. The prefilled title and description
    become ordinary task text the moment the human confirms, and nothing later
    re-renders draft/state/merged from a stored copy that would read exactly
    like a current one while being wrong.

## Sub-tasks

- [x] **064.1** — `internal/github`: `Body` and the head repository on
  `PullRequest`, `Fork()`, `HeadFetchRef()`; `body`/`headRepository` on the
  `gh` leg and `body`/`head.repo` on the REST leg; `FieldPull`,
  `PullCandidate`, `PullTitle`, `PullDescription`, `PullLinkLine`.
- [x] **064.2** — `internal/worktree`: `BranchSpec.Pull` and
  `BranchSourcePull`; `CreatePullAndClaim` and its three new `Reason*`
  constants; `fetchPullHead`; the fast-forward and checked-out checks; the
  archive skip and `BranchNotOurs`.
- [x] **064.3** — `internal/api`: `github_pull` on the create request,
  `applyPullPrefill`, `pullPrefill`, the link written at creation, `state=`
  and `workflow=`/`prefill` on the pull listing, the `branch_override`
  refusal.
- [x] **064.4** — `internal/taskrun`: admission routes a PR task through the
  second creation mode; archive answers "is this branch ours".
- [x] **064.5** — `internal/apiclient`, `internal/cli`: the wire types,
  `--github-pull`, `--state` on `vincent github prs`.
- [x] **064.6** — `internal/tui`: `c` on the takeover, the `s` state cycle, the
  hand-off to the new-task form and its prefill fetch.
- [x] **064.7** — `cmd/fakegh`: bodies, head repositories, a fork fixture and
  an honoured `--state`.
- [x] **064.8** — docs, the §3/§5.3/§8.5/§10/§12.1/§13.2/§14/§15/§18/§20 spec
  amendments, and this record.
- [ ] **064.9** — `scripts/064-gate.sh` and `docs/gates/064-task-from-pull-request.md`:
  the end-to-end walk against a local bare remote. Not in this pull request;
  see its body for why.
