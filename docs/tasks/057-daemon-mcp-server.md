# 057 — Serve MCP from the daemon so agents can drive vincent directly

**Status:** 🔄 in progress (7/9)
**Issue:** [#243](https://github.com/lezli01/vincent/issues/243)
**Spec:** adds §13.4; amends §3 (decision row 28), §9.1, §9.2, §9.3, §9.4,
§9.7, §11, §12.3, §12.4, §14, §16, §20

## Problem

Every vincent client is a thin consumer of the daemon's localhost REST+SSE API.
The one class of consumer with no good way in is the AI coding agent — which is
the workload vincent exists to run.

An agent that wants to reach the daemon has to shell out to the `vincent` CLI or
hand-roll curl against `/v1`, having first found the port in `daemon.json` and
the token in `token`. No tool discovery, no argument schemas, no typed errors to
branch on, and no way to follow a run: `GET /v1/events` is SSE, which an agent
cannot consume from a single shell command. In practice it polls `vincent task
ls` in a loop or gives up and asks the human.

The second half is closer to home. Vincent's own agent steps run with no channel
back to the daemon supervising them. A step that wants to file follow-up work,
read a sibling lane's transcript, or answer a gate has to go through the same
undiscoverable curl path — so in practice it does none of them.

MCP is the protocol every agent CLI vincent drives already speaks. The API
surface to expose over it already exists and is stable.

## Decisions

**1. Transport is `/mcp` on the existing server.** *(2026-08-29)*

A streamable-HTTP MCP endpoint registered in `internal/api/server.go`'s route
table beside the `/v1` routes, inside the same `recover → log → auth` chain. It
reuses §13.1 wholesale: loopback only, no TLS, `Authorization: Bearer {token}`
from `{data_dir}/token`, discovery via `daemon.json`. No new listener and no new
auth story.

Decision record row 4 is *added to*, not reversed. The §13.1 timeout posture
already suits this and needed no change: it fixes a read-header, a whole-request
*read* and an idle timeout, and deliberately no write timeout — so a long-lived
MCP response is bounded by nothing the transport imposes, which is the same
property §13.3's streams rely on.

**2. The protocol comes from the official Go SDK.** *(2026-08-29)*

`github.com/modelcontextprotocol/go-sdk` v1.7.0, confined to `internal/mcp`,
which is the only package that imports it. It buys spec conformance, schema
handling and the progress-notification plumbing the wait tool wants, against
hand-rolling JSON-RPC 2.0 and streamable-HTTP framing and then tracking the MCP
spec by hand.

The issue's risk note assumed a pre-1.0 dependency; the SDK reached 1.0 before
this landed, which removes most of that risk. The version is still pinned and
its churn is still worth watching — but a breaking release is bounded to one
package by construction.

**3. Tools dispatch by replaying against the existing mux.** *(2026-08-29)*

A tool marshals its arguments into an in-process `http.Request` and serves it
against the handler `buildHandler` already assembled, then reads the response.

Parity with the route table is then **mechanical and permanent rather than
maintained**: the §13.1 body bounds, the field bounds, the validation, the `409`
+ `details.state` envelopes and `Idempotency-Key` all apply by construction,
because the same handler runs. The cost is one marshal/unmarshal round trip per
call and one place (`errorFromEnvelope`) that translates an HTTP error envelope
into an MCP error.

Dependency direction stays one-way: `internal/mcp` takes an `http.Handler` and
returns an `http.Handler` and knows nothing about `internal/api`;
`internal/api` imports `internal/mcp`, never the reverse. The handler passed is
the **inner mux**, before the auth middleware — a tool call has already been
authenticated at the endpoint it arrived on, and re-presenting a token to the
same process would be ceremony, not a check.

*Implementation note.* One thing the replay does **not** inherit is a response
bound, because HTTP never had one: a transcript or a diff can be megabytes and a
tool result lands in an agent's context. `maxToolBytes` (256 KiB) is a backstop
with an explicit truncation note pointing at the route's own `offset`/`limit`.

**4. The tool surface is the route table minus destructive admin.** *(2026-08-29)*

Five routes are deliberately not tools, and the exclusion is a **design line**
written into §13.4 as such:

    POST   /v1/daemon/stop
    POST   /v1/daemon/backup
    DELETE /v1/projects/{id}
    POST   /v1/maintenance/gc
    POST   /v1/doctor/fix

An agent must not be able to stop, garbage-collect or reconfigure the daemon
supervising it. Everything else in §13.2 is a tool, including the three the
issue left unclassified:

- `POST`/`DELETE /v1/tasks/{id}/github/pull` — link and unlink. Stated
  explicitly because it has a consequence: decision record row 27 makes a
  *human* unlink **sticky**, so the reconciler never re-applies it. An agent
  unlink therefore suppresses that link permanently.
- `POST /v1/tasks/{id}/steps/{step_id}/status` — the task 036 status message.
- `POST /v1/tasks/{id}/archive` — despite removing the worktree and possibly
  deleting an empty branch under `delete_empty_branch_on_archive`.

The two SSE routes are not tools; the wait tool replaces them for an MCP client.

The alternative was a **curated handful** — create, list, read transcript,
answer, approve. It was rejected because the line between "core" and "not" is a
guess that would be relitigated with every new endpoint, whereas "everything
except destructive admin" is a rule that answers the question for routes that do
not exist yet. The guard against drift is a parity test in `internal/api` that
fails in *both* directions: a route added later cannot be silently unexposed or
silently exposed.

**5. The wait tool keeps its caller's slot, and refuses rather than deadlocks.**
*(2026-08-29)*

`task_wait` subscribes to the event broker server-side and returns when the
target reaches a terminal or human-blocking state (`done`, `aborted`,
`archived`, `awaiting_input`, `blocked`, `awaiting_gate`). It takes a timeout
with a hard 30-minute ceiling, so a call cannot hang forever. Step transitions
are emitted as MCP progress notifications while it is open, and it is correct
and useful for a client that drops every one of them — progress is an
enhancement to the wait, never the means of delivering its result.

The design question underneath was whether a step blocked in a wait should
release its §11 slot. **It should not.** Releasing it would create a §6 state
that owns a live agent process *and* holds no slot, which no state does today:
`awaiting_input` keeps its slot precisely because its process is live (decision
record row 22), and `awaiting_children` releases it precisely because the parent
owns no process. Taking the fourth quadrant would redefine what §11's caps
bound, leave live-but-uncounted agent CLIs accumulating, and — because
`awaiting_children` re-queues on wake — let a parked task sit behind the caps
after its target had already finished, blowing past the very ceiling the wait
tool promises.

So the deadlock §7.6 designed around is prevented by **refusal**: a typed
`would_deadlock` error, immediately, when the caller is itself a running step and
the target cannot be admitted while the caller holds its slot. A silent hang
becomes an error the agent can act on, no new state is introduced, and §11 keeps
its meaning.

*Implementation note.* The check is deliberately conservative — the target is
`queued`, and the global or the target project's cap is saturated — rather than
a simulation of the scheduler. `internal/scheduler` is the only place admission
happens, and reimplementing its walk here would be a second answer to "may this
run" that drifts from the first. What this has to be right about is the one case
that hangs forever.

**6. A step's session is identified by a per-step endpoint.** *(2026-08-29)*

The refusal needs to know which task and step opened the session, so the daemon
wires each step's agent to `/mcp/step/{run_id}`, carrying a per-run secret minted
for that step run and dead when the step ends. Identity comes out of band, so the
agent does not have to cooperate, and it also scopes what a step's calls are
attributed to.

This is **not** a security boundary and must not be documented as one: a
full-auto agent can read `{data_dir}/token` and reach `/mcp` directly. It exists
to make the wait tool correct. §16 says so in those words.

*Implementation note.* Because the tool call is replayed against the mux with the
*same context* the session put the identity on, `internal/api` reads provenance
via `mcp.CreatorTaskID(ctx)` — a value no real HTTP request can carry, so there
is no header for a client to forge.

**7. Recursion is bounded by a new provenance column.** *(2026-08-29)*

A step's agent can create a task whose step runs an agent that creates a task,
and the depth is discovered at run time — so neither §7.6's
`fan_out.max_depth`/`max_tasks` nor §7.9's `include.max_depth` covers it. Both
are creation-time checks over a static snapshot.

Migration `0019_mcp_provenance.sql` adds **`created_by_task_id`**, deliberately
distinct from `parent_task_id`. Reusing `parent_task_id` was **rejected**:
`store/subtree.go` counts children by that column for the `awaiting_children`
join and `store.ListTasks`'s `ChildrenExclude` filters roots by it, so an
MCP-created task placed there would make its creator's `fan_out` step wait on a
lane it never spawned. Config gains `mcp.max_depth` and `mcp.max_tasks`,
enforced at task creation by walking the new ancestry chain with a recursive CTE,
the way `subtree.go` already walks `parent_task_id`.

**8. Each adapter carries the server its own way, and a version that cannot
fails the step.** *(2026-08-29)*

`agent.RunSpec` gains `MCP *MCPServer` (§9.1).

- **claude** — `--mcp-config` with inline JSON plus `--strict-mcp-config`, so the
  user's own MCP servers do not leak into a step. Per-run, no global state. The
  token is consequently on the command line; the §12.3 `debug` record redacts it,
  because that transcript is something people paste into issues.
- **codex** — no `--mcp-config`, but `codex exec` takes `-c key=value` dotted
  TOML overrides. Wired as `-c mcp_servers.vincent.url=…` plus
  `-c mcp_servers.vincent.bearer_token_env_var=VINCENT_MCP_TOKEN`, with the token
  through the step env. Per-run, no `config.toml` mutation, and the token stays
  out of argv.
- **cursor** — no per-run MCP flag at all. The adapter writes `.cursor/mcp.json`
  into the task worktree before `Start`, removes it after `Wait`, and passes
  `--approve-mcps`. Two consequences are handled, not assumed away: the file is
  untracked inside a git worktree, so it is visible to `git status`, the task
  diff and dirty detection while the step runs; and a daemon crash leaves it
  behind, so §12.4 recovery removes a leftover one.

An adapter or version that cannot carry an MCP server returns
`agent.ErrMCPUnsupported` from `Start`, and the engine fails the step with
`mcp_unsupported`, mirroring `ErrRestrictedUnsupported`.

**This is a deliberate departure** from CLAUDE.md's standing rule that a
capability an adapter lacks is "stated in spec §9.x and ignored at run time", and
§9.1 says so rather than letting it read as an oversight: a workflow whose prompt
depends on the vincent tools should fail loudly rather than burn an agent run
producing work premised on a channel that was never there. Task 041's
version-compatibility surface is where the gap is reported ahead of a run.

**9. Restricted mode gets the vincent tools wholesale.** *(2026-08-29)*

Claude's restricted argv was `--allowedTools
"Read,Glob,Grep,Edit,Write,MultiEdit,Bash(git:*)"`, which does not match
`mcp__vincent__*` — so a restricted step would see every vincent tool and be
denied every call. `mcp__vincent__*` is added in full.

`restricted` therefore bounds what a step does to the filesystem and the shell,
not what it does to vincent: a restricted step can create and cancel tasks. §9.4
and §16 both state this, because it is the sort of thing that is only defensible
written down.

**10. Auto-wiring is an opt-out.** *(2026-08-29)*

`mcp.wire_steps` in `config.yaml`, defaulting **on**, the way `github.enabled` is
an opt-out per task 035 decision 6. The issue's acceptance criterion — an agent
step gets the tool list with no user configuration — holds by default, and one
line turns it off.

There is deliberately no `mcp.enabled`: `/mcp` is part of the API surface the way
`/v1` is, on the same listener behind the same token, so "serving MCP" is not a
mode the daemon is in. What a user can meaningfully turn off is the wiring into
their own steps.

## Tasks

- [x] **057.1** — `internal/mcp`: the SDK dependency, the tool table generated
  from the route table, the mux-replay dispatcher, the envelope→MCP-error
  translation, and the response bound.
- [x] **057.2** — `/mcp` and `/mcp/step/{run_id}` in `internal/api`'s route
  table; the auth exemption for the per-step endpoint; `Server.Routes()` and the
  parity test that fails in both directions.
- [x] **057.3** — `task_wait`: broker subscription, the six wake states, the
  ceiling, progress notifications, and the `would_deadlock` refusal.
- [x] **057.4** — Migration `0019_mcp_provenance.sql`, `created_by_task_id`
  through the store, `MCPAncestry`/`MCPChainSize`, and the `mcp.max_depth` /
  `mcp.max_tasks` refusal at task creation.
- [x] **057.5** — `agent.RunSpec.MCP`, `agent.ErrMCPUnsupported`, and the three
  adapters; `mcp__vincent__*` in claude's restricted allow-list.
- [x] **057.6** — Engine wiring: `mcp.wire_steps`, `ReasonMCPUnsupported`, the
  debug-record redaction, and the §12.4 sweep of a leftover `.cursor/mcp.json`.
- [x] **057.7** — Documentation: §13.4 and the eleven amendments, decision row
  28, the config reference, the API page, `docs/guides/mcp.md`, the feature tour,
  and this record.
- [ ] **057.8** — `scripts/m10-gate.sh`: an end-to-end walk over a real daemon —
  list the tools, create a project and a task, wait on it through one blocking
  call, read its transcript and diff, answer an `awaiting_input` question, and
  assert the five exclusions absent. Wired into `ci.yml`'s `gates` job on all
  three platforms, committed executable via `git update-index --chmod=+x`.
- [ ] **057.9** — `cmd/fakeagent` scenario that calls back into the daemon over
  its per-step endpoint, so auto-wiring is proven end to end from a step rather
  than from the adapter's argv alone.

## Risks

- **Mux replay inherits every §13.1 bound**, including the 64 KiB ordinary body
  limit. That is the correct default, but a tool whose arguments are a workflow
  source or a prompt maps to one of the 4 MiB routes and must be checked against
  the right bound.
- **Near-full parity floods a step agent's context with ~40 tool schemas.** The
  issue weighed this against a curated list and chose the rule over the guess
  (decision 4). The schemas are kept small deliberately — path parameters plus an
  unconstrained `body`/`query` object, with the route's own handler doing the
  validating. If it still proves costly, the answer is a narrower default surface
  for *wired steps* specifically, not a different rule for external clients.
  That is recorded in §20.
