# 082 — Report the real usage quota codex and claude now expose

**Status:** ✅ done (1/1)
**Issue:** #310
**Amends:** §9.2, §9.3, §9.6, §9.7, §13.2, §13.3, §13.4, §14, §15, §16
**Supersedes in part:** [026](026-agent-quota-observability.md) decision 1
("observed signal only; no adapter interface change") and §9.6's "no supported
CLI can report remaining quota from a non-interactive invocation" — by the
escape hatch those two wrote for themselves, not against them

§9.6 shipped `used_percent` and `window` as permanent nulls and named the day
they would fill: "the day a vendor ships a surface, at which point `source`
changes from `observed`". That day arrived for two of the three adapters, and
task 026 decision 1 kept `source` on the wire for exactly this. So this is the
clause being taken up rather than reopened, and both records are amended in
place with dated notes.

Both of the issue's factual claims were verified on a real machine before being
accepted, and one detail in each needed correcting.

**codex — confirmed against codex-cli 0.150.1.** `codex app-server --stdio`,
after `initialize` and `initialized`, answers `account/rateLimits/read` with
`result.rateLimitsByLimitId.codex` (this build also duplicates it at
`result.rateLimits`), carrying `primary` and `secondary`, each with
`usedPercent`, `windowDurationMins` and `resetsAt`. Two corrections: `resetsAt`
is **unix epoch seconds** (`1788371363`), not RFC3339; and the full round trip
measured **0.80 s**, not the "seconds" the issue assumed — which is what makes
decision 4 viable.

**claude — confirmed as a push only.** Claude Code hands `statusLine.command` a
JSON object carrying `.rate_limits.five_hour.used_percentage`,
`.rate_limits.seven_day.used_percentage` and each window's `resets_at`. There is
no pull, so vincent is a bystander to delivery.

**cursor — unchanged.** No surface. Observation-only; §9.7 stays true as
written, and is restated positively rather than left to look like an oversight.

## Decisions

1. **One task, both halves.** The codex pull, the claude status-line install and
   the shared wire and render work land together. Splitting codex from claude
   was offered and declined: they fill the same block, and a `windows[]` shape
   designed against one source is a shape that fits one source.

2. **claude is wired through `statusLine.command`, not a credentials read.** A
   fourth route exists and is strictly better on the pull-versus-push axis:
   `GET https://api.anthropic.com/api/oauth/usage` with the OAuth token from
   `~/.claude/.credentials.json` returns `five_hour.utilization` directly, with
   no settings write and no §16 amendment. It is **rejected** because reading
   that token is state-file parsing, which the v0 **T1.7 decision** forbids and
   which §9.5 cites as still standing. T1.7 is not reopened. The consequence is
   accepted: the claude reading exists only while the user runs Claude Code
   interactively, and vincent writes another tool's configuration file to get
   it.

3. **`windows[]`, with the scalars kept and carrying the tightest window.** Both
   reported sources give two windows against a block that has one
   `used_percent`, one `window` and one `resets_at`. The block grows a
   `windows[]` array and the existing scalars stay, carrying whichever window is
   closest to exhaustion (highest percentage, ties broken by earliest reset).
   That **fills** the fields §9.6 promised to fill rather than repurposing them:
   a client written against task 026's shape keeps working and gets the number
   that matters. Replacing the scalars outright was declined.

4. **A reported reading lives in the catalog cache, not in SQLite.** `quotaTTL`
   sits beside `authTTL` in `internal/agent/catalog.go`, refreshed on the same
   seam `logged_in` uses: on a normal request when the entry is stale, and
   unconditionally by `GET /v1/doctor`, which already forces the probe (task
   029's amendment). `agent_quota` keeps observations only and there is **no
   migration** — §14's stated reason for the absence of `used_percent` and
   `window` columns ("a column with no writer is dead schema in an append-only
   migration set") is left standing rather than reversed. The issue's assumption
   that the probe is too slow for `/v1/agents` does not survive the 0.80 s
   measurement, and the `authTTL` precedent already covers the case. The
   consequence for claude follows and is accepted rather than worked around: a
   pushed reading is **as durable as the daemon**, and a restart drops it until
   the next render repopulates it. That is honest rather than lossy — a
   percentage nothing has confirmed since the daemon started is one vincent
   should not be showing.

5. **The retirement rule narrows to observations.** §14's "an observation is
   retired by evidence — the next successful agent step on that adapter deletes
   it" applies to `source = observed` rows only. A step completing proves the
   wall vincent watched has come down; it proves nothing about a percentage a
   vendor reported.

6. **The capability is an optional interface, not a method on `AgentAdapter`.**
   `agent.QuotaReporter` is satisfied by codex alone. That is what keeps task
   026 decision 1's objection answered: cursor does not implement it and grows
   no "cannot report" stub, per the standing rule that a capability an adapter
   lacks is stated in §9.x and never emulated.

7. **The push route is excluded from the MCP tool surface**, extending row 28's
   list rather than drawing a new line: an agent must not be able to forge a
   daemon-level fact about the host it runs on. A step reporting its own adapter
   at 99% would paint every board in the installation with a wall that does not
   exist, and the reading carries a source, not a caller.

8. **Failure is silent, everywhere.** No probe is failed and `probe_error` keeps
   meaning only "the option probe failed": no codex CLI, an unauthenticated
   codex, a handshake that times out and a malformed response all degrade to
   task 026's observed behaviour. Symmetrically, `vincent statusline` must never
   break the user's status line — a daemon that is down or a push that fails
   still prints the chained command's output and still exits 0.

## What landed

| Package | Change |
|---|---|
| `internal/agent` | `QuotaReporter`, `ReportedQuota`/`ReportedWindow`, `quotaTTL` in `catalog.go` with prior-value-on-failure, `SetQuota` for the push, doctor forcing the refresh |
| `internal/agent/codex` | A JSON-RPC-over-stdio client for `app-server --stdio` in Go — no bash, no `jq`. Bounded timeout, tree-kill via `procx`, epoch-second `resetsAt`, `rateLimitsByLimitId.codex` preferred with `rateLimits` as fallback |
| `internal/api` | `windows[]` on `quotaResponse`, the reported-over-observed merge, the `source`-split `spent`, `POST /v1/agents/{name}/quota`, and one catalog read shared by `/v1/agents` and `/v1/info` |
| `internal/apiclient` | The matching wire types and `ReportAgentQuota`; `SpentAt` splits on `source` |
| `internal/cli` | `vincent statusline`: read Claude Code's JSON on stdin, push it, run the wrapped command with the same stdin and print its stdout |
| `internal/tui` | The daemon view's install/uninstall flow with an exact-JSON preview, `i` in `ctxDaemon`, a persisted decline, and `quotaNote` rendering real windows while keeping `→` versus `≈` |
| `internal/taskrun`, `internal/store` | Retirement narrowed to `observed` rows. No schema change |
| `cmd/fakeagent`, `scripts/m2-gate.sh` | An `app-server` dialect, and a gate leg asserting `windows[]` over curl |

## Follow-ups

None open. `docs/gates/` records no manual leg for this: the codex half is
asserted by `m2` scenario 5 over curl, and the TUI half by
`internal/tui`'s tests plus the two-shells-over-one-data-dir pattern for the
persisted decline.
