---
title: Jira issue sync
---

# Jira issue sync

A vincent project can take its issues from a Jira Cloud project instead of
GitHub. Bound to a Jira site, the daemon imports the project's open issues
into vincent's own [issues](issues.md), keeps them in sync on a timer, and
writes a person's close or reopen back to Jira as a workflow transition. From
there a Jira issue is worked like any other: it has a lane, starts tasks, and
can be handed off.

vincent speaks to Jira Cloud's REST API itself; there is no CLI to install.
Jira Data Center and Jira Server are not supported.

## Binding a project

A project's **issue source** is one setting, stored by the daemon: `derive`
(the default, which follows `origin` to GitHub or to nothing), `none`,
`github` or `jira`. A project has exactly one. Bind it with the
[issue-source flags](../reference/cli.md#issue-source-flags):

```sh
vincent project edit 1 --issue-source jira \
  --jira-site https://acme.atlassian.net --jira-project ENG
```

The same binding is the `issue_source` object of
[`POST` and `PATCH /v1/projects`](../reference/api.md#issue-source), and the
**jira site**, **jira project** and **jira jql** rows of the TUI's project form.

- **The site** is an absolute `https://` base URL with no path, user, query or
  fragment. `http://` is accepted only on `localhost`, a 127.0.0.0/8 address or
  `[::1]`.
- **The project** is a Jira project key, such as `ENG`.
- **The JQL** is optional: one line, no `ORDER BY`. It narrows the project's
  issues and never replaces the project, so `--jira-jql 'labels = backend'`
  imports only the bound project's issues labelled `backend`.

The binding lives in the daemon's database, never in a file in the
repository — see the [security model](../security-model.md#credentials) for
why.

Changing the provider, the site or the project key is a **rebind**. The
previous source's imported issues are kept, marked `missing`, and no longer
written back, and the project's sync starts over. Changing only the JQL or the
mapping is not a rebind. Binding back re-imports the kept issues and marks
them live again.

## Credentials

vincent stores no Jira credential. The daemon reads two environment
variables, **by name**, on every request:

| Variable | Holds | Renamed by |
|---|---|---|
| `JIRA_USER` | The e-mail address of the Atlassian account | [`jira.user_env`](../reference/configuration.md#jira) |
| `JIRA_API_TOKEN` | An API token of that account | [`jira.token_env`](../reference/configuration.md#jira) |

`config.yaml` holds the variable names, never the values, and nothing about
the credential is written to the database or the log. The values are read per
request, but from the environment the daemon started with: a new token set
under [`environment.set`](../reference/configuration.md#environment) takes
effect on the next sync pass with no restart, while one exported in a shell
reaches the daemon only when it is restarted from that shell.

The variables must be set **in the daemon's environment**. One exported in
your shell is enough when that shell started the daemon. Under
[`vincent service install`](running-at-login.md), which does not inherit your
login shell, set them under
[`environment.set`](../reference/configuration.md#environment) in
`config.yaml` — an owner-only file, but the token is in it in plain text; the
[security model](../security-model.md#credentials) states the trade-off.

While either variable is unset, the project's sync reports `no_credential`
and imports nothing.

## Checking it

- [`vincent jira status --project ID`](../reference/cli.md#vincent-jira-status)
  checks one binding on demand: is the site reachable, is the credential
  accepted, and does the bound project exist and is it visible to the
  account. It stores nothing and never syncs.
- [`vincent doctor`](../reference/cli.md#vincent-doctor)'s `JIRA` group shows
  the `jira.enabled` switch, whether the daemon sees each credential variable,
  one row per bound site and one per Jira project. It asks each distinct site
  one question, and only when probed — see
  [Troubleshooting](troubleshooting.md#jira) for why it never retries.
- [`vincent issue sync`](../reference/cli.md#vincent-issue-sync) shows the
  project's sync status and asks for a pass now; `S` on the TUI's issues list
  does the same.

## What syncs and how

The first pass imports the project's open issues, narrowed by the binding's
JQL. Each later pass, every
[`jira.poll_interval`](../reference/configuration.md#jira) (5 minutes by
default), reads what changed since the last one, closed issues included.

![An issue imported from Jira in the TUI: MOB-7 with its labels, `source
MOB-7`, its sync state, the description, the two public comments mirrored
from Jira and marked `· jira` — a third, restricted to a role, is not among
them — and the Source section with the Jira browse URL](../assets/tui-issue-jira.png)

- **Open or closed.** A Jira issue is closed in vincent if and only if its
  status is in Jira's *Done* category. Every other status — *To Do*,
  *In Progress*, *In Review*, any custom one — is open. The resolution never
  decides openness, and Jira's in-progress statuses are not vincent lanes: an
  issue's [lane](issues.md) comes from the vincent work on it, not from its
  Jira status.
- **Close reason.** A closed issue's reason comes from its resolution: one in
  the duplicate set reads as `duplicate`, one in the not-planned set as
  `not_planned`, anything else as `completed`. When the resolution is empty,
  a Done status named *Cancelled* (or one in the not-planned set) reads as
  `not_planned`. By default the duplicate set is `Duplicate` and the
  not-planned set is `Won't do`, `Won't Fix`, `Cannot Reproduce`, `Incomplete`
  and `Declined`, compared case-insensitively; the binding's
  [mapping](#transition-overrides) can replace either.
- **Kind** is seeded from the Jira issue type on the first import only, and
  stays yours to change. **Priority** stays local and is never read from Jira.
- **Labels** are mirrored; one longer than vincent's label limit is dropped.
- **Body and comments** are converted from Jira's rendered HTML to Markdown.
  An imported issue's thread is Jira's: vincent mirrors it and refuses a
  person's comment on it (`issue_mirrored`).

## Transition overrides

Jira has no open/closed field, so writing a close or reopen means picking a
workflow transition. vincent picks the one transition the issue offers *now*
that lands exactly where the change asks, and fails rather than guessing.
When your workflow needs a hint, give the binding a mapping:

```json
{
  "overrides": { "close_not_planned": "Won't do", "reopen": "Back to To Do" },
  "not_planned_resolutions": ["Won't Do", "Won't Fix"],
  "duplicate_resolutions": ["Duplicate"]
}
```

```sh
vincent project edit 1 --jira-mapping mapping.json
```

`overrides` names the transition for `close_completed`, `close_not_planned`,
`close_duplicate` and `reopen`; the two lists replace the resolution sets
[above](#what-syncs-and-how). Unknown fields are refused. The
[API reference](../reference/api.md#issue-source) lists the rules.

## Write-back

Only a person's change is written to Jira, plus the writes of a handoff you
granted:

- A person's **close or reopen** of a Jira issue, from the TUI, the CLI or the
  API, is sent as one transition.
- A [handoff](handoff.md) whose
  [grant](handoff.md#grants-letting-a-handoff-write-to-github-or-jira) names
  `issue.close` may close or reopen its items, and one that names
  `issue.comment` may comment on them; a comment is posted as an Atlassian
  Document Format comment.
- Anything else an agent asks for — over MCP, or a `vincent` command run by a
  step or a chat — is refused `forge_write_needs_human` and nothing is sent.

A close as `duplicate` sets the Duplicate resolution and creates no issue
link. Writes go through the same outbox as GitHub's: each is queued, retried
and counted on the project's sync row (`writes N pending, N failed, N
conflict`), and an issue's own `sync` field in `vincent issue show` carries
its reason. A write that gives up keeps the local state and fails with one
of:

| Reason | What to do |
|---|---|
| `no_transition` | No transition the issue offers lands there — say, no direct path from *In Review* to Done, or none at all because the account lacks *Transition issues*. Make the change in Jira, grant the permission, or add an override |
| `ambiguous_transition` | Several transitions qualify. Name one in the mapping's `overrides` |
| `transition_needs_fields` | The matching transition's screen requires a field vincent does not set. Make the field optional on that screen, or make the change in Jira |
| `no_write_scope` | Jira answered 403 to the write: the account may read the project but not change it. Grant it the permission, or use an account that has it |

While `jira.enabled` is `false` or `jira.poll_interval` is `0`, writes are
**not dropped**: they wait `pending` with `disabled`, and are sent once both
are back on.

## Moved and missing issues

An issue moved to another Jira project is marked `moved`, and its key and
browse URL are updated to the new ones. An issue a pass can no longer read —
deleted, no longer visible to the account, archived, or outside the binding's
JQL — is marked `missing`. vincent **never deletes** a Jira-imported issue:
the issue, its tasks and its thread stay, and it is marked live again if it
reappears.

## Restricted comments

A Jira comment restricted to a group or role is **never newly mirrored**. A
comment mirrored while it was public, and restricted in Jira afterwards,
stays mirrored with the body it had.

## Jira issues in workflows and triggers

A task created from a Jira issue gets:

- the title `PROJ-12 <title>`, and the body followed by a
  `Jira issue PROJ-12: <url>` line, all editable before the task exists;
- `.Issue.Source.Key` in templates, and `VINCENT_ISSUE_KEY` in the
  environment of command, check and agent steps, holding `PROJ-12`.

A Jira issue has no number: `.Issue.Number` is zero, `number` and
`source.number` are `null` in the `VINCENT_ISSUE_FILE` snapshot, and
`VINCENT_ISSUE_NUMBER` is not set. A workflow serving both trackers reads the
key. The `github_issue`, `assignee` and `milestone` prefill fields stay
GitHub-only. See [Writing workflows](workflows.md) for `.Issue`.

An [`issues` trigger](triggers.md#issues-watch-the-projects-issues) fires on
Jira-imported issues like any other, as `by: sync`. A Jira `labeled` or
`unlabeled` is **untrusted** — anyone who may edit a Jira issue may label it
— so a trigger on one must name `allowed_actors`. An `issues` trigger sees
`opened`, `closed`, `reopened`, `labeled`, `unlabeled`, `lane_changed` and
`handoff_changed`, never a move between two open Jira statuses. Every one of
those events by `sync` is untrusted — `opened`, `closed` and `reopened` for
every tracker — so an `issues` trigger that can match one must name
`allowed_actors`, which for a Jira issue are reporter display names (see
[Trust](triggers.md#issues-watch-the-projects-issues)).

Do not narrow the binding's JQL to a status to start work when an issue
reaches *Ready for Dev*. An issue the binding's JQL stops matching is marked
`missing` (see [Moved and missing issues](#moved-and-missing-issues)), so the
moment someone moves it on to *In Progress* its Jira close is no longer read
and a close in vincent is no longer written back. Keep the binding's JQL on
something an issue keeps while it is worked on, such as a label, and start
Ready-for-Dev work with a
[poll-script trigger](triggers.md#a-jira-status-starts-work-unattended). A
poll-script trigger also fits a Jira project vincent does not sync, or JQL
beyond what the binding narrows to. There is no `type: jira`.

## Turning it off

- `jira.enabled: false` in `config.yaml` makes no Jira call at all.
- `jira.poll_interval: 0` stops the sync pass.
- `vincent project edit ID --issue-source none` (or `derive`) unbinds one
  project; its Jira issues are kept and marked `missing`.

Either switch holds write-back too, without dropping a queued write.

## Troubleshooting

Every sync and write-back reason, and what to do about it, is in
[Troubleshooting → Jira](troubleshooting.md#jira).
