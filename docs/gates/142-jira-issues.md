# Task 142 gate — Jira issue sync

**Acceptance (task 142.18):** a project bound to a Jira Cloud site imports
its open issues, keeps them in sync both ways through the durable outbox,
refuses what must be refused, and reports what went wrong — proved over curl
against a real daemon and `cmd/fakejira`, on Linux, macOS and Windows.

## Scripted: `scripts/142-gate.sh`

Each scenario runs its own daemon and its own fake Jira over its own files;
`VINCENT_GATE_SCENARIO=N` runs one alone. The fake serves the built-in
corpus (project `VIN` with VIN-1 *To Do*, VIN-2 *In Progress* and VIN-3
*Done*, and a second project `OPS`) on a port it chooses and writes to a
file. The project is bound to it over the API as
`issue_source: {provider: jira, jira: {site: http://127.0.0.1:<port>,
project_key: VIN}}` — plain `http` is accepted for a loopback host only —
and the daemon reads the credential from `JIRA_USER` and `JIRA_API_TOKEN`.
What the daemon sent is read from the fake's request log.

| # | Scenario | Asserts |
|---|---|---|
| 1 | the import | VIN-1 and VIN-2 imported, open, `source.provider: jira`, keyed, synced; the Done VIN-3 not imported; the sync status `jira`, ok, import complete |
| 2 | idle sync | a forced sync over an unchanged corpus asks Jira and records no `issue.updated`, and no issue's version moves |
| 3 | closed in Jira | Done transitions POSTed to the fake close VIN-1 `not_planned` (Won't Do) and VIN-2 `completed` (Done), both by `sync`; no transition is written back |
| 4 | human close | the close reaches Jira as one transition POST with resolution Done; a second sync writes nothing more |
| 5 | crash-safe outbox | a close made while the fake is `unreachable` is `pending`; after `daemon stop --force` and a restart it is delivered, once |
| 6 | `jira.enabled: false` | set over `PATCH /v1/config`, the sync status says `disabled`, and a sync and a local close make no request |
| 7 | read-only | the write fails `no_write_scope`; the issue stays closed here and open in Jira |
| 8 | conflict | Jira closed the issue (Won't Do) while a local close was pending: the issue says `conflict`, holds `not_planned`, the write is never sent, and the sync status counts one conflict |
| 9 | no done transition | with In Progress's edge to Done removed (before the fake serves), VIN-2's close fails `no_transition`, local state kept, no transition POSTed |
| 10 | moved and deleted | after `fakejira move VIN-1 OPS OPS-7` and `fakejira delete VIN-2` and a sync now, VIN-1 is `moved` at `OPS`/7 with the `OPS-7` URL, VIN-2 is `missing` and still exists here |
| 11 | MCP guard | an agent's `issue_close` on VIN-1 is refused `forge_write_needs_human`; no write is enqueued and no request made |
| 12 | comment mirror | public comments are mirrored; comments restricted by `visibility` — the corpus's own, and one POSTed during the run — never are |
| 13 | granted handoff | a `role: handoff` task granted `issue.comment` and `issue.close` comments on and closes VIN-2 through the CLI: one comment POST, with an ADF body, and one transition POST |
| 14 | missing credential | the daemon restarted without `JIRA_API_TOKEN` reports `no_credential` and sends no request |
| 15 | GitHub origin | a Jira-bound project whose origin is GitHub resolves to `jira`; `vincent github pr link` still links a pull request through `gh`, and `gh` is never asked for issues |

Scenario 12 proves only that a comment restricted *before* it is read is
never mirrored. A comment mirrored while public and restricted afterwards is
task 142's open question 12, which this gate deliberately does not settle.

Scenario 10 asserts the moved issue's new location through `source.repo`,
`source.number` and `source.url`. Its `source.key` and `source.ref` still
read `VIN-1` after the move at the time of writing; that is recorded on the
gate's pull request for a follow-up rather than asserted here.

A run against a real Jira site is not part of this gate: the real-site facts
are task 142.3's (#899).

## Platforms

`ci.yml`'s `gate-group` runs it in group 1 on Linux, macOS and Windows. It is
the first gate with a background HTTP server: the fake binds `:0` and writes
its port to a file the gate polls for, and the gate stops it with
`POST /_fake/shutdown` and waits for it to exit before removing its
directory, because Windows has no graceful signal for a console-less child.

## Runs

| Date | Where | Result | By |
|---|---|---|---|
| 2026-11-06 | `scripts/142-gate.sh` on macOS (darwin/arm64) | PASS, all fifteen scenarios, and scenario 10 alone | task 142.18 |
| — | Linux and Windows | not yet run: the first run there is the gate's pull request's CI | |
