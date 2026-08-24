# 026 — Reporting each agent's usage-quota state

**Status:** ✅ done (7/7)
**Opened:** 2026-08-24

vincent only ever learned about an agent's usage quota after it was gone, and
then threw the fact away. Task 003 taught the adapters to recognize a CLI that
stopped because the account's window is spent (`agent.FailureUsageLimit`), and
the engine turns that into an admission hold rather than a failure — good
recovery, entirely reactive. Worse, the reset it computes lands **only** in the
held task's `admit_not_before`, which any transition out of `queued` clears
(task 003 decision 1). The observation survives exactly as long as the hold,
which is precisely the moment nobody needs to be told.

This task makes that observation durable, per adapter, and visible in the three
places an agent is chosen or watched.

It deliberately does **not** build the thing [#179](https://github.com/lezli01/vincent/issues/179)
opened with — a quota capability on `agent.Adapter` that probes the CLI. See
decision 1.

## Decisions

### 1. Observed signal only; no adapter interface change (2026-08-24)

`agent.Adapter` gains no method, `agent.Probe` gains no caller, and no adapter
grows a quota parser. The daemon reports what it has watched happen.

The issue named its own open question — which of `claude`, `codex` and `cursor`
can genuinely report remaining quota from a non-interactive invocation. Answered
against the installed binaries:

| CLI | version | quota surface |
|---|---|---|
| `claude` | 2.1.241 | commands are `agents auth auto-mode doctor gateway import install mcp plugin project setup-token ultrareview update` — no `usage`, no `limits` |
| `codex` | 0.149.0 | no usage/limits subcommand; `login status` and `doctor` only |
| `cursor-agent` | 2026.08.11-e8db854 | `about --format json` → `{cliVersion, model, subscriptionTier, osPlatform, osArch, userEmail, terminalProgram, shell, lastRequestId}` — a plan tier, no numbers |

**Beaten:** shipping the capability with three null implementations. It costs an
interface change, three "cannot report" spec paragraphs and four permanently
"unknown" renders, in exchange for nothing a user can read. The repo's own
convention points the same way — *a capability an adapter lacks is stated in
§9.x and ignored at run time; it is never emulated* — so the gap is recorded in
§9.2, §9.3 and §9.7 and nothing is built for it. The wire still carries a
`source` field so a probe-sourced value can join later without a second shape.

### 2. The observation is a one-row-per-agent table (2026-08-24)

Migration `0011_agent_quota.sql`: `agent` (PK), `observed_at`, `resets_at`,
`resets_at_reported`, `source`.

`resets_at_reported` mirrors the `reported_by_cli` field `holdForUsageLimit`
already logs, and is the difference between rendering `→ 14:20` and `≈ 14:20`.
A computed 15-minute estimate must not be shown as a fact the CLI stated.

**Beaten:** a `retry_after` column on `step_runs`. It keeps history, but every
read becomes a scan-and-pick-latest per adapter, and current state is what all
three surfaces want. **Beaten:** deriving from held task rows with no schema at
all — the signal vanishes the instant the last held task is admitted, so the
board would go quiet exactly when the window is still shut.

No `used_percent` or `window` **columns**. They exist on the wire (decision 5)
and nothing can fill them; a column with no writer is dead schema in an
append-only migration set.

### 3. The engine writes it, and clears it on evidence (2026-08-24)

- `holdForUsageLimit` upserts the row alongside the hold it already writes,
  with the same effective reset. The upsert is **monotonic**: an observation
  older than the stored one is discarded, so two actors hitting the same wall
  in the same second cannot make the state go backwards.
- A **successful** agent step on adapter X whose run finished after the stored
  `observed_at` deletes X's row. This matters because a hold with no
  CLI-reported reset is an estimate — `now + 15m` — and a task that succeeds
  five minutes in proves the window reopened. Without the clear, the board
  would claim "spent until 14:20" about an adapter it can watch doing work,
  which is a worse lie than saying nothing.
- A lapsed `resets_at` alone does **not** delete the row: the API derives
  `spent = now < resets_at` and keeps the timestamps as "last spent at"
  context. No sweeper goroutine, no timer.

The row is agent-scoped rather than task-scoped and the daemon remains the
single writer, so no taskrun or scheduler ownership invariant moves. Task 003's
out-of-scope note reserves the *agent-wide hold* — one task's `usage_limit`
suppressing admission of every other queued task on that adapter — for its own
issue; this is not that. Nothing here touches admission.

### 4. `logged_in` gets a TTL in the catalog cache, in this PR (2026-08-24)

§9.6 records a decision that giving `logged_in` its own short TTL "would fix
every surface rather than one and is the better follow-up… it was beaten here
because it splits a cache line that is currently one clean rule". That decision
is **superseded**, not relitigated silently: the follow-up is implemented here
and §9.6 is amended in place with a dated note.

The trigger is this task. The board grew a second per-adapter fact, and a
staleness rule that repaired one surface (`GET /v1/doctor` forcing refresh) and
not the others stopped being defensible.

`authTTL = 5 * time.Minute`, chosen the way `failureTTL` was: long enough that a
board, a detail view and a new-task form asking in the same second cost one
probe, short enough that a user who logs in and looks again is told the truth.
Only `Detect` re-runs — the `Options` catalog stays keyed on binary identity,
which is sound for it. Only adapters that can answer are re-asked: an adapter
whose `logged_in` is nil has no auth state to go stale. **A failed re-`Detect`
keeps the prior `logged_in` and records the error**, for the same reason T4.22
exists: a Windows deadline is `TerminateProcess(pid, 1)`, and reading that as
"not authenticated" is a false accusation against a logged-in account.
`GET /v1/doctor` keeps forcing refresh unconditionally.

### 5. Wire shape: the full block, nulls where nothing can fill it (2026-08-24)

Both `GET /v1/agents` **and** `GET /v1/info` carry a nullable `quota` object —
both, because the board header reads `/v1/info` while the new-task and repair
forms read `/v1/agents`, and a client should not need a second fetch to render
a badge. They are served from one read, so the two cannot disagree.

`"quota": null` when nothing has ever been observed — never a zero that reads as
"empty". `used_percent` and `window` ship as permanent nulls so clients are
written once against the final shape; the day a CLI ships a surface they fill in
and `source` changes. Probe-failure degradation is unchanged and untouched:
nothing here can fail a probe, and `probe_error` keeps its current meaning.

### 6. A new durable event, emitted only on change (2026-08-24)

`store.EventAgentQuotaChanged = "agent.quota_changed"`, payload
`{agent, spent, resets_at, source}`, appended when the upsert actually changed a
value or the clear actually deleted one — never on a re-observation identical to
what is stored, and never merely because a window lapsed. Precedent is
`workflow.registry_changed`: a daemon-level durable event with no `task_id`.

**Beaten:** reusing `task.state_changed`. It makes every client re-derive "a
task hold implies an agent-level fact", which is the kind of inference the
daemon publishes rather than delegates.

`scheduler.WakeOn` returns **false** for it — nothing about admission changes —
and there is a test pinning that.

### 7. Three TUI surfaces, not four (2026-08-24)

The issue asked for four. The fourth — the task detail header — is already
built: §15's task-003 amendment gives it `queued · usage limit → 14:20`. The
three that are new are the board header badge, the daemon view's adapter line,
and the new-task form's advisory. Task 003 decision 4 (no pre-flight refusal)
stands: the form warns and submits.

The board derives "still shut" from `resets_at` against its own clock rather
than from the wire's `spent`, because nothing is emitted when a window merely
lapses and a badge trusted to the wire would stay on screen indefinitely.

## Deviations from the issue

All following from the reshape in decision 1:

1. **No new `config.yaml` key and no background poll.** The signal is
   store-driven and event-driven; there is no probe to pace.
   `docs/reference/configuration.md` is unchanged.
2. **No quota capability on `agent.Adapter`**, and no per-adapter capability
   paragraphs beyond recording that no CLI has a surface.
3. **Three TUI surfaces, not four** — see decision 7.
4. **"A quota probe failure degrades exactly as the option probe does"** is
   moot: there is no quota probe. The equivalent guarantee that *is* tested is
   that a failed `Detect` under the new TTL never downgrades a good
   `logged_in`.
5. **Added, beyond the issue:** the `logged_in` TTL (decision 4).

## Tasks

- [x] **026.1** Migration `0011_agent_quota.sql`, the `AgentQuota` type, and
  monotonic `UpsertAgentQuota` / evidence-based `ClearAgentQuota` /
  `ListAgentQuota` / `GetAgentQuota`, each writing `agent.quota_changed` in the
  same transaction and only on a real change. ✓ 2026-08-24
- [x] **026.2** `internal/taskrun`: `holdForUsageLimit` records the window it
  acted on; a successful agent step retires the adapter's observation. The
  adapter name travels on `stepOutcome`, so a stop inside a `parallel` group
  keeps its attribution. ✓ 2026-08-24
- [x] **026.3** `agent.quota_changed` in `store`, with `scheduler.WakeOn` false
  for it and a test pinning that. `internal/scheduler` is otherwise untouched.
  ✓ 2026-08-24
- [x] **026.4** `internal/agent/catalog.go`: `authTTL`, Detect-only refresh,
  prior-value-on-failure with the error recorded. ✓ 2026-08-24
- [x] **026.5** The `quota` block on `GET /v1/agents` and `GET /v1/info`, and
  the matching `apiclient` wire types. ✓ 2026-08-24
- [x] **026.6** The three TUI surfaces: board header badge, daemon-view line
  (including "unknown" said out loud), new-task advisory that still submits.
  ✓ 2026-08-24
- [x] **026.7** Gate scenario 5 extended over curl; `scripts/screenshots.sh`
  seeds one observed-spent adapter and the affected shots are regenerated;
  §9.2/§9.3/§9.6/§13.3/§14/§15/§18 amended, dated; public docs updated.
  ✓ 2026-08-24

## Noted, deliberately not folded in

`claude` 2.1.241 ships `claude auth status`. §9.5 states that "claude exposes no
non-interactive auth surface at all… the captured `--help`
(`internal/agent/claude/testdata/help_2.1.224.txt`) carries no `login`, `auth`
or `status` command", which is now stale — claude's `logged_in: null` could
become a real boolean, and the v0 T1.7 decision it protects (no state-file
parsing) would be untouched by using an official subcommand. That is a genuine
improvement to the same panel this work touches, and it is a different change
with its own fixture to capture. It wants its own issue.
