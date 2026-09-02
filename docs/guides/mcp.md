---
title: Driving vincent from an agent (MCP)
---

# Driving vincent from an agent

The daemon speaks the **Model Context Protocol** at `http://127.0.0.1:{port}/mcp`,
on the same listener as the [REST API](../reference/api.md) and behind the same
bearer token. Point any MCP client at it and it gets the whole vincent API as
tools — with discovery, argument schemas and typed errors, instead of curl and a
guess.

## Connecting

Read the port from `{data_dir}/daemon.json` and the token from
`{data_dir}/token`, then configure your client with a streamable-HTTP server:

```json
{
  "mcpServers": {
    "vincent": {
      "type": "http",
      "url": "http://127.0.0.1:7777/mcp",
      "headers": { "Authorization": "Bearer <contents of {data_dir}/token>" }
    }
  }
}
```

`vincent daemon status` prints the port. There is no TLS and no second auth
story: this is loopback, and the token is the one every other client uses.

## What the tools are

The tool list **is** the route table: projects, workflows, `resolve`, tasks and
every human action (`task_cancel`, `task_pause`, `task_approve`, `task_answer`,
`task_archive`, `task_follow_up`, …), steps, step status, transcripts, diffs,
the GitHub reads, and the read-only `health`, `info`, `config_get`,
`agent_list`, `doctor`, `orphan_list`.

Ten routes are deliberately **not** tools:

- `POST /v1/daemon/stop`
- `POST /v1/agents/{name}/quota`
- `POST /v1/daemon/backup`
- `DELETE /v1/projects/{id}`
- `POST /v1/maintenance/gc`
- `POST /v1/doctor/fix`
- `PATCH /v1/config`
- `POST /v1/workflows`
- `PATCH /v1/workflows`
- `POST /v1/tasks/{id}/github/pull/create`

An agent should not be able to stop, garbage-collect or reconfigure the daemon
that is supervising it. Those stay CLI-and-curl only. The configuration one is
the sharpest of them: a patch can change the argv the daemon spawns
(`notify.command`, `agents.*.path`), what its children inherit (`environment`),
and whether steps are wired to MCP at all (`mcp.wire_steps` — a step could
otherwise switch these tools off for everyone). The two workflow writes are the
same line drawn one step out: a workflow file is what the daemon runs, so an
agent must not edit one. Nothing regresses — the built-in `create-workflow`
workflow writes its deliverable through the filesystem, not through this API —
and the read side is untouched: `workflow_list`, `workflow_definition`,
`workflow_validate` and `workflow_schema` are all ordinary tools.

The quota push is the same line drawn around a *fact* rather than an action: a
step that could report its own adapter at 99% would paint every board and status
line in the installation with a wall that does not exist, and nothing
downstream could tell that from the real thing — the
[reading carries a source, not a caller](../reference/api.md#usage-quota).
Nothing is lost, because the two things that push are a status line and an
app-server probe, neither of which is an agent step reaching for a tool.

The last one is the [one route that writes to GitHub](../features.md#open-a-pull-request):
it pushes a task's branch and opens its pull request, and the only thing gating
it is a human pressing the key — no config switch, no confirmation the daemon
can check. An agent-callable version would be consent nobody gave. Nothing is
lost: an agent that wants a pull request has a shell in its own worktree and
runs `git push` and `gh pr create` there.

`config_get` is the one tool whose body differs from its route's. The HTTP
response serves `config.yaml` in full; the tool masks `environment.set`'s
values and `notify.command`'s argv, keeping the names, because a tool result
lands in the model's context and in the step's transcript. Nothing else is
changed — see the [security model](../security-model.md).

**Nor is any `/v1/chats` route** — the whole
[chat](../reference/api.md#chats) family, reads included. A chat turn starts an
agent CLI without going through admission, so a tool that could send one would
let an agent start unqueued agent processes, which is the exact thing
`max_tasks` below bounds. The recursion bounds cannot help either: they walk
`created_by_task_id`, and a chat is not in that chain. Nothing is lost — an
agent calling these tools already has a session of its own.

Each tool takes the route's path parameters by name, plus `body` (for `POST` and
`PATCH`) or `query` (for `GET`):

```json
{ "name": "task_create",
  "arguments": { "body": { "project_id": 1, "workflow": "adhoc", "prompt": "fix the flaky test" } } }
```

```json
{ "name": "task_transcript",
  "arguments": { "id": 12, "run_id": "34", "query": { "format": "normalized", "limit": "200" } } }
```

`task_create` also takes an optional `idempotency_key`, which becomes the
`Idempotency-Key` header: re-sending the same arguments with the same key
returns the original task instead of creating a second one. A tool call has no
header surface, so it is an argument.

Errors come back as the API's own envelope, so a `409` still carries
`details.state` and you can branch on it rather than reading prose. One result is
capped at 256 KiB, with a note saying so — page with the route's own
`offset`/`limit`.

## Following a run

Task runs take minutes, so polling burns turns. `task_wait` blocks until a task
reaches a terminal or human-blocking state:

```json
{ "name": "task_wait", "arguments": { "task_id": 12, "timeout_seconds": 600 } }
```

It returns when the task is `done`, `aborted`, `archived`, `awaiting_input`,
`blocked` or `awaiting_gate`, or when the timeout elapses — default 5 minutes,
**hard ceiling 30**, so a call cannot hang forever. Check `woke` rather than
`state` to tell a wake from a timeout: a timed-out wait truthfully reports a task
that is still running.

Step transitions arrive as MCP progress notifications while the call is open, but
the result is complete without them. If your client drops every notification you
lose nothing but the live commentary.

## Your own steps get this too

By default the daemon registers the endpoint with the agent CLI it spawns for an
[agent step](workflows.md), so a step's agent has the vincent tools with no
configuration from you — it can file follow-up work, read a sibling lane's
transcript, or answer a gate. Each step gets its own endpoint, with a secret that
dies when the step does.

Turn it off with one line:

```yaml
mcp:
  wire_steps: false
```

Two things to know when it is on:

- **A step waiting on another task will be refused, not hung**, if the task it is
  waiting for cannot start while the step holds its concurrency slot.
  `task_wait` returns a `would_deadlock` error immediately. Raise
  `max_parallel_tasks` or wait on something already running.
- **Cursor steps get a `.cursor/mcp.json` in the task worktree** for the duration
  of the run, because `cursor-agent` has no per-run MCP flag. It is untracked, so
  it is visible in `git status` and in the task diff while the step runs, and it
  is removed when the step ends. Your global `~/.cursor/mcp.json` is never
  touched.

An adapter that cannot carry an MCP server fails the step with `mcp_unsupported`
rather than running an agent that silently has no tools. That is deliberate: a
prompt written against the vincent tools should fail loudly. Turn `wire_steps`
off if you would rather it ran anyway.

## Bounds

A step's agent can create a task whose step runs an agent that creates a task.
`mcp.max_depth` (default 3) and `mcp.max_tasks` (default 32) bound that chain,
enforced when the task is created; the refusal names the key it hit. See the
[configuration reference](../reference/configuration.md#mcp).

## What this is not

The per-step endpoint is **not a sandbox**. A full-auto agent can read the daemon
token and reach `/mcp` directly, and `permission_mode: restricted` bounds the
filesystem and the shell — not what a step does to vincent. A restricted step can
create and cancel tasks. See the [security model](../security-model.md).

## See also

- [HTTP API](../reference/api.md) — the routes the tools are.
- [Configuration](../reference/configuration.md#mcp) — `mcp:`.
- [Security model](../security-model.md).
