# 091 — Event triggers: starting vincent work from GitHub, Jira, Trello and CI systems

*Issue [#356](https://github.com/lezli01/vincent/issues/356). Planned 2026-09-10.*

Status: **in progress (1/5)**.

**Spec:** would amend §2, §3 (a new decision row), §5, §8.4, §12.3, §13.1, §13.2,
§13.3, §13.4, §14, §15, §16, §17, §20.

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

- [x] **091.1** Push-in, documented. A `guides/` page and `examples/` entries for
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
- [ ] **091.2** `internal/trigger`: the registry, the `type: command` source, the
  `create_task` action, `on_fire: propose`, the delivery ledger, and
  `vincent trigger test`. Depends: 091.1 (for the payload shapes its fixtures
  come from).
- [ ] **091.3** `type: github_issues` and `type: github_prs`, on the existing
  `github.poll_interval` reconciler tick. Depends: 091.2.
- [ ] **091.4** Reaction actions — `follow_up`, `retry` and `cancel` against the
  task whose `branch_name` matches the event's ref. Depends: 091.2.
- [ ] **091.5** `type: http` ingress (`POST /v1/triggers/{id}/events`) with
  per-source HMAC verification. Depends: 091.2.

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
091.3 is corrected by this.

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
advisory.** A triggered task's agent steps run in `restricted` permission mode
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
  allowlist degrades to the issue's author (decision 10).
- **Rate limits** per trigger, and a `max_task_cost_usd` (task
  [033](033-task-cost-cap.md)) on triggered tasks defaulting tighter than a
  hand-created one.
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
  the ones that fired.
- **A `trigger.fired` event** on §13.3's fan-out, published post-commit like
  every other.
- **A TUI surface** — a triggers view, or a section on the projects view —
  showing last poll, last fire and the recent ledger.
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
  not bundled.
- **Scheduled and recurring triggers.** Adjacent — §20's "task templates &
  recurring tasks" — and deliberately out. The `action:` block should be
  designed so a later `type: schedule` source reuses it verbatim; that is the
  named trigger for reopening it.

## Open questions

These block **091.2** and should be settled as decisions in this document before
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

## Appendix B — example triggers

Seven definitions covering the design surface. None of these run today; they are
the acceptance corpus 091.2 should be written against, and the shapes 091.1's
documentation should teach.

### 1. A GitHub label starts a task — the headline case

```yaml
# {config_dir}/triggers/label-to-task.yaml
id: label-to-task
enabled: true
source:
  type: github_issues
  project: vincent
  poll_interval: 60s
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
tick (091.3), synthesizing `labeled` from the difference between two snapshots
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
container:
  image: ghcr.io/lezli01/vincent-dev:latest
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
a `container:` block (§16, task 061/062) and a tightened `max_task_cost_usd`
(task 033).

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

*Demonstrates:* 091.4, and the distinction the whole action vocabulary rests on.
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
  conclusion: failure
if: '{{ eq .Event.workflow_run.head_branch_prefix "vincent/" }}'
action:
  type: retry
  target: branch
  branch: '{{ .Event.workflow_run.head_branch }}'
dedupe_key: 'gha:{{ .Event.workflow_run.id }}'
on_fire: propose
```

Delivered to `POST /v1/triggers/gha-check-failed/events` — reachable from a
self-hosted runner on the same machine, or through a tunnel the user runs and
vincent does not ship.

*Demonstrates:* 091.5; HMAC verification in the source's own dialect with the
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
  poll_interval: 5m
match:
  action: review_requested
  reviewer: lezli01
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

### 7. A merged pull request cancels its task

```yaml
# {config_dir}/triggers/pr-merged-cancel.yaml
id: pr-merged-cancel
enabled: false
source:
  type: github_prs
  project: vincent
  poll_interval: 5m
match:
  state: closed
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

