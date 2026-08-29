# Changelog

All notable changes to vincent are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with the pre-1.0 caveat spelled out
in [Versioning and stability](#versioning-and-stability) below.

Release Please creates release entries from Conventional Commit history. Its
release pull request is the review point for replacing the mechanical commit
list with the user-facing context a commit subject cannot carry.

## [0.7.0](https://github.com/lezli01/vincent/compare/v0.6.0...v0.7.0) (2026-08-29)


### Features

* **agents:** report version and restricted health verdicts ([ff0f741](https://github.com/lezli01/vincent/commit/ff0f741ddb4b3fa75dd1216c500ba28eeb82ce3c))
* **api:** accept an Idempotency-Key on task creation ([17b36bc](https://github.com/lezli01/vincent/commit/17b36bc8b92cffb0336208c0da31dbe0d6130836))
* **api:** refuse a restricted step on an adapter that cannot restrict ([c592593](https://github.com/lezli01/vincent/commit/c59259384c268022c41f878d99ce7218ad2def41))
* **api:** serve the step status message, and accept one ([4aaef4c](https://github.com/lezli01/vincent/commit/4aaef4c89fe4d0810ccb38956b273fc980eda3d2))
* **cli:** a subcommand for every §6 human action ([bbc0e94](https://github.com/lezli01/vincent/commit/bbc0e94a5626cc5c1345d0a00eec7313fe341abc))
* **cli:** add `vincent daemon logs` and `vincent task transcript` ([c415498](https://github.com/lezli01/vincent/commit/c41549834538dd6b1eec0607185d61aadd84a559))
* **cli:** add `vincent workflow init` to write a workflow file ([84dca03](https://github.com/lezli01/vincent/commit/84dca032cbad3b8b9e0dec86cb870190e7f7c76b))
* **cli:** add vincent project rm ([d06460a](https://github.com/lezli01/vincent/commit/d06460ad2f0897010205e23bc90340f94371522c))
* **cli:** add vincent status ([c5b939d](https://github.com/lezli01/vincent/commit/c5b939d0238016715b55fa823d436fa228bc5a09))
* **cli:** read task fields from a file or stdin in `task add` ([5b87208](https://github.com/lezli01/vincent/commit/5b8720886ec0bad792e2feed446bf384d6fb8fe6))
* **github:** create a task from a GitHub issue ([d963a2c](https://github.com/lezli01/vincent/commit/d963a2cab90f0f29ec00efde53ddbdf5c68ec19e))
* **github:** give the issue number a task field of its own ([ca3fc32](https://github.com/lezli01/vincent/commit/ca3fc326575a241fc62283abef538189fbc6b3a7))
* **notify:** run a command when a task enters a configured state ([146a5c3](https://github.com/lezli01/vincent/commit/146a5c3a182b9a359f5ad04c0028712f088765ad))
* **recovery:** guard PID reuse with an exact native process identity ([1bd8a56](https://github.com/lezli01/vincent/commit/1bd8a56355449d9dd872fea908294fdf3316ac1e))
* **release:** sign, notarize and staple the macOS artifacts ([bbf7a81](https://github.com/lezli01/vincent/commit/bbf7a810c1f71349140eb4f5519f1972e40d4fdf))
* **store:** record what a step says about itself ([ca28e4d](https://github.com/lezli01/vincent/commit/ca28e4d9ce913093778d84513d786d869dc31845))
* **taskrun:** accept a step's status, and give agent steps the VINCENT_* env ([0ac9a11](https://github.com/lezli01/vincent/commit/0ac9a11b2c10f3cf0d40ee031b52f80418df9855))
* **taskrun:** cap what one task may spend, enforced at attempt boundaries ([8fd5763](https://github.com/lezli01/vincent/commit/8fd576366101ef40839340cced9c31c407c307cf)), closes [#97](https://github.com/lezli01/vincent/issues/97)
* **tui:** add separate task workspace ([8a63491](https://github.com/lezli01/vincent/commit/8a63491922ea73454e7fa8dcf9be86098f8849e3))
* **tui:** add task details sidebar ([52ed3c4](https://github.com/lezli01/vincent/commit/52ed3c449d5700929a80eedf6b12faef8bae02c2))
* **tui:** select output attempts ([a5575a9](https://github.com/lezli01/vincent/commit/a5575a9a9d5db45513b55d5a7b91c754876ece2f))
* **tui:** show a step's own status, and finally show the result summary ([e72f2f0](https://github.com/lezli01/vincent/commit/e72f2f0f2bc8bd5fba6ba82c50a7fdd8c4498326))
* **workflow:** add `vincent workflow render`, a template dry run ([c1165b6](https://github.com/lezli01/vincent/commit/c1165b6fb0c6465a3c35bef65ba54ea31cfc31fd))
* **workflow:** record where a task's workflow definition came from ([db3a88c](https://github.com/lezli01/vincent/commit/db3a88cda460d35cbe3618f50a690ec0f6e58e69))
* **workflows:** add the prepare-release workflow ([7f05e9b](https://github.com/lezli01/vincent/commit/7f05e9bef81e6e9171aaa4d8ae81fe5134e9b1bb))
* **workflows:** add the update-workflows built-in ([0f1a88e](https://github.com/lezli01/vincent/commit/0f1a88e2e9a99532223163a62403d2c62fb29d30))
* **workflows:** block a release on either changelog, and stop thrashing on pending checks ([69d5a3f](https://github.com/lezli01/vincent/commit/69d5a3fb98b87bd9c432d3883faa1964a92c8a65))
* **workflows:** report step status from the project workflows ([c6ef8f8](https://github.com/lezli01/vincent/commit/c6ef8f82c59fd0505088be1539cab6d41ec46089))


### Bug Fixes

* **api:** bound an answers key as question text, not as an identifier ([0b6fd68](https://github.com/lezli01/vincent/commit/0b6fd681ab706547089167edf6ae5525a30d00f3)), closes [#197](https://github.com/lezli01/vincent/issues/197)
* **apiclient:** bound the probing calls by the adapters, not by loopback ([ff9866d](https://github.com/lezli01/vincent/commit/ff9866d2d1af8fa8e3689547df553a5c44569c9f))
* **gates:** assert m5 scenario 4's restricted refusal at task creation ([85419e8](https://github.com/lezli01/vincent/commit/85419e886b83c1760e7fa2927b53b44d9c7a5315))
* **procx:** end every native identity in the PID it names ([41d2a25](https://github.com/lezli01/vincent/commit/41d2a2598a95fe3f6e9703130693852164979ef0))
* **release:** ship unsigned macOS artifacts instead of failing the tag ([f1aa2ab](https://github.com/lezli01/vincent/commit/f1aa2abcd9063421b2f6a8c368c8b3cfe10b3998))
* **store:** renumber the step-status migration to 0015 ([1c6f767](https://github.com/lezli01/vincent/commit/1c6f767eaf21e35be56bde2e5d7a20b614919003))
* **tui:** wrap attempt timeline previews ([32b7bc0](https://github.com/lezli01/vincent/commit/32b7bc0d39cff74eadd13ef5e5b14e60cd6aeed9))
* **tui:** wrap task timeline header ([f3a002d](https://github.com/lezli01/vincent/commit/f3a002de1e7be3edc12e883b12838e271d55782b))
* **workflows:** pace every retry and poll, and stop a blip from ending a wait ([1f679f4](https://github.com/lezli01/vincent/commit/1f679f462a73db7560d988e3b0cf4345b456c707))

## [Unreleased]

### Added

- **The TUI now opens tasks in a dedicated full-screen workspace.** The home
  view is the task board alone; pressing `enter` opens four full-view tabs for
  Steps & Attempts, Task Details, Output, and Diff. Attempt selection follows
  across tabs, Output can switch between attempts, and entering an attempt from
  the timeline jumps directly to its output. Task Details exposes the complete
  task record through a section sidebar, while long titles, branches, status
  messages, and failed-attempt summaries wrap within the terminal instead of
  overflowing. `esc` returns to the board. See [Using the TUI](docs/guides/tui.md).

- **`vincent workflow render <file>` dry-runs a workflow's templates.**
  `workflow validate` only checks that a template *parses*, so
  `{{.Task.Titel}}`, `{{.Task.Fields.ticket}}` on a task that sets no `ticket`,
  and `{{.Steps.plan.Reslt}}` all passed validation and then failed the moment
  the step rendered — findable only by creating a task and watching it fail.
  `render` executes every template a file declares — `prompt`, `run`, `check`,
  `instructions`, `if` and `for_each` — and prints what each step would send,
  with the resolved agent/model/effort triple and the level that supplied each
  field. It needs no daemon, so it belongs in the same pre-commit hook as
  `validate`. Values a run discovers appear as visible placeholders
  (`<worktree>`, `<steps.plan.result>`), so the output reads as a preview rather
  than as the literal prompt an agent will receive; a field the workflow
  declares `required` binds, an optional one stays absent so a non-defensive
  read is reported. `--title`, `--description`, `--field k=v` and
  `--agent`/`--model`/`--effort` describe a hypothetical task; `--task ID` and
  `--project ID` bind a real one and resolve `include` steps and named fan-out
  lanes through the registry. Exit `0` clean, `1` a template that does not
  execute, `2` no daemon answered a `--task`/`--project`. See
  [the CLI reference](docs/reference/cli.md#vincent-workflow-render).

- **`vincent task add` can take its task fields from a JSON file or from
  stdin.** `--fields-file ./inputs.json` — or `--fields-file -` to read the
  document a `jq` pipeline just produced — fills the same
  [task field map](docs/guides/workflows.md#54-task-fields) that repeatable
  `--field name=value` fills, without every newline, quote and space having to
  survive the shell first. The two combine and `--field` wins the names it
  names, so one generated document can be reused across runs that vary a single
  input. The values must all be JSON strings; a number, boolean, `null`, array
  or object is refused with a message naming the **key** and never the value, as
  are an empty name, anything after the first JSON object, and a read over the
  API's own 4 MiB body bound. Everything else stays the daemon's call —
  required fields, `type`, `pattern` and the per-field limits — and names the
  workflow never declared are still accepted, exactly as before. Creating a task
  without `--json` now also confirms what it carries by **name and count and
  never by value** (`fields: notes, ticket (2)`), which catches a mistyped name
  while staying safe to leave in a CI log. See
  [Scripting vincent](docs/guides/scripting.md#supplying-task-fields).

- **vincent can now tell you it needs you, with nothing open.** Point
  `notify.command` in `config.yaml` at a program and the daemon runs it whenever
  a task enters one of the states you list in `notify.on` — `blocked`,
  `awaiting_gate`, `awaiting_input`, `done`, or any other state from the task
  lifecycle. It writes a JSON envelope to the command's standard input: task id
  and title, the transition, `block_reason`, the project, the workflow, the step
  cursor, the branch and worktree path, and on `awaiting_input` the agent's
  question — enough for a one-line script to write a message without calling
  back into the API. Until now the only alert in the whole system was the TUI's
  terminal bell, which rings on `awaiting_input` and only while a board is open,
  so a task could wait a full day for an answer, fail on the timeout, and the
  first you knew was the next time you looked. `command` is argv, not a shell
  line, so it composes with `terminal-notifier`, `notify-send`, `msg`, a Slack
  `curl` or a file drop without vincent picking a notification stack. It is
  **off unless you configure it**, it hot-reloads like everything else in the
  file, and an unknown state name fails the load naming the value rather than
  silently never firing. Delivery is deliberately modest: root tasks only (a
  fan-out lane does not send its own message), at most four notifiers at once
  drained from a bounded queue, a fixed 10-second budget after which the
  command's process tree is killed, failures logged and never retried, and
  nothing replayed for events that happened while the daemon was down. A
  notifier that hangs or fails changes nothing about the task it was about.
  Documented in the
  [configuration reference](docs/reference/configuration.md#notify) and the
  [security model](docs/security-model.md) — it is code the daemon runs as you,
  and its argv can hold a webhook secret, which is why it is not readable back
  through the API.

- **The daemon log and step transcripts now have command lines.** Both were
  reachable only from the TUI, or by knowing where the files live and reaching
  for `tail` — which is no help over SSH, and none at all when the daemon is what
  is broken. `vincent daemon logs [-n N] [-f]` prints the tail of
  `{data_dir}/logs/daemon.log`, 500 lines by default, `-f` following it. It reads
  the file **from disk and never contacts the daemon**, so it answers when no
  daemon does; that also means it never exits 2, a missing file is an error
  naming the path, and an empty log prints nothing and succeeds. Following is
  safe for the daemon's own rotation: every poll opens, reads and closes the
  file. `vincent task transcript <id> [--step RUN] [-f]` prints one attempt's
  transcript — the complete record `vincent task show` only names the file of.
  `--step` takes the `RUN` column's step_run id, which is unambiguous across
  retries; omitted, it picks the running attempt, else the newest. Output is
  rendered as text for a human, `--json` gives the normalized records as NDJSON
  for `jq`, and `--raw` gives the agent's own JSONL byte for byte. `-f` opens on
  the tail and resumes from the record boundary, ending when that attempt stops
  running. A manual gate, which records nothing, says so and exits 0; a
  transcript whose file was pruned exits 1. Documented in the
  [CLI reference](docs/reference/cli.md), the
  [scripting guide](docs/guides/scripting.md) and
  [troubleshooting](docs/guides/troubleshooting.md).

- **Every human action on a task now has a command line.** `vincent task` grew
  `pause`, `resume`, `skip`, `approve`, `reject`, `retry`, `repair`, `archive`
  and `answer`, and `vincent project` grew `rm` — so a blocked task can be
  rescued from a shell loop, a cron job or an SSH session with no usable
  terminal, instead of only from the TUI or hand-assembled `curl`. This matters
  most when an agent credential expires: every task that reaches an agent step
  blocks on `agent_unauthenticated` once its retry budget is spent, waiting
  fixes nothing, and until now that board could not be cleared from a script.
  `vincent task ls --state blocked --json | jq -r '.[].id'` piped into
  `vincent task retry` now does it. Each action takes one id, carries `--json`,
  and prints the daemon's own post-action view of the task rather than a guess.
  `retry` takes `--branch` (the `branch_exists` recovery: the task keeps its id
  and its transcripts) and `--prompt`/`--run` for edit+retry, each with a
  `-file` twin that reads stdin from `-`; `repair` takes a required `--prompt`
  plus `--agent`/`--model`/`--effort`; `archive` takes `--force` and tells you
  so when it refuses a dirty worktree; `answer` takes `--answer <n>=<value>`
  against the questions `vincent task show` numbers, `--allow`/`--deny` for a
  permission request, or `--body` to post a payload verbatim. `vincent task
  show` now prints the pending input request and the actions the daemon will
  accept right now, so a script can read what is legal instead of probing for
  errors. Documented in the [CLI reference](docs/reference/cli.md#vincent-task)
  and [Scripting vincent](docs/guides/scripting.md).
- **Creating a task can now be retried safely.** If the daemon commits your task
  but you never see the response — a timeout, a dropped connection, a script
  that dies mid-`curl` — re-sending the request used to create a second task, a
  second worktree and a second agent run against the same repository. Send an
  `Idempotency-Key` header on `POST /v1/tasks` and the retry returns the task the
  first request created instead. Same key with a *different* body is refused with
  a `409` carrying `details.reason: "idempotency_key_reused"`, so a key
  accidentally reused for a second operation cannot silently answer with the
  wrong task. Two requests racing with the same key commit exactly one task. The
  key and the task are written in one transaction, so neither can exist without
  the other. Keys are kept for 24 hours and then pruned, they are deleted along
  with the task they name, and `vincent doctor` counts them under
  `database.table_rows`. Nothing changes without the header: the CLI and the TUI
  do not send one, they do not retry a create, and two identical sends still make
  two tasks — which is what pressing enter twice means. Documented in the
  [API reference](docs/reference/api.md#replaying-a-create).
- **Vincent now tells you whether it has ever been tested against the agent CLI
  you have installed.** Each adapter carries the list of builds its parsers were
  captured against, and `GET /v1/agents`, `GET /v1/info`, `vincent doctor` and
  the TUI's daemon view report a `version_verdict` for the build you are running:
  `tested`, `untested`, or `incompatible` for a build vincent knows breaks. It is
  **advisory and blocks nothing** — `untested` is the normal, expected answer a
  few weeks after any vincent release, and it changes nothing about how a step
  runs. The `incompatible` list ships empty for all three adapters, because no
  such build has been observed. Adapter health is now five separately reported
  facets: installed, authenticated, protocol-compatible (`supports_input` plus
  the version verdict), permission-compatible (`restricted_verdict`), and
  model-catalog health, which is the existing `probe_error` and is not
  duplicated. ([#148](https://github.com/lezli01/vincent/issues/148))
- **Every task now records which workflow definition it actually ran.** A
  project or global workflow file shadows a built-in of the same name — that is
  by design, and it includes the `adhoc` a task falls back to when you create it
  without naming a workflow. Until now the task recorded only the *name*, so a
  repository's own `.vincent/workflows/adhoc.yaml` standing in for the built-in
  was invisible on the task forever afterwards. Tasks created from here on carry
  a `workflow_origin`: the scope that won (`builtin`, `global`, `project`, or
  `derived` for a fan-out lane), the source file relative to that scope's root,
  and a SHA-256 of the file's bytes as the registry loaded them. It appears as
  the `origin` row in `vincent task show`, beside the workflow name in the TUI's
  task-detail header, as `workflow_origin` on every task the API serves, and in
  the `task.created` event. It is captured once, at creation, and never
  recomputed, so editing a workflow file does not rewrite the history of tasks
  that already ran it. Tasks created before this change report `unknown` — the
  honest answer; vincent does not look the name up again to invent one.

### Changed

- **A `restricted` step bound for an agent that cannot restrict on this machine
  is now refused when you create the task, not when it runs.** Cursor's
  restricted mode needs its CLI sandbox, which exists on macOS and Linux only;
  on Windows such a step used to reach the engine, start nothing, and fail
  `restricted_unsupported` after spending a worktree, an admission and a retry.
  `POST /v1/tasks` now answers `400 validation_failed` naming the step and the
  agent, and the daemon publishes the `restricted_verdict` its gate uses. The
  answer needs no installed binary — it is a fact about the adapter and the
  operating system — so it is correct even on a machine where the CLI is
  missing. Nothing is downgraded to full-auto, then or now: a restricted mode
  that silently is not restricted is worse than none. The engine's
  `restricted_unsupported` failure stays exactly as it was, for a task whose
  daemon changed underneath it — a data directory carried to Windows, or a
  workflow edited after the task was queued. Retries are not gated; that
  backstop is what catches them.
  ([#148](https://github.com/lezli01/vincent/issues/148))
- **gosec now runs on every build, as part of the existing lint gate.** The
  security linter is enabled inside the golangci-lint the `go.mod` tool directive
  already pins, so `go run mage.go lint` is still one command and still the
  byte-identical one CI runs on Linux, macOS and Windows — a new unsuppressed
  finding now fails the build. Every one of the 39 current findings in production
  code was read individually and either fixed or suppressed with a reason at the
  site; the reasoning is recorded in
  [task 042](docs/tasks/042-gosec-static-analysis.md), and
  [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md) explain how
  gosec and govulncheck divide the work. No runtime behaviour changed and no file
  or directory permission moved: tightening one is a user-visible change that
  needs its own issue, not a side effect of turning a linter on.
  ([#147](https://github.com/lezli01/vincent/issues/147))

### Fixed

- **`vincent doctor` no longer times out instead of answering.** The report asks
  the daemon to probe every agent CLI, and each probe carries that adapter's own
  deadline — up to 145 seconds in total, with cursor's model catalog being an
  authenticated network call. The client gave up after ten, so on a machine
  where a probe was merely slow the command printed `context deadline exceeded`
  rather than the report that would have named the adapter holding it up. The
  two calls whose cost is that probe walk — `vincent doctor` and a forced
  refresh of the agent picker — now wait longer than the adapters can, so the
  diagnosis reaches you. Every other call still gives up after ten seconds: a
  daemon that cannot answer a cached read that fast is wedged.

## [0.6.0](https://github.com/lezli01/vincent/compare/v0.5.0...v0.6.0) (2026-08-25)

### Added

- **The database now reports its own size, row counts and history span.**
  Vincent keeps database rows forever — one `events` row per state change, plus
  the whole workflow YAML on every task — and that is a deliberate decision, but
  nothing measured what it cost, so "rows are small" was untestable on your own
  machine after six months of use. `vincent doctor` now prints the footprint
  *including* the WAL and SHM sidecars (the file size alone understates it
  between checkpoints), the row count for every table biggest-first so the one
  that is growing names itself, the total bytes of stored workflow snapshots,
  and how far back the event history reaches. The TUI's daemon view shows the
  same block, and `GET /v1/info` carries the byte figures for anything else that
  wants them. The counts are read from the schema itself, so a table a later
  version adds is counted without a client change. Purely informational: nothing
  prunes, nothing warns, no threshold exists and no exit code moved — the
  measurement comes first, and any argument about retention now has evidence
  behind it. `GET /v1/doctor` also accepts `?probe=false` to skip the forced
  agent re-probe; the default is unchanged and `vincent doctor` still forces it.
  ([#98](https://github.com/lezli01/vincent/issues/98))

- **`vincent daemon backup` and `vincent daemon restore` — a supported way to
  copy daemon state.** `vincent.db` holds every project, task, step run, cost
  record and transcript pointer, and until now nothing documented how to copy
  it and no command did. The obvious workaround was actively unsafe: under WAL
  a committed row lives in `vincent.db-wal` until a checkpoint, so `cp
  vincent.db` while the daemon runs produces a file missing recent work, and
  copying the three files separately produces a set that can restore into a
  torn database — a backup that looks fine until the day you need it.
  `vincent daemon backup <path.tar.gz>` writes one archive holding a
  `VACUUM INTO` copy of the database, every transcript, `config.yaml`, your
  global workflows and a manifest. The daemon takes it, so it is consistent
  **while tasks are running**, and it prints the bytes it wrote broken down by
  database and transcripts rather than pretending the artifact is small.
  `vincent daemon restore <path.tar.gz>` is the reverse and runs against a
  **stopped** daemon; it refuses a running one, an archive whose schema is
  newer than your binary, and a destination that already holds state unless
  `--force` — which moves the old state aside as `<name>.bak-<timestamp>` and
  deletes nothing. Worktrees are not in the archive (the branches they held are
  in your repositories) and neither is the API token, so a restored
  installation mints a fresh one at next start.
  ([#99](https://github.com/lezli01/vincent/issues/99))

- **Follow-up runs on a finished task, before it is archived.** A `done` or
  `aborted` task still owns its worktree, its branch and its commits until
  `archive` tears them down, and until now vincent could do nothing in that
  window — a branch that needed rebasing onto a `main` that had moved, one more
  commit a review asked for, or a stray file to drop all meant leaving vincent,
  finding the worktree by hand, and coming back only to press archive. The new
  `follow_up` action runs that work inside the daemon's own ledger: give the
  task an agent prompt, a shell command, or the name of a workflow from the
  registry, and it runs on that task's own branch in that task's own worktree,
  with a step run, a transcript, events and token and cost accounting like any
  other step. It is repeatable — each run is a *round* — and it never changes
  the task's verdict: a done task returns to `done` and an aborted one to
  `aborted`, whatever the run did. A follow-up round's rows are recorded past
  the workflow's last step index, so the workflow snapshot does not grow and
  `step k of n` still describes the workflow somebody wrote. A follow-up step
  that fails blocks the task at its own index, where `retry` re-runs the
  follow-up, `repair` sends an ad-hoc agent at *that* failure, `skip` abandons
  it and restores the task's original state, and `cancel` aborts. It is
  available as `POST /v1/tasks/{id}/follow_up`, as `F` in the TUI, and — alone
  among the human actions, because batches want a command line — as
  `vincent task follow-up <id> --prompt/--run/--workflow`.
  ([#181](https://github.com/lezli01/vincent/issues/181))

- **`retry_backoff`: workflow steps can pace their retries.** Retries were
  immediate and nothing could make them wait, so the default `max_retries: 1`
  meant a step hitting a transient problem — a flaky network call, a
  `git index.lock` held by another process — burned both of its attempts inside
  a few seconds and blocked, needing a human even though nothing was wrong with
  the work. A step (or a workflow's `defaults:`) may now carry
  `retry_backoff: 30s`, and the wait costs nothing: the task returns to `queued`
  showing `queued · retry backoff → 14:20`, **gives up its concurrency slot** so
  other work keeps running, and re-runs the step by itself when the wait is
  over. Nothing sleeps and no slot is held. The default is `0s` — an immediate
  retry — so every existing workflow behaves exactly as it did. The wait only
  decides *when* an attempt happens, never *whether*: the attempt still counts
  against `max_retries`, and a step out of budget blocks at once however long
  its backoff.

- **Each agent's usage window is reported in the daemon and TUI.** When an agent
  CLI stops because the account's quota for the window is spent, vincent now
  remembers *which adapter* it was and when it resets, instead of losing that
  the moment the held task moves on. The board header badges the agent
  (`claude ⏳14:20` in place of `claude ✓`), the daemon view names the reset
  beside path, version and login state, and the new-task form warns before you
  queue more work against a spent window. `→` marks a reset the CLI stated and
  `≈` one vincent estimated from `usage_limit_recheck_interval`, so an estimate
  is never presented as a fact. The warning is advisory only: nothing is
  refused, admission and both concurrency caps are unchanged, and the next
  successful step on that adapter clears the badge. `GET /v1/agents` and
  `GET /v1/info` carry it as a nullable `quota` block, and `agent.quota_changed`
  announces every change on the event stream. There is no probe and no
  percentage: no CLI vincent supports can report remaining quota without
  actually running, and vincent reports what it has watched rather than a
  number it would have to invent.
  ([#179](https://github.com/lezli01/vincent/issues/179))

- **Ad-hoc repair agents for a blocked task.** A blocked task now offers a
  `repair` action (`R` in the TUI, `POST /v1/tasks/{id}/repair`) that runs one
  throwaway agent in the task's existing worktree and branch, with the prompt
  you write and the blocked step's failure context around it. It is the escape
  hatch for a block that `retry`, `edit + retry` and `skip` cannot clear because
  the worktree itself is what is wrong. The repair decides nothing: whatever the
  agent exits with, the task returns to `blocked` at the same step with the same
  reason, so you read the diff and then choose. It is recorded as its own step
  run with its own transcript, tokens and cost, shown as its own timeline entry,
  and it does not consume the blocked step's retry budget.

- **The `create-workflow` built-in — a workflow whose deliverable is another
  workflow.** A first workflow no longer has to be written by hand. Its one
  agent step carries the published `vincent-workflows` authoring skill, embedded
  from `skills/vincent-workflows/SKILL.md` at build time, so the skill stays the
  single copy of that guidance and editing it changes the built-in at the next
  build. Two task fields shape the result: `workflow_name` is required and held
  to `^[a-z0-9][a-z0-9._-]*$` — stricter than the schema's rule for a workflow
  name, because the value is also a file name — and `global` picks the registry,
  true writing `{config_dir}/workflows` and false or unset writing the project's
  `.vincent/workflows`. Both are live registry directories rather than the task's
  worktree, since a file left in a worktree would not be a workflow until the
  branch merged. The step runs under `on_input: wait` and the prompt says so: it
  may stop and ask a design question the repository cannot answer, bounded by
  what asking costs under §7.4 — a parked task holds its concurrency slot, and an
  unanswered question fails the step on `input_timeout`.

### Fixed

- **Crash recovery no longer re-queues a task whose previous attempt it could
  not close.** Startup recovery ran as two independent sweeps — finalize every
  open step run, then re-queue every live task — with nothing carrying a failed
  finalize forward into the re-queue decision. A storage failure at that one
  write was logged and walked past, and the owning task went back to `queued`
  anyway: a `queued` task with a step run the database still called `running`,
  which the scheduler then admitted, starting a second attempt against a first
  one that was, durably, still open. Recovery is now atomic per task — the step
  runs, the task transition and its durable event commit together — and
  fail-closed: a task that cannot be reconciled is left exactly as found, and
  the daemon refuses to start rather than running the scheduler over rows it
  knows are contradictory. Restarting the daemon retries recovery, and nothing
  is duplicated if it runs twice. Two guards back it up: admission refuses a
  `queued` task that still has a `running` step run, and `vincent doctor`
  reports the combination under a new `tasks` problem and a
  `tasks.unreconciled[]` list.
  ([#142](https://github.com/lezli01/vincent/issues/142))

- **Reconnecting to the event stream no longer stalls the daemon.** A client
  resuming `GET /v1/events` or `GET /v1/tasks/{id}/events` with a
  `Last-Event-ID` had its whole backlog read into memory in one query, and
  written in full, before the stream went live. Event rows are kept
  indefinitely, so that backlog only grows: on a long-lived installation a
  single reconnect could allocate tens of mebibytes and hold the daemon's one
  SQLite connection for the length of the scan, delaying task transitions and
  every other write behind it. The catch-up now reads in fixed pages up to the
  newest event id at the moment the stream opened, so the memory and the
  connection are held for one page no matter how far behind the cursor is.
  Nothing about what a client receives changed: every event after the cursor
  is still delivered, in id order, exactly once across the hand-off to live
  events. ([#138](https://github.com/lezli01/vincent/issues/138))

- **A request body is now exactly one JSON document, and it is bounded.** The
  daemon read the *first* JSON value in a request body and discarded everything
  after it unread, so a client that framed two documents into one request — a
  retry that re-wrote the body, a `jq -c` loop piped into a single `curl -d @-`,
  a buggy generator — got a `201` for work vincent never saw, with nothing in the
  response to distinguish it from the single-document call. Trailing content is
  now `400 invalid_json`; trailing whitespace stays valid. Bodies are also read
  under a fixed bound instead of into memory whole (64 KiB, or 4 MiB on the
  routes that carry a prompt or a workflow source), over which is a new
  `413 payload_too_large` naming the limit and never echoing the body, and long
  strings, big maps and prompt or run overrides are bounded field by field with a
  `400` naming the field. `POST /v1/workflows/validate` now honours the same
  1 MiB workflow-source limit the registry applies to a file, so a source too
  large to be catalogued no longer validates cleanly. A body labelled a clearly
  non-JSON `Content-Type` — what a plain `curl -d` sends — is `415`; an absent
  header and any `*/json` are still accepted. The daemon additionally sets read
  and idle timeouts, so a client that sends headers and then dribbles a body no
  longer holds a connection indefinitely; SSE streams are unaffected and keep
  their unbounded response lifetime.
  ([#140](https://github.com/lezli01/vincent/issues/140))

- **`config.yaml` is no longer created world-readable.** On Linux and macOS the
  daemon created `{config_dir}/` `0755` and `config.yaml` `0644`, so any other
  local account could read the one file vincent creates that can hold your
  secrets — `environment.set` values are literal, which is where an API token or
  a license key ends up. Both are now created `0700`/`0600`, subject only to a
  stricter umask, and **every daemon start re-tightens an existing installation**
  the way it already re-tightens `{data_dir}/token`: group and other access is
  dropped, owner bits are kept and the file's contents are never rewritten. The
  change is announced rather than silent — the daemon logs the path and the mode
  it found, and `vincent doctor` grows a `permissions` warning row carrying the
  path, the observed mode, the expected mode and the exact `chmod`. That warning
  is not part of the closed set that makes `vincent doctor` exit `1`. Windows is
  unaffected: modes carry no access control there and access comes from the
  per-user ACL `%APPDATA%` inherits. The docs now also say plainly that
  "vincent stores no credentials" is about *vendor* credentials, and that
  `environment.set` is not a secret store.
  ([#141](https://github.com/lezli01/vincent/issues/141))

- **A step whose output vincent could not capture is no longer reported as a
  success.** Command output was read a line at a time with a one-mebibyte
  ceiling; a longer line — minified JSON, a base64 blob, a `git diff` of a
  generated file — stopped capture dead, sent the rest of the stream to
  `/dev/null`, and left the step `succeeded` on its exit code alone, with a
  megabyte of evidence gone and nothing a client could query saying so.
  Over-long lines are now captured in bounded `partial` pieces that rejoin in
  order, so an ordinary big-output command stays a success *with* its output.
  Genuine evidence loss now fails the attempt instead: transcript write, encode
  and close errors (a full disk, a revoked permission, a short write — `Close`
  is checked, because that is where a buffered filesystem reports ENOSPC) fail
  it with the new `transcript_io_error` reason, and a stream an agent adapter
  could not read to the end fails it with `agent_protocol_error` rather than
  blaming the CLI. Both retry normally and neither can be swallowed by
  `allow_failure:`. `transcript_max_bytes` is unchanged and remains the only
  size-based failure. ([#139](https://github.com/lezli01/vincent/issues/139))

- **The repair prompt's transcript excerpt keeps its own bound.** The 256 KiB
  ceiling #139 gave the shared output-tail helper silently halved the 512 KiB
  transcript window the repair prompt had already narrowed its own read to, so a
  repair agent saw half the failure context it was designed to get. The bound is
  now the caller's: the shared helper keeps §8.4's, and a caller that has already
  bounded its own read says so. A silent narrowing is the shape of the bug #139
  is about, so it should not arrive as a side effect of fixing it.

- **Workflow loading is bounded and refuses non-regular files.** A `*.yaml`
  entry in a workflow directory that is a symlink, named pipe, socket or device
  is no longer opened or followed, and a source is capped at 1 MiB. Previously a
  named pipe in a registered repository's `.vincent/workflows/` parked the loader
  in `open()` forever — enough to stop the daemon from starting, hang
  `POST /v1/projects`, or kill hot reload for every scope — and a symlink was
  followed out of the repository into whatever it pointed at. Rejected files are
  listed as invalid entries naming the reason, so the valid workflows in the same
  directory keep working. ([#136](https://github.com/lezli01/vincent/issues/136))

- **Logging in to an agent CLI is noticed within five minutes.** Adapter probe
  results are cached by binary identity, which is exact for the model and effort
  catalogs but only a floor for authentication — nothing about the binary
  changes when you log in, so `logged_in: false` survived on the board, the
  detail view and the new-task form until the CLI was upgraded or
  `?refresh=true` was passed. `vincent doctor` was the only surface that told
  the truth. That one field now has a five-minute freshness window of its own;
  the option catalog is untouched, and a refresh that fails keeps the previous
  answer rather than downgrading a logged-in account to "not authenticated".

- **A human action racing scheduler admission no longer fails with a state
  conflict.** Cancelling or pausing a queued task at the moment the scheduler
  started it returned `409` (`task 3 is running, not queued`) — in the TUI, a
  keypress that appeared to do nothing until pressed again — even though both
  actions are valid from `running` too. An action that loses its compare-and-swap
  is now re-applied once from the state it lost to, when the lifecycle allows it
  from there. Fan-out lanes, which are cancelled the moment their rows appear,
  hit this most. ([#127](https://github.com/lezli01/vincent/issues/127))

- **Simultaneous worktree creation in one project.** Tasks admitted at the same
  moment in the same project no longer fail each other's `git worktree add`
  with a `git_error` block — the daemon now serializes worktree creation and
  cleanup per project, since git leaves its own `.git/worktrees` bookkeeping
  unprotected while an entry is half-built. A `fan_out` step, whose lanes all
  live in the parent's repository and all start together, was the reliable way
  to hit this; a blocked lane left the parent waiting in `awaiting_children`.
  Creation in *different* projects stays parallel. ([#126](https://github.com/lezli01/vincent/issues/126))

- **`vincent task ls` reports each task's branch.** The list row carries
  `BRANCH` in the table and `branch_name` in `--json`, so the documented
  cleanup path — `vincent task ls --archived` — actually names the branches
  vincent made. Branch names are configurable, so a `vincent/*` glob is not
  guaranteed to find them. ([#137](https://github.com/lezli01/vincent/issues/137))

- **Corrected documentation that promised branches are never deleted.** The FAQ
  said archiving always keeps the branch; archiving deletes a branch that has no
  commits past its base, and has since `delete_empty_branch_on_archive` shipped
  defaulting to true. A branch carrying any commit is still never deleted. The
  README, quickstart and scripting guide also claimed full TUI/CLI parity, which
  does not hold for the human actions on a running task.
  ([#137](https://github.com/lezli01/vincent/issues/137))

- **The documentation site renders again.** GitHub Pages builds this repository
  with Jekyll, whose Liquid parser tried to evaluate the Go-template
  expressions that the workflow schema, guide, concepts, troubleshooting and
  bundled reference pages exist to document — breaking those builds. Every
  template-carrying page is now wrapped in `raw` blocks, internal engineering
  records (the spec, tasks, gates and history) are excluded from the site build
  rather than half-rendered, two pages whose names collided on case-insensitive
  filesystems were renamed, and the rendered changelog and contributing pages are
  published and linked from the site navigation.

## [0.5.0](https://github.com/lezli01/vincent/compare/v0.4.2...v0.5.0) (2026-08-22)

### Added

- **The vincent Workflows authoring skill.** A portable agent skill for
  designing workflows: it prefers deterministic commands and the language's
  native control flow over asking an agent, keeps the cost of each step in
  view, and asks about human gates, mid-run interaction, acceptance checks,
  side effects and failure policy before it generates anything. The skill is
  published at `skills/vincent-workflows/`.

- **Workflow-declared task fields.** Workflows can publish ordered task inputs
  with labels, descriptions, required flags, string/integer/number/boolean
  types and Go RE2 patterns. The TUI pre-renders those inputs, the daemon
  validates declared values for every client, and additional undeclared fields
  remain accepted and recorded on the task.

## [0.4.2](https://github.com/lezli01/vincent/compare/v0.4.1...v0.4.2) (2026-08-21)

### Fixed

- **RPM release verification.** Release validation now converts RPM payloads to normalized tar paths before extraction, avoiding GNU cpio's warning exit while still running the packaged binary and keeping extraction inside a temporary directory. ([#161](https://github.com/lezli01/vincent/pull/161))

## [0.4.1](https://github.com/lezli01/vincent/compare/v0.4.0...v0.4.1) (2026-08-21)

### Fixed

- **Release package verification.** RPM payloads are now extracted safely inside a temporary directory during validation, allowing provenance generation and Linux, macOS and Windows smoke tests to complete for published releases. ([#159](https://github.com/lezli01/vincent/pull/159))

## [0.4.0](https://github.com/lezli01/vincent/compare/v0.3.0...v0.4.0) (2026-08-21)

### Added

- **Roomier, responsive TUI workflows.** At terminals 128×24 and larger, task
  creation now uses a six-stage guided takeover layout, while Projects and
  Workflows gain persistent navigation rails and contextual main panes. The
  workflow graph stays alongside its registry, interaction state survives
  resizes, and smaller terminals retain the compact layouts.
  ([#153](https://github.com/lezli01/vincent/pull/153))

- **WinGet, Scoop, mise, deb and rpm distribution.** Stable releases now publish
  vincent's Windows archives through its Scoop bucket and submit them to
  Microsoft's WinGet catalog, attach native deb/rpm packages for x86-64 and
  ARM64, and support mise through its standard GitHub backend. All formats use
  the same checksummed release binaries and preserve the noncommercial license;
  deb/rpm packages install no root-owned service for vincent's per-user daemon.
  ([#158](https://github.com/lezli01/vincent/pull/158))

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

- **Vincent is now source-available and dual-licensed, not MIT.** Personal and
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
* **doctor:** one command that answers "why is nothing running?" ([e0d63c7](https://github.com/lezli01/vincent/commit/e0d63c7f99b8ad04e72c72cb6e64cff49cfac0b5))
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

Vincent is `0.x`. Until `1.0.0`:

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

[0.1.1]: https://github.com/lezli01/vincent/releases/tag/v0.1.1
[0.1.0]: https://github.com/lezli01/vincent/releases/tag/v0.1.0
