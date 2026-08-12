# Scripting vincent

Everything the TUI does is a subcommand, and everything either does is the same
localhost API. That makes vincent scriptable three ways, in increasing order of
control: the CLI with `--json`, the API with `curl`, and the SSE streams for
anything that needs to react rather than poll.

- [Exit codes](#exit-codes)
- [JSON output](#json-output)
- [Validating workflows in CI](#validating-workflows-in-ci)
- [Talking to the API directly](#talking-to-the-api-directly)
- [Reacting to events](#reacting-to-events)
- [A worked example](#a-worked-example)
- [Things worth knowing](#things-worth-knowing)

---

## Exit codes

Every subcommand uses the same three:

| Code | Meaning | What a script should do |
|---|---|---|
| `0` | Success | Continue |
| `1` | The daemon answered and rejected the request | Fix the request — a bad id, an action the task's state does not allow |
| `2` | No daemon answered | Start one (`vincent daemon start`) and retry |

That split is the point: a script can tell "start the daemon" from "fix your
request" without parsing stderr.

```sh
vincent task ls --json > tasks.json
case $? in
  0) ;;
  1) echo "request rejected"  >&2; exit 1 ;;
  2) vincent daemon start && vincent task ls --json > tasks.json ;;
esac
```

**The subcommands never auto-start a daemon.** Only the TUI does, because that
is an interactive session you asked for. A subcommand that silently spawned a
background process would be a surprise in a CI job.

`vincent daemon status` is the exception worth memorizing separately: `0`
healthy, `1` not running, `2` running but unresponsive.

## JSON output

`--json` works on every subcommand that prints anything:

```sh
vincent project ls --json
vincent task ls --state running --json
vincent task show 7 --json
vincent workflow ls --project 1 --json
```

Two guarantees make it safe to pipe into `jq`:

- An empty result is `[]`, never `null`.
- Advisory warnings go to **stderr**, so `--json` on stdout stays clean. A task
  created with a model that is not in any catalog still exits 0, prints its JSON,
  and warns on stderr — because the task exists and will run.

```sh
vincent task ls --state blocked --json | jq -r '.[] | "\(.id)\t\(.title)"'
```

## Validating workflows in CI

`vincent workflow validate` runs **entirely locally**: no daemon, no network, no
agent CLI installed. It parses the file and checks it against the built-in
adapter catalogs. That makes it the one command safe to put in a pre-commit hook
or a CI job on a machine that has never seen an agent.

```sh
for f in .vincent/workflows/*.yaml; do
  vincent workflow validate "$f" || exit 1
done
```

Exit `0` is valid, `1` is invalid. Warnings — a model no catalog knows, for
instance — are printed but do not fail the command, because the CLI is the final
authority on what your account can run.

## Talking to the API directly

The daemon serves REST + SSE on loopback. Two files in your
[data directory](../reference/files.md) are all a client needs:

```sh
DATA_DIR=${VINCENT_DATA_DIR:-$HOME/.local/share/vincent}      # Linux; see the table
PORT=$(jq -r .port  "$DATA_DIR/daemon.json")
TOKEN=$(cat "$DATA_DIR/token")

curl -s -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:$PORT/v1/tasks?state=running" | jq
```

- `daemon.json` carries `{ port, pid, started_at }` and is written atomically at
  startup, removed on graceful shutdown.
- `token` is created `0600` at first start. On Windows it relies on the per-user
  ACL of `%LOCALAPPDATA%`.
- `GET /v1/health` is the one unauthenticated endpoint.

The full endpoint list is in the [API reference](../reference/api.md).

Creating a task:

```sh
curl -s -X POST "http://127.0.0.1:$PORT/v1/tasks" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"project_id":1,"workflow":"feature-pr","title":"Add a health endpoint"}' | jq
```

Errors come back in a stable envelope with `snake_case` codes:

```json
{ "error": { "code": "invalid_state",
             "message": "task 7 is running, not queued",
             "details": { "state": "running" } } }
```

`details` is there so a client branches on a value instead of parsing prose. An
invalid state transition is always `409`, with `details.state` set to the state
actually found.

## Reacting to events

Polling works, but the daemon will tell you instead.

```sh
curl -N -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:$PORT/v1/events?types=task.state_changed"
```

- **State events are durable.** They are persisted with a monotonic id, so a
  client that reconnects with `Last-Event-ID: <n>` resumes without gaps.
- **A connection without `Last-Event-ID` starts live at the next event.** The
  stream never replays history unasked: catch-up is a REST snapshot first, then
  the stream.
- `?types=` and `?project_id=` filter it.

`GET /v1/tasks/{id}/events` adds that task's **live output** — agent output, tool
calls, reasoning, usage, command output. Those chunks are ephemeral: they are not
in the events table, because the transcript file is their durable copy. A slow
subscriber has output chunks dropped rather than the whole run stalled; durable
state events disconnect the subscriber instead, so it reconnects and resumes.

To read a transcript instead of following one:

```sh
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:$PORT/v1/tasks/7/steps/12/transcript?format=normalized&tail=65536"
```

`format=normalized` maps each raw line through the owning adapter's parser into
one common shape, which is the same rendering the TUI uses for both live tail and
scrollback. Omit it for the raw file, byte for byte. `X-Next-Offset` on the
response is where a follow-up fetch resumes — always on a record boundary, never
mid-line.

## A worked example

Create a task, wait for it to leave `running`, and report what happened:

```sh
#!/usr/bin/env bash
set -euo pipefail

id=$(vincent task add --project 1 --workflow feature-pr \
       --title "$1" --json | jq -r .id)

while :; do
  state=$(vincent task show "$id" --json | jq -r .state)
  case "$state" in
    done)                     echo "✓ task $id done";        exit 0 ;;
    blocked|aborted)          echo "✗ task $id $state" >&2;  exit 1 ;;
    awaiting_gate)            echo "task $id needs approval"; exit 0 ;;
    *)                        sleep 5 ;;
  esac
done
```

For anything longer-lived, replace the poll loop with the `/v1/events` stream —
the states are the same, the latency is not.

## Things worth knowing

- **The daemon is the only writer.** A script must never touch
  `{data_dir}/vincent.db`, a worktree, or an agent process directly. Everything
  goes through the API, which is what keeps concurrency correct.
- **Nothing pushes unless a step pushes.** A script that creates tasks is not a
  script that publishes anything; that is still whatever your workflow's
  `command` steps do, after whatever gates you put in front of them.
- **`vincent workflow validate` is the only command that works with no daemon.**
  Everything else exits 2.
- **The API is versioned by path** (`/v1`) and changes additively within a
  version, so a client written against it keeps working.

---

## See also

- [CLI reference](../reference/cli.md) — every command and flag.
- [HTTP API reference](../reference/api.md) — every endpoint and event type.
- [Running at login](running-at-login.md) — so there is always a daemon to talk
  to.
