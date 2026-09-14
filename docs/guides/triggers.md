---
title: Event triggers
---

# Event triggers

A trigger starts vincent work when something happens somewhere else. Someone
labels an issue, a CI build goes red, a Jira ticket reaches "Ready for Dev", and
a task is created, followed up, retried or cancelled with nobody pressing a key.

Each trigger is one YAML file under `{config_dir}/triggers/`. It names a
**source** of events, a **filter**, an **action**, and a **dedupe key** that
makes each event fire at most once. Everything a trigger does goes through a
route the API already has. A created task is a `POST /v1/tasks`, and a reaction
is the same `follow_up`, `retry` or `cancel` a person would send. The route's
validation, limits and state checks all apply, so from its first step on, a
triggered task behaves exactly like one you created by hand.

Triggers are global. There is no project-level `.vincent/triggers/`, so nobody
who can merge to a repository can start agents on your machine.

## Turning triggers on

A trigger is **off twice by default**. A file does nothing until both of these
are on:

1. its own `enabled: true`, and
2. `triggers.enabled` in `config.yaml`:

```yaml
triggers:
  enabled: true
```

The second key exists because a trigger has no keypress to be your consent.
Read the [security model](../security-model.md#event-triggers-let-someone-else-start-an-agent)
before you turn it on. See the
[configuration reference](../reference/configuration.md#triggers) for the key
itself.

A trigger that is enabled, valid and globally switched on is **armed**. Only an
armed trigger polls or accepts a push. `GET /v1/triggers` lists every file with
`armed` and, when it is not armed, a `disarmed_reason`: the file does not
validate, the trigger is disabled, or `triggers.enabled` is off.

**Arming seeds.** The first poll after a trigger is armed records what the
source already shows and **fires nothing**. That applies whether the trigger was
armed by its own `enabled:` or by the global key. A backlog that existed before
you turned a trigger on never starts work.

The rest of the lifecycle follows from that rule:

- **Disarming drops the cursor.** Setting `enabled: false`, or turning
  `triggers.enabled` off, forgets where the trigger had got to. Turning it back
  on seeds again, so events from the off period never fire.
- **A file that does not validate keeps its cursor.** An editor's half-written
  save does not re-seed a trigger that is about to come back unchanged.
- **Deleting the file drops the cursor too.** The delivery ledger is kept, so a
  file re-created under the same id cannot refire an event it already
  delivered.
- **A restart is none of these.** The cursor persists, and the next poll is a
  catch-up.

The daemon watches the directory and reloads a change on its own. It also
reloads `config.yaml`, so turning `triggers.enabled` on or off takes effect
straight away. Files the daemon writes (from the TUI or the API) are owner-only
(`0600`), because a poll command's argv can carry a token.

## The shape of a trigger

```yaml
# {config_dir}/triggers/label-to-task.yaml
id: label-to-task
enabled: true
source:
  type: github_issues
  project: 1
match:
  action: labeled
  labels: agent-please
action:
  type: create_task
  title: '{{ .Event.Issue.Title }}'
  github_issue: '{{ .Event.Issue.Number }}'
dedupe_key: 'gh:issue:{{ .Event.Issue.Number }}:label:agent-please'
limits:
  max_per_hour: 5
```

| Key | What it is |
|---|---|
| `id` | Required. Lowercase letters, digits, `-`, `_` and `.`, starting with a letter or digit, and equal to the file name without `.yaml`. |
| `enabled` | The per-trigger switch. Absent means `false`. |
| `source` | Where events come from: `type` (`command`, `github_issues`, `github_prs` or `http`), `project`, and the keys that type takes. |
| `match` | A cheap prefilter: dotted paths into the event, each with the value it must have. |
| `if` | A guard over `.Event` that must render `true` or `false`. |
| `allowed_actors` | GitHub sources only: the issue or pull-request **authors** whose events may pass. |
| `action` | What an event that passes does: `create_task`, `follow_up`, `retry` or `cancel`. |
| `on_fire` | `propose` (the default) or `create`. |
| `dedupe_key` | A template over `.Event`. Absent means the event's `id`. |
| `limits` | `max_per_hour`, and `max_task_cost_usd` for a `create_task`. |
| `permission` | `create_task` only: `restricted` (the default) or `workflow`. |

`source.project` is the numeric id of a project, the `id` that
`GET /v1/projects` and `vincent project ls --json` report. It is the project a
task is created in, or whose branches a reaction looks in.

Decoding is strict. An unknown key is an error, and so is a key the chosen
source or action does not take, such as `poll_interval` on a GitHub source or
`title` on a `cancel`. A key that was set but ignored would look like it does
something, so vincent refuses it instead. A file that does not validate is
still listed, with its errors, and never arms.

### How an event is judged

Every event goes through the same steps in order. The first one that stops it
decides its ledger outcome:

1. **`match:`** Every key must hold, or the event is `filtered`.
2. **`allowed_actors`**, on a GitHub source. An author the list does not name is
   `filtered`.
3. **`if:`** A render of `false` is `filtered`. A render that is neither `true`
   nor `false` is an `error`.
4. **Dedupe.** A key that already `fired` or was `seeded` is `deduped`.
5. **Rate limit.** Once `limits.max_per_hour` deliveries have fired in the
   trailing hour, the event is `rate_limited` and dropped, not queued.
6. **Render.** The action's templates are rendered. A render failure is an
   `error`.
7. **Target.** A reaction finds its task by branch. No task on that branch is
   `refused`.
8. **Replay.** The request goes to the route. A 2xx answer is `fired`, a 4xx
   (such as the state machine's `409`) is `refused`, and anything else is an
   `error`.

### Filtering: `match:` and `if:`

`match:` compares equality only. Each key is a dotted path such as
`workflow_run.conclusion`, and both sides are compared as text, so `42` in the
file equals `42` in the event. A list in the file means "any of these". A list
in the event means "contains", so `labels: agent-please` passes an event whose
`labels` is `[bug, agent-please]`. A path the event does not have is a miss, not
an error.

Anything richer goes in `if:`, which is a Go `text/template` like a workflow's
`if:`. It must render exactly `true` or `false` once surrounding whitespace is
trimmed.

Every trigger template (`if`, `dedupe_key` and the action's fields) sees a single
root, `.Event`, and has the standard template builtins and no extra functions.
Templates render with `missingkey=error`: a key the event does not carry makes
the delivery an `error` rather than rendering empty text. Filter sparse events
with `match:` first. JSON numbers arrive as floats, so write
`{{ printf "%.0f" .Event.number }}` where a large number must render as digits.

### Dedupe and limits

`dedupe_key` defaults to the event's `id`. Set it when "once" means something
other than "once per event id". The file above fires once per issue for its
label, so taking the label off and putting it back does not start a second
task. A key that renders empty is an `error`.

Only `fired` and `seeded` rows count as delivered. An event that was `filtered`,
`rate_limited`, `refused` or an `error` can still fire later. That covers a
relabel after you change the filter, the same event an hour later under the
limit, or a retry after the refusal's cause is fixed. Ledger rows are pruned
after a fixed 30 days.

A `create_task` replay also carries an `Idempotency-Key` derived from the
trigger id and a hash of the dedupe key. Two triggers that render the same key
do not collide with each other, or with a script using that key itself.

`limits.max_per_hour` counts `fired` deliveries in the trailing hour, and `0` or
absent means no cap. `limits.max_task_cost_usd` becomes the created task's own
cost cap, and `0` sends none. It is refused on a reaction, because a task's cap
is set when the task is created.

## Sources

### `command`: poll anything

```yaml
source:
  type: command
  project: 1
  poll_interval: 5m
  command: [/usr/local/bin/vincent-jira-poll, --project=VIN]
```

`poll_interval` is required and must be at least `1s`. `command` is required.
It is an argv that is **executed directly, never through a shell**, so a pipeline
belongs in a script that the argv names. The command inherits the daemon's
[`environment`](../reference/configuration.md#environment) policy like every
other child process, which is how it gets a vendor token without the trigger
file holding one. It gets a fixed **one-minute** timeout, after which its whole
process tree is killed.

The command prints **NDJSON** on stdout, one event per line:

```
{"id":"jira:VIN-42:2026-09-14T10:02:11Z","key":"VIN-42","fields":{"summary":"Fix the login redirect"}}
{"id":"jira:VIN-43:2026-09-14T10:04:37Z","key":"VIN-43","fields":{"summary":"Add rate limiting"}}
{"cursor":"2026-09-14 10:05"}
```

- Each line is a JSON object with a string **`id`**, and the whole object is
  `.Event`. There is no normalization, so a Jira event carries Jira's field
  names. Blank lines are skipped. A line that is not an object, or has no string
  `id`, is logged and skipped.
- An optional **last line** holding only `{"cursor": "..."}` is a watermark. The
  daemon stores it and hands it back on the next run in
  `$VINCENT_TRIGGER_CURSOR`, which is empty on the first run. The string means
  whatever your command wants: a timestamp, an action id, a build number. A run
  that prints no cursor line leaves the stored one where it was.
- A **non-zero exit** is a failed poll, never "no events". The cursor does not
  advance and the poll status reads failing with the exit status and the tail of
  stderr. Stderr always goes to the daemon log.
- Printing more than 8 MiB fails the poll rather than being cut short.

The **seed** run (the first after arming) writes one `seeded` ledger row per
event it printed, keyed by the rendered dedupe key, and runs no `match:` or
`if:`. It stores the cursor line, or an empty cursor when there is none, so a
command that keeps no watermark of its own does not fire its whole backlog on
the second poll.

After the seed, **at most 20 events** of one poll are judged. Events past that
are dropped, not deferred, and a warning is logged once per trigger per daemon
run. This is also the catch-up limit after a restart. A backlog of a hundred
events means a hundred agent processes, so the cap is fixed.

### `github_issues` and `github_prs`: watch a repository

```yaml
source:
  type: github_issues
  project: 1
```

A GitHub source takes no `poll_interval` and no `command`. It is judged on the
daemon's existing [`github.poll_interval`](../reference/configuration.md#github)
tick. On each tick, every project with an armed GitHub trigger gets at most one
issues listing and one pull-request listing, whichever its triggers need. Every
trigger on that project shares them, so API use does not grow with the number
of triggers. A listing covers every state and up to 100 items. It starts two
minutes before the oldest point the project's triggers have seen, because
GitHub's search index lags behind recent changes.

GitHub reports state, not events, so the trigger keeps a snapshot of what it last
saw and **synthesizes** events from the difference:

| Source | Events |
|---|---|
| `github_issues` | `opened`, `closed`, `reopened`, `labeled`, `unlabeled`, `assigned` |
| `github_prs` | `opened`, `ready_for_review`, `review_requested`, `closed`, `merged` |

Every event carries `id`, `action`, `author`, `state` and `number`, plus the item:

- **`.Event.Issue`** has `Number`, `Title`, `Body`, `URL`, `Author`, `State`,
  `Labels`, `Assignees` and `UpdatedAt`.
- **`.Event.Pull`** has `Number`, `Title`, `Body`, `URL`, `Author`, `State`,
  `HeadRef`, `BaseRef`, `Draft`, `Merged`, `Labels`, `RequestedReviewers` and
  `UpdatedAt`.

Some events add a field of their own. `labeled` and `unlabeled` add `labels`
(the labels just added or removed). `assigned` adds `assignees` (the new
assignees), and `review_requested` adds `reviewer` (the logins newly
requested). A merged pull request reads `state: closed`, and its `Merged` is
true.

The default event id is `github:issue:{number}:{action}:{updated-at}` (or
`github:pull:…`), where `{updated-at}` is Unix seconds, not the RFC 3339 form
`.Event.Issue.UpdatedAt` carries. The same issue labelled again later is therefore a new id, so
set a `dedupe_key` when you mean once per issue.

The first tick after arming stores the snapshot and fires nothing. After that,
an item the snapshot has never seen fires `opened` only if it was created after
the seed. An older one is taken as a baseline. A restart keeps the snapshot, so
the next tick is a catch-up diff, capped at 20 events like a command's.

The trigger needs `github.enabled: true`, a non-zero `github.poll_interval`, and
a project whose `origin` remote is a github.com repository. When any of these is
missing, or a listing fails, the poll status reads **failing** with the reason
instead of going quiet.

#### Trusted events and `allowed_actors`

Some events can be caused by anyone on a public repository, and some need rights
there:

| | Trusted: needs rights on the repository | Untrusted: anyone can cause it |
|---|---|---|
| `github_issues` | `labeled`, `unlabeled`, `assigned` (triage) | `opened`, `reopened`, `closed` |
| `github_prs` | `merged` (write) | `opened`, `ready_for_review`, `closed`, `review_requested` |

`closed` is untrusted because authors can close their own issues and pull
requests. `review_requested` is untrusted because CODEOWNERS requests reviews
automatically on a pull request a stranger opens.

A GitHub trigger that **can match an untrusted event** is refused at load unless
it names `allowed_actors`. A trigger with no `match.action` can match every
event, so it needs the list too. A `match.action` value that the source never
synthesizes is refused as well.

`allowed_actors` matches the **author** of the issue or pull request, ignoring
case. A state diff cannot see who applied a label or requested a review, so the
author is the only identity there is. The list stops a stranger's issue from
starting work. It does not say who acted on it.

```yaml
# {config_dir}/triggers/review-requested.yaml
id: review-requested
enabled: true
source:
  type: github_prs
  project: 1
match:
  action: review_requested
  reviewer: lezli01
allowed_actors: [lezli01, a-trusted-teammate]
action:
  type: create_task
  workflow: review
  title: 'Review #{{ .Event.Pull.Number }}: {{ .Event.Pull.Title }}'
  github_pull: '{{ .Event.Pull.Number }}'
limits:
  max_per_hour: 10
```

`allowed_actors` is refused on `command` and `http` sources. Their events carry
no identity vincent can verify: a command's trust is whatever its script
filters, and an `http` source's is its signature.

### `http`: accept a signed push

```yaml
source:
  type: http
  project: 1
  signature:
    scheme: github_hmac_sha256
    secret_env: VINCENT_CI_WEBHOOK_SECRET
```

An `http` source is never polled and has no seed. Events are pushed to
`POST /v1/triggers/{id}/events`, and a push must carry **both**:

- the daemon's **bearer token**, like every other API call, and
- an **`X-Hub-Signature-256`** header: `sha256=` followed by the hex HMAC-SHA256
  of the raw body, keyed with the secret in the daemon's environment variable
  named by `secret_env`. The secret is read when the push arrives and is never
  written to the file.

`github_hmac_sha256` is the only scheme. The body may be up to 4 MiB and must be
a JSON object. Its event id is the body's string `id`, or else the
`X-GitHub-Delivery` header.

```sh
# TOKEN holds the contents of {data_dir}/token; `vincent daemon status` prints the port.
body='{"id":"build-1234","result":"FAILURE","branch":"vincent/12-fix-login"}'
sig="sha256=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$VINCENT_CI_WEBHOOK_SECRET" | sed 's/^.* //')"
curl -sS -X POST "http://127.0.0.1:7777/v1/triggers/ci-push/events" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Hub-Signature-256: $sig" \
  --data-binary "$body"
```

| Status | Why |
|---|---|
| `200` | Judged. The body is the delivery: the judgement plus `delivery_id`, `task_id` and `detail`. |
| `400` | The trigger's source is not `type: http`. Or the body is not a JSON object, or it has no string `id` and no `X-GitHub-Delivery` header. |
| `401` | The bearer token is missing or wrong. Or the signature is missing or wrong, or the secret variable is unset in the daemon's environment (these last three look the same on purpose). No ledger row is written. |
| `404` | No trigger has that id. |
| `409` | The trigger is not armed: disabled, `triggers.enabled` off, or a file that does not validate. `details.reason` says which. |
| `413` | The body is over 4 MiB. |

A single push has no catch-up cap. Pushes to one daemon are judged one at a time,
so two deliveries of the same key cannot both fire.

**Because the route needs the bearer token, a GitHub.com webhook sent through a
bare tunnel cannot deliver.** GitHub cannot add the header. What does work is a
sender on the daemon's machine that can add it:

- a job on a **self-hosted runner** on that machine, which posts the event with
  the token and a signature it computes, or
- a **relay on the same machine** that receives GitHub's webhook from a tunnel
  and adds the bearer token before forwarding it.

The route is not an MCP tool, because an agent that could inject events could
start agents.

## Actions

### `create_task`

```yaml
action:
  type: create_task
  workflow: feature-pr
  title: '{{ .Event.key }} {{ .Event.fields.summary }}'
  description: '{{ .Event.fields.description }}'
  fields:
    ticket: '{{ .Event.key }}'
```

This replays `POST /v1/tasks` in `source.project`. `title` is required, and every
key is a template over `.Event`:

- `workflow` defaults to the project's default workflow.
- `description` and `fields` fill the task's description and workflow fields.
- `github_issue` or `github_pull` must render to a number (a leading `#` is
  allowed) or to nothing. They cannot both be set. They work as they do when you
  create a task from an issue or a pull request.

The task is created `paused` under `on_fire: propose`, and with its agent steps
clamped `restricted` unless the file says `permission: workflow`. When the route
refuses (for example, a workflow the project does not have), the delivery lands
`refused` with the route's error in its detail.

### `follow_up`, `retry` and `cancel`: act on an existing task

```yaml
action:
  type: follow_up
  target: branch
  branch: '{{ .Event.branch }}'
  prompt: 'CI failed on this branch: {{ .Event.url }}. Fix the cause.'
```

A reaction acts on work that already exists. `target: branch` is required and
is the only target. `branch` is a template, and the task it names is the
**unarchived** task in `source.project` whose branch is exactly that name. If
more than one task has it, the newest wins. When no task is on the branch, the
delivery is `refused` and its detail names the branch that was looked for. A
branch that renders empty is an `error`.

| Action | Replays | `prompt` |
|---|---|---|
| `follow_up` | `POST /v1/tasks/{id}/follow_up` | Required: what the follow-up run is told. |
| `retry` | `POST /v1/tasks/{id}/retry` | Optional: replaces the failed step's prompt. |
| `cancel` | `POST /v1/tasks/{id}/cancel` | Refused: a cancel runs nothing. |

The route's own rules still apply. An action the task's state does not allow,
such as a cancel of a task that is already `done`, gets the route's own refusal
(here a `409`) and lands `refused`, with the error in its detail. The ledger's `task_id` is the task that
was acted on.

A reaction refuses `workflow`, `title`, `description`, `fields`,
`github_issue`, `github_pull`, `permission` and `limits.max_task_cost_usd`. Each
of those describes a task being created.

### `on_fire` and `permission`

`on_fire: propose` is the default. A task that a trigger creates, retries or
follows up lands **`paused`**, and nothing runs until you `resume` it.
`on_fire: create` starts it straight away, with no keypress, and is always an
explicit line in the file.

A `cancel` has no paused form, so a `cancel` trigger is refused at load unless
it writes `on_fire: create`.

`permission: restricted` is the default for `create_task`. Every agent step of
the created task runs in [restricted mode](../security-model.md#restricted-mode),
even a step whose own `permission_mode` says otherwise. `permission: workflow`
lifts that clamp and runs the workflow as written, full-auto included.

The TUI asks before it writes `enabled: true`, `on_fire: create` or
`permission: workflow`.

## Dry runs

Two dry runs show what a trigger would do. **Neither writes anything to
vincent:** no task, no ledger row, no cursor change, no poll status change and
no event. Both work while the trigger or `triggers.enabled` is off.

**Judge a sample event** with
[`vincent trigger test`](../reference/cli.md#vincent-trigger):

```sh
vincent trigger test label-to-task --event labeled.json
```

It sends the event to `POST /v1/triggers/{id}/test`. It prints each stage in
pipeline order: the match, the `if:` verdict, the dedupe key and whether the
ledger already has it, the target task, and the request the action would
replay. Here `fired` means "would be replayed". The command exits `1` when the
outcome is `error`, so a pre-commit hook can use it.

**Run the source once for real** with `POST /v1/triggers/{id}/poll`. It runs the
command, or makes the GitHub listing, and judges every event it gets back. A
GitHub trigger that has not seeded yet has no snapshot to diff the listing
against, so it judges nothing and `events` comes back empty. The
answer holds `seed` (a real poll now would only seed), `events` (one judgement
each), `truncated` (events past the cap of 20), `refused` (command output lines
that were not events), the `cursor` the command printed, and `error` when the
command or listing failed. A failing poll still answers `200`. An `http` trigger
has no poll, and the route answers `400`.

The command itself really runs, so whatever it does outside vincent still
happens.

`POST /v1/triggers/validate` checks a document without writing it. The reads,
`validate` and both dry runs are also MCP tools. Creating, editing, deleting or
enabling a trigger is not.

## Watching what triggers do

**The delivery ledger** records every judged event, one row each:

| Outcome | Meaning |
|---|---|
| `fired` | The replayed route created or acted on a task. |
| `seeded` | A command source's seed poll after arming saw the event. Nothing fired, and the key counts as delivered. A GitHub source seeds its snapshot instead, and writes no rows. |
| `deduped` | The dedupe key had already fired or been seeded. |
| `filtered` | `match:`, `allowed_actors` or `if:` dropped it. |
| `rate_limited` | Over `limits.max_per_hour`. Dropped, not queued. |
| `refused` | The route answered with a 4xx, or a reaction found no task on the branch. `detail` holds the reason. |
| `error` | A template did not render, or the route answered with a 5xx or was never reached. |

Read it with `GET /v1/triggers/{id}/deliveries?limit=` (newest first, up to
1000 rows, 100 by default). The ledger outlives the file and is pruned after 30
days.

**Poll status** is on `GET /v1/triggers` for each trigger: `seeded`,
`last_poll_at`, `ok`, `error` and `last_fire_at`.

**Events** on the [SSE stream](../reference/api.md):

- `trigger.fired`, for every `fired` delivery. Payload
  `{trigger_id, delivery_id, action}`, with the event's `project_id` and
  `task_id` set to the task created or acted on. No other outcome publishes.
- `trigger.poll_changed`, on a trigger's first poll after arming and whenever it
  goes from ok to failing or back, never on every poll. Payload
  `{trigger_id, ok, error}`.

**The TUI's triggers view**, opened from the command palette, lists every
trigger with its armed state and poll status. It creates and edits triggers,
enables and disables them, shows the ledger and runs both dry runs. See
[Using the TUI](./tui.md#triggers).

## Examples

### A Jira status starts work unattended

```yaml
# {config_dir}/triggers/jira-ready-for-dev.yaml
id: jira-ready-for-dev
enabled: true
source:
  type: command
  project: 1
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
    Jira {{ .Event.key }}: {{ .Event.fields.summary }}

    {{ .Event.fields.description }}
  fields:
    ticket: '{{ .Event.key }}'
on_fire: create
limits:
  max_per_hour: 3
  max_task_cost_usd: 4.00
```

The poll script is yours, and it is the whole of the Jira integration. It
searches Jira using `$VINCENT_TRIGGER_CURSOR` and prints one line per issue,
with an `id` that includes the issue's update time. It finishes with a cursor
line. The Jira credential comes from the daemon's environment. `on_fire: create`
runs unattended, so pair it with a tight `max_task_cost_usd` and a workflow
whose steps run in a container.

### A red CI build follows up its task

```yaml
# {config_dir}/triggers/ci-failure-follow-up.yaml
id: ci-failure-follow-up
enabled: true
source:
  type: command
  project: 1
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

    Read the log, fix the cause, and push. If the failure is not in this
    branch's changes, say so and stop rather than guessing.
dedupe_key: 'jenkins:{{ .Event.job }}:{{ .Event.number }}'
limits:
  max_per_hour: 4
```

The follow-up lands `paused` on the task whose branch CI built. A build on a
branch that no vincent task owns lands `refused`. The build number is the
dedupe key, so a poller that sees the same red build twice fires once.

### GitHub Actions retries a blocked task over the ingress

```yaml
# {config_dir}/triggers/gha-check-failed.yaml
id: gha-check-failed
enabled: true
source:
  type: http
  project: 1
  signature:
    scheme: github_hmac_sha256
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
```

A job on a self-hosted runner on the daemon's machine posts the `workflow_run`
payload with the bearer token, the signature and its run id as
`X-GitHub-Delivery`. `if:` keeps branches vincent did not cut out, using `len`
and `slice` because templates have no extra functions. The retry lands `paused`,
and a task whose state the retry route refuses lands `refused`.

### A merged pull request cancels its task

```yaml
# {config_dir}/triggers/pr-merged-cancel.yaml
id: pr-merged-cancel
enabled: true
source:
  type: github_prs
  project: 1
match:
  action: merged
action:
  type: cancel
  target: branch
  branch: '{{ .Event.Pull.HeadRef }}'
on_fire: create
```

`merged` needs write access, so it is trusted and the file needs no
`allowed_actors`. `on_fire: create` is required for a cancel. A task that is
already `done` gets the route's `409` and lands `refused`.

## Letting an agent write triggers

An agent can author a trigger for you, but it can never arm one. Arming stays
your change.

### The `vincent-triggers` skill

The published `vincent-triggers` skill teaches an agent this page: the sources,
the trusted-event rule, dedupe keys, limits, poll scripts and the ledger. It
installs beside `vincent-workflows`:

```sh
vincent skills install
# or by hand:
npx skills add lezli01/vincent --skill vincent-triggers -g
```

The skill writes every trigger disarmed. It sets `enabled: false`, leaves out
`on_fire` and `permission`, and always sets a `dedupe_key` and `limits`. It
changes a switch only when you ask for that exact change. For the workflow a
trigger's `action.workflow` names, it defers to `vincent-workflows`.

### The `create-trigger` and `update-triggers` built-ins

Two built-in workflows use the skill against the task's own project:

- **`create-trigger`** writes one new trigger. Its `trigger_id` field becomes
  the trigger's `id:` and its file name. The agent step may ask you questions.
  It then stages the file, and a command step installs it with
  `vincent trigger apply`. There is no approval gate, because the file cannot
  fire until you arm it.
- **`update-triggers`** reviews every trigger whose `source.project` is the
  task's project. It lists them, stages whole rewritten copies, and stops at a
  manual gate whose instructions show the proposal. Approving installs it with
  `vincent trigger apply`. Rejecting leaves every trigger untouched. The pass
  never changes a trigger's `id`, file name, `source.project`, `enabled`,
  `on_fire` or `permission`, deletes no file, and never changes what a
  `dedupe_key` renders for events already delivered.

Neither built-in can produce a `cancel` trigger, because a cancel loads only
with `on_fire: create`. Write one by hand.

### Proposals and `vincent trigger apply`

A built-in's agent never writes `{config_dir}/triggers/` itself. It stages files
in `{data_dir}/trigger-proposals/<task id>/`, outside every repository, beside a
`manifest.json` that maps each trigger id to the version `vincent trigger ls
--project <id> --json` reported, or to `"absent"` for a new trigger. The
directory is removed after a successful apply, and when the task is deleted.

`vincent trigger apply --proposal <task id> --project <id>` checks every staged
file before it writes any. It refuses the whole proposal, and writes nothing,
when a file:

- does not validate;
- names a different `source.project` than `--project`;
- changed on disk since the proposal recorded its version, or appeared after
  being recorded as absent;
- **arms** the trigger: `enabled` from off to `true`, `on_fire` to `create`, or
  `permission` to `workflow`.

An already-armed value may be kept, and disarming is allowed. There is no
override flag. You arm a trigger in the TUI's triggers view, which asks first,
or in an editor. `triggers.enabled` lives in `config.yaml`, and apply never
touches it. See the [CLI reference](../reference/cli.md#vincent-trigger) for
`validate`, `ls` and `apply`.

### Where poll scripts live

By convention a `type: command` trigger's script goes in
`{config_dir}/trigger-scripts/`. vincent does not enforce the location. It sits
beside `triggers/`, never inside it, and never in a repository. On POSIX, make
the directory and each script owner-only (`0700`). Name the script by absolute
path. The argv runs with no shell, so on Windows write
`command: [pwsh, -NoProfile, -File, <absolute path>.ps1]`. Credentials come
from the daemon's environment, never from the script or the trigger file.

## Security

A trigger lets someone else start an agent that runs as you. Agents run
full-auto unless something narrows them, and a trigger's text comes from
outside. An issue body, a pull-request title or a CI log reaches `.Event`, and
whatever the file renders into a title, description, field or prompt reaches
the agent. That is prompt injection from anywhere a stranger can type.

The defaults are the protection. Keep them unless you have a reason:

- `on_fire: propose`, so you read each task before `resume`.
- `permission: restricted`, so agent steps run in restricted mode.
- `allowed_actors` on any GitHub trigger that can match an untrusted event.
- `limits.max_per_hour` and `limits.max_task_cost_usd`.
- For unattended work, an `action.workflow` whose steps run in a container.

Render as little of `.Event` as the task needs. See
[Event triggers let someone else start an agent](../security-model.md#event-triggers-let-someone-else-start-an-agent)
for the full posture.

## See also

- [Configuration](../reference/configuration.md#triggers): `triggers.enabled`,
  and [`github`](../reference/configuration.md#github) for the tick GitHub
  sources ride.
- [CLI](../reference/cli.md#vincent-trigger): `vincent trigger validate`, `ls`,
  `apply` and `test`.
- [HTTP API](../reference/api.md#triggers): the `/v1/triggers` routes and the event stream.
- [Using the TUI](./tui.md#triggers): the triggers view.
- [Security model](../security-model.md).
