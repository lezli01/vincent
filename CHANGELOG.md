# Changelog

All notable changes to vincent are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with the pre-1.0 caveat spelled out
in [Versioning and stability](#versioning-and-stability) below.

Release Please creates release entries from Conventional Commit history. Its
release pull request is the review point for replacing the mechanical commit
list with the user-facing context a commit subject cannot carry.

## [0.3.0](https://github.com/lezli01/vincent/compare/8efa4c8c7bb8b034831c04447f17122f9d8aaf0a...v0.3.0) (2026-08-19)

### Added

- **Reusable workflow composition with `type: include`.** An include splices a
  registry workflow's steps into the caller when the task is created, so shared
  fragments run in the same task and worktree and their results remain available
  through `.Steps`. Nested includes carry provenance, honour the included
  workflow's defaults, and are checked for cycles, depth, duplicate step ids and
  platform compatibility before execution. Includes work at top level and inside
  loops, parallel groups and inline fan-out lanes.

- **Workflow platform restrictions with `platforms:`.** A workflow can declare
  `linux`, `darwin`, `windows` or `posix`. Unsupported workflows remain visible
  and explain why they cannot run, but task creation refuses them and migrated
  snapshots block with `platform_unsupported` before creating a worktree.

- **Input-capability gating with `on_input: require`.** Workflows that depend on
  mid-run questions can now reject an agent that cannot pause for an answer,
  instead of silently continuing with a guess. Static incompatibilities fail
  validation; installed-agent capability is checked again at creation and just
  before the step starts.

- **A configurable, grouped task board.** `tui.board.group_by` defaults to
  project then workflow, with `g` cycling project/workflow/flat layouts for the
  current session. Groups preserve the existing attention-first ordering and
  never hide tasks behind collapsible headers.

- **Bulk task actions in the TUI.** `space` marks one task and `V` marks all
  visible tasks; the normal action keys then operate on the eligible selection.
  Successful tasks are unmarked, refusals remain selected for retry, and dirty
  worktrees receive the same explicit confirmation as single-task archive.

- **File-grouped diffs.** The task detail diff tab groups hunks by file and
  starts them folded, so large agent changes are navigable without losing the
  full patch.

- **A control-flow graph for workflows in the TUI — `g`.** The workflows screen
  explained a workflow as a numbered list of its top-level steps, which was
  enough while workflows were linear. The language now has structure —
  `parallel` groups, `fan_out` lanes and their merge, guards, `condition`,
  `loop` and `break` — and a list can name those constructs without showing
  where control goes. `g` on an entry draws it.

  The graph opens *over* the registry list rather than replacing it: `enter`'s
  step list still carries the findings, platform notes and agent resolution the
  picture does not show, and `esc` closes one layer at a time. Arrows move the
  selection and the view follows it; `shift`+arrows pan; a graph larger than the
  terminal is cropped and panned, never reflowed into a different shape. `e`
  works from inside the layer, so saving the file in your editor redraws the
  graph in place with the same node still selected.

  Everything the picture says survives having colour stripped: frame weights
  separate a `parallel` group from a `fan_out` from a `loop`, boxes carry the
  step's own type word, and a `condition`'s two ways out are labelled `true` and
  `false`. A `fan_out` shows a merge node because its join is a git merge that
  runs and can block; a `parallel` group shows none, because its join is only
  its members finishing. A guard on an ordinary step draws no second branch —
  false there means skip and carry on.

  A new endpoint backs it: `GET /v1/workflows/definition?name=&project_id=`
  serves one workflow's whole recursive structure, as authored, with workflow
  defaults kept in their own block. The registry list keeps its compact shape.

- **Loops in workflows — `type: loop` and `type: break`.** A workflow can now
  repeat a body of steps: `count:` a fixed number of times, or `for_each:` once
  per item in a list, including a list a step discovered at run time. That
  makes three shapes writable that were not: **converge** ("run the tests, fix
  what broke, run them again" — a probe under `allow_failure:`, a `break`
  reading it, a repair), **repeat** (ten passes of the race detector without
  ten copy-pasted steps), and **iterate a set** (once per changed file). A loop
  is one step, one index, one concurrency slot and the task's one worktree — no
  branch and nothing to merge, which is what separates it from `fan_out`.

  `type: break` ends the loop successfully when its `if:` is true. There is no
  `continue` type: a `condition` inside a loop body ends *that iteration*,
  which is what continue means, using the meaning that word already had. There
  is no `while:` either — a guard can only read what a run has produced, so a
  `while:` about its own body is either loud on iteration 1 or silently false,
  and `count:` plus `break` is the same loop written correctly.

  `.Loop` (`Index`, `Item`, `IsFirst`, `IsLast`) joins the template context,
  with `Index: 0` outside any loop. `loop.max_iterations` (default **10**) is
  the ceiling: `count:` is checked against it when the file loads, and a
  `for_each` list longer than it blocks with `loop_limit` before the first
  iteration rather than quietly doing the first ten — ten iterations of a
  three-step body is already thirty agent runs. A loop's position is derived
  from its step rows on every admission and never persisted, so `retry` and a
  daemon restart both resume **mid-iteration**; `skip` skips the whole loop,
  and `edit + retry` on a body step applies to every remaining iteration. The
  board shows `loop 4/10` and the detail view groups rows by iteration, folded
  with the latest open. See
  [`type: loop`](docs/reference/workflow-schema.md#type-loop).
- **Conditions between steps.** A workflow can decide at run time what to do
  next. `if:` on any step is a guard: false skips that step and the workflow
  carries on, recording a `skipped` row whose reason says a condition did it
  rather than you. On a fan-out lane or a `parallel` sub-step the same `if:`
  subsets the set instead — the others still run and the join still happens.
  `type: condition` is a step whose whole body is the guard: false ends the run
  and the task is `done`, which is how a workflow finishes early. And
  `allow_failure:` on agent and command steps turns the failures a step itself
  produced into an advance, so a guard has something a run *discovered* to
  read — without it, a guard could only see what you typed when you created the
  task. Guards are ordinary templates that must render exactly `true` or
  `false`, are re-evaluated every time rather than cached, and can now read
  `.Host.OS`. See [Conditions](docs/reference/workflow-schema.md#conditions).
- **`type: parallel` — sub-steps that run at once.** A group runs its
  sub-steps concurrently in the task's one worktree: one step, one index, one
  concurrency slot, no branch and no merge. It succeeds when every sub-step
  does, a failure does not cancel its siblings, and a retry re-runs only what
  failed. `parallel.max_parallel` (default 4) bounds it, and is a **second
  concurrency dimension your task caps do not govern** — a board reading "1
  running" can be a machine running four compilers. `manual`, nested groups
  and `on_input: require` are refused inside a group.
- **`type: fan_out` — lanes as real child tasks.** Each lane becomes an
  ordinary task with its own worktree, branch, retries, gates and blocks, and
  their branches are merged back (`--no-ff`, in declared order) into the
  branch the task already owns, so one branch is still delivered. A lane is a
  named workflow or inline steps, resolved into the task's snapshot at
  creation; lanes may nest to any depth, bounded by `fan_out.max_depth` (3)
  and `fan_out.max_tasks` (64), both checked at creation with a `400` naming
  what is wrong.

  A merge conflict blocks the task with `merge_conflict` and leaves the
  worktree conflicted so you resolve it in place, stage, and retry;
  `merge: {on_conflict: agent}` opts into an agent attempt first. A lane that
  is cancelled or ends without finishing blocks with `lane_failed` and merges
  **nothing**.

  Two things worth knowing before you use it: a fan-out **fills** your
  concurrency caps rather than exceeding them, and N lanes leave N worktrees
  on disk until the tree is archived.

  New `awaiting_children` task state (holds no slot), `?parent_id=` and
  `?include_children=` on `GET /v1/tasks`, a `children` rollup on the task
  detail, the `task.children_changed` event, `vincent task ls
  --include-children/--parent`, and `L` in the TUI to drill into a fan-out's
  lanes.

### Fixed

- **Long structured-input prompts no longer truncate in the TUI.** The answer
  popup is wider and wraps both questions and free-text answers, keeping the
  full prompt usable in ordinary terminal sizes.

- **A fan-out whose spawn failed part-way could never be retried.** Lanes were
  created one at a time, each committing before the next, so a failure on lane
  two left lane one committed; the cleanup cancelled it, and a cancelled lane
  stays attached to its step. The parent's `retry` therefore found a lane, took
  the *join* path instead of re-spawning, read the lane as aborted and blocked
  `lane_failed` — again on every retry, with nothing in the API or the TUI able
  to clear it. Lanes are now inserted in one transaction, so a failure leaves no
  lane behind and `retry` re-spawns from a clean slate.

- **A fan-out could join lanes that had not started.** The parent decided
  *spawn or join* on whether the step had lanes, which only answers "have the
  lanes finished" if the park after spawning always commits — and it is a
  compare-and-swap. A parent left `running` with `queued` lanes joined on its
  next admission and blocked `lane_failed` against work about to run perfectly
  well. It now parks again instead.

- **A `parallel` sub-step's guard could read a sibling after a retry.** §7.5
  says a group is a set whose members cannot see each other, and that held on a
  group's first admission only: a re-admitted group skips the sub-steps that
  already succeeded, and their rows were still visible in `.Steps`. The same
  guard against the same context answered one way on the first run and another
  after a human pressed `retry`. Set-invisibility now holds in every admission.

- **A `loop` whose `for_each` list re-derived shorter than its own rows.** The
  extent came from the fresh list alone, so a shorter one would have left the
  loop reporting success over iterations it started and never revisited. The
  extent is now the longer of the list and the recorded iterations, with the
  `max_iterations` ceiling re-checked against it. Every `for_each` source §8.4
  offers is stable between admissions, so this bounds the derivation rather than
  a reachable failure.

- **A `loop` with an empty `for_each` list left its step index with no row.**
  The two structure steps each have a case where they are reached and run
  nothing — every `fan_out` lane guarded off, an empty `for_each` list — and only
  the fan-out recorded a row saying so, leaving a detail view unable to tell
  "ran nothing" from "never reached" for the loop. The empty case now records
  one row under the loop's own id. That row is deliberately not a `.Steps`
  entry: a loop's id is never one, or it would be a key present exactly when the
  loop did nothing and absent when it did something.

- **A leaked context in `parallel` and `loop` steps.** Both created a
  cancellable context and then overwrote it — `cancel` included — when the step
  carried a `timeout:`, leaving the first context attached to the parent for the
  rest of the task's run.

### Changed

- **vincent is now source-available and dual-licensed, not MIT.** Personal and
  non-commercial use is free under the
  [PolyForm Noncommercial License 1.0.0](LICENSE); commercial or business use —
  including running vincent inside a for-profit company's own development
  workflow, without selling it — requires a separate commercial license, see
  [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md). This is deliberately *not* an
  OSI-approved open-source license: restricting commercial use is incompatible
  with the Open Source Definition, so "source-available" is the accurate word.

  **The change is not retroactive.** `v0.2.0` and every release before it were
  published under the MIT License and stay usable under it forever, on the terms
  they shipped with; no tag or published artifact is modified. The first release
  published after this change is the first release under the new licensing, and
  every release after it follows.

- **Archiving removes a task's empty branch by default.** A branch is deleted
  only when it contains no commit beyond the task's recorded base; dirty,
  diverged or unverifiable branches are retained and archiving still succeeds.
  `delete_empty_branch_on_archive: false` restores the old behaviour, while the
  opt-in `delete_remote_branch_on_archive` also removes a configured upstream
  after the safe local deletion. Project deletion never deletes remote branches.

- **Release dependencies were refreshed without major-version changes.** This
  release uses Bubble Tea 2.0.9, Lip Gloss 2.0.6, `x/ansi` 0.11.8,
  modernc.org/sqlite 1.57.0 and govulncheck 1.7.0.

## [0.2.0](https://github.com/lezli01/vincent/compare/v0.1.1...v0.2.0) (2026-08-16)


### Features

* **codex:** report logged_in from `codex login status` ([7c1a506](https://github.com/lezli01/vincent/commit/7c1a506ef826d2508c1164eb484d2d07067e9783))
* **doctor:** `vincent doctor` — one command that answers "why is nothing running?" ([e298676](https://github.com/lezli01/vincent/commit/e298676533dfab7cab42693dc4ff2be36590cb59))
* **doctor:** one command that answers "why is nothing running?" ([e0d63c7](https://github.com/lezli01/vincent/commit/e0d63c7f99b8ad04e72c72cb6e64cff49cfac0b5))
* reclaim orphaned worktrees with `vincent gc` ([e1195e4](https://github.com/lezli01/vincent/commit/e1195e4f6e611e68a6c25d20dae6beb6f940bfbf))
* reclaim orphaned worktrees with `vincent gc` ([5a9037d](https://github.com/lezli01/vincent/commit/5a9037d8392fb32b9e539f072d5fd75c73429743)), closes [#95](https://github.com/lezli01/vincent/issues/95)

## [0.1.1] — 2026-08-15

### Added

- **Repository workflows for GitHub issues and releases.** The checked-in
  project registry now includes `github-issue`, `github-enhancement`,
  `github-bug`, `prepare-release` and `release`. `github-issue` turns a rough
  task into a `bug` or `enhancement` issue, parks in `awaiting_input` while
  Claude asks for missing detail, and puts a manual gate before the
  non-retrying `gh issue create`; it is POSIX-only and deliberately leaves its
  empty worktree branch behind.

  `github-enhancement` takes an open enhancement's id as the first token of the
  task title — `42`, `#42` and a full issue URL still work, and optional prose
  after that token now produces a useful branch and PR name — then separates
  clarification from implementation, runs the expensive cross-platform check
  without an agent retry, and gates the diff before the non-retrying push and PR
  creation. `github-bug` likewise proves a regression test red before fixing it.
  Both are Claude-only and POSIX-only; codex and cursor do not support the
  `on_input` clarification they rely on.

  `prepare-release` audits all six build targets, dependencies, archive
  contents, smoke assertions and pinned actions, lets an agent clear FAIL
  findings, and verifies a real `dry_run` artifact without publishing anything;
  its task-title version is optional. `release` follows `RELEASING.md` through
  preflight and the changelog PR, then stops at a manual gate before the tag.
  Its PR and tag steps do not retry, and everything after the tag only verifies
  the published result. `release` is Claude-only and POSIX-only.

- **Explicit child-process environments.** The new
  [`environment`](docs/reference/configuration.md#environment) config block
  applies to agent steps, command steps and checks: `inherit` accepts `all`
  (the unchanged default), `none` or a list of names; `unset` removes names
  next; and literal `set` values win last. An empty inherit list means nothing,
  `$` is not expanded in `set`, and a step's own `env` still wins. This makes
  hermetic runs possible and, on Windows, lets `unset: [MSYSTEM]` stop Cursor
  importing Claude Code hooks through the MSYS environment. The daemon logs
  resolved variable names, never their credential-bearing values or the values
  in a transcript, and warns rather than rewriting an environment with no
  `PATH` or Windows `SystemRoot`.

- **Richer output and complete transcripts in the TUI.** Tool calls now show a
  useful subject rather than only `Bash` or another bare tool name, followed by
  their success or failure; reasoning is marked with `·`, calls with `▸`, and
  outcomes are indented beneath them. Claude, codex and cursor all surface tool
  outcomes. Claude and cursor surface complete reasoning blocks, and codex now
  does too when its CLI emits the effort-dependent `reasoning` item. Long
  assistant text wraps with a hanging indent instead of disappearing past the
  output pane's right edge; tool output bodies remain in the raw transcript.

  Pressing `e` from either detail pane opens the selected attempt's complete raw
  JSONL transcript in `$EDITOR`, including the beginning omitted by the TUI's
  256 KB tail. The truncation notice advertises the binding, and a pruned
  transcript, a gate with no transcript, or an editor failure is reported
  instead of opening a misleading empty file.

- **Agent usage limits are now a wait, not a failure**
  ([task 003](docs/tasks/003-usage-limit-classification.md)). When a step stops
  because the agent's usage quota for the window is spent, vincent records the
  attempt as `usage_limit`, **consumes no retry**, releases the task's
  concurrency slot, and re-queues it until the window reopens — the step then
  re-runs with **no human action**. Previously that run was indistinguishable
  from a genuine failure: it burned the whole retry budget in seconds (there is
  no delay between attempts) and blocked the task with `agent_error`, which sent
  you to read a transcript about a task that was fine. With several tasks running
  on the same agent, the whole board went down at once.

  A held task shows when it resumes — `queued → 14:20` on the board,
  `queued · usage limit → 14:20` in the detail header — and gives up its slot, so
  other work carries on. The resume time is the reset the CLI reported; when it
  reports none, vincent waits
  [`usage_limit_recheck_interval`](docs/reference/configuration.md#usage_limit_recheck_interval)
  (new, default 15m) and tries again. Cancelling, pausing or resuming a held task
  drops the wait immediately.

  Only the **claude** adapter recognizes usage-limit wording today. Capturing a
  real quota exhaustion means burning a real five-hour window, so codex and
  cursor deliberately ship no pattern and behave exactly as before — a wrong
  guess would park a genuinely failed task in a wait it never leaves.

- **`agent_unauthenticated` block reason**
  ([task 003](docs/tasks/003-usage-limit-classification.md)). A claude step that
  fails because the CLI is not logged in now says so, instead of surfacing as
  `nonzero_exit` or `agent_error`. Nothing else changes: the step still runs, the
  retry budget still applies, and the task still blocks — the reason just names
  the fix.

- `queued_reason` and `admit_not_before` on every task in the API and on
  `GET /v1/config`'s `usage_limit_recheck_interval`. Both task fields are `null`
  for an ordinarily queued task, so the addition is invisible to existing
  clients, and they are separate from `block_reason`, which still means only
  "stopped, needs a human".

- **Homebrew install on macOS** ([task 002](docs/tasks/002-homebrew-tap.md)).
  `brew install lezli01/tap/vincent`. The cask clears the quarantine attribute
  during install, so macOS users no longer meet the Gatekeeper "unidentified
  developer" prompt or have to run `xattr -d com.apple.quarantine` by hand — the
  release binaries are still cosign-signed rather than Apple-notarized, so the
  archive path is unchanged. `brew uninstall --zap vincent` also unloads the
  LaunchAgent and removes the config and data directory. Linux and Windows keep
  the release archives and `go install`.

- **Configurable branch names**
  ([task 001](docs/tasks/001-configurable-branch-names.md)). A task's branch no
  longer has to be `vincent/{id}-{slug}`. Names resolve through a chain, most
  specific first: a per-task literal (`vincent task add --branch feat/OPS-123`, or
  `branch_name` on `POST /v1/tasks`), a project template
  (`branch_template` on `PATCH /v1/projects/{id}`), the global
  [`branch_template`](docs/reference/configuration.md#branch_template) in
  `config.yaml`, and finally the built-in name. **The default is unchanged**, so
  nothing moves unless you configure it.

  Templates get `{{.ID}}`, `{{.Title}}`, `{{.Slug}}`, `{{.BaseBranch}}`,
  `{{.Fields.NAME}}`, `{{.Project.*}}` and a `slug` function. The new-task form
  previews the resolved name and the level it came from as you type, via
  `POST /v1/resolve`.

  Two things to know. Because vincent never deletes branches, a template with no
  discriminator in it collides on the *second* task for the same input — put
  `{{.ID}}` in it or expect to name repeats by hand. And `{{.Fields.x}}` fails
  loudly on a missing field while `{{ index .Fields "x" }}` renders nothing, which
  yields a legal-but-wrong name like `feat/-fix-login`; prefer the first.

- `branch_override` on `POST /v1/tasks/{id}/retry`, which makes a `branch_exists`
  block recoverable: the branch is renamed and the task re-admitted, keeping its
  id and history. Previously nothing in the API could change a branch name.

- `branch_name_invalid` block reason. Branch names are validated by
  `git check-ref-format` rather than a hand-rolled matcher, and a rejected name is
  reported rather than silently rewritten.

- `go run mage.go vuln` and a weekly `Vulnerabilities` workflow: govulncheck
  over the module's reachable code, swept across `linux`, `darwin` and `windows`
  because 15 packages (`x/sys/windows/svc`, `modernc.org/libc/*`) reach the
  binary on Windows only.
- `CHANGELOG.md`, [`RELEASING.md`](RELEASING.md) and `.github/CODEOWNERS`.
- `go install github.com/lezli01/vincent/cmd/vincent@latest` as a documented
  install path, and a versioning-and-stability policy in the README and
  `SECURITY.md`.

### Changed

- **Normalized transcript and live-output clients must read structured tool
  records.** On
  `GET /v1/tasks/{id}/steps/{run_id}/transcript?format=normalized` and the
  per-task SSE stream, `agent.tool_use.tools` changed from `[]string` to
  `[{name, summary, call_id}]`; `agent.tool_result` adds
  `results: [{call_id, name, summary, is_error}]`, and `agent.thinking` adds
  whole-block reasoning text. Clients that rendered each tool as a string must
  read its `name` field and should tolerate the two new record types.
  Normalization happens on read, so stored raw transcripts need no migration
  and gain the richer rendering retroactively.

- A task's branch name is now resolved and written inside the same transaction as
  the task row, so no committed task can carry an empty one. This removes a window
  in which a crash between two writes left the name unset — harmless while names
  were derived from `(id, title)`, but it would have silently discarded a
  configured name and run the task on a default branch.
- `docs/` is no longer versioned: `docs/versions/v0/spec.md` is now
  [`docs/spec.md`](docs/spec.md), the single platform spec, amended in place.
  Planned work lives in [`docs/tasks/`](docs/tasks/README.md), the closed v0 ledger
  in `docs/history/`, and the gate walkthroughs in `docs/gates/`.
- A `go install`ed binary now reports the module version from `vincent version`
  instead of `dev`.
- CI runs the three acceptance gates as steps of one per-OS job rather than
  three separate jobs, sharing a checkout and a toolchain setup.
- Release notes are grouped into Features / Bug fixes / Other changes instead of
  one flat list of commit SHAs.
- Third-party actions in the release workflow are pinned to commit SHAs; every
  job carries a `timeout-minutes`.

### Fixed

- **Release binaries no longer depend on a stale vulnerable Go patch.** The
  module and release workflow now pin Go 1.26.6, which clears five reachable
  standard-library advisories across linux, darwin and windows instead of
  accepting whichever 1.26 patch happens to be on a runner. A weekly workflow
  proposes patch-only toolchain bumps within the declared minor series, and
  admits them only after build, test and the vulnerability sweep pass.

- **Installed daemons now start as the right user with a usable `PATH`.** macOS
  launchd and Linux systemd units capture the installing shell's `PATH`, so
  Homebrew, npm, nvm and `~/.local/bin` agent CLIs are visible after login;
  reinstall the service after changing that path. Windows now uses an
  unelevated, per-user Scheduled Task at login instead of a LocalSystem service,
  so the daemon shares the user's data, token, git config and agent credentials.
  The task pins the new internal `vincent daemon --config-dir` and `--data-dir`
  flags; only removing a legacy LocalSystem service still needs Administrator.

- **Windows login no longer leaves a daemon console open or a slow agent
  permanently unavailable.** The scheduled daemon's `--hide-console` path now
  releases the Windows Terminal console safely and starts agent probes without
  replacement windows, while a manually entered flag leaves the user's terminal
  alone. Failed probes expire after one minute instead of being cached for the
  daemon's lifetime, cold-login probes get 20/25-second bounds, and a timed-out
  Cursor status probe is no longer reported as definitely unauthenticated.
  `vincent service status` points users to `Task Scheduler Library\\vincent`, not
  `services.msc`, and diagnoses elevated task ownership that would prevent a
  later unelevated reinstall or uninstall.

- **The advertised output-tab keys now work.** `[` and `]` switch between the
  detail view's output tabs from either pane, as the help, palette and footer
  already promised; the existing `d` alias continues to make the same toggle.

- **The TUI answer form no longer truncates what it asks you**
  ([#83](https://github.com/lezli01/vincent/issues/83)). A question, an option
  label or a permission summary longer than the popup's inner width was cut with
  an `…` and there was no way to see the rest from inside the form — no wrap, no
  scroll, no expand — and because the popup is capped at 76 columns, a wider
  terminal did not help. That hid the end of an agent's question, the
  `(Recommended)` suffix agents put at the *end* of an option label, and, in
  `restricted` mode, the tail of the command you were being asked to approve.
  Rows now wrap inside the popup, with continuation lines indented under the
  marker so a wrapped option still reads as one option; `up`/`down` still move a
  whole option at a time, and the focused row is kept fully on screen.

## [0.1.0] — 2026-08-12

First release. All 70 tasks of the
[v0 breakdown](docs/history/v0-tasks.md) are complete, and the M1, M2, M4 and
M5 acceptance gates are met.

### Added

- **Daemon** owning all state and execution — SQLite (WAL, single writer),
  git worktrees, and agent CLI subprocesses. Work continues with no client
  attached; crash recovery finalizes interrupted step runs and kills verified
  orphans (PID *and* start time must match).
- **Localhost REST + SSE API** with bearer auth, the single interface every
  client uses.
- **Workflow engine** — YAML registry with builtin < global < project
  shadowing, live reload, and three step types (agent, command, manual) plus
  retries, timeouts and human actions.
- **Bubble Tea TUI** with six views (board, detail, new-task, projects,
  workflows, daemon), holding no state the daemon does not have.
- **CLI subcommands** over the same API.
- **Agent adapters** for Claude Code, Codex and Cursor. Capability differences
  are documented and ignored at run time, never emulated.
- **OS service registration** on Windows (Scheduled Task, as the invoking
  user), macOS (launchd) and Linux (systemd user unit).
- **Signed releases** — cross-compiled archives for linux/darwin/windows on
  amd64/arm64, SHA-256 checksums, a keyless cosign signature over the checksum
  file, and GitHub build provenance attestations.

### Security

- Agents run **full-auto by default** — a documented design decision
  ([spec §16](docs/spec.md)), surfaced once by the TUI on first run.
  Git worktrees isolate collisions between tasks, not privileges.
- The daemon's trust boundary is the OS user: loopback-only listener, bearer
  token stored `0600` in the data directory, and no agent credentials stored.

## Versioning and stability

vincent is `0.x`. Until `1.0.0`:

- **Breaking changes may land in any minor release** (`0.1` → `0.2`), and are
  called out under a `### Changed` heading here with the migration needed.
- **Patch releases** (`0.1.0` → `0.1.1`) are fixes only, with no breaking
  change to the config file, the workflow YAML schema, the REST API or the CLI
  flags.
- The **config file, workflow schema, REST API and CLI surface are not stable
  yet.** Pin a version if you script against them.
- The **on-disk database migrates forward automatically** and is append-only by
  policy (`internal/store/migrations/`); downgrading a binary across a
  migration is not supported.

[Unreleased]: https://github.com/lezli01/vincent/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/lezli01/vincent/releases/tag/v0.1.1
[0.1.0]: https://github.com/lezli01/vincent/releases/tag/v0.1.0
