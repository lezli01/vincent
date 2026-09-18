# vincent trigger schema

Use this compact reference while authoring or reviewing a trigger. If vincent
has evolved since this skill was installed, the repository's
`docs/guides/triggers.md` is authoritative. The running daemon also serves its
schema as data through the `trigger_schema` MCP tool
(`GET /v1/triggers/schema`), which lists the fields, the source and action
variants, the trusted GitHub events and the dangerous values. The installed
binary's `vincent trigger validate <file>` gives the final verdict.

## File and top-level keys

A trigger is one file, `{config_dir}/triggers/<id>.yaml`. Only `.yaml` files are
read. Triggers are global, and no project-level directory exists.

Decoding is strict. An unknown key is an error, and so is a key the chosen
source or action does not take. A file that does not validate is still listed,
with its errors, and never arms.

A disarmed, bounded starting point:

```yaml
# {config_dir}/triggers/label-to-task.yaml
id: label-to-task
enabled: false
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
  max_task_cost_usd: 3
```

| Key | Required | Absent means | Notes |
|---|:---:|---|---|
| `id` | yes | — | Matches `^[a-z0-9][a-z0-9_.-]*$`, contains no `..`, and equals the file stem |
| `enabled` | no | `false` | **Dangerous:** `true` |
| `source` | yes | — | `type`, `project`, and the keys that type takes |
| `match` | no | — | Dotted event path → a scalar, or a non-empty list of scalars |
| `if` | no | — | Template that must render exactly `true` or `false` |
| `allowed_actors` | see below | — | GitHub sources only: issue or pull-request author logins |
| `action` | yes | — | `type` and the keys that type takes |
| `on_fire` | no | `propose` | `propose` or `create`. **Dangerous:** `create`. A `cancel` requires `create` |
| `dedupe_key` | no | `{{ .Event.id }}` | Template. Rendering empty is an `error` |
| `overrun` | no | `parallel` | `parallel`, `skip`, `cancel_previous`, `queue_coalesce`, `queue_serial`: what to do when the group has unfinished work. **Dangerous:** `cancel_previous` |
| `concurrency_key` | no | the trigger id, or a reaction's resolved target task | Template naming the group. Refused unless `overrun` is set to something other than `parallel` |
| `limits.max_per_hour` | no | `0`, no cap | Integer ≥ 0. Counts `fired` deliveries in the trailing hour |
| `limits.max_task_cost_usd` | no | `0`, none sent | Number ≥ 0. `create_task` only. It becomes the task's own cap, which can tighten the global cap but never lift it |
| `permission` | no | `restricted` | `restricted` or `workflow`. `create_task` only. **Dangerous:** `workflow` |

`source.project` is the numeric project id that `vincent project ls` and the
`project_list` MCP tool report. It is the project a task is created in, or the
one whose branches a reaction searches.

The global switch is `triggers.enabled` in `config.yaml`. It is off by default
and hot-reloaded, and turning it on arms every enabled trigger with a fresh
seed. A trigger is **armed** when its file validates, it has `enabled: true`,
and `triggers.enabled` is on.

## Sources

### `command`

```yaml
source:
  type: command
  project: 1
  poll_interval: 5m
  command: [/abs/path/to/trigger-scripts/poller, --flag=value]
```

- `poll_interval` is required: a Go duration of at least `1s`.
- `command` is required. It is an argv run directly, never through a shell.
- `signature` and `allowed_actors` are refused.
- The output contract, timeout and caps are in
  [poll-scripts.md](poll-scripts.md).
- The seed poll writes one `seeded` ledger row per event and fires nothing.

### `github_issues` and `github_prs`

```yaml
source:
  type: github_prs
  project: 1
```

They take only `type` and `project`, and refuse `poll_interval`, `command` and
`signature`.

- **Schedule.** They are judged on the daemon's `github.poll_interval` tick.
  Each project gets at most one listing per kind per tick, shared by all its
  triggers, and each listing covers up to 100 items.
- **Requirements.** They need `github.enabled: true`, a non-zero
  `github.poll_interval`, and a project whose `origin` is a github.com
  repository. When any of these is missing, poll health reads failing with the
  reason.
- **Seeding.** The first tick after arming stores a snapshot, fires nothing and
  writes no ledger rows. After that, an item the snapshot has never seen fires
  `opened` only if it was created after the seed.
- **Catch-up.** At most 20 events are judged per tick.

| Source | Trusted: needs repository rights | Untrusted: an outsider can cause it |
|---|---|---|
| `github_issues` | `labeled`, `unlabeled`, `assigned` | `opened`, `reopened`, `closed` |
| `github_prs` | `merged` | `opened`, `ready_for_review`, `review_requested`, `closed` |

- **The trust rule.** A trigger that can match an untrusted event is refused at
  load unless it names `allowed_actors`, and a trigger with no `match.action`
  can match every event. `match.action` accepts a scalar or a list, and each
  value must be an event the source synthesizes.
- **Why some events are untrusted.** Authors may close their own items, and
  CODEOWNERS requests reviews on a stranger's pull request.
- **`allowed_actors`** matches the item's **author**, ignoring case. A state
  diff cannot see who labelled, assigned or requested. There is no
  `.Event.Actor` and no `.Event.Label`.

What an event carries:

| Key | Value |
|---|---|
| `id` | `github:issue:{number}:{action}:{updated-at unix seconds}`, or `github:pull:…` |
| `action`, `author`, `state`, `number` | The event and its item. `number` is an integer |
| `Issue` | `Number`, `Title`, `Body`, `URL`, `Author`, `State`, `Labels`, `Assignees`, `UpdatedAt` |
| `Pull` | `Number`, `Title`, `Body`, `URL`, `Author`, `State`, `HeadRef`, `BaseRef`, `Draft`, `Merged`, `Labels`, `RequestedReviewers`, `UpdatedAt` |
| `labels` | `labeled` and `unlabeled` only: the labels just added or removed |
| `assignees` | `assigned` only: the new assignees |
| `reviewer` | `review_requested` only: the logins newly requested, never teams |

A merged pull request reads `state: closed` with `Merged: true`. The id changes
on every update, so set `dedupe_key` when you mean once per item.

### `schedule`

```yaml
source:
  type: schedule
  project: 1
  cron: "0 9 * * 1-5"        # or: every: 6h — exactly one of the two
  timezone: Europe/Budapest  # optional; default is the daemon host's zone
```

- **Keys.** Exactly one of `cron` or `every` is required; both set, or neither,
  is refused. `timezone` is optional.
- **`cron` grammar.** Five space-separated fields — minute (`0-59`), hour
  (`0-23`), day of month (`1-31`), month (`1-12`) and day of week (`0-7`,
  where both `0` and `7` are Sunday). Each field is a `*`, a single value, a
  range `a-b`, a comma list of either, or any of those with a `/n` step. There
  are **no** `@daily`-style descriptors, no seconds field and no `L` or `#`
  extensions. An expression no calendar satisfies (`0 0 31 4 *`) is refused at
  load.
- **Both day fields restricted means either.** With `*` in one of `day of
  month` and `day of week`, the other decides. With both restricted, an
  occurrence matches *either*, as crontab(5) has always done: `0 9 1 * 1` is
  the first of the month **and** every Monday.
- **`every`.** A duration of at least `1s`, counted from the moment the
  trigger was enabled — not from a wall-clock boundary. `every: 6h` on a
  trigger enabled at 10:17 fires at 16:17.
- **Refused.** `poll_interval`, `command`, `signature` and `allowed_actors`.
- **`.Event`.** `id` and `scheduled_at` are the occurrence in UTC with
  fixed-width nanoseconds; `weekday` (`"Monday"`), `hour`, `minute` and `date`
  (`"2026-09-21"`) are the same instant in the schedule's zone. Because `id`
  is the occurrence, the default `dedupe_key` already makes two evaluations of
  one occurrence fire once.
- **Overdue fires once.** A weekend of downtime produces one task, not forty:
  the tick fires the last occurrence that passed and moves on.
- **DST.** An occurrence inside the skipped spring-forward hour fires once, at
  the first real instant after the jump. An occurrence inside the repeated
  fall-back hour fires once, not twice. `every` is a duration and is affected
  by neither.
- **No poll and no seed row.** `POST /v1/triggers/{id}/poll` refuses a
  schedule: there is no source to run once. Use `vincent trigger test` with a
  synthetic event.

### `http`

```yaml
source:
  type: http
  project: 1
  signature:
    scheme: github_hmac_sha256
    secret_env: VINCENT_CI_WEBHOOK_SECRET
```

- **Keys.** `signature.scheme` is required, and `github_hmac_sha256` is its
  only value. `signature.secret_env` is required and must be an environment
  variable name. The secret lives only in the daemon's environment.
- **Refused.** `poll_interval`, `command` and `allowed_actors`.
- **Delivery.** Events arrive at `POST /v1/triggers/{id}/events` and need both
  the daemon's bearer token and `X-Hub-Signature-256: sha256=<hex HMAC-SHA256
  of the raw body>`.
- **Body.** A JSON object of at most 4 MiB. The event id is its string `id`,
  or else the `X-GitHub-Delivery` header.
- **No seed, and no MCP route.** An `http` source has no poll and no seed. The
  push route is not an MCP tool.
- **Status codes.**
  - `401`: bad bearer token or signature, or an unset secret variable. No
    ledger row is written.
  - `409`: the trigger is not armed.
  - `400`: the source is not `http`, or the body is not a usable event.
  - `404`: unknown id.
  - `413`: body too large.

## Actions

### `create_task`

```yaml
action:
  type: create_task
  workflow: feature-pr
  title: '{{ .Event.key }} {{ .Event.fields.summary }}'
  description: 'Jira {{ .Event.key }}'
  fields:
    ticket: '{{ .Event.key }}'
```

| Key | Notes |
|---|---|
| `title` | Required template |
| `workflow` | Template. Absent means the project's default workflow |
| `description` | Template |
| `fields` | Map from workflow field name to template |
| `github_issue`, `github_pull` | Templates rendering a number (a leading `#` is allowed) or nothing. They cannot be combined |

- **Refused keys.** `target`, `branch` and `prompt`.
- **Replay.** The action replays `POST /v1/tasks` in `source.project` with an
  `Idempotency-Key` derived from the trigger id and the dedupe key.
- **State.** Under `propose` the task is created `paused`, and its agent steps
  are clamped `restricted` unless `permission: workflow`.
- **Refusal.** A route refusal, such as a workflow the project lacks, lands the
  delivery `refused`.

### `follow_up`, `retry` and `cancel`

```yaml
action:
  type: follow_up
  target: branch
  branch: '{{ .Event.branch }}'
  prompt: 'CI failed on this branch: {{ .Event.url }}. Fix the cause.'
```

| Key | `follow_up` | `retry` | `cancel` |
|---|---|---|---|
| `target` | required: `branch` | required: `branch` | required: `branch` |
| `branch` | required template | required template | required template |
| `prompt` | required template | optional: overrides the failed step's prompt | refused |
| `on_fire` | optional | optional | **must be `create`** |

- **Refused keys.** All three refuse `workflow`, `title`, `description`,
  `fields`, `github_issue`, `github_pull`, `permission` and
  `limits.max_task_cost_usd`.
- **Finding the task.** The target is the unarchived task in `source.project`
  whose branch equals the rendered `branch`. When several match, the newest
  wins.
- **Outcomes.** No matching task is `refused`, and a branch that renders empty
  is an `error`.
- **State.** Under `propose`, a follow-up or retry lands the task `paused`. The
  route's own state rules still apply, and its 409 lands `refused`.

## Templates

- **Root and functions.** `if`, `dedupe_key` and every action string see a
  single root, `.Event`. They get Go `text/template`'s standard builtins and no
  extra functions.
- **Missing keys.** Templates render with `missingkey=error`, so a missing key
  makes the delivery an `error`. Filter sparse events with `match:` first.
- **`if:`** must render exactly `true` or `false` after trimming. `false` is
  `filtered`, and anything else is an `error`.
- **`match:`** compares values as text, so `42` in the file equals `42` in the
  event.
  - A list in the file means any of its values, and a list in the event means
    contains.
  - A path the event lacks is a miss, not an error.
  - A path must not start or end with `.` or contain `..`.
- **JSON numbers.** A number in a command's NDJSON or a pushed body arrives as
  a float, so a large one renders in exponent form. Use
  `{{ printf "%.0f" .Event.number }}`. Numbers in GitHub events are integers,
  and `printf "%.0f"` would garble them.
- **`.Event` stays in the trigger.** It never reaches a workflow step template.
  The task receives only what the trigger rendered into its title,
  description, fields, prompt or branch.
- **Once per what.** Only `fired` and `seeded` rows count as delivered.
  Changing what `dedupe_key` renders for an already-delivered event makes that
  event fire again.

## How an event is judged

Each stage can stop the event, and the first one that does decides its ledger
outcome.

1. `match:` fails → `filtered`.
2. `allowed_actors` does not name the author (GitHub) → `filtered`.
3. `if:` renders `false` → `filtered`, or something that is neither → `error`.
4. The dedupe key already `fired` or `seeded` → `deduped`.
5. `limits.max_per_hour` is spent → `rate_limited`, dropped, never queued.
6. An action template fails to render → `error`.
7. A reaction finds no task on the branch → `refused`.
8. `overrun:` finds unfinished work in the group → `superseded` (`skip`),
   `queued` (either queue mode), or a cancel of everything in flight and then a
   fire (`cancel_previous`). `parallel` never reaches this stage.
9. The replayed route answers 2xx → `fired`, 4xx → `refused`, anything else
   → `error`.

An event the group already holds is recorded `superseded` rather than held
again, so a source that re-shows its window every poll does not fill the
backlog with copies of one event.

A held (`queued`) event is judged again from stage 1 when its group empties,
minus stage 8 — so a rate cap met in the meantime is honoured. A `queued` row
does not count as delivered, so stage 4 does not swallow a repeat while one is
held. "Unfinished" is every state but `done`, `aborted` and `archived`:
`paused` — which is what `on_fire: propose` creates — holds its group, and so
do `blocked`, `awaiting_gate` and `awaiting_children`.

Held events live in a table, so they survive a daemon restart; there are at
most 100 per trigger, and at the cap the oldest is dropped and recorded
`superseded`. Disarming a trigger discards its backlog and records each held
event.

## Dangerous values

The TUI asks before writing any of these. Change one only when the user asks
for that exact change.

| Value | What it does |
|---|---|
| `enabled: true` | Polls and acts on every event that passes the filter while `triggers.enabled` is on |
| `on_fire: create` | Starts agents for every event with no keypress; `propose` holds each task |
| `permission: workflow` | Lifts the `restricted` clamp: agent steps run as the workflow wrote them, full-auto included |
| `overrun: cancel_previous` | An inbound event cancels every unfinished task in its group before firing: it destroys agent work mid-run. Name `allowed_actors` on a GitHub source |
| `triggers.enabled: true` | The global switch in `config.yaml`, which arms every enabled trigger |

## Examples

These are all written disarmed. A human arms them.

A red CI build follows up its task (a command source):

```yaml
id: ci-failure-follow-up
enabled: false
source:
  type: command
  project: 1
  poll_interval: 2m
  command: [/abs/path/to/trigger-scripts/jenkins-poll, --job=vincent-ci]
match:
  result: FAILURE
action:
  type: follow_up
  target: branch
  branch: '{{ .Event.branch }}'
  prompt: |
    CI failed on this branch: {{ .Event.url }}
    Read the log and fix the cause. If the failure is not in this branch's
    changes, say so and stop.
dedupe_key: 'jenkins:{{ .Event.job }}:{{ .Event.number }}'
limits:
  max_per_hour: 4
```

A review request, which is untrusted and therefore needs `allowed_actors`:

```yaml
id: review-requested
enabled: false
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
dedupe_key: 'gh:pull:{{ .Event.Pull.Number }}:review'
limits:
  max_per_hour: 10
  max_task_cost_usd: 2
```

A signed push retries a blocked task. The prefix test uses `len` and `slice`,
and the run id needs `printf "%.0f"`:

```yaml
id: gha-check-failed
enabled: false
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
limits:
  max_per_hour: 6
```

A merged pull request cancels its task. Write this only when the user asks for
exactly this, because `on_fire: create` is required:

```yaml
id: pr-merged-cancel
enabled: false
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
dedupe_key: 'gh:pull:{{ .Event.Pull.Number }}:merged'
limits:
  max_per_hour: 20
```
