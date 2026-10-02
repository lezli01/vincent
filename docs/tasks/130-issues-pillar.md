# 130 — The issues pillar: vincent-owned issues per project

**Status:** ⏳ not started (0/18)

Issue [#659](https://github.com/lezli01/vincent/issues/659), part of
[#658](https://github.com/lezli01/vincent/issues/658). Spec §3 (rows 11, 26
and the new row 36) in this document's pull request; §5, §8, §12, §13, §14 and
§15 in the pull request of each item whose code makes them true.

## What this is

vincent owns and tracks issues itself. Each project gets an issue set that
users create and edit — title, description, labels, kind, priority — whose
state vincent manages, and tasks are created **from** an issue. For a
GitHub-based project the GitHub integration's job becomes **importing and
syncing** GitHub issues into that set, and a state change a human makes in
vincent on an imported issue is **written back** to GitHub. Issue selection
disappears from new-task creation: there is no issue picker of any kind in the
new-task form.

This is the intake pillar of the three-pillar architecture
(<https://blog.lezli01.is-a.dev/blog/vincent-three-pillars/>, 2026-09-30). The
handoff pillar — posting comments to GitHub, closing an issue when its work is
done, merging — is a later piece of work and nothing here builds it.

An earlier decomposition of a similar requirement (#637, sub-issues
#638–#657) was closed `NOT_PLANNED` on 2026-10-02. This plan differs where the
requirement does: no picker in the form, not even a local-issue one (unlike
#655); state is written back (unlike #643); the cross-project TUI navigation is
kept (unlike #649).

The pillar overturns or departs from several binding records — task 035, the
reach of task 068/069's write rules, spec §3 row 26, spec §12.3, task 073.
`CLAUDE.md` holds that such a decision is not relitigated "without saying so
explicitly", so every one is named below, with what it keeps, supersedes or
departs from, before any code lands. This document is the plan and the
decision record; it changes no code. Each `130.n` item is one issue of #658
and roughly one pull request.

## Decisions

Recorded 2026-10-02. The proposal is #659's; the author's answers to #658's
open questions 1, 2 and 5 rewrote decisions 7, 4 and 10, and are written in as
decisions rather than as questions. Each decision is **taken now; its effect
lands with its item.** The older record keeps governing the code until that
item's pull request merges. The dated notes in task 035, task 068 and spec §3
rows 11 and 26 are written now, by this document's pull request (decision 13).

### 1. Identity: a global `issues.id` (2026-10-02)

An issue is identified by one global integer `issues.id`, like a task. There is
no per-project number in v1 (`PROJ-12`); an imported issue displays its GitHub
reference `owner/repo#N` beside its id. The schema is migration `0036` (130.1);
`0035` is the last applied migration.

*Alternative beaten:* a per-project number. It needs a counter per project, a
second key every route and CLI argument must disambiguate, and renumbering
rules on project removal — none of which the first version needs. The schema
leaves the seam.

### 2. External identity lives in `issue_remotes` — departs from task 035 decision 5 (2026-10-02)

An imported issue's link to its source is a row in
`issue_remotes(provider, remote_key, repo, number, url, …)`. For GitHub,
`remote_key` is the issue's `node_id`, which survives a transfer and a rename;
`repo` and `number` are stored for display and for the API calls. Sync also
stores the repository each project is bound to.

**Departs from task 035 decision 5** (`035-github-issue-selection.md:109-123`):
"'GitHub-based' is parsing the `origin` remote … Neither is stored." That held
for a once-at-creation fetch. Durable sync cannot hold it: if the repository
were re-derived from `origin` at each use, a re-pointed origin would silently
re-key every imported issue onto a different repository. Decision 5 itself
named "PR checking, which needs a durable repo identity" as the expected
trigger to revisit it; issue sync is the trigger that arrived. The `provider`
column is the minimal seam for trackers other than GitHub, none of which is
built.

*Alternative beaten:* keying on `repo` + `number`. A transferred issue keeps
its `node_id` and changes both.

### 3. State: `open | closed`, a close reason, and derived activity (2026-10-02)

An issue's state is `open` or `closed`. A closed issue carries a
`close_reason` — `completed`, `not_planned` or `duplicate` — and a duplicate
carries `duplicate_of_issue_id`. The column has **no `CHECK` constraint**, so
a later state (the handoff pillar's `waiting`, say) needs no table rebuild;
`internal/issuestate` (130.1) is the one definition of what may happen next,
as `internal/taskstate` is for tasks.

Whether an issue is being worked on is **derived, never stored**: `active` and
`task_count` are read from the issue's root tasks, counting those for which
`!taskstate.Settled` (`internal/taskstate/taskstate.go:73`). Whether a
human-settable state such as `in_progress` is wanted as well is open question
1.

*Alternative beaten:* GitHub's state vocabulary extended with vincent states.
GitHub can hold only `open`/`closed` plus a reason, and sync would have to
preserve every state it cannot represent.

### 4. Labels plus `kind` and `priority`, and no `tags` (2026-10-02)

Labels are one catalogue per project, `UNIQUE(project_id, name COLLATE
NOCASE)`. Beside them an issue has a `kind` — an unconstrained token, with
`bug`, `feature`, `task`, `chore`, `question` and `docs` suggested — that a
workflow can branch on instead of probing a label, and a local `priority`:
`0` none, `1` urgent … `4` low, Linear's scale. That scale is **inverted**
relative to a task's priority ("integer, default 0; higher runs first",
`docs/spec.md:392`); anything that maps one onto the other says so where it
does.

*Settled by the author (#658 question 2):* labels plus `kind` only. There are
no vincent-only `tags`.

*Alternative beaten:* a second free-form classifier. No surveyed tracker has
two, and a label that is never written back already exists — decision 9 makes
labels a read-only mirror for imported issues and fully local otherwise.

### 5. The task link: a pointer **and** a snapshot — keeps task 035 decisions 3 and 9 (2026-10-02)

A task created from an issue carries `tasks.issue_id` (`ON DELETE SET NULL`)
and a `tasks.issue_json` snapshot taken at creation. The pointer answers "which
tasks came from this issue" by reading it backwards; the snapshot is what
templates render.

- **Keeps task 035 decision 3** (`035-…:74-85`, "the snapshot is one nullable
  JSON column"): the snapshot is still the thing a run reads, so a render is
  still offline and reproducible. The pointer sits beside it; it does not
  replace it.
- **Keeps task 035 decision 9** (`035-…:208-214`): fan-out lanes inherit both
  `issue_id` and `issue_json` from their parent.
- **Lanes never count as an issue's tasks.** Decision 3's `task_count` and
  `active` read root tasks only — the same rule as notify's
  (`internal/notify/notify.go:279-285`, task 046 decision 2): a twenty-lane
  tree is one piece of work, not twenty.
- **The task snapshot is still never re-fetched.** That half of task 035
  decision 10 (`035-…:216-232`, "re-fetching a stale issue" out of scope) and
  of spec §3 row 26 ("never re-fetched") is **kept for the snapshot**; it is
  superseded only for the issue entity, which sync re-reads (decision 9).
  Whether a follow-up run may refresh its snapshot is open question 5.

### 6. Delete in any state; no archive (2026-10-02)

An issue may be deleted in any state, and there is no archive. A task's
`issue_id` becomes NULL and its snapshot stays, so the task still renders what
it was created from. Deleting an imported issue turns its `issue_remotes` row
into a **tombstone**, so the next poll does not import it again.

*Alternative beaten:* an archive like tasks and chats have. An issue holds no
worktree, branch or transcript to keep, and a closed issue already answers
"no longer wanted".

### 7. `github_issue` is removed from every create surface — supersedes task 035 decision 2, retargets decision 7 (2026-10-02)

`github_issue` on `POST /v1/tasks`, `vincent task add --github-issue` and a
trigger's `action.github_issue` are **removed**, not kept as a shorthand. A
GitHub issue reaches a task only by being imported into the project's issue
set (decision 9) and then created from with `issue_id` (130.7).

*Settled by the author (#658 question 1):* hard removal. This **reverses** the
proposal in #659 ("a permanent resolve-or-import shorthand; field kept
byte-for-byte for idempotency digests") and #658's "Contradictions resolved:
`github_issue` lifetime → permanent". The consequences are accepted by name:

- **API clients get a 400.** Request decoding is `DisallowUnknownFields`
  API-wide (`decodeJSONLimit`, `internal/api/projects.go:636`), so a client
  still sending `github_issue` is refused rather than silently ignored.
- **User trigger files stop validating.** Trigger files decode strictly
  (`internal/trigger/definition.go`), so a trigger carrying
  `action.github_issue` fails validation and is disarmed until a human edits
  it.
- **Every `POST /v1/tasks` idempotency digest changes once.**
  `createTaskRequest.GitHubIssue` is `*int` with no `omitempty`
  (`internal/api/tasks.go:497`), and the digest re-marshals the decoded struct
  (`internal/api/idempotency.go:68-83`), so `"github_issue":null` is in every
  digest today. Removing the field changes the digest of every create request:
  a retry that spans the upgrade inside the 24-hour window is reported as a key
  conflict. Any create field added from here on is `omitempty`, so a request
  that does not carry it keeps its digest.
- **Every in-repo consumer migrates in the pull request that removes the
  field**, not in this one: `.vincent/workflows/github-resolve-issue*.yaml` and
  `github-create-issue.yaml`, the embedded `skills/vincent-triggers/` text
  (`SKILL.md`, `references/trigger-schema.md`, `references/debugging.md`),
  `scripts/screenshots.sh`, `docs/guides/triggers.md` and
  `docs/reference/{api,cli,configuration,workflow-schema}.md`.
- **Release notes carry it as a breaking change** for API clients and trigger
  files.

What stays:

- The raw listing, `GET /v1/projects/{id}/github/issues` and `vincent github
  issues`, stays as a remote browse. Only its `?workflow=` prefill goes.
- The legacy `tasks.github_issue_json` snapshots are still backfilled into the
  issue set (130.11). Removal concerns the create surface, not stored history.

**Supersedes task 035 decision 2** (`035-…:44-72`, "the daemon owns the
prefill; the TUI previews it"): the `?workflow=` prefill on the GitHub listing
and the `github_issue` create path it existed to match are both gone. Its
principle — **one daemon-side mapping**, which made "the CLI and the TUI
produce the same stored task" a testable claim — is **kept**, and moves to
prefill from a vincent issue (130.4).

**Retargets task 035 decision 7** (`035-…:143-198`, "prefill rules"): the
rules map a vincent issue onto task fields instead of a GitHub issue, and are
otherwise kept — title prefix, link line, exact-name declared fields, values
offered only when the declaration would accept them, no invented names.
Decision 8 below changes what the declared `issue` field is filled with.

**Supersedes task 035 decision 4 for the new-task form only**
(`035-…:87-107`): the form's issue row, and with it the form's call to the
availability probe, is deleted (130.13). The probe itself,
`GET /v1/projects/{id}/github`, and the rows `vincent doctor` and `vincent
github status` read from the same machinery, stay.

### 8. `.Issue` describes the vincent issue — keeps task 035 decision 8 (2026-10-02)

`.Issue.Number` is the **vincent issue id**; the GitHub reference of an
imported issue is `.Issue.Source`. A declared `issue` field is filled with the
vincent id, and a declared `github_issue` field with the GitHub number, so a
workflow that acts on GitHub can still reach it from a `run:` body.

**Keeps task 035 decision 8** (`035-…:200-206`): `.Issue` is zero-valued when
absent, `.Issue.Number` is non-zero for every linked task, and rendering reads
the snapshot from the task row, never the network. **Amends task 035 decision
7's 2026-08-27 note** (`035-…:179-194`): the declared `issue` field held the
GitHub number; it now holds the vincent id, and the GitHub number moves to
`github_issue`.

### 9. Sync: state two-way, content a read-only mirror, GitHub wins — departs from task 035 decision 6 (2026-10-02)

For a GitHub-based project, open GitHub issues are imported into the project's
issue set and kept in sync on the existing `github.poll_interval` tick (130.8),
riding the pull-request reconciler's loop.

| Field | Direction |
|---|---|
| state and close reason | two-way |
| title, body, labels, assignees, milestone | GitHub → vincent, read-only mirror |
| comments | GitHub → vincent, read-only mirror |

A true conflict — both sides changed state since the last sync — is resolved
in GitHub's favour. Issues created in vincent are never pushed to GitHub in v1;
publishing one is a later, explicit human action. The depth of the initial
import is open question 4.

**Departs from task 035 decision 6** (`035-…:125-141`) and its two restatements
— spec §12.3 (`docs/spec.md:7354-7360`, "makes no call at all until a human
opens the issue picker or names an issue on the command line") and the comment
on `config.GitHub.Enabled` (`internal/config/config.go:383-388`). A
GitHub-based project with the integration enabled now has **standing**
per-project issue traffic, as task 052 already added for pull-request links.
Both restatements go stale when 130.8 lands, and that item's pull request
amends them.

**Supersedes task 035 decision 10's "re-fetching a stale issue" for the issue
entity** (`035-…:216-232`): sync re-reads imported issues. It is kept for the
task snapshot (decision 5).

*Alternative beaten:* two-way content. Title, body and label write-back each
need their own conflict rule and consent, and the requirement asks only for
state to be synced back.

### 10. Only a human writes back: MCP- and step-originated changes are refused — keeps spec §3 row 11 and task 068 decision 1, supersedes task 035 decision 10 for issue state (2026-10-02)

A state change a **human** makes on an imported issue — through the TUI, the
CLI or the API — is written back to GitHub, through a crash-safe outbox written
in the same transaction as the change (130.10). An **MCP-originated or
step-originated** state change to an imported issue is **refused** with
`forge_write_needs_human`; on a local issue both are allowed, since nothing
leaves the machine. Nothing closes an issue automatically — closing on task
`done` belongs to the handoff pillar.

*Settled by the author (#658 question 5):* a step running `vincent issue close`
is refused like MCP, not treated like a human. How the API tells a step's call
from a human's is open question 6, for 130.10.

- **Keeps spec §3 row 11** ("Nothing on the step path reaches GitHub … every
  write route is excluded from the MCP tool surface"). Issue-state write-back
  is one more human-triggered write in row 11's sense; the step-path and MCP
  exclusions are unchanged, and refusing step-originated writes is what keeps
  them so.
- **Keeps task 068 decision 1** (`068-pull-request-tab.md:49-84`) and its
  restatement in `internal/github/doc.go:37-41` ("no step type, default
  workflow or automatic behaviour calls a write").
- **Keeps task 069 decision 3** (`069-open-a-pull-request-from-vincent.md:64`,
  the write route is excluded from MCP): the MCP tools may write local issues,
  and the guard refuses the rest.
- **Supersedes task 035 decision 10's "writing anything to GitHub" for issue
  state only.** Title, body, labels and comments are still never written.
- **Amends spec §3 row 26** ("nothing here writes to GitHub"), by a dated note
  pointing at row 36.

### 11. Issue `PATCH` requires `version` — departs from spec §12.3 (2026-10-02)

`PATCH /v1/issues/{id}` carries the `version` the client read; a stale one is
`409 invalid_state` with `details.reason: issue_changed` (130.3). State changes
are action routes (`close`, `reopen`), never a writable `state` in a `PATCH`.

**Departs from spec §12.3** (`docs/spec.md:7709-7712`, "There is no
`ETag`/`If-Match`: a precondition concept no other endpoint in this API
carries, for a race between a human and themselves") and its restatement at
`internal/api/config.go:684-688`. That reasoning holds for `config.yaml`; it
does not hold here, because the second writer is the sync loop, not the same
human.

Issue create takes an `Idempotency-Key` through a new table. **Departs from
migration 0016's** expectation that "a later route joins the table without a
migration" (`internal/store/migrations/0016_idempotency.sql:13-15`): it did
not hold for a non-task route, because `task_id` is `INTEGER NOT NULL
REFERENCES tasks(id)` (`:40`).

### 12. Issue bodies render as Markdown — widens task 073 decision 5 (2026-10-02)

An issue's description renders as Markdown in the TUI, with the same raw
toggle the output pane has (130.9). **Widens task 073 decision 5**
(`073-assistant-markdown-in-output.md:117`, "what stays literal, precisely")
and its restatement at `internal/tui/markdown.go:12-18` ("Only assistant prose
reaches this file"): issue descriptions are prose a human wrote as Markdown,
which is the property that admitted assistant prose. Everything else that
decision keeps literal stays literal.

### 13. Spec §3 records this ahead of its code (2026-10-02)

Spec §3 row 36 is added, and rows 11 and 26 take dated notes, **in this
document's pull request**, before any item lands. That is an explicit exception
to this directory's rule that a behaviour change lands in the spec "in the same
pull request as the code that makes them true — never ahead of it"
(`README.md`); every earlier §3 row followed the rule (row 35 landed in
`bc90c1e1` and row 33 in `2888b76e`, each in the pull request that carried its
code, #475 and #417). The author chose it because every
item of #658 cites these decisions, and §3 is where a decision record is read.
Row 36 says in its own note that it records a decision ahead of its code and
names the items that make it true, so a reader of §3 is not misled about what
is built. The exception covers §3 only: §5, §8, §12, §13, §14 and §15 are
amended by each item in its own pull request.

## Open questions

Each has a proposed default, which stands unless the author answers otherwise
before the item that needs it starts.

1. **A human-settable "in progress"?** (#658 question 3.) *Proposed:* no —
   activity is derived (decision 3). A state such as `in_progress` or `triage`
   is one GitHub cannot hold, so sync would have to preserve it.
2. **An opt-out of import alone?** (#658 question 4.) *Proposed:* no new key —
   `github.enabled` and `github.poll_interval: 0` are the controls, and there
   is no `github.issue_sync`. Adding one would depart from task 069 decision 2
   (`069-…:54`, "`github.enabled` is the only gate. No new config key"), and
   would have to say so.
3. **Projects sharing an origin.** (#658 question 6.) *Proposed:* two issue
   sets, one per project, which is what per-project rows give; both write
   back.
4. **Initial import depth.** (#658 question 7.) *Proposed:* open issues only,
   capped at 500, resumable across ticks.
5. **Refreshing the snapshot on `follow_up`.** (#658 question 8.) *Proposed:*
   no refresh. An opt-in `refresh_issue` would relitigate task 035 decision 8
   and the never-re-fetched half of decision 5 for that case.
6. **Telling a step's call from a human's.** (Decision 10, for 130.10.) A step
   running `vincent issue close` reaches the API the way a human's CLI does.
   The mechanism that marks it step-originated is an implementation question
   for #669.

## Tasks

In #658's delivery order. Items without a `Depends:` can proceed in parallel.
Each item amends the spec sections and public pages its code makes true, in
its own pull request.

- [ ] **130.1** ([#660](https://github.com/lezli01/vincent/issues/660))
  `internal/issuestate`, migration `0036` (issues, remotes, labels, comments,
  `tasks.issue_id`/`issue_json`), store CRUD, in-transaction `issue.*` events,
  the `internal/issues` write path.
- [ ] **130.2** ([#661](https://github.com/lezli01/vincent/issues/661))
  `cmd/fakegh` with a mutable issue corpus, `gh api` with ETag/304, issue
  writes and a scenario file.
- [ ] **130.3** ([#662](https://github.com/lezli01/vincent/issues/662))
  `/v1/issues` routes, `issue.*` SSE events, apiclient, MCP tools, idempotent
  create. Depends: 130.1.
- [ ] **130.4** ([#663](https://github.com/lezli01/vincent/issues/663)) Prefill
  from a vincent issue, the `.Issue` reshape, snapshot build, legacy rendering,
  skill and checklist lines. Depends: 130.1.
- [ ] **130.5** ([#664](https://github.com/lezli01/vincent/issues/664)) Durable
  issue listing (`node_id`, pagination, conditional requests, gone/moved) and
  issue state writes in `internal/github`. Depends: 130.2.
- [ ] **130.6** ([#665](https://github.com/lezli01/vincent/issues/665)) The
  `vincent issue …` CLI tree. Depends: 130.3.
- [ ] **130.7** ([#666](https://github.com/lezli01/vincent/issues/666))
  `issue_id` on task create, the prefill preview, `?issue_id=` filter, the task
  DTO link, `Closes #N`, `vincent task add --issue`. Depends: 130.3, 130.4.
- [ ] **130.8** ([#667](https://github.com/lezli01/vincent/issues/667)) Import
  and refresh on the reconciler tick, sync status, config and doctor text.
  Amends spec §12.3's "no call until a human opens the issue picker"
  (`docs/spec.md:7354-7360`), `internal/config/config.go:383-388` and
  `docs/reference/configuration.md` (decision 9). Depends: 130.1, 130.3, 130.5.
- [ ] **130.9** ([#668](https://github.com/lezli01/vincent/issues/668)) The TUI
  Issues list and Issue detail. Depends: 130.3.
- [ ] **130.10** ([#669](https://github.com/lezli01/vincent/issues/669)) The
  write-back outbox, its compare-and-set drain, and the guard refusing MCP- and
  step-originated writes (decision 10, open question 6). Depends: 130.8.
- [ ] **130.11** ([#670](https://github.com/lezli01/vincent/issues/670)) SQL
  backfill of task 035's snapshots into issues, and the removal of
  `github_issue` from `POST /v1/tasks`, `--github-issue` and
  `action.github_issue` with every consumer decision 7 lists. Depends: 130.7,
  130.8.
- [ ] **130.12** ([#671](https://github.com/lezli01/vincent/issues/671)) The
  TUI issue create/edit form with close and reopen, and a shared `$EDITOR`
  helper. Depends: 130.9.
- [ ] **130.13** ([#672](https://github.com/lezli01/vincent/issues/672)) Seed a
  new task from an issue, delete the new-task form's issue picker, the task
  workspace's Issue section. Depends: 130.9, 130.7.
- [ ] **130.14** ([#673](https://github.com/lezli01/vincent/issues/673))
  `VINCENT_ISSUE_FILE`, and the repo's resolve workflows migrated onto it.
  Depends: 130.7, 130.6, 130.8.
- [ ] **130.15** ([#674](https://github.com/lezli01/vincent/issues/674)) A
  `type: issues` trigger source, `issue:` on `create_task`, the trigger skill.
  Depends: 130.3, 130.7.
- [ ] **130.16** ([#675](https://github.com/lezli01/vincent/issues/675)) The
  local discussion thread and the read-only GitHub comment mirror. Depends:
  130.3, 130.8.
- [ ] **130.17** ([#676](https://github.com/lezli01/vincent/issues/676)) An
  end-to-end gate on all three platforms. Depends: 130.7, 130.10.
- [ ] **130.18** ([#677](https://github.com/lezli01/vincent/issues/677))
  Screenshot seed, new tapes, recaptures, the features page. Depends: 130.12,
  130.13, 130.10.
