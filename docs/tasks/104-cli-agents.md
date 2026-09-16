# 104 — `vincent agents`: login state, compatibility and quota from the command line

**Status:** ✅ done (4/4)
**Issue:** [#393](https://github.com/lezli01/vincent/issues/393)
**Spec:** amends §12.1

## Problem

`GET /v1/agents` already carried everything a person asks about an agent CLI
before trusting a task to it: the installed `version`, the task 041
`version_verdict` and `tested_versions`, §9.5's `logged_in`, the `input_verdict`
and `restricted_verdict`, `probe_error`, and the nullable `quota` block (task
026, with task 082's `windows[]`). `apiclient.ListAgents` and its wire types
existed, `AgentQuota.SpentAt` included. No CLI command called it, so the facts
were reachable only through the TUI's daemon view or curl.

Three public pages were already wrong about it. `docs/guides/troubleshooting.md`
told readers to check `input_verdict` in `vincent agents`, a command that did not
exist. `docs/reference/cli.md` gave `vincent daemon status` a `--json` flag and
said it reported "which agent CLIs it resolved"; it had neither, and never had
(e1ac224, T4.4). `docs/guides/agents.md` repeated the second claim.

`agents` was on task 048's "What is still API-only" list, tracked as #393. No
recorded decision stood in the way.

## What shipped

```
vincent agents [--json] [--refresh]
```

A top-level thin API client, `internal/cli/agents.go`. The table is
`AGENT  VERSION  BUILD  LOGIN  QUOTA  NOTES`, one row per adapter in the daemon's
registration order. No daemon, store, API, apiclient, doctor or TUI change, and
no migration.

## Decisions

1. **2026-09-16 — Quota is the merged block, as served.** The CLI prints the one
   `quota` block `GET /v1/agents` returns, labelled by its `source`, and merges
   nothing: a reading wins and an observation is the fallback, as task 082 chose.
   The issue's "observed and reported, with their source" is met by showing
   whichever the daemon serves, with the source that tells them apart. A null
   block is `unknown` (task 026 decision 5). A reading prints its source in prose,
   every window as `<label> <pct>` with its reset, and `read <observed_at>`. An
   observation still shut prints `spent → <reset>` for a reset the CLI stated and
   `spent ≈ <reset>` for one vincent estimated (task 026 decision 2); a lapsed one
   prints `ok · last spent <observed_at>`. Extending `/v1/agents` to carry the
   underlying observation beside a reading was rejected: it is a wire change
   across `internal/api`, `apiclient`, §9.6/§13.2 and the TUI, made only to
   reverse a merge task 082 made on purpose.

2. **2026-09-16 — `vincent doctor` is not extended.** Quota lives in
   `vincent agents`. A quota field on `doctor.Agent` and `/v1/doctor` was
   rejected: doctor's agent rows are built with no daemon running, where no quota
   exists, and it would be a second wire change for a fact one command away. The
   doctor reference's Agents row points to `vincent agents` instead.

3. **2026-09-16 — Four facts are columns; the other facets are bad-news notes.**
   Version, build verdict, login and quota are columns. `not found: <error>`,
   `no mid-run input`, `no restricted mode on <GOOS>` and
   `option probe failed (curated catalog)` trail the row in `NOTES`, printed only
   when true, the way `doctorAgentVerdicts` trails doctor's rows. A `tested`
   build or a `supported` verdict adds nothing. `supports_resume` and the
   tested-builds list are `--json` only. A column per facet was rejected because
   it squeezes the quota cell out of a terminal. The issue's four columns alone
   were rejected because troubleshooting.md would then point readers at a verdict
   the command hides.

4. **2026-09-16 — The stale `daemon status` claims point at `vincent agents`, in
   this change.** `vincent daemon status` keeps its behaviour. Its reference
   section loses the phantom `[--json]` and the agent claim, and `agents.md`
   names `vincent agents` as the surface that reports what vincent resolved.
   The same claim turned up in the installation page, the macOS and Linux
   platform pages and troubleshooting's "An agent CLI is not found", and is
   pointed the same way. Building agent output into `daemon status` was rejected
   as a duplicate of the new command. Deferring the fix was rejected: it would
   leave those pages contradicting the command this change adds.

5. **2026-09-16 — Conventional defaults, settled without a question.**
   - The name is `vincent agents`, from the issue and the existing docs.
   - `--refresh` sends `?refresh=true`, the TUI's `R`; the default answers from
     the catalog cache, as the TUI's pickers do.
   - Exit 0 whenever the daemon answered, whatever the adapters' health (task 041
     decision 4, task 006 decision 7); 1 on an API error; 2 with no daemon.
   - No auto-start (PR U decision).
   - `--json` is the endpoint's `agents` array, bare and never `null` (PR U
     decision).
   - Timestamps are local RFC3339, as task 101's hold row: a `7d` window's reset
     is days away and the TUI's `15:04` would not say which day.
   - `LOGIN` uses doctor's `loggedInWord`, and `-` for an adapter not installed.
   - The renderer is the CLI's own, on the `doctorAgentVerdicts` precedent. The
     TUI's quota helpers stay unexported and unchanged: the formats differ (full
     timestamps and no glyphs or styles here).

## Work

- [x] **104.1 — The command** in `internal/cli/agents.go`, registered in
  `newRootCmd`, with the row, cell and quota renderers as pure functions of
  `(apiclient.Agent, now)`. ✓ 2026-09-16
- [x] **104.2 — Renderer tests**: `internal/cli/agents_test.go` — every quota
  cell (null, stated and estimated resets, lapsed, a two-window reading, a window
  with no reset or label, an unknown source, a 100% reading), the login tri-state
  and `-`, and notes that carry bad news only, with the restricted note asserted
  against `runtime.GOOS`. ✓ 2026-09-16
- [x] **104.3 — Live and e2e tests**: `internal/cli/agents_live_test.go` against
  the real handlers and a catalog over `cmd/fakeagent` (the transcript harness
  gains `withAgentCatalog`) — a store observation and a pushed reading in the
  table, `--json` equal to `ListAgents`, `--refresh` reaching the handler as
  `?refresh=true`, exit 0 with no usable adapter; and `{"agents"}` in
  `commands_e2e_test.go`'s no-daemon table. ✓ 2026-09-16
- [x] **104.4 — Documentation**: §12.1 amended, `docs/reference/cli.md` (new
  section, `daemon status` and doctor's Agents row corrected),
  `docs/guides/agents.md`, `docs/guides/troubleshooting.md`,
  `docs/guides/scripting.md`, `docs/features.md`, the same stale
  `daemon status` claim in `docs/getting-started/installation.md`,
  `docs/platforms/linux.md` and `docs/platforms/macos.md`, `CHANGELOG.md`, and
  the dated note on task 048's "What is still API-only".
  ✓ 2026-09-16
