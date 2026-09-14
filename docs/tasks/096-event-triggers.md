# 096 — Event triggers: starting vincent work from GitHub, Jira, Trello and CI systems

*Issue [#356](https://github.com/lezli01/vincent/issues/356). Planned 2026-09-10.*

*Renumbered 2026-09-11 from 091, which [the usage-limit switch](091-usage-limit-auto-continue.md)
took on `master` first. Commit subjects on this branch written before the move
say "091"; they mean this document.*

Status: **done (6/6)**.

**Spec:** amends §3 (decision rows 33 and 34), §5.3, §6, §8.4, §12.2, §12.3,
§13.1, §13.2, §13.3, §13.4, §14, §15, §16, §17, §20.

*Amended 2026-09-13:* this line used to read "would amend §2, §3 (a new
decision row), §5, …". §2 is not amended after all: its secret-management
non-goal holds as written, because a `type: http` source names the environment
variable holding its secret and the daemon reads it at the moment of use
(decision 31G). §12.2 joined the list for `{config_dir}/triggers/`.

## Problem

vincent can be driven by a human — the TUI and the `vincent` subcommands — and,
since task [057](057-daemon-mcp-server.md), by an agent over MCP. It cannot be
driven by a **system**.

Every task that exists was created because a person decided to create it. That
is the right default and it is not the whole of the job: the decision "this
issue should become a task" is frequently already encoded somewhere else — a
label on a GitHub issue, a Jira transition into *Ready for Dev*, a Trello card
moved into a list, a Jenkins build that went red on a branch vincent itself
owns. In each case the human has already made the call, in the tool where the
work is tracked, and then has to make it a second time in vincent.

Spec §20 records the shape of the gap: *"Task templates & recurring tasks;
issue-tracker ingestion (Jira → task)"*. Task [035](035-github-issue-selection.md)
promoted the GitHub half of ingestion — a task can be created **from** an issue,
which prefills it and reaches templates as `.Issue`. But a person picks that
issue from a list. This task is the other half, and the difference is the whole
of the problem: **nobody picks.**

## The constraint that shapes every part of this

Decision record rows 1 and 4: single-user, local daemon, localhost-only API,
bearer token from `{data_dir}/token`, no TLS, no accounts, no non-loopback
exposure. §2's last non-goal: secret management — the daemon inherits the user's
environment and vincent stores no credential of its own, which is the property
`internal/github` was built to preserve.

GitHub, Jira and Jenkins therefore **cannot reach the daemon**. A webhook needs a
public callback URL; a developer's machine behind NAT does not have one, and
giving it one is §20's multi-host line, which needs the auth story §20's first
bullet names.

The line this task takes, and the one every decision below follows from:

> **Polling is the default transport. Local ingress is opt-in. Push-in works
> only when the caller runs on the same box.**

A design that ignores this ends up either shipping a tunnel — vincent ships no
`gh`, no container runtime and no notification backend, and will not ship
`cloudflared` — or reversing row 1.

## What this would build

A trigger is a definition with four parts — a source, a filter, an action and a
dedup key:

```yaml
# {config_dir}/triggers/label-to-task.yaml — global scope only (decision 8)
id: label-to-task
enabled: false                 # off by default, per trigger
source:
  type: github_issues          # or: command | http
  project: vincent
  poll_interval: 60s
match:                         # cheap structural prefilter
  action: labeled
  labels: [vincent]
if: '{{ ne .Event.Issue.Author "dependabot[bot]" }}'   # §8.4 template, §7.7 truthiness
action:
  type: create_task
  workflow: feature-pr
  title: '{{ .Event.Issue.Title }}'
  github_issue: '{{ .Event.Issue.Number }}'
on_fire: propose               # the default (decision 7), spelled out here
dedupe_key: 'gh:issue:{{ .Event.Issue.Number }}:label:vincent'
limits:
  max_per_hour: 5
  max_task_cost_usd: 2.00
```

*Amended 2026-09-11:* the first draft of this example read `.Event.Actor` and
`.Event.Label`, filtered on `allowed_actors`, and offered `.vincent/triggers/`
as a location. None of those survive decisions 8 and 10 — a state-diff poller
has no actor, and there is no project scope.

`internal/trigger` is wired in `daemon.Run` beside `internal/notify`, and the
pairing is the point: notify is the daemon's **outward** signal (task
[046](046-notify-hook.md)), trigger is its **inward** one.

## Tasks

- [x] **096.1** Push-in, documented. A `guides/` page and `examples/` entries for
  Jenkins, TeamCity and GitHub Actions calling `POST /v1/tasks` with an
  `Idempotency-Key`. **Zero code** — this works against today's daemon and proves
  the demand before anything below is built. ✓ 2026-09-11 — landed as
  "Starting tasks from CI" in `guides/scripting.md`, beside "Talking to the API
  directly" and "Reacting to events", with the three snippets inline rather than
  in `examples/`: that directory is the embedded workflow set
  (`examples/embed.go`, `//go:embed *.yaml`, the `.goreleaser.yaml` glob), so a
  `ci/` subdirectory would be embedded and released by nothing. Each snippet
  keys on its build's identity (`GITHUB_RUN_ID`, `BUILD_TAG`,
  `teamcity.build.id`), and `TestDocsCISnippetsCreateOnce` (`internal/cli`)
  lifts all three off the page and runs them against a real daemon — the same
  build twice makes one task, the next build another. TeamCity's is a build
  *step* (*Only if build status is failed*), not a build feature: TeamCity has
  no build feature that makes an arbitrary HTTP call without a plugin. No spec
  amendment: nothing here changes behaviour.
- [x] **096.2** `internal/trigger`: the registry, the `type: command` source, the
  `create_task` action, `on_fire: propose`, the delivery ledger, and
  `vincent trigger test`. Depends: 096.1 (for the payload shapes its fixtures
  come from). *2026-09-11:* started together with 096.6 on one branch
  (decision 15). The first piece has landed there: `POST /v1/tasks` grows
  `paused`, `restricted` and `max_task_cost_usd` for every client (decisions 9,
  17, 18), with migration 0028, `vincent task add --paused --restricted
  --max-task-cost-usd` and the new-task form's *start* row. Those three fields
  are not a workflow feature — no field, step type or semantics reaches a
  workflow author — so `skills/vincent-workflows/SKILL.md`,
  `internal/workflow/builtin.go` and `update-workflows`' checklist are
  deliberately not amended, as 065 recorded for its own. ✓ 2026-09-13 — the
  rest landed in the pull request that closed this task (decision 31A): the
  registry with fsnotify live reload, the manager that arms, seeds, polls and
  fires, `triggers.enabled` in `config.yaml`, the `seeded` ledger outcome and
  migration 0030 (decision 31B), `trigger.fired` and `trigger.poll_changed` on
  §13.3's fan-out, the `/v1/triggers` routes with their MCP tools and
  exclusions, and `vincent trigger test` (decision 29). The manager is wired in
  `daemon.Run` beside `internal/notify`, as the opening of this document said it
  would be, and the config applier wakes it on a reload, so turning
  `triggers.enabled` on arms at once rather than on the manager's five-second
  backstop. A poll command gets a fixed **one-minute** timeout, longer than
  notify's ten seconds because a poll is a vendor search over the user's
  network rather than a local toast, and inherits the §12.3 `environment`
  policy like every other child of the daemon.
- [x] **096.3** `type: github_issues` and `type: github_prs`, on the existing
  `github.poll_interval` reconciler tick. Depends: 096.2. ✓ 2026-09-13 —
  decisions 31D, 31E, 31F, 32 and 35. The reconciler asks the manager what each
  project's armed GitHub triggers need, makes at most one issues listing and one
  pulls listing for that project per tick, and hands the result to every
  trigger on it. `internal/github`'s `ListOptions` gains `Since` and `StateAll`
  on both legs (on `gh`, which has no since flag, a
  `--search 'updated:>=… sort:updated-desc'` qualifier); `PullRequest` gains
  `Labels` and `RequestedReviewers` and `Issue` gains `Assignees`, with fixtures
  captured from gh 2.100.0 and REST API 2022-11-28. The snapshot rides in
  `trigger_cursors.cursor`, so there is no migration.
- [x] **096.4** Reaction actions — `follow_up`, `retry` and `cancel` against the
  task whose `branch_name` matches the event's ref. Depends: 096.2. ✓ 2026-09-13
  — decisions 31C, 33 and 34. Each replays its §6 route against the unarchived
  task in `source.project` on the rendered branch (`store.FindTaskForBranch`).
  Under `propose` a follow-up or retry sends `paused: true`, which both routes
  now accept for every client through `internal/taskstate`'s held table;
  `vincent task retry` and `vincent task follow-up` gained `--paused`, and the
  TUI's follow-up form gained the new-task form's **start** row. A `cancel` is
  refused at load unless it writes `on_fire: create`.
- [x] **096.5** `type: http` ingress (`POST /v1/triggers/{id}/events`) with
  per-source HMAC verification. Depends: 096.2. ✓ 2026-09-13 — decision 31G.
  The route stays behind the bearer token and verifies `github_hmac_sha256`
  over the raw body, read under §13.1's 4 MiB tier. A GitHub.com webhook cannot
  deliver through a bare tunnel, because it cannot add the bearer; appendix B
  example 5 and *Explicitly not in scope* are amended to say what does work.
- [x] **096.6** The Triggers takeover in the TUI (§15 view 11, issue
  [#362](https://github.com/lezli01/vincent/issues/362)): list, schema-driven
  create and edit on task 065's form, enable/disable, delete, the ledger, and
  both dry runs, over the write routes, served schema and dry-run routes it
  adds. Takes over *Observability*'s "TUI surface" bullet. Depends: 096.2.
  Decisions 14–30. ✓ 2026-09-13 — reached from the command palette, with
  decision 25's keys, confirmation before any value the served schema marks
  dangerous (decision 19), a banner while `triggers.enabled` is off, and a
  sample event held in session memory only (decision 27). The config form marks
  `triggers.enabled` with task 060's `dangerous` flag (decision 31H).

Spec amendments and the derived documentation pages land in the same pull
request as the sub-task that makes each true, per `docs/tasks/README.md`.

## Decisions

**1 (2026-09-10). A trigger replays an existing route; it introduces no
execution semantics of its own.** Decision record row 28 already settled this
shape for MCP: a call *"replays its arguments as an in-process request against
the same handler, so the §13.1 bounds, the validation, the `409` +
`details.state` envelopes and `Idempotency-Key` hold by construction"* — minus a
five-route destructive-admin exclusion list. Triggers are that pattern a second
time, with a different front door: MCP lets **agents** drive vincent, triggers
let **external systems** do it.

The design line to record in the spec, in these words: *a trigger is a robot
pressing a key a human could have pressed.*

*Beaten:* a trigger subsystem with its own task-creation path. That would put a
second definition of what may happen next beside `internal/taskstate`, which the
architecture invariants name as the single one, and would duplicate §13.1's
bounds and validation at the exact place a hostile payload arrives.

A consequence worth stating up front: triggers inherit row 28's exclusion-list
mechanism, and their list is **not** the same as MCP's. It excludes the five
destructive-admin routes *and* `POST /v1/tasks/{id}/github/pull/create`, which
row 11 and task [069](069-open-a-pull-request-from-vincent.md) decision 2 make
human-only on precisely the reasoning that applies here — "the keypress is the
consent" is only true while a human is pressing it, and under a trigger nobody
is.

*Narrowed 2026-09-11 by decision 9:* where the replayed route lacks an
affordance a trigger needs, the route grows it for every client — `propose` is
`"paused": true` on `POST /v1/tasks` — and the trigger still introduces no
execution semantics of its own.

**2 (2026-09-10). The generic `type: command` source is the primary one, and
ships before any first-party poller.** It is the exact mirror of
`notify.command`: the daemon runs a user-supplied argv on an interval and reads
events from its stdout as NDJSON.

This single source covers Jira, Trello, Linear, Azure DevOps, Gitea, Bitbucket,
TeamCity and Jenkins — every system this task is named for — because all of them
have a `curl`-able REST API and most have a CLI. Jira Cloud has no first-party
CLI, so `curl` against a JQL search with `JIRA_API_TOKEN` from the daemon's
inherited environment is the honest answer; Trello's webhooks require a public
callback URL that Trello validates with a `HEAD` request, so for Trello polling
is not merely easier, it is the only option on a laptop.

*Beaten:* per-vendor adapter packages in-tree. That is a maintenance treadmill
against a posture the product already takes everywhere — vincent drives the
user's `gh`, the user's `claude`/`codex`/`cursor-agent`, the user's container
runtime, and builds, publishes and bundles nothing. It also reopens the secret
question §2 closed: a first-party Jira client wants a Jira credential, and the
moment vincent holds one, "the daemon inherits the user's environment" stops
being true.

The §16 posture is `notify`'s, verbatim: arbitrary code run as the invoking user,
argv that may legitimately carry a secret such as an API token, and the file that
holds it owner-only.

**3 (2026-09-10). The filter is an §8.4 template rendering `true` or `false`,
not a new expression language.** Identical to §7.7's `if:` guards — re-evaluated
every time, never cached. A `match:` block sits in front of it as a cheap
structural prefilter so the common case does not render a template per polled
event.

*Beaten:* a filter DSL. Task [015](015-conditional-steps.md) decision 4 settled
that argument for the whole product, and a trigger filter has no property that
distinguishes it from a step guard.

**4 (2026-09-10). Definitions are YAML in the workflow registry's shape; runtime
state is SQLite.** `{config_dir}/triggers/*.yaml` and `.vincent/triggers/*.yaml`,
builtin < global < project shadowing, live reload, and a
`POST /v1/triggers/validate` route. A trigger names a project, a workflow and a
set of templates — a workflow-sized artifact with an authoring story, not a
settings key.

Poll cursors, the delivery ledger and the dedup rows go in SQLite behind an
append-only migration, with `store.timeFormat` on every timestamp.

*Beaten:* a `triggers:` block in `config.yaml` alongside `notify:`. `notify:` is
one command and a list of states; a trigger carries templates and wants to be
committed to a repository and reviewed. Also beaten: rows in the database as the
source of truth, which would make a trigger un-diffable and un-reviewable.

*Narrowed 2026-09-11 by decision 8:* global scope only — no
`.vincent/triggers/`, no shadowing chain. The YAML/SQLite split stands.

**5 (2026-09-10). Delivery is deduplicated in a table of its own, not by
widening task [040](040-api-idempotency-keys.md)'s retention.** A `dedupe_key`
template rendered from the event feeds the existing idempotency machinery, so
same-key-same-digest replays the task that was created and a different digest is
`409 invalid_state` with `details.reason = "idempotency_key_reused"`.

But 040 fixes retention at **24 hours** deliberately — *"a key exists to cover a
transport retry, which happens in seconds"* — and that reasoning is intact and
must not be disturbed. A trigger's horizon is different in kind: an issue
relabelled next week must not refire. So `trigger_deliveries`, keyed
`(trigger_id, dedupe_key)`, with its own longer retention pruned by the same §17
pass.

*Beaten:* raising 040's 24 hours. That changes the meaning of a bound whose
justification is written down and still true, to serve a caller with a different
requirement.

**6 (2026-09-10). No cold-start flood.** Task 046's rule — *"no replay of events
the daemon did not observe; a weekend of downtime must not produce a
notification storm on the next start"* — applies harder here, because the
consequence of a storm is not a hundred toasts but a hundred agent processes and
a hundred worktrees.

A trigger's first ever poll seeds its cursor to *now* and fires nothing.
Catch-up after downtime is capped, with the truncation logged at warn (§17 logs
four things at warn, each once; this joins them).

**7 (2026-09-11). `on_fire: propose` is the default, everywhere.** A trigger
that does not declare `on_fire` creates its task held, and a human admits it
with one key. `on_fire: create` is always the explicit opt-in, whatever the
action type and whatever the scope.

*Beaten:* the per-action split appendix B example 7 argued for — `cancel` starts
no agent, so it could safely default to `create`. A default that varies by
action is a rule authors must memorise, and the value of "a robot pressing a key
a human could have pressed" is that a human is still the one pressing it. Closes
open questions 1 and 6.

**8 (2026-09-11). Triggers are global-scope only.** `{config_dir}/triggers/*.yaml`;
there is no `.vincent/triggers/`, no shadowing chain and no trust key. This is
the one place the design deliberately departs from the workflow registry that
decision 4 otherwise models it on, and the reason is the supply-chain hole: if a
trigger could be merged into a repository, anyone with merge rights could start
agents on a maintainer's machine. The cost is accepted — a trigger cannot be
reviewed alongside the repository it serves. Decision 4's split (YAML
definition, SQLite runtime state) is otherwise unchanged.

*Beaten:* project scope behind a per-project `trust_triggers: true`, and project
scope always forced to `propose`. Both keep a file an outsider can merge able to
put work on the board. Closes open question 2.

**9 (2026-09-11). `propose` is a new `paused` field on `POST /v1/tasks`, not a
new state and not two calls.** §6 has no "created but held" state and
`taskCreateRequest` (`internal/api/tasks.go`) has no held option, so propose had
no spelling. The route gains `"paused": true`, creating the task directly in
`paused`; `POST /v1/tasks/{id}/resume` admits it. The TUI's new-task form and
`vincent task create` get the same flag.

This is a real widening of the route triggers replay, so decision 1 is narrowed
in place rather than quietly bent: a trigger still replays an existing route and
introduces no execution semantics of its own, and where the route lacks an
affordance the trigger needs, the *route* grows it for every client.

*Beaten:* create-then-pause, because the scheduler can admit the task and start
its agent between the two calls — the exact outcome propose exists to prevent. A
new `proposed` state, as the thing decision 1 was written to avoid: a state costs
`internal/taskstate`, the FSM, the API, the TUI and every §17 aggregate. A
`manual` first step, because it needs the worktree cut and a slot walked first,
it is a property of the workflow rather than of the trigger, and it cannot hold a
`follow_up` or a `retry`.

**10 (2026-09-11). `type: github_issues` is state-diff polling, and it has no
actor.** `internal/github` cannot see issue events: `Client.List` returns issues,
`ListOptions` is `State` + `Limit` only — no `since`, no label filter — and
`gh issue list` has no event stream either. The poller therefore extends
`Client.List` with a `since` cursor, holds the last snapshot per issue, and
synthesizes `labeled` / `unlabeled` / `assigned` from the difference. `Issue`
already carries `Labels`, `Author`, `Assignee` and `UpdatedAt`, so the diff has
everything it needs but one thing.

The thing it does not have is **who**. A diff cannot say which account applied a
label, so `.Event.Actor` does not exist on this source, and `allowed_actors:`
degrades to the issue's *author* — on a public repository, precisely the
attacker-controlled field the allowlist was meant to guard against. That is
stated plainly in §16 and in the source's own documentation rather than papered
over: a control that does not control is worse than an absent one. Appendix B
example 1 is rewritten accordingly, and the issue's "nearly free" claim for
096.3 is corrected by this.

*Beaten, for now:* a real actor from the timeline API — an `internal/github`
method over `/issues/{n}/timeline` and `gh api`, N+1 calls per poll against the
rate limit, and a second capture corpus. Needing a trustworthy actor on this
source is the named trigger for reopening it.

**11 (2026-09-11). `.Event` reaches trigger templates only; it is never
snapshotted onto the task.** The action's `title`, `description`, `prompt`,
`fields` and `dedupe_key` render at fire time inside `internal/trigger`, and the
event is then gone. Whatever a workflow needs arrives inside the rendered title,
description and fields, exactly as it would from a human. **§8.4 gains no
`.Event` root** — the issue's claim that it becomes one alongside `.Task`,
`.Issue` and `.Loop` is wrong on its own terms, because those three are read
from the task row and an event is not on it. This is decision 1 taken literally:
from the step path down, a triggered task is indistinguishable from a
hand-created one.

*Beaten:* snapshotting the event the way task 035 snapshots an issue. It costs a
column, a migration, and §5.3 and §8.4 edits, and it pipes remote
attacker-controlled text into every step template of the run rather than only
the ones the trigger's author wrote.

**12 (2026-09-11). A triggered task defaults to `restricted`; `container:` is
advisory.** *Corrected 2026-09-11 by decisions 17 and 23:* the Windows cursor
case is refused at creation, not failed at its step, and "advisory" means
documentation — there is no trigger-level `container:`. A triggered task's agent steps run in `restricted` permission mode
unless the trigger says otherwise, and `container:` (§16, task 061) is
documented as the real control and recommended for every `on_fire: create`
trigger — never required.

One consequence is stated rather than discovered: **cursor cannot honour
`restricted` on Windows**, returning `agent.ErrRestrictedUnsupported` from
`Start`, so a triggered task on a Windows cursor installation fails its step
under this default. That is the documented adapter difference behaving as
designed, and the trigger's `permission:` override is the escape.

*Beaten:* requiring `container:`. Task 062 (agent steps in containers) is
planned and unstarted, so gating unattended triggers on it would mean
`on_fire: create` cannot ship until that work does. Closes open question 3.

**13 (2026-09-11). Both bounds are fixed values, not config keys.**
`trigger_deliveries` retention is 30 days, pruned by the same §17 pass; catch-up
after downtime is capped at 20 events per trigger per poll, with the truncation
logged at warn alongside §17's existing four. This matches how task 040's 24
hours and §13.1's body bounds are fixed — a bound with a written justification,
not a knob — and the numbers move when someone hits them. Decisions 5 and 6 are
unchanged.

*Beaten:* a config key for each, which asks every user to choose a number nobody
has yet needed to change. Closes open questions 4 and 5.

**14 (2026-09-11). The TUI surface is 096.6, a §15 view 11 takeover** (from
#362). Reached from the command palette like Workflows and Projects. *Beaten:*
a section of the projects view, which would suggest a project scope decision 8
ruled out; and a tab of the workflows view, when a trigger has its own status
and ledger. Recorded here rather than as a new task document, which also avoids
a number race with #363's sibling skill.

**15 (2026-09-11). 096.2 and 096.6 land in one pull request** (author), in
dependency order: the `POST /v1/tasks` widenings, `internal/trigger`, config,
events, routes, CLI and MCP, then the view, then the gate and the docs. The
repository merges with merge commits, so that order is `master`'s history, and
a partial branch stays coherent — the widenings are useful on their own.

**16 (2026-09-11). Cursors follow the file, and re-arming seeds to now**
(author). A trigger is *armed* when its file is present and valid, `enabled:
true`, and `triggers.enabled` is on; only an armed trigger polls. Its first
poll after arming — from `enabled` or from `triggers.enabled` — seeds the cursor
and fires nothing, so events from an off period never fire: decision 6 extended
to disarming. The cursor is dropped when the file leaves the registry by any
route (the API's delete, `rm`, `$EDITOR`, #363's built-ins); a present but
invalid file keeps it; a reload that cannot read the directory is not every
file gone. Across a restart the cursor persists, so a restart is decision 6's
capped catch-up, not a re-seed. *Beaten:* cursors keyed by id and outliving the
file, which would let a re-created trigger fire a backlog nobody armed.

**17 (2026-09-11). The permission override is a one-way `restricted` clamp**
(author). `restricted: true` on `POST /v1/tasks`, for every client, snapshotted
on the task, forces every agent step to `restricted` — including one whose own
field says `full-auto`. It is applied after §8.6/§9.4 resolution
(`workflow.ClampedPermissionMode`), not as a level in the chain, because a step
field would otherwise beat it; it can never make a step looser than its
workflow wrote it. A trigger's `permission:` is `restricted` (default) or
`workflow`. *Corrects decision 12:* task 041's creation gate evaluates the
clamped mode through the same function the engine calls, so on a Windows cursor
installation a clamped task is refused at creation with `400` — the delivery
lands as `refused` — rather than failing its step. §9.4's "no daemon-global
hardcoded policy" stays true, because the clamp is per task. *Beaten:* a
`permission_mode` value on the create route, which would be a looser-or-tighter
override and a new level in §8.6's chain.

**18 (2026-09-11). The cost cap is per task, with no default** (author).
`max_task_cost_usd` on `POST /v1/tasks`; the engine blocks `cost_limit` at the
lower of the task's and config's, 0 on either side meaning "no cap from this
side", so a task cap can tighten the global and never lift it. A trigger sends
one only when `limits.max_task_cost_usd` is written. *Narrows the security
section:* "defaulting tighter than a hand-created one" is dropped, for decision
13's reason — nobody has needed a number yet. The cap stays inert on codex and
cursor, which report no cost.

**19 (2026-09-11). Dangerous values are marked in the served schema**
(author): `enabled: true`, `on_fire: create` and `permission: workflow` each
carry warning text, and the TUI asks before committing any marked value from
the form or the toggle key. Values 096.3–096.5 add get a confirmation without
TUI changes. Disabling never asks. The config editor's TUI-local `dangerous`
flag (task 060) is unchanged.

**20 (2026-09-11). Trigger files are always written 0600**, new or existing —
`config.WriteFile`'s rule, not workflows' "an existing file keeps its mode".
065 decision 8 kept a mode because a repository owns a project workflow;
decision 8 means no repository owns a trigger, and decision 2 makes the file
owner-only.

**21 (2026-09-11). Delete is a scoped departure for triggers** (from #362) from
065's "no destructive action on a registry file": ledger rows kept (so a
re-created id cannot refire a delivered event), cursor dropped by decision 16,
version token required. A trigger is global-scope, belongs to the machine's
user, and is not a file a repository shares. Workflows keep 065's rule, and
§15 view 5 is not amended.

**22 (2026-09-11). MCP excludes the three write routes** (from #362; task 057
decision 4, task 065 decision 5). `POST`, `PATCH` and `DELETE /v1/triggers…`
join `mcp.Excluded`: an agent must not author or arm a trigger that starts
agents, and enabling is a `PATCH`. The reads, `validate`, `test` and `poll` are
ordinary tools — a dry run fires nothing, and `poll` runs only a command the
user already configured, which a full-auto agent could run anyway (§16: MCP is
not a security boundary).

*Narrowed 2026-09-14 by task [098](098-trigger-authoring-skill-and-builtins.md)
decision 2:* "an agent must not author or arm" now reads **a built-in a human
started may author a disarmed trigger file; arming stays human-only.** Arming is
any of `enabled: true`, the global `triggers.enabled`, `on_fire: create` and
`permission: workflow`. The built-ins write through `vincent trigger apply`,
which refuses every arming change, not through these routes, so the MCP
exclusion list is unchanged.

**23 (2026-09-11). No trigger-level `container:`** (settled from the code).
Containers resolve from a workflow's `defaults.container` and `config.yaml`
(task 061), agent steps cannot run in one until task 062, and a trigger-level
block would need a fourth task-level widening while doing nothing a workflow's
own `container:` does not. Decision 12's "advisory" therefore means
documentation: recommend an `action.workflow` whose steps are containerized.
Appendix B example 2 is amended.

**24 (2026-09-11). Poll health is published on transitions, not per poll.**
`trigger.poll_changed` fires on the first poll, ok → failing and failing → ok;
the view's own timer keeps "last poll" times current. *Beaten:* a durable event
per poll, a row in the events table every `poll_interval` for every trigger.

**25 (2026-09-11). The view's keys** (from #362, task 093's vocabulary): `a`
create, `i`/`enter` form, `e` `$EDITOR`, `D` delete after asking, `space`
toggle `enabled` (the answer and create-PR forms' toggle rows), `R` re-read,
`/` filter, `tab` to the ledger where `enter` opens a delivery's task (the
daemon view's list/log split). The dry-run, live-poll and banner keys are
picked at implementation from keys the vocabulary leaves free.

**26 (2026-09-11). `trigger_deliveries` gains a nullable `task_id`**, which
*Observability*'s column list lacked and the view's `enter` needs.

**27 (2026-09-11). The dry-run sample event is session memory only** — never
written to `tui.json`, which holds view state; a vendor payload may carry
sensitive text.

**28 (2026-09-11). Rate-limited events are recorded and dropped, never
queued.** `limits.max_per_hour` counts `fired` rows in the trailing hour.

**29 (2026-09-11). `vincent trigger test` is a remote command** over
`POST /v1/triggers/{id}/test`: "would the ledger dedupe it" needs the daemon's
database, unlike the purely local `vincent workflow render`.

**30 (2026-09-11). `internal/trigger` takes an `http.Handler` and imports
`internal/workflow`, never `internal/api`** — the `internal/mcp` shape.
`internal/api` imports it for the registry, schema, writer and dry run.

**31 (2026-09-13). Settled in the evaluation of issue
[#365](https://github.com/lezli01/vincent/issues/365).** Eight calls, cited
from the code as 31A–31H.

*31A. Scope* (author). The five remaining sub-tasks, which are the rest of 096.2
plus 096.3, 096.4, 096.5 and 096.6, land in one pull request, and that pull
request closes this task. This **widens decision 15**, which bundled only 096.2
and 096.6. Decision 15 is not edited: its reasoning about order still holds,
and the branch follows it. Config, registry, poller and events come first, then
routes, CLI and MCP, then the view. After those come the GitHub sources, the
reactions with their route widenings, the ingress, the gate, and last the
records.

*31B. Seeding records the ledger* (author). Decision 6's seed fires nothing, and
for a `type: command` source that keeps no cursor that is not enough. Poll 2
sees the events poll 1 saw, the ledger holds nothing for them, and every one of
them fires. So the first poll after arming writes one ledger row per event it
returned, with a new outcome, **`seeded`**, keyed by the event's rendered
`dedupe_key`. A seed runs no `match:` and no `if:`, and no catch-up cap applies,
because recording is not firing and every event the source showed before arming
must be covered. An event whose key does not render is logged at warn and
skipped. `store.TriggerKeyDelivered` treats `fired` **or** `seeded` as
delivered, while `CountTriggerFiredSince` still counts `fired` alone, so a seed
spends none of the hourly limit. Migration 0030 rebuilds `trigger_deliveries`
to widen the `outcome` CHECK, because SQLite cannot alter one in place. It
renames the old table aside, creates the new one, copies the rows, drops the
old table and recreates the three indexes. 0029 is not edited. The seed still
stores the command's cursor line, or `""` when it prints none. GitHub sources
seed through their snapshot (31D) rather than through ledger rows, and
`type: http` has no poll, so it has no seed. *Beaten:* a seed that stores a
cursor and nothing else, which is exactly the flood above; and a seed capped at
20, which lets event 21 of a pre-arming backlog fire on poll 2.

*31C. Reactions under `propose`* (author). Decision 9's pattern again: the
affordance goes on the route, for every client. `POST /v1/tasks/{id}/follow_up`
and `/retry` accept `"paused": true`, the task lands in `paused` instead of
`queued`, and `resume` admits it. `internal/taskstate` gains a second, *held*
table beside the first (`NextHeld` and `CanHold`), with `retry` from blocked to
paused, and `follow_up` from done or aborted to paused. These are not new
actions, because a held follow-up is still a follow-up: a 409 still names
`follow_up`, and `available_actions` must not list a second spelling. A held
retry from `awaiting_children` is a **400**, not a 409. That retry is task
090's cascade: it writes nothing to the parent and never queues it, so there is
no `queued` for a hold to replace, and the request is one the caller can
correct. `vincent task retry`, `vincent task follow-up` and the TUI's follow-up
form get the flag the way 096.2's widenings did. MCP picks it up by replaying
the same routes.

A `cancel` trigger is refused at load unless it writes `on_fire: create`. With
`on_fire` absent the default is propose, and there is no held cancel to
propose. Appendix B example 7 already carried that line "because it is
required", and examples 4 and 5 stay valid.

Target resolution for `target: branch` looks for the task in `source.project`
whose `branch_name` equals the rendered `branch:`. It ignores archived tasks,
and takes the newest when more than one row holds the name
(`store.FindTaskForBranch`). With no match the delivery is `refused`, and its
detail names the branch; having nothing to act on is the route's own 404 in
spirit. An FSM-invalid target gets the route's own 409, recorded `refused` with
the envelope as its detail, which is example 5's wording. The ledger's `task_id`
is the task acted on (#362: "created or acted on"). A reaction refuses
`workflow`, `title`, `description`, `fields`, `github_issue`, `github_pull`,
`permission` and `limits.max_task_cost_usd` at load. Each of those is a
property of a task being created, and a key that is set and then ignored is a
control that does not control. `Idempotency-Key` rides only a `create_task`
replay: a reaction's dedupe belongs to the ledger alone, and a header its route
ignores would be a false claim.

*31D. GitHub sources ride the reconciler tick* (author). `type: github_issues`
and `type: github_prs` are judged on `internal/daemon/pullreconcile.go`'s
`github.poll_interval` tick. Each project with an armed GitHub trigger gets one
listing per kind per tick, shared by every trigger on it, so API use does not
grow with the number of triggers. `source.poll_interval` on a GitHub source is
refused at load, and examples 1, 6 and 7 drop it. The daemon owns the fetch:
`internal/trigger` defines a `GitHubLister` and receives listings, and never
imports `internal/github`. `ListOptions` gains `Since` and `StateAll` on both
legs. A listing asks for `state: all`, so close and merge transitions are
visible, with a limit of 100. It starts two minutes before the lowest watermark
on the project, because the `gh` leg answers through the search index, which
lags writes, and the diff is idempotent over an unchanged item.

The per-trigger snapshot is JSON in `trigger_cursors.cursor`, which is opaque
by design: `{kind, seeded_at, watermark, items}`. Each item, keyed by number,
holds the state, labels and assignees, plus draft, merged and requested
reviewers for a pull request, and its `updated_at`. A restart is therefore
decision 16's capped catch-up diff, not a re-seed, and no migration is needed.
The first tick after arming stores the snapshot and fires nothing. An item the
snapshot has never seen fires `opened` only when it was created after the seed.
An older one, beyond the seed listing's limit, becomes a baseline, because
calling it "opened" would be a lie. Closed items not updated for 30 days (the
ledger's horizon) are pruned, so a busy repository's cursor does not grow
without end.

`github_issues` synthesizes `opened`, `closed`, `reopened`, `labeled`,
`unlabeled` and `assigned`. `github_prs` synthesizes `opened`,
`ready_for_review`, `review_requested`, `closed` and `merged`, with `gh`'s
`MERGED` state normalized to REST's `closed` plus `merged: true`. There is no
`.Event.Actor` (decision 10). An unknown `match.action` value on a GitHub source
is refused at load.

The trigger's poll status reads failing, with the reason, in each of these
cases: `github.enabled` is off, `github.poll_interval` is `0`, the project's
`origin` is not a github.com repository, or the listing failed. It is never
quiet. That is the opposite of the reconciler's own debug-level failure policy,
and deliberately so: a trigger that silently never fires is exactly the
question the ledger exists to answer.

*31E. `review_requested` is in* (author). `internal/github.PullRequest` gains
`RequestedReviewers` on both legs, holding user logins only (a team request is
not a login and is excluded), and gains `Labels`. The new fixtures are named for
the gh version and the REST API version they came from. *Narrowed the same day
by decision 32:* example 6 works, but only with `allowed_actors`.

*31F. Trusted events and `allowed_actors`.* An event is **trusted** when the
change behind it is one an outsider cannot make on a public repository. Each
event was checked against GitHub's repository role table: issue `labeled`,
`unlabeled` and `assigned` need the triage role, and pull-request `merged` needs
write. Every other event is untrusted:

- `opened` and `reopened` are the author's, on issues and pull requests alike.
- `closed` is too, because an author may close their own.
- `ready_for_review` is the author's.
- `review_requested` is untrusted per decision 32.

A GitHub trigger with no `allowed_actors` is **refused at load** when its
`match.action` could match an untrusted event, and the refusal names that event.
That covers both a `match.action` that lists an untrusted event and one that is
absent, since an absent one matches every event the source has. On these
sources `allowed_actors` matches the **author** of the issue or pull request,
case-insensitively, because that is the only identity a state diff has. §16 and
the guide say so in those words. The key is refused on `command` and `http`
sources, whose events carry no identity vincent can verify. *Beaten:* arming
such a trigger with a warning, which is the "control that does not control"
that decision 10 refused.

*31G. HTTP ingress requires the bearer and a signature* (author).
`POST /v1/triggers/{id}/events` stays behind §13.1's bearer token like every
other route, and additionally verifies the trigger's own signature. Only a
caller on this machine that can read `{data_dir}/token` **and** holds the shared
secret can deliver, and no auth exemption is added to `authMiddleware`. These
consequences are stated here rather than left to be discovered:

- **A GitHub.com webhook through a bare tunnel cannot deliver**, because GitHub
  cannot add the bearer header. Example 5 is amended to a self-hosted runner, or
  to a same-box relay that adds the header, and the tunnel bullet under
  *Explicitly not in scope* is narrowed the same way.
- `source.signature.scheme` is a closed set holding `github_hmac_sha256` alone.
  That scheme is `X-Hub-Signature-256`: `sha256=` followed by the hex HMAC-SHA256
  of the raw body. The secret is read from `secret_env` in the daemon's
  environment at the moment of use (§2) and compared in constant time. The
  schema is shaped so that a later scheme is one more value.
- The handler reads the raw bytes under §13.1's **4 MiB** tier, because a webhook
  payload routinely exceeds 64 KiB. It verifies them, then decodes. A body over
  the bound is 413.
- The route checks, in this order:
  1. An unknown id is 404.
  2. A body over the bound is 413.
  3. A valid file whose source is not `type: http` is 400.
  4. A trigger that is not armed (disabled, `triggers.enabled` off, or a file
     that does not validate) is 409 `invalid_state` with `details.reason`.
  5. A bad or missing signature, or an unset secret variable, is 401 with no
     ledger row. The three are indistinguishable on purpose.
  6. A body that is not a JSON object is 400, and so is an event with neither a
     string `id` nor an `X-GitHub-Delivery` header to take one from.

  An event that passes all of these runs the pipeline a poll runs, with no
  catch-up cap for a single push, and the answer is the delivery. Pushes to one
  daemon are judged one at a time, so two deliveries of one key cannot both
  pass the dedupe lookup.
- The route joins `mcp.Excluded` beside decision 22's three writes, on decision
  22's reasoning: an agent that can inject events can start agents.

*31H. Smaller calls, settled from the recorded design.*

- **Dry runs.** The two dry runs are #362's.
  `POST /v1/triggers/{id}/test` judges a supplied event and writes nothing.
  `POST /v1/triggers/{id}/poll` runs the source once for real and judges what it
  returns, with no fire, no cursor advance, no ledger row and no change to poll
  health; it is a 400 for `type: http`. Both work while the trigger or
  `triggers.enabled` is off, since neither fires anything.
- **`triggers.enabled`** (§12.3) defaults to `false` and is hot-reloaded. It is
  editable over `PATCH /v1/config`, which is already MCP-excluded, and the
  config form marks it with task 060's `dangerous` flag. Turning it on re-arms
  every enabled trigger with a fresh seed (decision 16).
- **Events.** `trigger.fired` publishes only for a `fired` delivery, carrying the
  project and the task created or acted on. `trigger.poll_changed` publishes on
  the first poll after arming and on each transition between ok and failing
  (decision 24). No other outcome publishes, because the view refreshes its
  ledger on its own timer.
- **CLI and MCP.** The CLI gains exactly `vincent trigger test --event
  fixture.json` (decision 29). The reads, `validate` and both dry runs stay MCP
  tools (decision 22).
  *Narrowed 2026-09-14 by task [098](098-trigger-authoring-skill-and-builtins.md)
  decision 3:* "exactly" no longer holds. The CLI also gains `vincent trigger
  validate <file>` and `vincent trigger ls --project <id>`, which need no
  daemon, and `vincent trigger apply --proposal <task_id> --project <id>`,
  which installs a staged proposal and refuses any change that arms a trigger.
  The MCP tools are unchanged.

**32 (2026-09-13). `review_requested` is untrusted, which narrows decision
31E.** Requesting a reviewer by hand needs the triage role. On that reading the
event would be trusted, and appendix B example 6 would work as written. But
CODEOWNERS requests reviews **automatically** when an outsider opens a pull
request that touches owned paths. So an outsider can cause the reviewer field
to be set, and a trigger matching `reviewer: lezli01` would start a review task
for any stranger's pull request. The event is therefore untrusted under 31F's
rule that an event which cannot be confirmed is untrusted, and example 6 gains
`allowed_actors`. *Beaten:* trusting it and documenting CODEOWNERS as a caveat,
which leaves the unsafe choice as the default.

**33 (2026-09-13). A held retry holds its cascade too.** A retry on a `blocked`
fan-out parent does two things: the parent's own move from `blocked` to
`queued`, then task 090's cascade over the blocked lanes beneath it. With
`paused: true` the parent lands in `paused`, and every lane the cascade
re-admits lands in `paused` as well; `retried_descendants` counts them.
`paused: true` promises that the call starts nothing anywhere in the tree. A
parent held while its lanes were admitted would break that promise for every
lane. *Beaten:* holding the parent alone, which reads as held on the board while
agents start underneath it.

**34 (2026-09-13). An aborted-origin follow-up that passes through `paused`
needs no change to task 027's `Restore`.** Checked, as 31C required. `paused`
is not a settled state, so the store keeps the follow-up request (its origin,
round and cursor) through the hold and through `resume`. The admission after
that runs the follow-up and ends it the way an unheld one ends: through
`Restore` (from `running` to `aborted`) for an aborted origin, and through
`Complete` for a done one. One consequence is recorded rather than fixed.
Cancelling a held follow-up on a **done** task leaves it `aborted`, under task
027 decision 8's rule that `cancel` means what it always means. A hold can now
make the wait before that choice indefinite, where before it lasted only as
long as the scheduler took to admit the task.

**35 (2026-09-13). The reconciler's own listing is untouched.** A trigger
listing is a separate call from task 052's open-pull-request listing, even on a
project that has both. That listing is open-only and shaped around links; a
trigger's lists every state and is bounded by a watermark. Merging them would
make the link reconciler's correctness depend on whether a trigger happens to be
armed. A project's GitHub cost per tick is therefore task 052's call plus at
most one issues listing and one pulls listing, however many triggers it has. The
reconciler's quiet failure policy is also unchanged for its own call; only the
trigger half reports failures (31D).

## Security: this inverts §16, and it is the substance of the work

Every agent run in vincent today traces to a human keypress or a human-authored
workflow. §16's posture — agents run full-auto by default, an agent can run
arbitrary commands as the invoking user, and the worktree isolates collisions
rather than privileges — is defensible **because you pressed the key**.

A trigger breaks that chain. A third party labelling an issue causes arbitrary
code to run as you. That is not a reason not to build this; it is the reason the
defaults here must differ from every other default in the product, and the §16
amendment is the most important edit this task makes.

- **Off by default**, both per trigger (`enabled: false`) and globally
  (`triggers.enabled`). Two keys — unlike task 069, where decision 2 held that
  the keypress and the editable popup in front of it *are* the consent and a
  second config key nobody would turn on adds nothing. Here there is no
  keypress, so the config key has to be it.
- **`on_fire: propose`** creates the task held, for a human to admit with one
  key on the board — §6's gate shape reused rather than a new state invented.
  `on_fire: create`, fully unattended, is the explicit opt-in. `propose` is the
  default everywhere (decision 7), spelled `"paused": true` on the create route
  (decision 9).
- **`allowed_actors:`.** On a public repository, issue creation and comments are
  attacker-controlled; labelling and assignment generally require write access.
  Each source documents which of its events are trusted, and a trigger on an
  untrusted event with no actor allowlist should be refused at load rather than
  silently armed. On `type: github_issues` there is no actor at all, and the
  allowlist degrades to the issue's author (decision 10). *Settled 2026-09-13
  by decisions 31F and 32:* the trusted events are issue `labeled`, `unlabeled`
  and `assigned`, and pull-request `merged`. A GitHub trigger that can match
  any other event must name `allowed_actors`, which matches the author, or it
  is refused at load. The key is refused on `command` and `http` sources.
- **Rate limits** per trigger, and a `max_task_cost_usd` (task
  [033](033-task-cost-cap.md)) on triggered tasks defaulting tighter than a
  hand-created one. *Narrowed 2026-09-11 by decision 18:* the cap is per task
  with no default — a trigger sends one only when `limits.max_task_cost_usd` is
  written.
- **Prompt injection becomes remote.** Issue body → `.Event` → agent prompt →
  full-auto shell. The exposure exists already through `.Issue` (task 035), but
  there a human chose the issue and read it. Container execution (§16, task
  [061](061-container-step-execution.md), with agent steps arriving in task
  [062](062-agent-steps-in-containers.md)) stops being a convenience here and
  starts being the control — decision 12: `restricted` by default, `container:`
  advisory.
- **Project-scope triggers are a supply-chain hole.** If triggers shadow the way
  workflows do, anyone who can merge to `.vincent/triggers/` can start agents on
  a maintainer's machine. Decision 8 closes it: there is no project scope.

## Observability

"Why did my trigger not fire?" is unanswerable without a ledger, and polling
makes it worse: there is no delivery receipt on the sending side to go and check.

- **`trigger_deliveries`**: `(trigger_id, event_id, dedupe_key, received_at,
  outcome, action_json)`, where `outcome` is one of `fired`, `deduped`,
  `filtered`, `rate_limited`, `refused`, `error` — and the rendered action for
  the ones that fired. *Amended 2026-09-13 (decision 31B):* and `seeded`, for an
  event a seed poll was shown.
- **A `trigger.fired` event** on §13.3's fan-out, published post-commit like
  every other.
- **A TUI surface** — a triggers view, or a section on the projects view —
  showing last poll, last fire and the recent ledger. *Taken over 2026-09-11 by
  096.6 (decision 14):* a takeover of its own.
- **`vincent trigger test --event fixture.json`**, which renders the action that
  *would* be taken and creates nothing. A direct mirror of `vincent workflow
  render` (task [044](044-workflow-render-preview.md)), and it is what gives the
  package table-driven tests against captured real payloads in `testdata/`,
  named for the vendor and API version they came from — exactly how the adapters
  are tested against captured CLI output.

## Explicitly not in scope

- **Per-vendor adapter packages in-tree.** Decision 2.
- **A filter expression language.** Decision 3.
- **Storing a vendor credential.** §2's secret-management non-goal, kept the way
  `internal/github` keeps it.
- **Shipping a tunnel or a relay.** §20's multi-user / remote-daemon / fleet
  line. `cloudflared` and `smee.io` are documented for `type: http`, the way §20
  documents `terminal-notifier` and `notify-send` for `notify:` — one line away,
  not bundled. *Narrowed 2026-09-13 by decision 31G:* a tunnel on its own
  delivers nothing from a sender that cannot add a header. The ingress needs
  the daemon's bearer token as well as the signature, so GitHub.com's webhook
  cannot use one directly. What does work is either a sender on the daemon's
  machine, such as a self-hosted runner, or a relay on this machine that
  receives from the tunnel and adds the bearer before forwarding.
- **Scheduled and recurring triggers.** Adjacent — §20's "task templates &
  recurring tasks" — and deliberately out. The `action:` block should be
  designed so a later `type: schedule` source reuses it verbatim; that is the
  named trigger for reopening it.

## Open questions

These block **096.2** and should be settled as decisions in this document before
it starts.

*Settled 2026-09-11:* all six — 1 and 6 by decision 7, 2 by decision 8, 3 by
decision 12, 4 and 5 by decision 13. Kept as they were asked.

1. **Is `propose` or `create` the default `on_fire`?** `propose` keeps the human
   in the chain and is safe, but a trigger that still needs a keypress is
   arguably a nicer inbox rather than automation, and unattended is what people
   are asking for. A possible split: `propose` forced for project-scope
   triggers, `create` available to global-scope ones the machine's owner wrote.
2. **Global scope only, or global plus project with a trust opt-in?** Candidates:
   global only; project behind a per-project `trust_triggers: true`; or project
   scope always forced to `propose` whatever it declares.
3. **Does a triggered task default to `restricted`, require a `container:`
   block, or neither?** Requiring a container couples this task to 062, which is
   itself unstarted.
4. **Is `trigger_deliveries` retention a config key or a fixed value**, the way
   040's 24 hours and §13.1's body bounds are fixed?
5. **How much catch-up after downtime** — none, N events, or a time window?
6. **Is the `on_fire` default global, or per action type?** Raised by appendix B
   example 7: `cancel` starts no agent and spends nothing, so the risk that makes
   `propose` the safe default for `create_task` does not apply to it uniformly.

## Appendix A — the `type: command` envelope

The generic source (decision 2) is only as good as the contract between the
daemon and the command it runs, so that contract is fixed here rather than left
to the implementation.

**The command emits NDJSON on stdout, one event per line.** Every line is an
object; the whole object lands under `.Event`. One key is reserved:

- **`id`** (required, string) — the source's own identity for this event. It is
  the default `dedupe_key` when a trigger does not declare one, and a line
  without it is refused and logged rather than fired.

Everything else is the source's business and reaches templates verbatim, so a
Jira event carries Jira's field names and a Trello event carries Trello's. There
is no normalization layer — that would be a per-vendor adapter by another name
(decision 2).

**A watermark line** may be emitted last: `{"cursor": "<opaque string>"}`. The
daemon persists it and hands it back on the next invocation as
`$VINCENT_TRIGGER_CURSOR` (empty on the first run, which decision 6 makes a
seed-and-fire-nothing run). The command owns the meaning of the string entirely —
an ISO timestamp for Jira, an action id for Trello, a build number for Jenkins.

**Exit status** is the health signal: non-zero means the poll failed, the cursor
is *not* advanced, and the failure is logged; it is never treated as "no events".
Stderr is captured into the daemon log. The child gets a fixed timeout and has
its whole process tree killed on expiry, exactly as `notify` treats its own
children (task 046).

*Amended 2026-09-13 (decisions 31B and 31H):* the fixed timeout is one minute.
A seed run records a `seeded` ledger row per event line. It stores the cursor
line when the command prints one and `""` when it does not, and in both cases
the next run is judged rather than seeded. A line that is not a JSON object, or
has no string `id`, is logged and counted, and a dry-run poll reports that count
as `refused`. Stdout over 8 MiB fails the poll rather than being parsed short.

## Appendix B — example triggers

Seven definitions covering the design surface. None of these run today; they are
the acceptance corpus 096.2 should be written against, and the shapes 096.1's
documentation should teach.

*Amended 2026-09-13:* they run now. One mechanical difference applies to all
seven and is left unedited below: `source.project` is the project's numeric id,
not its name (096.2), because the replay then needs no resolution step and the
form's project picker writes the id. Read `project: vincent` as that id.
Examples 1, 5, 6 and 7 are amended where the shipped design differs in
substance.

### 1. A GitHub label starts a task — the headline case

```yaml
# {config_dir}/triggers/label-to-task.yaml
id: label-to-task
enabled: true
source:
  type: github_issues
  project: vincent
match:
  action: labeled
  labels: [agent-please]
action:
  type: create_task
  workflow: feature-pr
  title: '{{ .Event.Issue.Title }}'
  github_issue: '{{ .Event.Issue.Number }}'
dedupe_key: 'gh:issue:{{ .Event.Issue.Number }}:label:agent-please'
limits:
  max_per_hour: 5
```

*Demonstrates:* the first-party poller on the existing `github.poll_interval`
tick (096.3), synthesizing `labeled` from the difference between two snapshots
(decision 10); `github_issue:` reusing task 035's prefill so the run gets
`.Issue` snapshotted and renders offline; and the default `on_fire` — no line
at all — so the task is created `"paused": true` (decision 9), the board shows
it held, and `resume` admits it (decision 7). The label is spelled literally in
`dedupe_key` because `match:` fixes it and the event carries no `.Label`.

*Rewritten 2026-09-11:* the first draft filtered on `allowed_actors:
[lezli01]` and read `.Event.Actor` and `.Event.Label`. A state-diff poller
cannot say who applied a label, so on this source `allowed_actors` would match
the issue's *author* — on a public repository, the field an outsider controls
(decision 10). An example that taught it as the guard would teach the wrong
thing. Labelling needs write access, which is what actually protects this
trigger; the file lives in `{config_dir}` because there is no project scope
(decision 8).

*Amended 2026-09-13 (decisions 31D and 31F):* `poll_interval: 60s` is gone,
because a GitHub source is judged on `github.poll_interval`'s tick and refuses
an interval of its own. `labeled` is a trusted event, so this file needs no
`allowed_actors` and loads as written. The protection is that applying a label
needs the triage role, not write access as the paragraph above said. On a
`labeled` event `.Event.labels` holds the labels just added, and `match:` reads
an event list as "contains", so `labels: [agent-please]` passes an event that
added that label alongside others.

### 2. Jira "Ready for Dev" — the generic source, unattended

```yaml
# {config_dir}/triggers/jira-ready-for-dev.yaml
id: jira-ready-for-dev
enabled: true
source:
  type: command
  project: vincent
  poll_interval: 5m
  command:
    - /usr/local/bin/vincent-jira-poll
    - --project=VIN
    - --status=Ready for Dev
action:
  type: create_task
  workflow: feature-pr
  title: '{{ .Event.key }} {{ .Event.fields.summary }}'
  description: |
    Jira {{ .Event.key }} — {{ .Event.fields.summary }}

    {{ .Event.fields.description }}

    Acceptance criteria live in the ticket. Do not close the ticket; vincent
    does not write to Jira.
  fields:
    ticket: '{{ .Event.key }}'
on_fire: create
limits:
  max_per_hour: 3
  max_task_cost_usd: 4.00
```

The poll script is the user's, and is the whole of the Jira integration:

```sh
#!/bin/sh
# vincent-jira-poll — emits NDJSON, tracks its own watermark
since="${VINCENT_TRIGGER_CURSOR:-$(date -u -v-5M '+%Y-%m-%d %H:%M')}"
now="$(date -u '+%Y-%m-%d %H:%M')"
curl -sf -u "$JIRA_USER:$JIRA_API_TOKEN" \
  --get "$JIRA_BASE/rest/api/3/search" \
  --data-urlencode "jql=project = VIN AND status = 'Ready for Dev' AND updated >= '$since'" \
  --data-urlencode 'fields=summary,description,assignee' \
| jq -c '.issues[] | {id: ("jira:" + .key + ":" + (.fields.updated // "")), key, fields}'
printf '{"cursor":"%s"}\n' "$now"
```

*Demonstrates:* the whole of decision 2 — no Jira package, no stored credential
(`JIRA_API_TOKEN` comes from the daemon's inherited environment, §2), vendor
field names reaching templates verbatim, and the cursor protocol from appendix A.
Also `on_fire: create` paired with the two things that should always accompany it:
an `action.workflow` whose steps are containerized (§16, task 061/062) and a
tightened `max_task_cost_usd` (task 033). *Amended 2026-09-11 by decision 23:*
the example used to carry a trigger-level `container:` block, which does not
exist; containment is the workflow's.

### 3. A Trello card moves list

```yaml
# {config_dir}/triggers/trello-doing.yaml
id: trello-doing
enabled: true
source:
  type: command
  project: vincent
  poll_interval: 10m
  command: [/usr/local/bin/vincent-trello-poll, --board=abc123, --list=Doing]
if: '{{ if .Event.card.desc }}true{{ else }}false{{ end }}'
action:
  type: create_task
  workflow: fix-and-test
  title: '{{ .Event.card.name }}'
  description: '{{ .Event.card.desc }}'
on_fire: propose
```

*Demonstrates:* why Trello is a polling source and not a webhook one — Trello
validates a webhook callback URL with a `HEAD` request against a public address,
which a laptop does not have. Also an `if:` guard doing what a `match:` block
cannot: refusing a card with an empty description, because an agent given a
three-word card name and nothing else will invent the requirements.

### 4. A red CI build starts a follow-up run — the reaction verb

```yaml
# {config_dir}/triggers/ci-failure-follow-up.yaml
id: ci-failure-follow-up
enabled: true
source:
  type: command
  project: vincent
  poll_interval: 2m
  command: [/usr/local/bin/vincent-jenkins-poll, --job=vincent-ci]
match:
  result: FAILURE
action:
  type: follow_up
  target: branch
  branch: '{{ .Event.branch }}'
  prompt: |
    CI failed on this branch. Job {{ .Event.job }} build #{{ .Event.number }}:
    {{ .Event.url }}

    Failing stages: {{ .Event.failed_stages }}

    Read the log, fix the cause, and push. If the failure is not in this
    branch's changes, say so and stop rather than guessing.
dedupe_key: 'jenkins:{{ .Event.job }}:{{ .Event.number }}'
on_fire: propose
limits:
  max_per_hour: 4
```

*Demonstrates:* 096.4, and the distinction the whole action vocabulary rests on.
A CI event is about work that **already exists**: `target: branch` resolves to
the task whose `branch_name` equals the event's ref — the link task 052's
reconciler already computes — and a `follow_up` (task 027) continues it rather
than opening a second task against the same branch. `dedupe_key` is the build
number, so a poller that sees the same red build twice fires once.

### 5. GitHub Actions pushes in over the local ingress

```yaml
# {config_dir}/triggers/gha-check-failed.yaml
id: gha-check-failed
enabled: true
source:
  type: http
  project: vincent
  signature:
    scheme: github_hmac_sha256          # X-Hub-Signature-256
    secret_env: VINCENT_GHA_WEBHOOK_SECRET
match:
  action: completed
  workflow_run.conclusion: failure
if: '{{ and (ge (len .Event.workflow_run.head_branch) 8) (eq (slice .Event.workflow_run.head_branch 0 8) "vincent/") }}'
action:
  type: retry
  target: branch
  branch: '{{ .Event.workflow_run.head_branch }}'
dedupe_key: 'gha:{{ printf "%.0f" .Event.workflow_run.id }}'
on_fire: propose
```

Delivered to `POST /v1/triggers/gha-check-failed/events` by a sender on the
daemon's machine that can add both headers. One such sender is a job on a
self-hosted runner there, run on `workflow_run: completed`, that posts the
event with the daemon's bearer token, an `X-Hub-Signature-256` it computes, and
its run id as `X-GitHub-Delivery`. The other is a relay on this machine that
receives GitHub's webhook from a tunnel and adds the bearer before forwarding.

*Amended 2026-09-13 (decisions 31C and 31G):* four corrections to the first
draft.

- **Delivery.** The draft said "or through a tunnel the user runs". A tunnel
  alone cannot deliver: the route needs the daemon's bearer token as well as the
  signature, and GitHub.com cannot add a header.
- **The guard.** The `if:` read a `head_branch_prefix` that no payload carries.
  Trigger templates get §8.4's builtins and no FuncMap (§20), so the prefix test
  is written with `len` and `slice`.
- **The match.** `match:` read `conclusion` at the top level, where a
  `workflow_run` payload has none.
- **The key.** `dedupe_key` needs `printf "%.0f"`, because a JSON number
  reaches `.Event` as a float and a large run id would otherwise render in
  exponent form.

A branch that names no vincent task lands `refused` rather than being dropped
silently, and under `propose` the retry lands `paused` for `resume` to start.

*Demonstrates:* 096.5; HMAC verification in the source's own dialect with the
secret read from the environment rather than stored (§2); and `retry` as a
trigger action, which only makes sense against a task the FSM has already put in
`blocked` — an invalid target returns the same `409` a client would get
(decision 1) and lands in the ledger as `refused`.

### 6. A review request on a pull request

```yaml
# {config_dir}/triggers/review-requested.yaml
id: review-requested
enabled: true
source:
  type: github_prs
  project: vincent
match:
  action: review_requested
  reviewer: lezli01
allowed_actors: [lezli01, a-trusted-teammate]
action:
  type: create_task
  workflow: cursor-review
  title: 'Review #{{ .Event.Pull.Number }} — {{ .Event.Pull.Title }}'
  github_pull: '{{ .Event.Pull.Number }}'
on_fire: propose
limits:
  max_per_hour: 10
```

*Demonstrates:* task 064's second worktree creation mode reached from a trigger —
the task runs on the pull request's head branch with an upstream, which is what a
review workflow needs. Note it creates no `.Pull` template variable: row 27 keeps
a pull request a pointer and never a snapshot, and this changes nothing about
that.

*Amended 2026-09-13 (decisions 31D, 31E and 32):* `poll_interval` is gone, for
example 1's reason. `review_requested` is untrusted, because CODEOWNERS requests
a review on a pull request an outsider opens, so without `allowed_actors` this
file is refused at load. The list names the pull-request **authors** whose
review requests may start a task. `.Event.reviewer` holds the logins newly
requested, and `match:` reads it as "contains". A review requested at the moment
a pull request opens arrives as its own event beside `opened`. `.Event.Pull`
belongs to the trigger's templates and is gone once the task exists (decision
11), so the note above about `.Pull` still holds.

### 7. A merged pull request cancels its task

```yaml
# {config_dir}/triggers/pr-merged-cancel.yaml
id: pr-merged-cancel
enabled: false
source:
  type: github_prs
  project: vincent
match:
  action: merged
action:
  type: cancel
  target: branch
  branch: '{{ .Event.Pull.HeadRef }}'
on_fire: create
```

*Demonstrates:* the cheapest possible action and the strongest argument that
`on_fire: create` is not always dangerous — `cancel` starts no agent and spends
nothing, so the risk calculus behind open question 1 is not uniform across the
action vocabulary. Whether the default `on_fire` should therefore be
*per action type* rather than global is a sixth open question this example
raises.

*Settled 2026-09-11 by decision 7:* it should not. The `on_fire: create` line
above stays because it is required — `cancel` gets no default of its own.

*Amended 2026-09-13 (decisions 31C, 31D and 31F):* `poll_interval` is gone, and
`match: state: closed` became `action: merged`. With no `match.action` the
trigger could match every `github_prs` event, several of them untrusted, and it
would be refused at load for naming no `allowed_actors`. `merged` needs write
access and is trusted. A cancel against a task that is already `done` gets the
FSM's 409 and lands `refused`. The `on_fire: create` line is now enforced at
load as well as required.

