# 130 — The issues pillar: vincent-owned issues per project

**Status:** 🔄 in progress (13/18)

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

*Settled with 130.7 (2026-10-02), by the author:*

- **A closed issue may back a new task.** `issue_id` naming a closed issue
  creates the task and adds a line to the create response's `warnings` rather
  than refusing it: one issue backs many tasks, and follow-up work on a closed
  issue is legitimate.
- **The chat handoff carries `issue_id`.** It goes through the shared
  `prepareTaskCreate`, so a handed-off task is linked and snapshotted exactly
  like a direct create, and a closed issue's warning lands in the handoff
  response's `warnings` too.

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
  `action.github_issue` fails validation and stops polling until a human edits
  it. An invalid file is not armed, but it is not disarmed in the manager's
  sense either: it keeps its cursor (`internal/trigger/registry.go:30-32`), so
  the edited trigger resumes rather than re-seeding.
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

*Settled with 130.4 (2026-10-02, issue #663):*

1. **The `github_issue` create path stays legacy until 130.11 removes it.**
   `POST /v1/tasks` `github_issue` and `vincent task add --github-issue` still
   fetch from GitHub and still write only `tasks.github_issue_json`, never
   `issue_id` or `issue_json`. Their prefill fills **both** a declared `issue`
   and a declared `github_issue` field with the GitHub number, so a workflow
   switched to `github_issue` keeps working through the window, and `.Issue`
   on such a task follows the legacy mapping (`Number` is the GitHub number,
   `Source` repeats it). `issue_id` on create stays in 130.7: the
   snapshot-backed prefill ships as a tested library (`issues.PrefillFrom`,
   wrapped by the API's `vincentIssuePrefill`) that 130.7 wires in, so "prefill
   makes no GitHub call" is a property of that path, proved by its tests.
2. **A local issue's prefilled title is its bare title.** A `#N` prefix would
   read as a GitHub number. An imported issue keeps `#<GitHub number> title`,
   never doubling a prefix the title already carries.
3. **A local issue's prefilled description has no link line** — it is the
   body, CRLF-normalized. An imported issue keeps body, blank line,
   `GitHub issue #N: <url>`.
4. **Oversized bodies still fail with 400 and are never truncated.** Both
   paths fold through one bound re-check; the issue's "truncate with a
   visible marker" proposal is dropped.

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

### 14. 130.3: `author` is daemon-derived, imported content is refused now, `duplicate_of` is same-project (2026-10-02)

Settled with the author while scoping #662.

1. **`author` is not a request field.** An HTTP create records the OS
   username the daemon runs as; an MCP create from a step records `task N` and
   sets `issues.created_by_task_id` (migration 0037), as `POST /v1/tasks`
   records a task's MCP provenance; a create on the shared `/mcp` endpoint
   records `agent`. A client cannot forge either. Sync fills the GitHub login
   when it lands. Every MCP write is actor `agent`, every other API write
   `human`; telling a step's CLI call from a person's stays open question 6
   (settled by decision 17: a marker header makes it `agent`).
2. **An imported issue's mirrored content is refused in 130.3, not deferred to
   #667.** A `PATCH` touching `title`, `body` or labels on an issue with a live
   remote row is `409 invalid_state` with `details.reason: issue_mirrored`
   (decision 9); `kind` and `priority` stay editable. The guard is
   `internal/issues`', keyed on the actor not being `sync`, and the issue DTO's
   `editable` lists what a client may offer.
3. **`duplicate_of` is optional and same-project.** Only with reason
   `duplicate`, naming another existing issue in the same project (otherwise
   `400`); omitting it is valid. It is set in the close's transaction and
   carried by `issue.state_changed`.

Also standing from the issue's proposed defaults: the list is a bare array
(no `{issues, counts}` envelope until 130.9 needs counts), there is no
`mcp.max_issues` cap, and `DELETE /v1/issues/{id}` is excluded from MCP on
task 092's line.

### 15. 130.8: the importer lives in the daemon, imports emit per-issue events, `vincent issue sync` lands first (2026-10-02)

Settled with the author while scoping #667.

1. **No `internal/issuesource` package.** The importer is
   `internal/daemon/issuesync.go`: it calls `github.Client.ListIssuesSince`
   and maps each result onto `store.RemoteIssue`. The `provider` column is the
   only provider seam; the first provider does not design for the second.
2. **Per-issue `issue.created` on import, no `issue.imported` kind.** §13.3's
   six `issue.*` kinds stand and clients debounce. The one new kind is
   `issue.sync_changed {project_id, ok, reason?}`, emitted only on ok↔failing
   transitions, on `trigger.poll_changed`'s precedent.
3. **`vincent issue sync` lands now** as the first leaf of the `vincent issue`
   tree, ahead of 130.6, which grows the rest around it.
4. **Kind and priority are local.** A refresh never writes either; an import
   creates the issue with `kind` empty. Assignees, milestone, the author's
   login, `closed_at` and `state_reason` go into `remote_json`.
5. **The repo binding is sticky.** The first successful sync records the
   repo; a later tick whose `origin` names another stops that project's sync
   with `origin_changed` and never re-keys. A GitHub-side rename is followed
   through the `node_id` match.
6. **Nothing is deleted.** The daily open-set scan marks an imported issue
   `remote_status: moved` (transferred) or `missing` (gone, 404/410, or
   converted to a discussion, pending #664's experiment); neither removes the
   local row.
7. **Sync failures are never quiet**, unlike the pull-request half of the
   same tick: each is recorded on the project's sync row with a reason, and a
   rate limit backs off until GitHub's reset.
8. **`POST /v1/projects/{id}/issues/sync` is not an MCP tool.** It only nudges
   the importer today, but once write-back (#669) lands it flushes pending
   edits to GitHub, so it stays a human act. The status `GET` is a tool.

Kept, not relitigated: the mirrored-field refusal stays `issue_mirrored`
(decision 14.2); `github.enabled` and `poll_interval: 0` are the only switches
(open question 2); projects sharing an origin import their own copies (open
question 3); the initial import is open issues only, 500 per pass, resumable
(open question 4).

### 16. 130.9: the TUI issue screens (2026-10-02)

Settled with the author while scoping #668. Spec §15 views 12 and 13 record
the screens.

1. **The list is cross-project and grouped by project**, like the
   pull-requests takeover; the headings are drawn, not rows. It is one
   `GET /v1/issues`, not one call per project, so a load error is
   screen-wide and view 7's per-project error band does not carry over. `/`
   filters client-side on id, title, label, kind and project name.
2. **`R` re-reads only.** It never requests a GitHub sync; that stays on the
   reconciler tick and `vincent issue sync` (130.8). 130.9 does not depend on
   130.8.
3. **For now, the detail's linked tasks are the active ones**: `tasks.count`
   and the tasks in `tasks.active_ids`, each fetched with `GetTask` and drawn
   with its state glyph. `enter` opens one; the workspace's `esc` returns to
   the issue. 130.13 widens this to every root task, newest first, once
   `?issue_id=` exists.
4. **A list row's linked-task summary is the count plus an active marker**,
   from the list DTO's `task_count` and `active`. No worst live state, no
   per-row task fetch.

### 17. 130.10: marker headers for step and chat callers, and the outbox's defaults (2026-10-03)

Settled with the author on #669; closes open question 6.

1. **A step's call is marked by an env-driven header.** `internal/apiclient`
   sends `X-Vincent-Task-Id: N` on every request whenever `VINCENT_TASK_ID`
   is in its environment, which §8.5 puts in every step's — agent and command
   steps alike. The API treats a marked request as actor `agent`, exactly
   like an MCP tool call: refused on an issue that writes back, attributed
   `agent` on a local one. It is best-effort, not a privilege boundary — a
   full-auto agent can unset its environment, which is spec §16's stated
   posture. *Alternative beaten:* a per-step scoped token. The agent can
   still read the daemon token from the data directory, so it is no
   stronger, and it costs a token lifecycle and a recovery path.
2. **Chat agents are marked too.** `internal/chatrun` puts `VINCENT_CHAT_ID`
   in a chat agent's environment, the client sends `X-Vincent-Chat-Id`, and
   the API treats it as `agent`. Only a human's own keypress or command
   writes to a forge.
3. **The header never makes a caller human:** its absence means human, its
   presence agent, and `mcp.ViaTool` wins whatever the headers say.
4. **Defaults the brief took.** While an issue has a pending write the
   importer's refresh keeps the local state (content still mirrors), so the
   tick before the drain cannot revert a human's close as "GitHub wins". A
   conflict adopts GitHub's value by actor `sync` through the transition
   path, and the row ends `conflict`. `duplicate_of` sends the target's
   number when it is an issue of the same repository, and otherwise closes
   as `duplicate` with no target. A moved or missing remote enqueues nothing
   — the change stays local and the `sync` block says `moved`/`gone` — and a
   write that finds one at send time ends `failed`. Echo suppression
   compares state and `state_reason`, never timestamps. Mutative calls are
   paced at least 1 s apart and a rate limit waits for its reset. A write
   undone while it was in flight (close then reopen) that turns out to have
   landed enqueues the write back to the local state.

### 18. 130.6: the `vincent issue` CLI tree (2026-10-03)

Settled with the author while scoping #665; spec §12.1's `vincent issue` row
records them.

1. **`edit` labels are deltas only.** `--add-label` and `--remove-label` map
   to `add_labels` and `remove_labels`; no flag sends the full `labels`
   replacement, so two editors cannot clobber each other's set. `--label`
   means one thing: `add`'s initial labels.
2. **`delete <id>...` (alias `rm`) requires `--force`**, as `project rm` does.
   An issue can be deleted in any state (decision 6), so no daemon refusal is
   the confirmation; without `--force` it refuses locally and never prompts.
3. **`add --idempotency-key K` is opt-in.** The key is sent only when given;
   the CLI never generates one, because it never retries (spec §12.1).
4. **`ls --project` is optional.** Without it the list spans every project and
   the table gains a PROJECT column.

The body flag is `--body`/`--body-file`, matching the API's `body` field that
130.3 shipped, not the issue's suggested `--description`.

### 19. 130.12: the TUI issue writes (2026-10-03)

Settled with the author while scoping #671. Spec §15 views 12 and 13 record
the keys.

1. **`n` files a new issue, `a` stays for #672.** `n` is the `new` operation
   (§15's "make a new one here"), whose meaning widens to the issue screens
   the way it already covers the chats board. `a` is left unbound so 130.13
   can bind it as `add` — "create a task from this issue", the pull-requests
   takeover's precedent. `i` edits in the form (the trigger list's key), `X`
   closes or reopens, `D` deletes.
2. **Close and reopen on an imported issue are offered now, and say they are
   local.** The write-back outbox (130.10) does not exist yet, so the
   confirmation says the change applies to vincent's copy only, is not
   written to GitHub yet, and may be overwritten by the next sync. What `X`
   offers is still exactly the daemon's `available_actions`; there is no
   client-side gate, and on the list, whose rows carry none, `X` reads the
   issue first. Replacing that text with "this will be written to GitHub"
   (the keypress is the consent, task 069 decision 2) is 130.10's job, in its
   own pull request. *Done 2026-10-03:* 130.10 landed first, so the merge
   train that stacked the two replaced it — the confirmation says the change
   is written to GitHub too, or, when the `sync` block's reason is `moved` or
   `gone`, that it changes vincent's copy only.
3. **Which rows are read-only comes from the DTO's `editable`**, never from a
   client copy of "imported ⇒ locked". `issue_mirrored` is still rendered, for
   an issue imported between the form's read and its save.
4. **An edit sends `version` and only the changed fields**, labels as
   `add_labels`/`remove_labels` against the issue the form read. An
   `issue_changed` 409 is shown in the form; `R` rebases onto the current
   issue, keeping the user's edits where those fields are still editable. A
   create sends an `Idempotency-Key` generated once per opened form.
5. **Delete always asks**, in any state (decision 6), and on an imported
   issue says it never deletes on GitHub and that the tombstone keeps sync
   from importing it again.

### 20. 130.15: the `type: issues` trigger source (2026-10-03)

Settled with the author while scoping #674. Spec §3 row 33, §12.3 and §13.3
record the result.

1. **The actor is `by: human|agent|sync`**, the actor the store already writes
   on every `issue.*` event — not an `origin: local|sync`. Agent writes are
   where echo loops come from, and `match: {by: human}` is how a trigger drops
   its own. `human` and `agent` changes are trusted; a `sync` change follows
   `github_issues`' table (task 096 decision 31F): `labeled`/`unlabeled`
   trusted, `opened`/`closed`/`reopened` needing `allowed_actors` against the
   issue's author. The load check refuses an `issues` trigger that can match
   an untrusted event from sync without the list, unless its `match.by`
   leaves `sync` out; at judge time the list applies to `sync` events only,
   so a local person's `opened` is never refused.
2. **The delta rides the events, not a per-trigger snapshot.**
   `issue.labels_changed` gains `labels_added`/`labels_removed`; an import
   refresh's `issue.updated` gains them when labels moved, and `from`/`to`
   (with `reason` on a close) when the state did. Ids, names and states only.
   The source is a pure mapper whose only state is an event-id cursor.
3. **The mapping** is github_issues' vocabulary minus `assigned`:
   `issue.created` → `opened`; a state change → `closed`/`reopened`; labels
   added → one `labeled`, removed → one `unlabeled`. Edits, comments,
   deletes and `issue.sync_changed` fire nothing.
4. **No `assigned`.** A vincent issue has no assignee; `match.action:
   assigned` on `type: issues` is a load error.
5. **`action.github_issue` is still removed in #670**, as decision 7
   settled — the issue body's "keeps working (#670)" was wrong. Instead
   `github_issues` events carry `.Event.IssueID` (and `issue_id`), the
   vincent issue the project imported that GitHub issue as through a live
   `issue_remotes` link, empty otherwise, so `issue: '{{ .Event.IssueID }}'`
   is a migration path that survives #670. Until then `issue` with
   `github_issue` or `github_pull` is a load error.
6. **`github_issues` stays and keeps firing.** `vincent trigger apply` (and
   `validate`) print a non-fatal deprecation warning; no file is disarmed.
   Removing it is a later breaking change.
7. **Delivery is at-least-once from the events table.** The cursor is the last
   handled event id; the manager wakes on the post-commit broker and reads
   `issue.*` events after it, scoped to `source.project`. Arming seeds at the
   newest event and fires nothing; disarming drops the cursor. The event id is
   `issue:{issue_id}:{action}:{event_id}`. A pass judges at most 20 events
   (task 096 decision 13), but unlike a command source's catch-up the rest are
   not dropped: the cursor stops at the last event handled and the next pass
   carries on.

### 21. 130.13: a task from an issue in the TUI (2026-10-03)

Settled with the author before the work started (#672).

1. **Decision 16.3's widening is in scope.** The issue detail lists every root
   task linked to the issue, newest first, finished and archived ones
   included, from `GET /v1/tasks?issue_id=&archived=all` — no longer limited
   to `tasks.active_ids`. `enter` opens a task, and `esc` in the workspace
   returns to the issue.
2. **The prefill is re-applied on a workflow switch until the human edits.**
   This keeps the old picker's rule, not the pull-request seed's apply-once
   rule: declared fields differ per workflow, so each time the draft settles
   on a workflow W the form fetches `GET /v1/issues/{id}?workflow=W` and fills
   only rows the human has not typed in. A row is untouched while it is blank
   or holds what the previous prefill wrote; an untouched field the new
   prefill no longer fills is withdrawn. A typed value is never overwritten,
   and an answer for a stale (project, workflow, issue) is dropped, the guard
   `applyPullPrefill` uses. The pull-request seed is unchanged.
3. **The "already started" note is one line: a count plus an active marker**
   — `2 tasks already started from this issue (1 active)` — from the issue
   DTO's `tasks` block, with no extra fetch. Starting another is allowed. This
   amends #672's "the note lists the existing tasks": the list lives on the
   issue detail (1), not in the form.
4. **Both optional pieces ship:** an issue fact on the task workspace's
   Overview (129.12), and a key-less command-palette row, "open this task's
   issue", which opens the issue detail; `esc` returns to the workspace.
5. **The form has no way to unlink or change the source.** A plain task is
   `esc` and `n`; the source is chosen where the issue is on screen, as for a
   pull request.

The probe route, `vincent github status` and doctor stay; the GitHub issue
listing loses only `?workflow=` and its per-row `prefill`, which the form's
picker was the one consumer of (decision 7).

### 22. 130.11: the backfill, placeholder re-keying and a remote-number lookup (2026-10-03)

Settled with the author while scoping #670. #670's body proposed keeping
`github_issue: N` as a permanent resolve-or-import shorthand; the author was
asked and kept decision 7. Spec §3 row 26, §5.3, §12.1, §12.3, §13.1, §13.2,
§14 and §15 record the result.

1. **Decision 7 stands.** 130.11 removes `github_issue` from `POST /v1/tasks`,
   `--github-issue` and `action.github_issue`. #670's permanent-shorthand
   proposal is not built, and with it go the acceptance criteria that
   depended on it (import on demand, digest-identical replay, `github_issue`
   beside a disagreeing `issue_id`). An API client still sending the field
   gets `400 validation_failed` (`unknown field "github_issue"`, the strict
   decoder's answer to an unknown key); a trigger file still carrying it fails validation
   with `unknown field "github_issue"`, stops firing, keeps its cursor, and
   resumes once edited to `issue:`. The release notes carry both as a
   breaking change. The task DTO's read-only `github_issue` stays: removal
   concerns create, not stored history.
2. **Backfilled state is the newest snapshot's**, even for issues linked only
   to archived tasks. Sync corrects it, and GitHub wins. Content (title, body,
   author, labels) comes from the newest snapshot too; timestamps are copied
   from the linked tasks, never generated, and migration 0040 writes no
   events.
3. **Placeholder keys, re-keyed on first sight.** Backfilled remotes are keyed
   `legacy:owner/repo#N` with `synced_at` NULL. The importer and the daily
   scan match never-synced placeholders by `(project, repo, number)`, re-key
   to `node_id`, adopt remote state and enqueue no write-back. Real keys are
   never matched by number (decision 15.5 kept). A placeholder whose `repo`
   differs from the project's sticky sync binding — `origin` re-pointed, or
   the repository renamed, since the task was created — is **never matched**,
   and stays a never-synced imported issue with its backfilled content; the
   API and CLI sync docs say so.
4. **A remote-number lookup replaces the shorthand for scripts.** It is
   `GET /v1/issues?project_id=&remote_number=` (a 400 without `project_id`,
   since a number means nothing across projects), the `issue_list` MCP tool's
   `remote_number`, and `vincent issue ls --project P --github N`. The repo's
   resolve workflows use it, triggering `vincent issue sync` when the issue is
   not yet imported, and their dedupe reads the linked issue's GitHub number
   beside the legacy snapshot, so a re-run stays safe across the backfill.

Also settled in the same pull request: a legacy-snapshot task brought in by
task import (task 117) is relinked by its snapshot's repository and number
against the **live** store's GitHub remotes, never by the staged database's
issue id; the new-task form's GitHub issue picker, which submitted `github_issue`,
goes with the field — 130.13 (decision 21) had already replaced it with the
read-only source row;
and decision 7's accepted consequence that "every `POST /v1/tasks`
idempotency digest changes once" is **not** paid after all. The digest is
computed over a separate shape that keeps an always-`null` `github_issue`
slot where the field was, so a keyed retry spanning the upgrade still
replays, while the request itself can no longer carry the field. This
departs from decision 7's expectation, not its rule: the field is still
removed from every create surface.

### 23. 130.14: `$VINCENT_ISSUE_FILE` (2026-10-03)

Settled with the author while scoping #673. Spec §8.5 and the task row's
`issue` entry record the result.

1. **The file is gh-faithful plus vincent extras.** It mirrors `gh issue view
   --json number,title,body,url,createdAt,state,stateReason,labels,author,
   comments`, so the workflows' existing `jq` keeps working and `fetch`
   becomes a copy. `number` is the **GitHub** number, `null` for a local
   issue — the file's contract is gh's, so it does not follow decision 8's
   `.Issue.Number`. Beside gh's keys: `id`, `kind`, `priority`, `source`.
   `createdAt` needed an optional `created_at` on the snapshot (no migration).
2. **The comment thread is not carried yet, and that gap is accepted.**
   `comments` is always `[]`, in gh's element shape, so #675 (130.16) fills it
   with no workflow change. The resolve workflows' prompts say the issue body
   is the brief until then.
3. **The file lives in the worktree's own git dir**, written before every
   attempt from the task row: never staged by `git add -A`, visible to a
   containerized step under the already-mounted repository (the m12 gate's
   scenario 1b), removed with the worktree. The fallback — a new read-only
   mount from the data dir — was not needed.
4. **One block for all three step types.** The variables are part of §8.5
   (task 036), so command, check and agent steps get them alike.

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
   *Settled 2026-10-02 (130.1, by the author):* the default stands, and the
   schema makes it structural — `issue_remotes` is keyed
   `UNIQUE(project_id, provider, remote_key)`, not #660's global
   `UNIQUE(provider, remote_key)`, which would make the second project's
   import a constraint violation. A tombstone is per project and goes with
   it.
4. **Initial import depth.** (#658 question 7.) *Proposed:* open issues only,
   capped at 500, resumable across ticks.
5. **Refreshing the snapshot on `follow_up`.** (#658 question 8.) *Proposed:*
   no refresh. An opt-in `refresh_issue` would relitigate, for that case, the
   never-re-fetched half of task 035 decision 10 and spec §3 row 26 that
   decision 5 keeps for the task snapshot.
6. **Telling a step's call from a human's.** (Decision 10, for 130.10.) A step
   running `vincent issue close` reaches the API the way a human's CLI does.
   The mechanism that marks it step-originated is an implementation question
   for #669. *Settled 2026-10-03 by decision 17:* an env-driven marker
   header, for steps and chat agents alike.

## Tasks

In #658's delivery order. Items without a `Depends:` can proceed in parallel.
Each item amends the spec sections and public pages its code makes true, in
its own pull request.

- [x] **130.1** ([#660](https://github.com/lezli01/vincent/issues/660))
  `internal/issuestate`, migration `0036` (issues, remotes, labels, comments,
  `tasks.issue_id`/`issue_json`), store CRUD, in-transaction `issue.*` events,
  the `internal/issues` write path.
- [x] **130.2** ([#661](https://github.com/lezli01/vincent/issues/661))
  `cmd/fakegh` with a mutable issue corpus, `gh api` with ETag/304, issue
  writes and a scenario file. ✓ 2026-10-02
- [x] **130.3** ([#662](https://github.com/lezli01/vincent/issues/662))
  `/v1/issues` routes, `issue.*` SSE events, apiclient, MCP tools, idempotent
  create. Depends: 130.1. ✓ 2026-10-02
- [x] **130.4** ([#663](https://github.com/lezli01/vincent/issues/663)) Prefill
  from a vincent issue, the `.Issue` reshape, snapshot build, legacy rendering,
  skill and checklist lines. Depends: 130.1. ✓ 2026-10-02
- [x] **130.5** ([#664](https://github.com/lezli01/vincent/issues/664)) Durable
  issue listing (`node_id`, pagination, conditional requests, gone/moved) and
  issue state writes in `internal/github`. Depends: 130.2. ✓ 2026-10-02
- [x] **130.6** ([#665](https://github.com/lezli01/vincent/issues/665)) The
  `vincent issue …` CLI tree; drops `ListIssues`/`GetIssue` from
  `tuiOnlyClientCalls` and their row from `docs/reference/cli.md`'s "What only
  the TUI does", which 130.9 added — and the six issue writes and their row,
  which 130.12 added. Depends: 130.3. ✓ 2026-10-03 (decision 18)
- [x] **130.7** ([#666](https://github.com/lezli01/vincent/issues/666))
  `issue_id` on task create, the prefill preview, `?issue_id=` filter, the task
  DTO link, `Closes #N`, `vincent task add --issue`. Depends: 130.3, 130.4.
  ✓ 2026-10-02
- [x] **130.8** ([#667](https://github.com/lezli01/vincent/issues/667)) Import
  and refresh on the reconciler tick, sync status, config and doctor text.
  Amends spec §12.3's "no call until a human opens the issue picker"
  (`docs/spec.md:7354-7360`), `internal/config/config.go:383-388` and
  `docs/reference/configuration.md` (decision 9). Depends: 130.1, 130.3, 130.5.
- [x] **130.9** ([#668](https://github.com/lezli01/vincent/issues/668)) The TUI
  Issues list and Issue detail. Depends: 130.3. ✓ 2026-10-02 (decision 16)
- [x] **130.10** ([#669](https://github.com/lezli01/vincent/issues/669)) The
  write-back outbox, its compare-and-set drain, and the guard refusing MCP- and
  step-originated writes (decision 10, open question 6). Depends: 130.8.
  Also replaces the TUI's local-only close/reopen confirmation on an imported
  issue (decision 19.2) with its own, done when the two were stacked.
  ✓ 2026-10-03 (decision 17)
- [x] **130.11** ([#670](https://github.com/lezli01/vincent/issues/670)) SQL
  backfill of task 035's snapshots into issues, and the removal of
  `github_issue` from `POST /v1/tasks`, `--github-issue` and
  `action.github_issue` with every consumer decision 7 lists. Depends: 130.7,
  130.8. Also removes the new-task form's issue picker, which submitted
  `github_issue` (130.13's first item), and adds the remote-number lookup.
  #676's `github_issue: N` shorthand scenario for `scripts/130-gate.sh`,
  conditional on #670, lapses with the shorthand, which was not built
  (decision 22.1). ✓ 2026-10-03 (decision 22)
- [x] **130.12** ([#671](https://github.com/lezli01/vincent/issues/671)) The
  TUI issue create/edit form with close and reopen, and a shared `$EDITOR`
  helper. Depends: 130.9. ✓ 2026-10-03 (decision 19)
- [x] **130.13** ([#672](https://github.com/lezli01/vincent/issues/672)) Seed a
  new task from an issue, delete the new-task form's issue picker, the task
  workspace's Issue section. Depends: 130.9, 130.7. Also widens the issue
  detail's linked tasks from the active ones to every root task, newest first,
  over `?issue_id=` (decision 16.3). ✓ 2026-10-03 (decision 21)
- [x] **130.14** ([#673](https://github.com/lezli01/vincent/issues/673))
  `VINCENT_ISSUE_FILE`, and the repo's resolve workflows migrated onto it.
  Depends: 130.7, 130.6, 130.8. ✓ 2026-10-03 (decision 23)
- [x] **130.15** ([#674](https://github.com/lezli01/vincent/issues/674)) A
  `type: issues` trigger source, `issue:` on `create_task`, the trigger skill.
  Depends: 130.3, 130.7. ✓ 2026-10-03 (decision 20)
- [ ] **130.16** ([#675](https://github.com/lezli01/vincent/issues/675)) The
  local discussion thread and the read-only GitHub comment mirror. Depends:
  130.3, 130.8.
- [ ] **130.17** ([#676](https://github.com/lezli01/vincent/issues/676)) An
  end-to-end gate on all three platforms. Depends: 130.7, 130.10.
  `scripts/130-gate.sh` and its `ci.yml` step landed with eleven scenarios,
  recorded in `docs/gates/130-issues.md`. Scenario 12 (the `github_issue`
  shorthand) moved to 130.11. Scenario 8 found that a refresh of an
  unchanged remote reverted the local state of an issue whose write ended
  `failed/no_write_scope`, which the spec says is kept; the fix landed in the
  same pull request. This item closes when the gate is green on all three
  platforms in CI.
- [ ] **130.18** ([#677](https://github.com/lezli01/vincent/issues/677))
  Screenshot seed, new tapes, recaptures, the features page. Depends: 130.12,
  130.13, 130.10.
