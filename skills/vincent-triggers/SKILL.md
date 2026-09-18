---
name: vincent-triggers
description: Create, edit, review, arm, and debug vincent event triggers, the YAML under {config_dir}/triggers and the poll scripts they run. Use for trigger sources (command, github_issues, github_prs, http, schedule), match and if filters, dedupe keys, limits, on_fire, permission, arming, dry runs, the delivery ledger, or trigger validation errors. Do not use for vincent workflows, including the workflow a trigger's action.workflow names (use vincent-workflows), or for GitHub Actions.
license: LICENSE.txt
metadata:
  author: lezli01
  version: 1.2.0
---

# vincent Triggers

A trigger lets something other than a person start vincent work. An event from
a poll script, from GitHub, or from a signed push can create a task, or follow
up, retry or cancel the task on a branch. Nobody presses a key, so design the
smallest trigger that is safe to leave on, and leave turning it on to a human.

This skill covers four jobs:

- authoring a trigger,
- writing its poll script,
- arming and debugging it,
- migrating or reviewing existing triggers.

## The safety rule

Every trigger you write starts disarmed and bounded:

- `enabled: false`.
- No `on_fire` line, so a fired event's task lands `paused` (`propose`).
- No `permission` line, so a created task's agent steps run `restricted`.
- An explicit `dedupe_key`.
- `limits.max_per_hour`, plus `limits.max_task_cost_usd` on a `create_task`.

Four switches arm a trigger or widen what it may do:

- `enabled: true`
- `on_fire: create`
- `permission: workflow`
- the global `triggers.enabled` in `config.yaml`

Change one only when the user asks for that exact change. Never flip one to
make something work, and say what it does before you write it. When they stay
off, end by naming what a human must do to arm the trigger.

Consequences of the rule:

- **Cancel triggers.** A `cancel` action is refused at load unless it writes
  `on_fire: create`, because a cancel cannot be held for approval. Write one
  only when the user asks for exactly that. Otherwise explain this and write
  nothing.
- **No `container:`.** A trigger has no `container:` key, and a file with one
  is refused. For `on_fire: create`, recommend an `action.workflow` whose steps
  run in a container, plus a tight `limits.max_task_cost_usd`. That limit caps
  one task, not a tree. If the workflow fans out, also recommend
  `max_tree_cost_usd` in `config.yaml`, which caps the task and all its lanes
  together. It is a config key, not a trigger key, so never write it into the
  trigger file.
- **Global only.** Triggers are global. There is no `.vincent/triggers/`, so
  never write a trigger or its poll script into a repository.
- **Untrusted event text.** Event text comes from outside. An issue body or CI
  log rendered into a title, description, field or prompt reaches an agent.
  Render as little of `.Event` as the task needs.
- **No secrets.** Never put a secret in a trigger file, a poll script or any
  argv. Credentials come from the daemon's inherited environment.

## Gather only decisions that matter

Look before you ask:

- **Project.** `source.project` is the project's **numeric id**, not its name.
  Find it with `vincent project ls` or the `project_list` MCP tool.
- **Existing triggers.** `vincent trigger ls --project <id>` lists that
  project's triggers. Never silently overwrite an id that already exists.
- **Workflow.** `vincent workflow ls` shows whether `action.workflow` exists.
  If it is missing, say so. Writing it is the `vincent-workflows` skill's job.

Then ask only what changes the YAML:

- Which system and event? Should it create a task, or follow up, retry or
  cancel the task on the event's branch?
- What makes two events "the same"? That becomes the `dedupe_key`.
- On GitHub, which events, and whose issues or pull requests may start work?
- What hourly rate is plausible, and what should one task cost at most?
- Which platform does the daemon run on, and where do credentials come from?

## Read the references

Inside a vincent source checkout, read its `docs/guides/triggers.md`. It is
newer and more authoritative than a bundled snapshot. Otherwise use
[references/trigger-schema.md](references/trigger-schema.md).

Read [references/poll-scripts.md](references/poll-scripts.md) for any
`type: command` source. Read
[references/debugging.md](references/debugging.md) when a trigger does not do
what was expected.

Run `vincent version` when the binary is available, and keep the output. That
binary's `vincent trigger validate` gives the compatibility verdict. If it
rejects a feature the reference describes, report the version mismatch and ask
before changing the design.

## Choose the source

- **`github_issues` or `github_prs`** watch repository state.
  - They take no `command` and no `poll_interval`. An interval is refused,
    because they run on `github.poll_interval` in `config.yaml`.
  - Only four events are trusted, because an outsider cannot cause them:
    issue `labeled`, `unlabeled` and `assigned`, and pull request `merged`.
  - A trigger is refused unless it names `allowed_actors` when its
    `match.action` could match any other event. That includes a trigger with
    no `match.action` at all.
  - `allowed_actors` matches the issue's or pull request's **author**,
    ignoring case. It does not match whoever acted.
  - `review_requested` is untrusted, because CODEOWNERS requests reviews on a
    stranger's pull request.
  - Prefer a label as the signal to start.
- **`command`** covers any other system with an API or CLI: a poll script run
  on a `poll_interval` of at least `1s`, in practice minutes.
- **`schedule`** is the clock, for work no outside event announces: a nightly
  sweep, a weekday morning report.
  - Set exactly one of `cron` (five fields: minute, hour, day of month, month,
    day of week) or `every` (a duration of at least `1s`). Both set, or
    neither, is refused.
  - `timezone` is an IANA name such as `Europe/Budapest`; absent means the
    daemon host's zone. An unknown name is refused, never silently UTC.
  - `poll_interval`, `command`, `signature` and `allowed_actors` are all
    refused: there is nothing to poll, run, sign or attribute.
  - `.Event` carries `id` and `scheduled_at` (the occurrence, UTC), plus
    `weekday`, `hour`, `minute` and `date` in the schedule's own zone.
  - **Re-arming resets the clock.** Enabling anchors it at that moment and
    fires nothing. A daemon restart, a suspend or a reboot keeps the anchor, so
    an occurrence missed while the machine slept fires **once** on the next
    tick; but disabling and re-enabling — or toggling `triggers.enabled` off
    and on — anchors afresh and fires nothing.
- **`http`** fits a sender on the daemon's machine that can push.
  - It requires `signature.scheme: github_hmac_sha256` and
    `signature.secret_env`, which names a variable in the daemon's
    environment. The secret itself never goes in the file.
  - The sender also needs the daemon's bearer token, so a GitHub.com webhook
    sent through a bare tunnel cannot deliver.

`allowed_actors` is refused on `command`, `http` and `schedule` sources, whose
events carry no identity vincent can verify.

## Choose the action

- **`create_task`**:
  - `title` is required.
  - `workflow` defaults to the project's default workflow.
  - `description` and `fields` are optional.
  - Use `github_issue` or `github_pull` (never both) to link the task, rather
    than parsing a number into the title.
  - `permission` is `restricted` (the default) or `workflow`.
- **`follow_up`, `retry` and `cancel`** act on an existing task:
  - `target: branch` is required, with a `branch` template naming the task's
    branch.
  - `follow_up` requires `prompt`, `retry` may override the failed step's
    prompt, and `cancel` refuses one.
  - Each refuses `permission`, `workflow`, `title`, `description`, `fields`,
    `github_issue`, `github_pull` and `limits.max_task_cost_usd`.

## Write the templates

- `if`, `dedupe_key` and every action string are Go `text/template`s over one
  root, `.Event`. They get the standard builtins (`and`, `eq`, `len`, `slice`,
  `index`, `printf` and so on) and no extra functions, so a prefix test uses
  `len` and `slice`.
- Rendering uses `missingkey=error`. Put a cheap `match:` in front of `if:` so
  sparse events are dropped before a template reads a key they lack.
- `match:` compares values as text. A list in the file means "any of these",
  and a list in the event means "contains".
- `if:` must render exactly `true` or `false`.
- A JSON number from a poll script or a pushed body arrives as a float, so
  render it with `{{ printf "%.0f" .Event.workflow_run.id }}`. Numbers in
  GitHub events are already integers.
- `.Event` exists only in trigger templates, never in a workflow step.
  Anything the workflow needs goes through the rendered title, description or
  fields.
- `dedupe_key` defines what "once" means. When absent it is the event's `id`,
  and a GitHub event's id includes the update time, so a relabel counts as a
  new event.
- **Changing an existing `dedupe_key` must not change what it renders for
  events already delivered.** A new rendering misses the ledger and fires those
  events again.

## Overrun: the previous run is still going

`overrun:` says what to do with an event whose group already has unfinished
work. Absent it is `parallel`, which is no check at all — every event fires,
however many of this trigger's tasks are mid-flight. Set it on any source that
emits repeatedly about the same object.

| value | behaviour |
|---|---|
| `parallel` | the default: no check |
| `skip` | record the event and drop it; the work in flight stands |
| `cancel_previous` | cancel every in-flight task in the group, then fire |
| `queue_coalesce` | hold it; when the group empties, fire the **newest** held event |
| `queue_serial` | hold it; when the group empties, fire the **oldest**, and repeat |

`concurrency_key:` names the group, as a template over `.Event`. Absent it is
the trigger id — one at a time per trigger — and, for a reaction, the task its
`branch:` resolved to.

- It is **not** `dedupe_key`. On the case this exists for, the dedupe key is
  per modification (`{{ .Event.id }}`) and the group is the object
  (`{{ .Event.ticket }}`). Setting them to the same thing disarms the feature.
- It is refused without an `overrun:` other than `parallel`: a group nothing
  consults is a control that does not control.
- **Changing it on a live trigger re-groups events already in flight**, the
  same standing warning `dedupe_key` carries.

Read the ledger before you believe a trigger did nothing: `superseded` is an
event overrun dropped, `queued` one it is holding.

Two things to say out loud when you write one:

- **A `paused` task holds its group, and `on_fire: propose` creates every task
  paused.** With `skip`, one unreviewed proposal holds its group until a human
  admits or archives it. That is intended — a second proposal for the same
  object is noise — but with `propose` and `skip` together, a trigger that
  looks dead is usually one waiting on a proposal nobody looked at. So do
  `blocked`, `awaiting_gate` and `awaiting_children`; only `done`, `aborted`
  and `archived` release a group.
- **`cancel_previous` destroys work.** An inbound event kills an agent mid-run.
  The cancelled task keeps its branch and worktree and the delivery records
  which task it superseded, but nothing un-cancels it. On a GitHub source, name
  `allowed_actors` — it is what stands between a stranger's event and a
  cancelled run.

A queue mode holds at most 100 events per trigger; at the cap the oldest is
dropped and recorded `superseded`. Held events survive a daemon restart and are
discarded when the trigger is disarmed.

## Poll scripts

Put the script in `{config_dir}/trigger-scripts/`. This is a convention; vincent
does not enforce it.

- Keep it beside `triggers/`, never inside it, and never in a repository.
- On POSIX, make the directory and the script owner-only (`0700`), and name
  the script by absolute path.
- `command:` runs directly with no shell. On Windows, write
  `command: [pwsh, -NoProfile, -File, <absolute path>.ps1]`.

The script's contract:

- It prints one JSON object per stdout line, each with a string `id`.
- It may print a last line `{"cursor": "..."}`. The next run receives that
  value in `$VINCENT_TRIGGER_CURSOR`.
- It exits non-zero on any failure.

Read the reference before writing one.

## Author and validate

1. **Write the file.** It goes at `{config_dir}/triggers/<id>.yaml`, and `id`
   must equal the file name without `.yaml`: lowercase letters, digits, `-`,
   `_` and `.`.
   - `vincent doctor` names the config dir, and `trigger_list` reports the
     triggers dir.
   - On POSIX, keep the file `0600`, as the daemon's own writer does.
   - The daemon reloads the file on save.
2. **Validate.** Run `vincent trigger validate <file>`.
   - It needs no daemon, and gives the same verdict as
     `POST /v1/triggers/validate` plus the check that `id` matches the file
     name.
   - It exits 0 when the file is valid and 1 when it is not.
   - Fix every error. Decoding is strict, and a key the chosen type does not
     take is an error, never ignored.
3. **Dry-run.** Run `vincent trigger test <id> --event event.json`.
   - It judges a sample event through the real pipeline and writes nothing.
   - It needs the daemon, the trigger loaded under that id, and a fixture
     shaped like the source's real events.
   - For a command source, `trigger_poll` runs the real command once and
     judges its output without firing.
4. **No binary?** Say validation was not run. Never substitute a generic YAML
   parser.

Report back:

- the file path,
- the `vincent version` output and the validate and dry-run results,
- the source, action and project id,
- the trust decision: which events, and `allowed_actors`,
- what the dedupe key means, and the limits,
- which credentials the daemon's environment must hold,
- each switch still off, and how a human turns it on.

## Arm and debug

A trigger is **armed** when its file validates, it says `enabled: true`, and
`triggers.enabled` is on.

- **Arming seeds.** The first poll records what the source already shows and
  fires nothing. A `schedule` anchors its clock at that moment instead, and
  the first occurrence it ever fires is one that falls after the keypress.
- **Disarming drops the cursor**, so arming again seeds again — and a
  `schedule` anchors afresh, which is why a disable/enable cycle fires nothing
  even when occurrences went by in between.
- **A save that does not validate keeps the cursor.**

Humans arm triggers in the TUI's Triggers view, which asks before enabling one,
or in an editor. No MCP tool writes or enables a trigger.

The built-in `create-trigger` and `update-triggers` workflows stage files for
`vincent trigger apply --proposal <task_id> --project <id>` to install. Apply
refuses any change that arms a trigger.

When a trigger misbehaves, work in this order:

1. `trigger_list` shows whether each trigger is valid, `armed`, its
   `disarmed_reason`, and its poll health.
2. `trigger_deliveries` shows each event's `outcome` (`seeded`, `fired`,
   `deduped`, `filtered`, `rate_limited`, `refused`, `error`, `superseded` or
   `queued`) and `detail`.
3. Reproduce with `vincent trigger test` or `trigger_poll`. A `schedule` and
   an `http` source have no poll, so `trigger_poll` refuses both: supply a
   synthetic event to `vincent trigger test` instead.

A scheduled reaction — `follow_up` or `retry` whose `branch` renders from the
occurrence — is **silent by design** when no unarchived task is on that branch:
the ledger records `refused` and nothing else happens. Check the deliveries
before concluding the clock did not strike.

The debugging reference maps each outcome to its causes.

## Migrate and review

List a project's triggers with `vincent trigger ls --project <id> --json`. It
gives each file's id, version, validity, `enabled`, `on_fire`, `permission` and
errors.

Report correctness and safety findings first, then check each trigger for:

- `match:` in front of `if:`,
- an explicit `dedupe_key`,
- `overrun:` on any source that can emit twice about one object, with a
  `concurrency_key:` naming that object and not repeating `dedupe_key`,
- `limits.max_per_hour`, and `max_task_cost_usd` on a `create_task`,
- `github_issue` or `github_pull` prefill,
- no `poll_interval` on a GitHub source,
- `allowed_actors` wherever an untrusted GitHub event can match,
- reactions that carry none of the keys they refuse,
- `http` signatures that use `secret_env`,
- no secret in any argv,
- poll scripts that follow the contract,
- `printf "%.0f"` on keys built from JSON numbers,
- nothing that relies on `.Event` in a step template.

Older drafts need these corrections:

- a project name becomes the project's id,
- a `container:` block becomes a containerized workflow,
- `poll_interval` comes off GitHub sources,
- `.Event.Actor` and `.Event.Label` do not exist, so remove them.

Some things must stay as they are:

- `id`, the file name and `source.project`.
- `enabled`, `on_fire` and `permission`, unless the user asks to change them.
- What `dedupe_key` renders for events already delivered.
- What `concurrency_key` renders, while a group has work in flight.

A trigger that is already right stays byte for byte.
