# 142 — Jira issue sync: Jira Cloud issues in a project's issue set, at parity with GitHub issue sync

**Status:** 🔄 in progress (14/20) · **Opened:** 2026-11-03

Issue [#897](https://github.com/lezli01/vincent/issues/897), part of
[#896](https://github.com/lezli01/vincent/issues/896), the parent. Today a
GitHub project's open issues are imported into vincent's per-project issue set
and kept in sync (task 130, spec §5.6 and §12.3): polled, mirrored read-only,
state synced both ways, human and granted handoff writes sent back through the
crash-safe outbox, moved and deleted issues swept, and failures reported on the
project's sync row. This work gives a project whose tickets live in Jira Cloud
the same experience, and leaves everything downstream of an issue — tasks from
an issue, `.Issue`, `type: issues` triggers, handoffs, lanes, issue chats —
working on Jira issues unchanged.

It departs by name from three binding records — task 096 decision 2 (for issue
sync only), task 130 decision 15.1, and the native half of task 137 decision 10
— and each carries a dated note pointing here. The record table below lists
every other record this work keeps, narrows or generalises.

**No spec amendment lands with this document** (decision 10). The spec §3
decision-record row for Jira (it would be row 39) lands with 142.12, the
client and importer, under the same-pull-request rule. It does not take the
ahead-of-code exception rows 36–38 took.

Conventions for this file are in [the tasks README](README.md).

## Tasks

IDs map one to one onto #896's breakdown, in its delivery order. `Depends:`
lists only the hard dependencies the parent records as "Blocked by".

- [x] **142.1** ([#897](https://github.com/lezli01/vincent/issues/897)) This
  document: decisions 1–10, the kept and departed records with citations, and
  the open questions' answers or proposed defaults; the dated notes on task
  096 decision 2, task 130 decision 15.1 and task 137 decision 10; the index
  row. ✓ 2026-11-03
- [x] **142.2** ([#898](https://github.com/lezli01/vincent/issues/898)) A pure,
  stdlib-only package for the mapping of decision 9: state and reason both
  ways, the kind seed, labels and the transition chooser. ✓ 2026-11-03
- [ ] **142.3** ([#899](https://github.com/lezli01/vincent/issues/899)) Cloud
  REST v3 responses captured from a real site, settling the facts this
  document carries unverified. Needs a person with a Jira Cloud site.
- [x] **142.4** ([#900](https://github.com/lezli01/vincent/issues/900)) The
  issue-provider interface (decision 2); GitHub implements it with no
  behaviour change. Depends: 142.1. `internal/issuesource` holds the
  `Source` interface, the seam's reason vocabulary (pinned equal to
  `github.Reason*`) and the GitHub source; the repository-wide comment
  listing and placeholder adoption are optional capabilities found by type
  assertion. The importer and the drain name no `github` type; the drain
  dispatches each row on its remote's provider, still one writer; and
  `store.IssueIDByRemoteKey` attaches a per-issue thread for a source with
  no repository-wide listing. ✓ 2026-11-03
- [x] **142.5** ([#901](https://github.com/lezli01/vincent/issues/901)) The
  `"github"` equality checks replaced, so a second provider writes back and is
  guarded. Depends: 142.4. The new stdlib-only leaf `internal/issueprovider`
  holds the provider ids, `Known`, `WritesBack` (true for GitHub and Jira)
  and `All`; `issues.WritesBack`, the store's outbox enqueue and the
  duplicate-of lookup (now within the closing issue's own provider and repo)
  ask it, so a live Jira remote is guarded `forge_write_needs_human` and its
  human or granted writes are enqueued. With no Jira writer until 142.13
  (#909), those rows wait `no_client` and are never failed.
  `store.GitHubStateOf` is `RemoteStateOf`; `RequestIssueSync` takes the
  provider; `GET /v1/issues`' `source` vocabulary is `local` plus the
  registry. ✓ 2026-11-03
- [x] **142.6** ([#902](https://github.com/lezli01/vincent/issues/902)) The
  per-project issue-source binding (decision 5): migration, API, CLI, TUI
  form, MCP and sticky semantics. The GitHub importer honours it now
  (`source_not_github`), and a rebind marks the old source's remotes
  `missing` in the project update's transaction. ✓ 2026-11-03
- [x] **142.7** ([#903](https://github.com/lezli01/vincent/issues/903)) Issue
  sync scheduled per bound provider, decoupled from pull-request
  reconciliation, and the `jira:` config block (decision 13). Depends: 142.4,
  142.6. ✓ 2026-11-04
- [x] **142.8** ([#904](https://github.com/lezli01/vincent/issues/904))
  Daemon-rendered `source.key`/`ref`/`short_ref` and provider display names;
  clients stop building references; provider literals in issue text name the
  provider or go neutral (decision 11). A Jira `source.ref` is the stored
  payload's `key`, which equals the row's `repo` + `-` + `number` (decision
  9.4's note, fixed by 142.9). ✓ 2026-11-03
- [x] **142.9** ([#905](https://github.com/lezli01/vincent/issues/905))
  `remote_key` and `source=jira` filters, and `vincent issue ls --remote`.
  Fixed the Jira display-key storage convention (decision 9.4's note).
  ✓ 2026-11-03
- [x] **142.10** ([#906](https://github.com/lezli01/vincent/issues/906))
  HTML→Markdown for bodies and comments, and Markdown→ADF for outgoing
  comments (decision 9.5). ✓ 2026-11-03
- [x] **142.11** ([#907](https://github.com/lezli01/vincent/issues/907)) The
  fake Jira corpus, an `httptest` leg and a `cmd/fakejira` server.
  ✓ 2026-11-03
- [x] **142.12** ([#908](https://github.com/lezli01/vincent/issues/908)) The
  `internal/jira` client and the importer for Jira: listing, initial import,
  sweep, comments, rate limits and environment credentials; spec §3 row 39
  (decision 10). The importer writes the Jira project key in `repo` and the
  issue number in `number` (decision 9.4's note, fixed by 142.9). It also
  records the Jira project id on `issue_sync_state`, compares it stickily,
  and adds the `binding_changed` reason — moved here from 142.7, since this
  is the id's first writer (decision 13.6).
  Depends: 142.7, 142.5, 142.2, 142.10, 142.11.
  ✓ 2026-11-05 — also the `GET /v1/projects/{id}/jira` probe (open question
  7), `vincent jira status` and the `project_jira` MCP tool; settles open
  questions 4, 5 and 11 and decision 12.4's question. The outbox is not wired
  to the Jira source: Jira write-back rows keep waiting `no_client` until
  142.13 (#909).
- [ ] **142.13** ([#909](https://github.com/lezli01/vincent/issues/909)) The
  outbox for Jira (decision 3): transitions with resolution, ADF comments,
  echo handling. Depends: 142.12.
- [x] **142.14** ([#910](https://github.com/lezli01/vincent/issues/910))
  Provider-aware sync status reasons, the TUI `S` key, the overview glyph, and
  the `issue sync` CLI (decision 14). Depends: 142.7.
- [ ] **142.15** ([#911](https://github.com/lezli01/vincent/issues/911)) A
  JIRA doctor row with a credential check and per-project sync health.
  Depends: 142.12.
- [x] **142.16** ([#912](https://github.com/lezli01/vincent/issues/912))
  `.Issue.Source.Key`, `VINCENT_ISSUE_KEY`, `source.key` in the issue file,
  and the Jira prefill. Depends: 142.8.
- [x] **142.17** ([#913](https://github.com/lezli01/vincent/issues/913)) The
  built-ins' checklists and both skills cover Jira-imported issues. Depends:
  142.16, 142.9. Done: `update-workflows` item 19 and `update-triggers` item
  21 carry Jira issues, the `by: sync` refusal names the issue's tracker with
  the trust rule unchanged, and this repository's `github-*` workflows carry a
  GitHub-only header note — there are no Jira variants. The trusted
  `labeled`/`unlabeled` exemption is verified for GitHub only (open
  question 11). *Note 2026-11-05:* open question 11 is settled by 142.12
  (#908) — the exemption no longer reaches Jira, and `update-triggers` item 21
  and the triggers skill say so.
- [ ] **142.18** ([#914](https://github.com/lezli01/vincent/issues/914)) A
  scripted end-to-end gate on all three platforms in CI. Depends: 142.13.
- [ ] **142.19** ([#915](https://github.com/lezli01/vincent/issues/915)) A new
  Jira guide and the docs sweep, including security, privacy and SEO.
  Depends: 142.13, 142.14, 142.15, 142.16.
- [ ] **142.20** ([#916](https://github.com/lezli01/vincent/issues/916)) A
  Jira-bound project seeded in `scripts/screenshots.sh`, and the issue shots
  recaptured. Depends: 142.12, 142.8, 142.14.

## Decisions (2026-11-03)

Settled with the author while scoping #897, except the points marked
*proposed default*: those carry the default an open question below proposes,
and are open until the issue it names settles them. Decisions 1–9 merge #897's
draft list with #896's "Decisions taken"; decision 10 answers #897's own open
question; decision 11 was taken delivering 142.8 (#904).

### 1. A native in-tree Jira Cloud provider; trigger sources stay generic

Jira issue sync is a native in-tree provider against Jira Cloud REST v3.
Trigger sources stay generic: there is no `type: jira`, and Jira as a trigger
source keeps working through `type: command`
(`skills/vincent-triggers/references/poll-scripts.md`). This is the **departure
from task 096 decision 2, for issue sync only** — see the record table — and
it is bounded: Jira is the one native non-GitHub provider (decision 8).

It is the only option that reaches task 130 parity — compare-and-set
write-back with resolution, the comment mirror, the sweep, rate-limit
backoff — without encoding Jira's semantics in two shell dialects.

*Beaten:* task 137 frame gap F5's command provider with Jira as its first
user. It would put ADF conversion, transition choice, `Retry-After` handling
and move detection into both sh and pwsh, tested on three operating systems.

*Beaten:* Atlassian's `acli`
([command reference](https://developer.atlassian.com/cloud/acli/reference/commands/jira-workitem-search/)).
It is Cloud-only, each version is supported for six months, it transitions by
status name with no resolution, and its JSON is undocumented (#896's
integration-design finding).

*Beaten:* `ankitpokhrel/jira-cli`. It is third-party, has "partial" Windows
support, and needs an interactive `jira init`.

### 2. A provider interface is extracted, GitHub first

An issue-provider interface is extracted (142.4, #900), and GitHub is its first
implementation, with no behaviour change. This **supersedes task 130 decision
15.1**, as task 137's F5 row anticipated ("superseded once a second exists",
`docs/tasks/137-handoff-pillar.md:845`). Every finding of #896 that touched
the seam agreed on it, and it is needed whichever implementation follows —
including F5's.

*Beaten:* a second hard-coded provider beside GitHub's, on the
`internal/daemon/issuesync.go:21-24` premise that "the row's provider column is
the only seam a second provider would need". #896's sync-seam finding counts
`"github"` hard-coded in ten non-test places, three of which make any other
remote silently local-only for state.

### 3. Jira write-back goes natively through the existing outbox

A human's close or reopen, and a granted handoff's state change or comment,
reach Jira through the existing crash-safe outbox (`internal/daemon/issueoutbox.go`).
Every other handoff target stays a command step in the admin's workflow. This
is the **departure from the native half of task 137 decision 10**, whose own
text says a second intake provider's "write-back arrives with it".

*Beaten:* Jira write-back as a command step. It would leave the one provider
vincent syncs natively with a write path outside the outbox's crash safety and
echo handling.

### 4. Credentials come only from named environment variables

The Jira credential is read only from the daemon's inherited environment, from
named variables whose defaults are `JIRA_USER` and `JIRA_API_TOKEN` — the names
the trigger poll-script example already uses
(`skills/vincent-triggers/references/poll-scripts.md:104`). They are read per
use, so a hot reload governs the next call. Config may **name** the variables
but never holds a value, as spec §3 row 34's trigger ingress secret does
(`docs/spec.md:145`). Nothing is stored in SQLite. In service mode, where the
daemon lacks the login shell's environment, `environment.set` in the
owner-only `config.yaml` is the answer. A missing variable surfaces as
`no_credential` in sync status and doctor.

This keeps spec §2, §3 row 26, §12.3's "no token key" and §16, and the
credential half of task 096 decision 2.

*Beaten:* a token key in `config.yaml` or the database. The moment vincent
holds a Jira credential, "the daemon inherits the user's environment" stops
being true.

### 5. The binding is explicit, per project, daemon-side

A project is bound explicitly to one Jira Cloud site, and the Jira project
**id** is recorded on the first successful sync, because a key rename changes
every issue key but not the id. *Settled by the author 2026-11-03, delivering
#902 (open question 2, the proposed default overturned):* the binding is the
site plus one project key, with an **optional JQL narrowing clause**. The
client ANDs it after `project = <id>` (#908). It is one line with no
`ORDER BY`, so it can only narrow the set, never replace it: move detection
and the sweep still ask "is it in this project?".

The binding is stored on the `projects` row (migration 0051): `issue_provider`
is `''` (derive from `origin`, the default every existing project keeps),
`none`, `github` or `jira`, beside `jira_site`, `jira_project_key`, `jira_jql`
and `jira_mapping_json` (a `jiramap.Config` override, decoded strictly by
`jiramap.ParseConfig`). The recorded Jira project id belongs on
`issue_sync_state` and arrives with #908, its first writer. On the wire the
project DTO carries `issue_source: {provider, effective, jira}`; `effective`
is the daemon's resolution of `derive`, so no client derives it. One pure
validator in `internal/api/projectsource.go` judges every surface's input.
Setting any provider but `jira` clears the Jira fields.

**Rebind** (open question 3, settled as proposed): a change to the effective
provider, the Jira site or the Jira project key — a repoint of `path` at
another origin included — marks the old provider's live remotes `missing`,
deletes the project's `issue_sync_state` row and announces
`issue.updated {changed: ["remote_status"]}` per issue, all in the project
update's transaction, without waiting for a sync that `none` would never run.
A change to only the JQL or the mapping is not a rebind (decision 9.11).

**The GitHub importer honours the binding from #902 on**: a project bound to
`none` or `jira` makes no GitHub issue or comment call and records the sync
reason `source_not_github`; pull-request linking is untouched (decision 6). A
`jira` binding keeps that reason until #903 schedules Jira sync, and #910
refines provider-aware reasons.

1. **Sticky**, generalising task 130 decision 15.5: a later mismatch stops that
   project's sync rather than re-keying.
2. **Stored daemon-side**, on the project, and **never read from `.vincent/`**.
   A repository file naming a site would let anyone who can merge point the
   daemon, and the user's token, at their own host.
3. **HTTPS only**, except for loopback.

*Beaten:* a binding inferred from the repository (as GitHub's is from
`origin`), or declared in `.vincent/`.

### 6. One issue source per project

`issue_sync_state`'s primary key is `project_id`
(`internal/store/migrations/0038_issue_sync.sql:17`), so one provider per
project is already what the store enforces. A Jira-bound project whose
`origin` is on GitHub keeps pull-request linking and skips GitHub issue import.

*Beaten:* syncing one project from two trackers at once, which would need a
new key and a rule for which tracker wins a conflicting state.

### 7. Cloud first

Jira Cloud is the target. Data Center and Server (REST v2, PAT bearer, wiki
markup) are a later follow-up, filed when a user asks, behind a small edition
seam the client keeps around paging and auth. #896's research records that
Atlassian ends Data Center on 2029-03-28 and stopped new subscriptions on
2026-03-30
([Atlassian Ascend](https://www.atlassian.com/blog/announcements/atlassian-ascend));
those dates are #896's findings, not verified here.

*Beaten:* both editions at once, doubling the client for an edition with an
announced end of life.

### 8. Every other tracker goes through F5

Every other tracker — Linear, GitLab, a markdown backlog, a mailbox — goes
through task 137 frame gap F5's command-backed provider, as a later second
implementation of decision 2's interface, in its own task. A second native
non-GitHub provider needs its own recorded decision.

*Beaten:* treating decision 1 as a precedent that admits native providers
generally, which is the maintenance treadmill task 096 decision 2 refused.

### 9. Mapping

Each point is numbered so later issues can cite it. 142.2 implements the pure
parts; 142.3 settles the Jira facts against a real site.

1. **Closed iff `statusCategory.key == "done"`**, never from `resolution`.
   State is a per-project workflow, and team-managed projects do not support
   resolution. *Beaten:* deriving closed from a set `resolution`, which a
   team-managed project never sets and a company-managed workflow can set on
   an open status.
2. **The reason comes from configurable resolution sets.** Duplicate is
   `Duplicate`; not planned is `Won't do`, `Won't Fix`, `Cannot Reproduce`,
   `Incomplete` or `Declined`; anything else, or none, is `completed`.
   *Amended 2026-11-03 (142.2, #898):* one narrowing on the reason side.
   When the resolution is null, a done status whose name is in the
   not-planned set, or is `Cancelled`/`Canceled`, reads as `not_planned`. A
   team-managed `Won't do → Cancelled` transition sets no resolution, and
   without this the chooser's achieved reason and the next read-back would
   disagree on every poll. A set resolution always wins over the status name.
3. **Write-back picks from the transitions available now**, with an optional
   per-binding override map, and fails with `no_transition`,
   `ambiguous_transition` or `transition_needs_fields` rather than guess.
   *Beaten:* guessing a transition by status name, which picks the wrong edge
   in any workflow with two paths into the same category.
4. **`remote_key` is the Jira issue id**, which is stable across a move; the
   key is not. The daemon renders the display reference (`PROJ-12`), and
   clients print `source.ref` (142.8, #904).
   *Note 2026-11-03 (142.9, #905):* the display key's storage is fixed here,
   because the `remote_key` lookup needs it before any Jira row exists. A Jira
   `issue_remotes` row stores the Jira **project key** in `repo` (`PROJ`) and
   the issue **number** in `number` (`12`); `remote_key` stays the stable
   issue id. Existing columns, no migration, a plain-SQL lookup. #904 renders
   `source.ref` from the stored payload's `key`, and #908's importer writes
   `repo` and `number` to match it, rewriting a moved issue's row to its new key — no key history is kept, so a
   moved issue's old key answers an empty list. `GET /v1/issues?remote_key=`
   matches this display key, never the `remote_key` column, and chooses the
   provider by the key's shape (digits → GitHub, `KEY-N` → Jira), not by the
   project's binding.
5. **Bodies are rendered from `renderedFields`/`renderedBody` HTML to
   Markdown**, not by walking ADF, so one converter would also cover a later
   Data Center leg. Outgoing comments are wrapped as minimal ADF. *Beaten:*
   walking ADF, a node tree whose extensions and macros would need a renderer
   of vincent's own and would not carry to Data Center's wiki markup.
6. **Restricted-visibility comments are skipped** (privacy). *Proposed
   default, open until #908 (open question 5).* *Beaten:* mirroring them,
   which would show a role- or group-restricted comment to everyone with the
   vincent project.
7. **No Duplicate issue link is written.**
8. **A sweep 404 marks the issue `missing` and never deletes it.** Jira answers
   404 for deleted and forbidden alike and has no 410, so GitHub's "410
   deletes" rule has no safe analogue. *Proposed default, open until #908
   (open question 4).* *Beaten:* deleting after a confirming probe, which
   still cannot tell deleted from forbidden.
9. **`kind` is seeded from `issuetype` on first import only**, narrowing task
   130 decision 15.4 for this provider. Priority stays local.
10. **"In Progress" is not reflected in lanes.** Jira's `indeterminate`
    category is kept only in `remote_json`. *Proposed default, open until
    #910 (open question 8).*
11. **An issue that leaves the JQL-narrowed set is marked `missing`** while it
    stays in the bound Jira project, exactly like a sweep 404 (9.8): never
    deleted, and reset to live when it matches again. *Settled 2026-11-03
    delivering #902; implemented by the sweep in #908.*

### 10. Spec placement

The spec §3 decision-record row for Jira (row 39) lands with 142.12 (#908),
the client and importer, in the same pull request as the code that makes it
true. Each implementation pull request carries its own spec amendment. This
answers #897's open question.

*Beaten:* landing row 39 with this document, by the ahead-of-code exception
rows 36–38 took (task 130 decision 13, task 132 decision 14, task 137).

### 11. Rendered references and provider names (142.8, #904)

Taken while delivering 142.8. Each point is numbered so code can cite it.

1. **The daemon renders `short_ref`; the client picks.** The daemon always
   fills `short_ref` (`gh#142`, `PROJ-12`), but it serves each issue alone and
   cannot see the list a row sits in, so the TUI keeps only the choice: it
   prints `short_ref` when every imported row on the list shares one provider
   and repo (spec §15's #796 rule, generalized from repo alone) and `ref`
   otherwise. *Beaten:* the list endpoint deciding, which would make a
   filtered page and an unfiltered one disagree.
2. **The snapshot stores only `key`; the rest is rendered at read time.**
   `store.IssueSnapshotRemote` gains an optional `key` (the task 130.4
   precedent: a new `omitempty` key in `issue_json`, no migration). `ref`,
   `short_ref` and `provider_name` are never stored: the issue DTO and both
   halves of a task's `issue` render through `issues.RenderSource`, so a
   snapshot taken before this change also gets refs — GitHub's key falls back
   to its number. *Beaten:* storing rendered strings, which would freeze a
   rendering bug into every snapshot.
3. **Static text is neutral; per-issue text names the provider.** Where an
   issue is in context — state-change and write-back notes, "mirrored from …",
   the issue detail, the 409 `forge_write_needs_human` prose — the string
   names `source.provider_name`. Text with no issue in context (MCP tool
   descriptions, the issue key-table rows, CLI help) says "a remote tracker"
   or "the issue's source". GitHub-only surfaces keep their names:
   `vincent github …`, `project_github*`, `/v1/projects/{id}/github*`, the
   pull-request surfaces and the `S`/sync-status labels (#910). *Beaten:*
   "the remote" everywhere, since "written to Jira too" is what a user wants
   to read.

### 12. Jira keys in templates, the issue file and the prefill (142.16, #912)

Settled by the author 2026-11-03 while delivering 142.16. Each point is
numbered so code can cite it.

1. **No `jira_issue` declared field.** Workflows read `.Issue.Source.Key` or
   `$VINCENT_ISSUE_KEY`; the prefill's field table is unchanged. This closes
   open question 6 with its proposed default.
2. **Only `Key` is added to templates and the issue file.** `.Issue.Source`
   and the file's `source` gain `Key`/`key`, spelled by `issues.RenderSource`.
   142.8's `Ref`, `ShortRef` and `ProviderName` stay on the API DTOs; adding
   them later breaks nothing. *Beaten:* exposing all four now.
3. **`source.number` is nullable.** A Jira file writes `null` for both
   `number` and `source.number`, and a Jira step gets no
   `VINCENT_ISSUE_NUMBER`. A GitHub file's bytes are unchanged apart from the
   added `key`. *Beaten:* writing `0`, which reads as a real number.
4. **The Jira prefill is `PROJ-12 <title>`** with a `Jira issue PROJ-12: <url>`
   link line, never doubling the prefix. A Jira snapshot with no key prefills
   the local shape. `github_issue`, `assignee` and `milestone` stay
   GitHub-only; whether a Jira assignee or milestone should fill them is a
   question for 142.12, which stores them. *Settled 2026-11-05 by 142.12
   (#908):* they do not — the Jira assignee is kept in `remote_json` only.

### 13. Issue sync on its own loop, per bound provider (142.7, #903)

Settled while scoping and delivering 142.7, 2026-11-04. Each point is
numbered so code can cite it.

1. **Two loops.** Issue sync runs apart from the pull-request reconciler,
   with one timer per provider: GitHub issue import on `github.poll_interval`
   gated on `github.Polls()`, the Jira pass on `jira.poll_interval` gated on
   `jira.Polls()` (`internal/daemon/issuesyncer.go`). A slow or hanging Jira
   site never delays pull-request linking, and neither provider's switch
   governs the other. *Beaten:* one loop ticking at the smaller of the two
   intervals and skipping whatever is not due, which keeps one goroutine
   but lets a hanging provider stall the other's pass.
2. **The ordering invariant is kept inline.** For a project resolving to
   `github`, the reconciler's own tick runs the GitHub issue pass (shared
   code) before it judges that project's GitHub triggers, so a `type: issues`
   trigger sees the same tick's import. *Beaten:* the reconciler waiting on
   the issue loop's pass, a handshake that re-couples the two loops.
3. **One resolver.** `issuesource.Resolve` gives a project's effective source
   — an explicit `issue_provider` wins, otherwise `origin` derives it — and
   the daemon's tick, "sync now" and the API all call it. "Sync now" stamps
   the resolved provider on the row and is served under that provider's
   switches alone. The write-back drain gates each row on its own provider.
4. **The Jira pass ships before the client.** Its gates run in order:
   `jira.Polls()`, both named variables set (else `no_credential`, decision
   4), a wired source (else `no_client`, until #908). The row is stamped
   `provider: jira`, and a `jira` binding stops carrying `source_not_github`,
   as decision 5 anticipated. `jira.enabled` is a new key, not a departure
   from task 069 decision 2, which is GitHub-scoped.
5. **No sync row for `none`.** A project resolving to `none` — explicitly or
   derived from a non-GitHub origin — gets no `issue_sync_state` row, and
   migration 0052 deletes the old `not_github` rows. The API derives the
   reason when there is no row: `not_github` for a derived project,
   `source_not_github` for an explicit `none`. A row's
   `enabled`/`github_disabled` fold reads its own provider's config.
6. **`binding_changed` moves to 142.12.** Decision 5 stands: the Jira project
   id, its column, the sticky comparison and the `binding_changed` reason
   arrive with #908, the id's first writer. A change of site or key is
   already a rebind (142.6), which deletes the sync row, so nothing is lost
   meanwhile.

### 14. The sync status names its source, and `S` follows it (142.14, #910)

Settled while scoping 142.14, 2026-11-04. 142.7 had already made the status
provider-aware on the daemon side; this is the display and the clients.

1. **142.7's reason vocabulary is kept.** `github_disabled` stays GitHub's
   switch and `disabled` Jira's; `not_github` and `source_not_github` stay the
   two no-source reasons, and the overview glyph reads both — and any switch
   off — as a dash. *Beaten:* #910's `provider_disabled` and `not_bound`:
   GitHub output stays byte-for-byte v0.12.0's, and a field cannot carry an
   alias, so a rename would break every client that matches the old strings.
2. **The status gains `source` and `provider_name`, rendered by the daemon**
   (decision 11: clients never build a reference). `source` is `owner/repo`
   for GitHub, `<site host>/<PROJECTKEY>` for Jira, absent for none or while
   no repository is known; `provider_name` comes from `issues.RenderSource`'s
   table. Both are filled by the per-project read only; doctor and
   `projectstats` do not carry them. `repo` and `site` stay.
3. **`S` is offered iff there is a sync pass to wake.** The gate is the issues
   list's own loaded sync status — not the GitHub probe, which says nothing
   about a Jira project, and not `projectstats`. Not loaded, `not_github` or
   `source_not_github` hide it from the footer, `?` and the palette, and the
   key then does nothing. A switch that is off keeps it: pressing it says
   import is off. Mechanically, a `syncable` binding flag the issues view's
   live rows and the root's `?` sheet both apply. The label is
   provider-neutral, and the note names `source`.

## Records kept, departed from, narrowed or generalised

Citations are to the lines as of this document's commit, after its dated notes were inserted.

| Record | Where | What it says | Treatment |
|---|---|---|---|
| Task 096 decision 2 | `docs/tasks/096-event-triggers.md:216-235`; premise `:223-224`; credential reason `:233-235` | `type: command` is primary; "*Beaten:* per-vendor adapter packages in-tree", a maintenance treadmill; "a first-party Jira client wants a Jira credential, and the moment vincent holds one, 'the daemon inherits the user's environment' stops being true" | **Departed from, for issue sync only** (decision 1): a native provider is the only route to task 130 parity without Jira semantics in sh and pwsh. The credential half is **kept** (decision 4). The premise "Jira Cloud has no first-party CLI" is **corrected**: Atlassian ships `acli`, evaluated and rejected |
| Task 130 decision 15.1 | `docs/tasks/130-issues-pillar.md:442-445` | "No `internal/issuesource` package … the first provider does not design for the second." | **Superseded** (decision 2): a second provider now exists |
| Task 137 frame gap F5 | `docs/tasks/137-handoff-pillar.md:845` | Supersedes 15.1 "once a second exists" | The precedent for superseding 15.1; F5 stays the path for every other tracker (decision 8) |
| Task 137 decision 10 | `docs/tasks/137-handoff-pillar.md:670-680` | No in-tree target adapters; "When a second intake provider lands (frame gap F5), its write-back arrives with it." | **Native half departed from** (decision 3): Jira write-back is native, through the outbox. Every other target stays a command step |
| Spec §2 | `docs/spec.md:103` | "Secret management (daemon inherits the user's environment)." | **Kept** (decision 4) |
| Spec §3 row 26 | `docs/spec.md:137` | Prefer `gh`, else `GITHUB_TOKEN`/`GH_TOKEN`; "vincent stores no credential" | **Kept** |
| Spec §12.3 `github` | `docs/spec.md:8950-8955` | "deliberately **no token key here** … Read per use" | **Kept**: Jira mirrors it, with names, not values |
| Spec §16 | `docs/spec.md:17597-17607` | "The daemon stores no secrets"; `environment.set` is not a secret store | **Kept** |
| Spec §3 row 34 | `docs/spec.md:145` | The trigger ingress secret is read from the environment | The precedent for config naming an environment variable |
| Task 130 decision 15.4 | `docs/tasks/130-issues-pillar.md:455-457` | Kind and priority are local | **Narrowed** (decision 9.9): kind is seeded from `issuetype` on first import |
| Task 130 decision 15.5 | `docs/tasks/130-issues-pillar.md:458-461` | The repo binding is sticky; a mismatch stops sync with `origin_changed` | **Generalised** to the explicit Jira binding (decision 5) |
| Task 130 decision 15.6, amended 2026-10-23 | `docs/tasks/130-issues-pillar.md:462-473`; amendment `:466` | A GitHub 410 deletes as `sync`; 404 only marks | **No Jira analogue** (decision 9.8): a Jira 404 means deleted or forbidden, so it marks `missing` only |
| Task 130 decision 15.8 | `docs/tasks/130-issues-pillar.md:477-479` | The sync route is not an MCP tool | **Kept** |
| Task 130 decision 24.1 | `docs/tasks/130-issues-pillar.md:780-784` | Deleted remote comments are kept and never detected | **Kept** for parity. Jira could detect deletions; recorded as a possible later improvement |
| Task 130 decision 7 | `docs/tasks/130-issues-pillar.md:184-192` | `github_issue` is removed from every create surface | **Kept**: there is no `jira_issue` field (open question 6) |

The code citations the records rely on hold too:
`internal/daemon/issuesync.go:21-24` ("the row's provider column is the only
seam a second provider would need") and `issueProvider = "github"` at `:46`.

## Open questions

Answered, or carrying a default that the named implementing issue settles
explicitly. A "proposed default" is open until that issue.

1. **Accept the departure from task 096 decision 2, or route Jira through F5?**
   *Settled by the author 2026-11-03:* the departure is accepted, for issue
   sync only, with the credential half kept and the premise corrected
   (decision 1).
2. **Site plus project key, or also a JQL narrowing clause?** *Settled by the
   author 2026-11-03 (#902), overturning the proposed default:* an optional
   JQL narrowing clause **is** in v1, ANDed after the project, single-line and
   without `ORDER BY` (decision 5). Free JQL as the whole binding stays
   rejected: it would break move detection and the sweep.
3. **May a project switch its issue source after imports?** *Settled
   2026-11-03 (#902), as proposed:* yes, as an explicit rebind at PATCH time.
   Earlier imports stay, marked `remote_status: missing` (decision 5).
4. **Sweep 404: mark-only, or delete after a confirming probe?** *Settled
   2026-11-05 by #908:* mark-only (decision 9.8). An absent, archived or
   JQL-excluded issue is `missing`, never deleted, and live again when it
   reappears.
5. **Mirror restricted-visibility comments?** *Settled 2026-11-05 by #908:*
   no — they are always skipped, with no config knob (decision 9.6). One
   mirrored before it was restricted stays mirrored for now: open question
   12.
6. **The Jira prefill title, and a `jira_issue` field?** *Settled by the
   author 2026-11-03: `PROJ-12 <title>`, no `jira_issue` field (decision 12).*
   Was the proposed default: the prefill title is `PROJ-12 <title>`, and there is no
   `jira_issue` field. #896's question refers to "task 130 decision 2's
   `github_issue`", but `github_issue` was removed from every create surface
   by task 130 **decision 7** (`docs/tasks/130-issues-pillar.md:184`). A Jira
   issue reaches a task the way a GitHub one does: imported, then created
   from with `issue_id`.
7. **A `GET /v1/projects/{id}/jira` probe, or a `vincent jira issues` remote
   browse?** *Settled by the author 2026-11-03 (#902), overturning the
   proposed default:* the `GET /v1/projects/{id}/jira` probe **is** in v1. It
   lands with the Jira client in #908, because nothing can answer it before
   then; #902 adds only the binding. The remote browse stays out of v1.
8. **Should "In Progress" (`indeterminate`) appear anywhere?** *Proposed
   default, open until #910:* nowhere in v1; it is kept only in `remote_json`
   (decision 9.10).
9. **Doctor JSON: additive `jira.sync`, or a provider-neutral `issue_sync`
   list?** *Proposed default, open until #911:* the additive split — `jira.sync`
   beside `github.sync`.
10. **#897's own: should spec §3's Jira row land ahead of code, or with
    #908?** *Settled by the author 2026-11-03:* with #908 (decision 10).
11. **Is a Jira `labeled`/`unlabeled` by sync trusted?** *Open, raised
    2026-11-04 by #913's review (F4).* An `issues` trigger reads one trust
    table for `by: sync` whatever the tracker (`trustedEvents` in
    `internal/trigger/definition.go`), and it exempts `labeled` and
    `unlabeled` from `allowed_actors`. That exemption was confirmed against
    GitHub's repository role table, where labelling needs triage. Nobody has
    checked it against Jira, where labels are an ordinary field that anyone
    who may edit the issue can change, and the reporter may be one of them,
    depending on the permission scheme. 142.17 left the behaviour as it
    was, because a trust change is out of its scope. This should be settled
    before the importer (142.12, #908) delivers Jira label changes.
    *Settled 2026-11-05 by #908:* not trusted. On `type: issues` a Jira
    `labeled`/`unlabeled` by `sync` goes through `allowed_actors` like any
    other sync action; GitHub's exemption is unchanged. The trust table is
    provider-aware (`syncUntrusted` in `internal/trigger/definition.go`).
12. **What happens to a mirrored comment later restricted in Jira?** *Open,
    raised 2026-11-05 by #943's review (F6).* #908 skips a comment whose
    `visibility` is set when it reads a thread (decision 9.6), but a skipped
    comment looks to the importer exactly like a deleted one, and task 130
    decision 24.1 keeps a mirrored row whose remote comment disappeared. So
    a comment that was public when it was mirrored and is restricted to a
    role or group afterwards — often because it held something sensitive —
    stays here with its old body, visible to every local reader and agent.
    #908 only narrows the claim to "never newly mirrored". The fix is a
    choice the comment mirror has never had to make, because GitHub has no
    restriction: the source would report a restricted comment with a marker
    instead of dropping it, and the importer would either delete the
    mirrored row or redact its body, with an event either way. Both need a
    store write path that does not exist (no comment is ever deleted or
    rewritten by sync today). To settle before 142.18's end-to-end gate.

## Facts carried unverified

These are #896's research findings, not vincent-verified; 142.3 (#899) settles
them against a real site:

- Search is `GET /rest/api/3/search/jql` with `nextPageToken` and no `total`;
  the old `/search` is deprecated, with its removal reported for 2025 (#896's
  finding). Search is eventually consistent, and JQL
  dates are minute-precision in the user's time zone unless given as epoch
  milliseconds.
- The issue `id` is stable across a move and the key is not; 404 answers both
  deleted and forbidden, with no 410; there is no ETag and no project-wide
  comment feed; bodies are ADF JSON.
- API-token traffic is outside the 2026 points quota and only burst-limited
  (429 with `Retry-After`). Cloud tokens expire within 365 days, so a 401 is a
  routine failure.
- The `acli` facts in decision 1 and the Atlassian Ascend dates in decision 7.

## Risks

- **The departure.** Holding task 096 decision 2 strictly would mean F5 with
  Jira as its first user: sh plus pwsh scripts, a `jq` dependency and a richer
  protocol. 142.4 is needed either way.
- **A wrong site sends the token there.** Mitigated by HTTPS-only (decision
  5.3) and by showing the site on every status surface.
- **Search lag.** Eventual consistency can make "Jira wins" revert a fresh
  human write on the next tick unless the importer reconciles against outbox
  results.
- **Cloud API churn**, as the `/search` deprecation shows (its 2025 removal is
  #896's finding, settled by 142.3). Endpoints are
  pinned in one place and tested against recorded fixtures (142.3, 142.11).
- **Service-mode environments** lack the login shell's variables, so a missing
  token must surface as `no_credential` in sync status and doctor (decision 4).
- **The first gate with a background HTTP server on Windows** (142.18). Budget
  for Windows-only faults.
