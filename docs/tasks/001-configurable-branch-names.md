# 001 — Configurable branch names

**Status:** not started (0/11) · **Opened:** 2026-08-13

Today every task's branch is `vincent/{id}-{slug}`, computed by
`worktree.BranchName` and unchangeable. The goal is that a user can decide what
the branch is called: a **convention** set once per project (or globally), and a
**literal** typed for one task when the convention doesn't fit.

Conventions for this file are in [the tasks README](README.md). Behaviour lands
in [the spec](../spec.md) as dated amendments, in the same PR as the code.

## Decisions (2026-08-13, grill session)

Binding, in the same way v0's phase decisions are. Each records the alternative
it beat, because that is the part that is expensive to reconstruct later.

- **Resolution chain:** `built-in default < config.yaml < project < per-task
  literal`. Chosen for consistency with the two chains already in the product —
  the workflow registry (`builtin < global < project`) and §8.6's
  `workflow defaults < task override`. *A literal alone* was rejected: it makes
  the user retype the convention on every task and offers no way to keep
  uniqueness. *A template alone* was rejected: one-off branches must be nameable.

- **The default stays a Go function.** `worktree.BranchName` keeps producing
  today's names; the template engine runs only when someone configures one.
  Expressing the default as a template constant was considered and rejected for
  now. It is expressible — but only carefully: the naive
  `vincent/{{.ID}}-{{.Slug}}` is **wrong**, yielding `vincent/12-` where the
  function yields `vincent/12`; the faithful form is
  `vincent/{{.ID}}{{with .Slug}}-{{.}}{{end}}`. Rejected because it would put
  every existing user's naming on a brand-new engine from day one to buy a
  code-path merge nobody asked for. The accepted cost is two paths that produce
  names.

- **Collisions are no longer impossible, and that is the crux.** §10 asserts
  "collisions are impossible (ids are unique)", which is what makes
  `branch_exists` a defensive check for a hand-made branch. Because **vincent
  never deletes branches** (§10, §17), a template without a discriminator — say
  `feat/{{ index .Fields "ticket" }}` — collides on the *second* task for the
  same ticket. Collision is therefore the normal case on any repeat, not an edge
  case, and most of what follows is downstream of that one fact.

- **Two collision checks, in two places, for two reasons.** At creation: reject
  with `400`, mirroring how `base_branch` already fails fast, so a typo or a bad
  template is caught before the task is accepted. At admission: keep the existing
  `branch_exists` block as the **authoritative** guard, because the creation
  check is inherently racy — a branch can appear between the two. *Auto-dedupe*
  (`-2`, or appending the id) was rejected: it hands the user a branch they did
  not ask for, and a workflow's `git push` step would put that surprise on the
  remote.

- **The collision check is wider than an exact ref match.** `git rev-parse
  --verify refs/heads/feat/foo` reports **not found** while `feat/foo/bar`
  exists, yet `git branch feat/foo` then fails with `'refs/heads/feat/foo/bar'
  exists; cannot create` — a directory/file conflict, verified empirically
  2026-08-13. Unreachable today (the generator emits one `/` and a unique id),
  reachable the moment names carry arbitrary `/`. So the probe is: the exact ref,
  any ref *under* the name, and any prefix of the name that is itself a ref.

- **Legality is git's to judge.** Validation delegates to `git check-ref-format
  --branch`, not a Go regex. `gitx` is documented as "the single door to the git
  CLI… vincent never links a git library", and a hand-rolled matcher would have
  to reproduce `..`, `~^:?*[\`, control characters, `@{`, a `.lock` suffix, a
  leading `-`, a trailing `.` and `//` — then stay correct on three OSes as git
  evolves. Rejection is loud: a new `branch_name_invalid` reason, never a silent
  sanitize, because quietly rewriting a name the user typed is the same
  dishonesty as faking an adapter capability (§9.x).

- **The name is committed atomically, and the recompute goes away.** Today
  `CreateTask` inserts and the API then writes the branch name in a second
  statement, with `engine.go:158` recomputing it when empty. That recompute is
  harmless while the name is a pure function of `(id, title)`; with customization
  it **silently discards the user's choice** and runs the task on a default
  branch, violating crash-first. Since `CreateTask` already runs in a transaction
  and already has `LastInsertId` in scope, the name is written inside that
  transaction. `SetTaskBranchName` and the `engine.go` fallback are both deleted.

- **`.ID` is a method that errors when unset, which is also how we route.** A
  name that needs the id cannot be known before the insert; a name that doesn't
  must be checked *before* it, so the 400 can fire. Rather than walk the template
  parse tree — which must handle `with`, `range` and `$var` aliasing to be
  correct, and where a wrong detector means a silent `ID=0` —
  `BranchContext.ID()` returns `(int64, error)` and errors while the id is nil.
  `text/template` propagates it, so pass 1's error **is** the signal to take the
  post-insert path, and its absence means the name is final. `ID=0` is
  unrepresentable.

- **Git never runs inside the write transaction.** `withTx` takes SQLite's write
  lock at the `INSERT`, and `gitx.QueryTimeout` is 30s, so a slow or hung git —
  large repo, cold cache, network mount — would stall the scheduler's admissions
  and every actor's `step_run` write. The task-claim check (*is another
  unarchived task already holding this name?*) is a plain DB query and **does**
  run in the transaction, on **both** paths: an id-bearing template is not
  self-protecting, since `feat/{{.ID}}` on task 5 collides with a literal
  `feat/5` typed on task 9.

- **No `UNIQUE(project_id, branch_name)` index.** Archived tasks keep their
  `branch_name`, so an index would forbid ever reusing a name — including after
  the user manually deleted the branch, which is the one case where reuse is
  legitimate. The in-transaction query scopes itself to unarchived tasks instead.

- **A dedicated `BranchContext`, not `workflow.RenderContext`.** `Render` already
  uses `Option("missingkey=error")` (phase 2 decision), so a missing *map* key
  fails loudly — but `RenderContext` carries `.Task.BranchName`,
  `.Worktree.Path`, `.Step` and `.Steps`, which are real struct fields that
  render as **empty strings** at creation time. That is precisely the hole the
  phase 2 decision exists to prevent, so the branch context omits them and
  referencing one is an error. It also makes self-reference unrepresentable.

- **Resolution stays server-side.** The branch chain is precedence resolution,
  and `POST /v1/resolve` exists "so no client re-implements the precedence"
  (T4.7; the PR L decision that resolution is server-side is honored, not
  relitigated). The preview therefore extends `/v1/resolve` rather than rendering
  templates in the TUI. An id-bearing template previews honestly as
  `vincent/<id>-fix-login`, since there is no id before the insert.

- **`branch_exists` must become recoverable.** It is unreachable today, so
  nothing in the API can change a branch name: `retry` takes `{prompt_override,
  run_override}` and `PATCH` takes `{priority}`. A blocked task would be
  permanently dead, its transcripts orphaned, and one bad project template could
  strand a whole queued batch. `retry` gains `branch_override` — it is already
  the blocked-only "fix it and try again" endpoint. Verified mechanically:
  `ensureWorktree` returns early only when `WorktreePath != ""`, and that is set
  only after a successful create, so a task blocked at creation re-enters
  `Worktrees.Create` on re-admission with the new name.

- **The `vincent/` prefix is not enforced; the cleanup docs change instead.**
  Arbitrary names break `git branch --list 'vincent/*'`, which
  `getting-started/installation.md` presents as how you find leftover branches. A
  *mandatory prefix* would defeat the point of the feature (no `feat/OPS-123`
  ever). Writing provenance into the user's `.git/config` was rejected as vincent
  mutating a repo it does not own. The daemon already knows: `vincent task ls
  --archived` exists and already prints the branch column, so this is a docs
  change, not a code one.

## Tasks

- [ ] **001.1 — `BranchContext` and the branch template renderer.** New type in
  `internal/workflow` holding `ID()` (method, errors while nil), `Title`, `Slug`,
  `BaseBranch`, `Fields`, `Project`; a `slug` template func for arbitrary values;
  rendering through the same `missingkey=error` option `Render` uses. Sentinel
  `ErrBranchNeedsID`.
  *Done when:* table tests cover `{{.Slug}}`, `{{ slug (index .Fields "ticket") }}`,
  a missing field erroring, `.Worktree`/`.BranchName` failing, and `{{.ID}}`
  returning `ErrBranchNeedsID` with a nil id and the real id otherwise.

- [ ] **001.2 — Ref legality and the widened collision check.** `gitx` gains
  `CheckRefFormat` (delegating to `git check-ref-format --branch`).
  `internal/worktree` gains a conflict probe covering the exact ref, refs under
  the name, and refs that are prefixes of it, plus
  `ReasonBranchNameInvalid = "branch_name_invalid"`.
  *Done when:* tests in a `testrepo` prove the D/F case both directions
  (`feat/foo` blocked by `feat/foo/bar`, and `feat/foo/bar` blocked by
  `feat/foo`), and a table of illegal names is rejected — including `a..b`,
  `a~b`, `a.lock`, `-a`, `a.`, `a//b`, `a@{b}`, and one with a control character.

- [ ] **001.3 — Resolve the chain server-side.** `config.branch_template`
  (global, hot-reloaded like the rest of `config.yaml`), a `branch_template`
  column on projects (append-only migration), and the resolver producing
  `(name, source)` where source is `default|config|project|task`. Templates are
  parsed at config load and at project write, so a broken one fails there rather
  than at every task creation. *Depends: 001.1.*
  *Done when:* unit tests cover each level winning, a project template shadowing
  config, a literal beating both, and an unparseable template rejected at
  `PATCH /v1/projects` with a 400 rather than accepted.

- [ ] **001.4 — Atomic persist; delete the second write and the recompute.**
  Resolve inside `CreateTask`'s existing transaction: pass 1 without an id (final
  name → collision-checked before the transaction opens → single `INSERT`), or on
  `ErrBranchNeedsID` render after `LastInsertId` and `UPDATE` in the same
  transaction. The unarchived-task-claim query runs in the transaction on both
  paths. Remove `store.SetTaskBranchName` and the `engine.go:158` fallback.
  *Depends: 001.1, 001.2, 001.3.*
  *Done when:* a test proves no committed row ever carries an empty
  `branch_name`; a test proves a rejected creation leaves no row and emits no
  `task.created` event; and a test proves a literal survives what used to be the
  crash window — the recompute is gone, so a custom name cannot degrade to a
  default.

- [ ] **001.5 — `POST /v1/tasks` accepts `branch_name`.** Request field, 400s for
  `branch_name_invalid` and for both collision kinds, and the resolved name in
  the response. *Depends: 001.4.*
  *Done when:* handler tests cover an invalid ref, a name taken by an existing
  git branch, a name claimed by another queued task, and a happy path asserting
  the stored name.

- [ ] **001.6 — `POST /v1/tasks/{id}/retry` accepts `branch_override`.**
  Blocked-only, validated and collision-checked exactly as creation, persisted
  before the block is cleared. *Depends: 001.5.*
  *Done when:* a test drives a task to `blocked`/`branch_exists`, retries with a
  free name, and asserts the worktree is created on the new branch with the
  task's history intact; a second asserts a still-colliding override returns 400
  and leaves the task blocked.

- [ ] **001.7 — `POST /v1/resolve` previews the branch.** Request gains `title`,
  `fields`, `base_branch`; response gains `branch: {value, source}`. An id-bearing
  template renders with an `<id>` placeholder rather than a fabricated number.
  *Depends: 001.3.*
  *Done when:* a live test through the real handlers asserts each source level is
  reported, and that an id-bearing template previews with the placeholder and
  never `0`.

- [ ] **001.8 — CLI surface.** `--branch` on `task add` and on `task retry`,
  following the existing pointer-flag pattern in `internal/cli/task.go`.
  *Depends: 001.5, 001.6.*
  *Done when:* the flags round-trip through `apiclient` and appear in
  `vincent task add --help`; a `task ls` run shows a custom branch in the branch
  column.

- [ ] **001.9 — TUI new-task row and live preview.** A branch-name row distinct
  from the existing base-branch input — which is labelled "base branch" and must
  not read as renamed — reusing the debounced `/v1/resolve` round-trip and the
  `resolveKey` stale-reply drop the form already has. Shows the resolved name and
  the level it came from. *Depends: 001.7.*
  *Done when:* a live test types a title and asserts the previewed branch updates
  from the real handlers; a second asserts a stale reply for a superseded draft is
  dropped; `bindings.go` and the TUI key docs updated if a key is added.

- [ ] **001.10 — Spec amendments and user docs.** Dated in-place amendments to
  [../spec.md](../spec.md): §5.3 (`branch_name`), §10 (branch naming, and the
  retirement of "collisions are impossible"), §17 row 17, §18 (the
  `branch_exists` row, plus `branch_name_invalid`), §12.2 (the `retry` payload)
  and §13.2 (`/v1/resolve`). User docs: `reference/configuration.md`,
  `reference/cli.md`, `reference/api.md`, `getting-started/concepts.md`,
  `guides/troubleshooting.md`, `faq.md`, and the cleanup guidance in
  `getting-started/installation.md` moving from `git branch --list 'vincent/*'`
  to `vincent task ls --archived`. *Depends: 001.1–001.9.*
  *Done when:* no page still states `vincent/{id}-{slug}` as the only possible
  shape, and §10 no longer claims collisions are impossible.

- [ ] **001.11 — Acceptance gate.** Extend
  [`scripts/m2-gate.sh`](../../scripts/m2-gate.sh) with a scenario that drives a
  real daemon over curl: create a task with a project template and assert the
  branch, create a second task that collides and assert the 400, force a
  `branch_exists` block, recover it with `retry --branch`, and assert the
  worktree lands on the new branch. *Depends: 001.10.*
  *Done when:* the new scenario is green on ubuntu, macOS and Windows in CI, and
  [`CHANGELOG.md`](../../CHANGELOG.md) carries the feature under `## [Unreleased]`.

## Notes

- **Release shape.** Additive to the API and config, and the default branch name
  is unchanged, so this is a **minor** bump (`0.2.0`) under the policy in
  [CHANGELOG.md](../../CHANGELOG.md#versioning-and-stability) — not a breaking
  change, even though it retires a spec invariant.
- **Not in scope.** `PATCH /v1/tasks { branch_name }` for a queued task was
  considered and left out: `retry`'s `branch_override` covers the failure that
  actually needs recovery, and a second entry point doing the same validation
  earns its keep only if renaming-before-first-run turns out to be a real want.
