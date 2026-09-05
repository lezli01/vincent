# Scripting vincent

Everything the TUI does is a subcommand, and everything either does is the same
localhost API. That includes the actions that act on a *live* task — approve,
reject, retry, repair, skip, pause, resume, answer, archive — so a blocked task
can be rescued from a shell loop, a cron job or an SSH session with no usable
terminal. The [CLI reference](../reference/cli.md) is the full tree.

The shape is always the same: one id per invocation, `--json` if you want the
task back as data, and the daemon's own post-action view of the task printed
rather than a guess about where it went.

```sh
vincent task ls --state blocked --json |
  jq -r '.[].id' |
  while read -r id; do
    vincent task retry "$id"
  done
```

That loop is the answer to the failure this guide exists for. An agent
credential expires, every task that reaches an agent step fails its retry budget
and blocks on `agent_unauthenticated`, and no amount of waiting helps: someone
has to fix the login and then move each task. (`usage_limit` is deliberately
*not* this case — it is a queued reason, never a block reason, and the scheduler
re-admits on its own.)

The same loop with a different verb does the other batches — archiving a day's
finished work, or running one more command in each of several finished tasks'
own worktrees with
[`task follow-up`](../reference/cli.md#vincent-task-follow-up):

```sh
vincent task ls --state done --json |
  jq -r '.[].id' |
  while read -r id; do
    vincent task follow-up "$id" --run 'git rebase origin/main'
  done
```

That makes vincent scriptable three ways, in increasing order of control: the
CLI with `--json`, the API with `curl`, and the SSE streams for anything that
needs to react rather than poll.

- [Exit codes](#exit-codes)
- [JSON output](#json-output)
- [Supplying task fields](#supplying-task-fields)
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
| `1` | The request was rejected — by the daemon, or by the client before it sent one | Fix the request — a bad id, an action the task's state does not allow, a `--fields-file` that is not one JSON object of strings |
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

`vincent doctor` overloads them the same way — `0` healthy, `1` problems found,
`2` no daemon answered — and is the one subcommand that still prints its whole
report on exit `2`, because "no daemon" is one of the answers it exists to give:

```sh
vincent doctor --json > doctor.json   # written whether or not a daemon answered
case $? in
  0) ;;
  1) jq -r '.problems[] | "\(.group)\t\(.message)"' doctor.json >&2; exit 1 ;;
  2) vincent daemon start ;;
esac
```

[`vincent update`](../reference/cli.md#vincent-update) overloads them too, and
differently: with `--check`, `2` means an update **is available** and `1` that
the check itself failed; without it, `2` means an update exists but a package
manager owns this install, and `1` that verification or the swap failed and the
binary was left untouched. Neither `2` means "no daemon" — the command never
contacts one.

What sets exit `1` is a **closed set**: `config.yaml` exists and does not parse,
the daemon is alive but not answering, `PRAGMA integrity_check` is not `ok`, the
database is at a schema version this binary does not understand, orphaned
worktrees are present, or a task is unreconciled — `queued` (or finished) while
one of its step runs is still marked `running`, which is crash recovery having
failed to close the previous attempt. A `fan_out` step's own row is not counted:
a parent waiting on its lanes is `queued` with that round's row open on purpose. A missing or logged-out agent CLI is
reported and does *not* set the exit code — most machines have one of three
adapters installed, so a doctor that exited `1` almost everywhere would be no
use here. Neither do task *counts*: twelve blocked tasks is information, not a
defect.

## JSON output

`--json` works on every subcommand that prints anything, with one exception
noted below:

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

[`vincent chat`](../reference/cli.md#vincent-chat) carries `--json` on every
subcommand too. `chat send --json` emits the finished turn, so a script reads
the answer and the accounting off one object and still exits 1 when the turn
failed.

`vincent task show <id> --json` carries two fields worth knowing about:
`available_actions`, which is what the daemon will accept right now — read it
instead of probing for `409`s, and every name in it has a `vincent task <action>`
subcommand once `_` is replaced with `-` (the API says `follow_up`, the tree says
`follow-up`) — and `pending_input`, the §7.4 request an `awaiting_input` task is
parked on. The human rendering numbers those questions, and
`vincent task answer <id> --answer <n>=<value>` takes the numbers.

## Supplying task fields

A workflow reads its inputs from `.Task.Fields`, an open `map[string]string`
supplied when the task is created (see
[task fields](workflows.md#54-task-fields)). Two flags fill it.

For a handful of short values, repeat `--field`:

```sh
vincent task add --project 1 --workflow release --title "Release 2.0" \
  --field ticket=OPS-42 --field owner=ana
```

Everything after the **first** `=` is the value, so URLs and regexes need no
escaping, and a repeated name takes its last value.

For generated input — or anything with newlines, quotes or spaces — pass a JSON
object of strings instead. `--fields-file -` reads it from stdin, which is what
makes `jq` the natural producer:

```sh
jq -n --arg ticket "$TICKET" --arg notes "$(cat release-notes.md)" \
     '{ticket: $ticket, notes: $notes}' |
  vincent task add --project 1 --workflow release --title "Release 2.0" \
    --fields-file - --json
```

The two combine, and **`--field` wins the names it names**. That is what lets a
script keep one generated document and vary a single input per run:

```sh
for ticket in OPS-42 OPS-43; do
  vincent task add --project 1 --workflow release --title "Release $ticket" \
    --fields-file ./base-inputs.json --field "ticket=$ticket" --json
done
```

Everything checked locally is checked before the daemon is called, and exits
`1` like any other rejected request: a value that is not a JSON string (the
message names the key, never the value), an empty name, anything after the
first JSON object, and a document over 4 MiB — the API's own body bound,
applied to the read so a pipe cannot be unbounded.

Everything else stays **daemon-authoritative**, because the CLI is not the only
client: required fields, `type`, `pattern`, and the per-field size bounds are
checked by `POST /v1/tasks` and reported through the same exit `1`. Names the
workflow never declared are still accepted — declaring `fields:` does not close
the map — so a script may attach its own metadata to a task without touching
the workflow.

Without `--json`, the created task is confirmed with the field **names and a
count and no values**:

```
task 62 created: Release OPS-42 (release, branch vincent/62-release-ops-42)
  fields: notes, ticket (2)
```

That line is safe to leave in a CI log, and it still catches the mistake worth
catching — a name typed wrong, which a count alone would hide.

## Reading transcripts and the daemon log

`vincent task transcript <id>` prints one attempt's transcript. Its `--json` is
**NDJSON in vincent's own vocabulary** — one normalized record per line,
including vincent's `vincent.*` annotations — so `jq` reads it a line at a time:

```sh
# every tool the last attempt ran
vincent task transcript 7 --json | jq -r 'select(.type == "agent.tool_use") | .tools[].name'

# what a specific attempt's step said on stderr
vincent task transcript 7 --step 12 --json |
  jq -r 'select(.stream == "stderr") | .text'
```

`--raw` is the other machine route: the agent's own JSONL, byte for byte, for
when you want the dialect rather than vincent's reading of it. Without either
flag the records are rendered as text for a human, with a command step's stderr
tagged `[stderr]` on stdout — stdout carries the transcript, stderr carries the
command's own diagnostics.

The daemon log has a command too, and it is the one that still works when the
daemon does not — it reads `{data_dir}/logs/daemon.log` off disk and never calls
the API:

```sh
vincent daemon logs -n 200
vincent daemon logs -f | grep -i error
```

## Validating workflows in CI

`vincent workflow validate` runs **entirely locally**: no daemon, no network, no
agent CLI installed. It parses the file and checks it against the built-in
adapter catalogs. `vincent workflow render` is local in the same way and goes
one step further: it *executes* every template the file declares, which is where
a typo'd `{{.Task.Titel}}` or a `.Task.Fields` key nothing supplies surfaces.
Both are safe in a pre-commit hook or a CI job on a machine that has never seen
an agent.

```sh
for f in .vincent/workflows/*.yaml; do
  vincent workflow validate "$f" || exit 1
  vincent workflow render   "$f" || exit 1
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
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"project_id":1,"workflow":"feature-pr","title":"Add a health endpoint"}' | jq
```

`Idempotency-Key` is optional, and this is the one request worth setting it on.
Creating a task inserts a row, claims a branch and wakes the scheduler, so a
create that commits and then loses its response — a timeout, a dropped
connection, a script killed mid-`curl` — makes a second task, a second worktree
and a second agent run on the same repository when the script re-sends it. Under
a key, re-sending the same body returns the task the first send created;
re-sending a *different* body under that key is a `409` rather than a wrong
answer. Keys last 24 hours, no other route needs one, and without the header
nothing changes — two identical sends make two tasks. The rules are in
[Replaying a create](../reference/api.md#replaying-a-create).

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

### Being told without holding a connection open

A long-lived SSE reader is the right tool for a dashboard and the wrong one for
"text me when something blocks". For that, let the daemon spawn your script:

```yaml
# config.yaml
notify:
  on: [blocked, awaiting_gate, awaiting_input]
  command: ["/usr/local/bin/vincent-notify"]
```

The daemon runs that command on every matching transition and writes a JSON
envelope — task id and title, `from`/`to`, `block_reason`, project, workflow,
step cursor, branch, worktree path, and the agent's question on
`awaiting_input` — to its standard input. No token, no cursor, no reconnect
loop, and nothing to keep alive: the daemon is already the process with the
supervised lifetime.

```sh
#!/bin/sh
# /usr/local/bin/vincent-notify
jq -r '"vincent: #\(.task_id) \(.title) → \(.to) \(.block_reason)"' \
  | xargs -I{} curl -fsS -XPOST -d "text={}" "$SLACK_WEBHOOK"
```

`command` is argv, not a shell line, and there is a fixed 10-second budget per
run. See [`notify`](../reference/configuration.md#notify) for the full envelope
and the delivery guarantees, and the
[security model](../security-model.md) for what it means that the daemon runs
it as you.

## A worked example

Create a task, wait for it to leave `running`, and report what happened:

```sh
#!/usr/bin/env bash
set -euo pipefail

id=$(vincent task add --project 1 --workflow feature-pr \
       --title "$1" --json | jq -r .id)
retried=0

while :; do
  state=$(vincent task show "$id" --json | jq -r .state)
  case "$state" in
    done)          echo "✓ task $id done";                  exit 0 ;;
    aborted)       echo "✗ task $id aborted" >&2;           exit 1 ;;
    awaiting_gate) vincent task approve "$id" >/dev/null ;;
    blocked)
      # One retry, then hand it to a human. A script that retries a
      # blocked task forever burns tokens on the same failure.
      if [ "$retried" -eq 0 ]; then
        retried=1
        vincent task retry "$id" >/dev/null
      else
        echo "✗ task $id blocked: $(vincent task show "$id" --json | jq -r .block_reason)" >&2
        exit 1
      fi
      ;;
    *) sleep 5 ;;
  esac
done
```

Approving from a script is a real decision, not a formality: a `manual` gate is
there because someone wanted a person to look. Approve unattended only where the
gate is a pacing device rather than a review.

For anything longer-lived, replace the poll loop with the `/v1/events` stream —
the states are the same, the latency is not.

## Things worth knowing

- **The daemon is the only writer.** A script must never touch
  `{data_dir}/vincent.db`, a worktree, or an agent process directly. Everything
  goes through the API, which is what keeps concurrency correct.
- **Nothing pushes unless a step pushes.** A script that creates tasks is not a
  script that publishes anything; that is still whatever your workflow's
  `command` steps do, after whatever gates you put in front of them.
- **Three commands work with no daemon: `vincent workflow validate`,
  `vincent workflow init` and `vincent daemon restore`.** Everything else exits
  2. Validate never wants one; `init` wants one only for `--project`, to resolve
  the id to a repository; restore *requires* the daemon to be stopped, since it
  replaces the files a running daemon has open.
- **The API is versioned by path** (`/v1`) and changes additively within a
  version, so a client written against it keeps working.

---

## See also

- [CLI reference](../reference/cli.md) — every command and flag.
- [HTTP API reference](../reference/api.md) — every endpoint and event type.
- [Driving vincent from an agent](mcp.md) — the same API as MCP tools, for when
  the script is an AI agent.
- [Running at login](running-at-login.md) — so there is always a daemon to talk
  to.
