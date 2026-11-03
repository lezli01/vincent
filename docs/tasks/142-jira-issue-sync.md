# 142 — Jira issue sync: Jira Cloud issues in a project's issue set, at parity with GitHub issue sync

**Status:** 🔄 in progress (1/20) · **Opened:** 2026-11-03

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
- [ ] **142.2** ([#898](https://github.com/lezli01/vincent/issues/898)) A pure,
  stdlib-only package for the mapping of decision 9: state and reason both
  ways, the kind seed, labels and the transition chooser.
- [ ] **142.3** ([#899](https://github.com/lezli01/vincent/issues/899)) Cloud
  REST v3 responses captured from a real site, settling the facts this
  document carries unverified. Needs a person with a Jira Cloud site.
- [ ] **142.4** ([#900](https://github.com/lezli01/vincent/issues/900)) The
  issue-provider interface (decision 2); GitHub implements it with no
  behaviour change. Depends: 142.1.
- [ ] **142.5** ([#901](https://github.com/lezli01/vincent/issues/901)) The
  `"github"` equality checks replaced, so a second provider writes back and is
  guarded. Depends: 142.4.
- [ ] **142.6** ([#902](https://github.com/lezli01/vincent/issues/902)) The
  per-project issue-source binding (decision 5): migration, API, CLI, TUI
  form, MCP and sticky semantics.
- [ ] **142.7** ([#903](https://github.com/lezli01/vincent/issues/903)) Issue
  sync scheduled per bound provider, decoupled from pull-request
  reconciliation, and the `jira:` config block. Depends: 142.4, 142.6.
- [ ] **142.8** ([#904](https://github.com/lezli01/vincent/issues/904))
  Daemon-rendered `source.key`/`ref`/`short_ref` and provider display names;
  clients stop building references.
- [ ] **142.9** ([#905](https://github.com/lezli01/vincent/issues/905))
  `remote_key` and `source=jira` filters, and `vincent issue ls --remote`.
- [ ] **142.10** ([#906](https://github.com/lezli01/vincent/issues/906))
  HTML→Markdown for bodies and comments, and Markdown→ADF for outgoing
  comments (decision 9.5).
- [ ] **142.11** ([#907](https://github.com/lezli01/vincent/issues/907)) The
  fake Jira corpus, an `httptest` leg and a `cmd/fakejira` server.
- [ ] **142.12** ([#908](https://github.com/lezli01/vincent/issues/908)) The
  `internal/jira` client and the importer for Jira: listing, initial import,
  sweep, comments, rate limits and environment credentials; spec §3 row 39
  (decision 10). Depends: 142.7, 142.5, 142.2, 142.10, 142.11.
- [ ] **142.13** ([#909](https://github.com/lezli01/vincent/issues/909)) The
  outbox for Jira (decision 3): transitions with resolution, ADF comments,
  echo handling. Depends: 142.12.
- [ ] **142.14** ([#910](https://github.com/lezli01/vincent/issues/910))
  Provider-aware sync status reasons, the TUI `S` key, the overview glyph, and
  the `issue sync` CLI. Depends: 142.7.
- [ ] **142.15** ([#911](https://github.com/lezli01/vincent/issues/911)) A
  JIRA doctor row with a credential check and per-project sync health.
  Depends: 142.12.
- [ ] **142.16** ([#912](https://github.com/lezli01/vincent/issues/912))
  `.Issue.Source.Key`, `VINCENT_ISSUE_KEY`, `source.key` in the issue file,
  and the Jira prefill. Depends: 142.8.
- [ ] **142.17** ([#913](https://github.com/lezli01/vincent/issues/913)) The
  built-ins' checklists and both skills cover Jira-imported issues. Depends:
  142.16, 142.9.
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
question.

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
every issue key but not the id. *Proposed default, open until #902 (open
question 2):* the binding is the site plus one project key, with no JQL
narrowing clause.

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
   resolution.
2. **The reason comes from configurable resolution sets.** Duplicate is
   `Duplicate`; not planned is `Won't do`, `Won't Fix`, `Cannot Reproduce`,
   `Incomplete` or `Declined`; anything else, or none, is `completed`.
3. **Write-back picks from the transitions available now**, with an optional
   per-binding override map, and fails with `no_transition`,
   `ambiguous_transition` or `transition_needs_fields` rather than guess.
4. **`remote_key` is the Jira issue id**, which is stable across a move; the
   key is not. The daemon renders the display reference (`PROJ-12`), and
   clients print `source.ref` (142.8, #904).
5. **Bodies are rendered from `renderedFields`/`renderedBody` HTML to
   Markdown**, not by walking ADF, so one converter would also cover a later
   Data Center leg. Outgoing comments are wrapped as minimal ADF.
6. **Restricted-visibility comments are skipped** (privacy). *Proposed
   default, open until #908 (open question 5).*
7. **No Duplicate issue link is written.**
8. **A sweep 404 marks the issue `missing` and never deletes it.** Jira answers
   404 for deleted and forbidden alike and has no 410, so GitHub's "410
   deletes" rule has no safe analogue. *Proposed default, open until #908
   (open question 4).*
9. **`kind` is seeded from `issuetype` on first import only**, narrowing task
   130 decision 15.4 for this provider. Priority stays local.
10. **"In Progress" is not reflected in lanes.** Jira's `indeterminate`
    category is kept only in `remote_json`. *Proposed default, open until
    #910 (open question 8).*

### 10. Spec placement

The spec §3 decision-record row for Jira (row 39) lands with 142.12 (#908),
the client and importer, in the same pull request as the code that makes it
true. Each implementation pull request carries its own spec amendment. This
answers #897's open question.

*Beaten:* landing row 39 with this document, by the ahead-of-code exception
rows 36–38 took (task 130 decision 13, task 132 decision 14, task 137).

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
2. **Site plus project key, or also a JQL narrowing clause?** *Proposed
   default, open until #902:* the site plus the project key only; no JQL
   narrowing clause in v1. Free JQL as the whole binding would break move
   detection and the sweep.
3. **May a project switch its issue source after imports?** *Proposed default,
   open until #902:* yes, as an explicit rebind. Earlier imports stay, marked
   `remote_status: missing`.
4. **Sweep 404: mark-only, or delete after a confirming probe?** *Proposed
   default, open until #908:* mark-only (decision 9.8).
5. **Mirror restricted-visibility comments?** *Proposed default, open until
   #908:* skip them (decision 9.6).
6. **The Jira prefill title, and a `jira_issue` field?** *Proposed default,
   open until #912:* the prefill title is `PROJ-12 <title>`, and there is no
   `jira_issue` field. #896's question refers to "task 130 decision 2's
   `github_issue`", but `github_issue` was removed from every create surface
   by task 130 **decision 7** (`docs/tasks/130-issues-pillar.md:184`). A Jira
   issue reaches a task the way a GitHub one does: imported, then created
   from with `issue_id`.
7. **A `GET /v1/projects/{id}/jira` probe, or a `vincent jira issues` remote
   browse?** *Proposed default:* neither in v1.
8. **Should "In Progress" (`indeterminate`) appear anywhere?** *Proposed
   default, open until #910:* nowhere in v1; it is kept only in `remote_json`
   (decision 9.10).
9. **Doctor JSON: additive `jira.sync`, or a provider-neutral `issue_sync`
   list?** *Proposed default, open until #911:* the additive split — `jira.sync`
   beside `github.sync`.
10. **#897's own: should spec §3's Jira row land ahead of code, or with
    #908?** *Settled by the author 2026-11-03:* with #908 (decision 10).

## Facts carried unverified

These are #896's research findings, not vincent-verified; 142.3 (#899) settles
them against a real site:

- Search is `GET /rest/api/3/search/jql` with `nextPageToken` and no `total`;
  the old `/search` is being removed. Search is eventually consistent, and JQL
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
- **Cloud API churn**, as the 2025 `/search` removal shows. Endpoints are
  pinned in one place and tested against recorded fixtures (142.3, 142.11).
- **Service-mode environments** lack the login shell's variables, so a missing
  token must surface as `no_credential` in sync status and doctor (decision 4).
- **The first gate with a background HTTP server on Windows** (142.18). Budget
  for Windows-only faults.
