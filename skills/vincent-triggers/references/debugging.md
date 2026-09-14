# Debugging a trigger

Work outward, one question at a time:

1. Is the trigger armed?
2. Is its source healthy?
3. What did the ledger record for the event?
4. What happens when you reproduce the event without firing?

None of these steps requires turning a trigger on. Do not flip `enabled`,
`on_fire`, `permission` or `triggers.enabled` to "see if it works". Change one
only when the user asks for that exact change. The repository's
`docs/guides/triggers.md` is authoritative if vincent has evolved since this
skill was installed.

## 1. Is it armed?

Two sources answer this, depending on whether the daemon is running:

- **With the daemon:** the `trigger_list` MCP tool (`GET /v1/triggers`) returns
  the triggers `dir` and, for each file:
  - `valid` and `errors`
  - `enabled`, `armed` and `disarmed_reason`
  - `source_type`, `action_type`, `project_id`, `on_fire` and `permission`
  - `poll`, holding `seeded`, `last_poll_at`, `ok`, `error` and `last_fire_at`
- **Without the daemon:**
  - `vincent trigger validate <file>` exits 0 for a valid file and 1 otherwise.
  - `vincent trigger ls --project <id> --json` shows each file's id, version,
    validity, `enabled`, `on_fire`, `permission` and errors.

`disarmed_reason` names the first switch that is off:

| Reason | What to do |
|---|---|
| `the trigger file does not validate` | Fix the errors `validate` reports. The file keeps its cursor while invalid |
| `the trigger is disabled` | This is the safe default. Tell the user a human sets `enabled: true`, in the TUI (which asks first) or in an editor |
| `triggers.enabled is off in config.yaml` | The global switch. A human turns it on |

Arming seeds. The first poll after arming records what the source already
shows and fires nothing. "I turned it on and nothing happened" is therefore
correct behaviour for events that existed before arming. Disarming drops the
cursor, so each re-arm seeds again, and events from the off period never fire.

## 2. Is the source healthy?

`poll.ok: false` comes with `poll.error`. What it means depends on the source.

**`command`:**

- **Non-zero exit.** The error carries the exit status and the tail of stderr,
  and the full stderr is in `vincent daemon logs`.
- **Timeout.** The one-minute limit killed the process tree.
- **Stdout over 8 MiB.**
- **The argv could not start.** Common causes:
  - a relative path,
  - a POSIX script that is not executable or has no shebang line,
  - shell syntax such as `|` or `$VAR` inside `command:`,
  - a `.ps1` named without `pwsh -NoProfile -File`.
- **A missing credential.** The variable is absent from the *daemon's*
  environment. The daemon may have been started as a service, or
  `environment.inherit` may filter the variable out.

**`github_issues` / `github_prs`:**

- **Configuration.** `github.enabled` is off, `github.poll_interval` is `0`, or
  the project's `origin` is not a github.com repository.
- **The listing failed.** Authentication or the rate limit are the usual
  causes.

**`http`:** An `http` source has no poll. Its failures surface as the status a
push receives:

- **`401`.** The bearer token is bad, the signature is bad, or the `secret_env`
  variable is unset in the daemon. No ledger row is written.
- **`409`.** The trigger is not armed. `details.reason` says why.
- **`400`.** The source is not `http`, the body is not a JSON object, or the
  event has no string `id` and no `X-GitHub-Delivery` header.
- **`404` and `413`.** An unknown id, or a body over 4 MiB.

## 3. Read the ledger

The `trigger_deliveries` MCP tool (`GET /v1/triggers/{id}/deliveries?limit=`)
returns rows newest first, 100 by default and up to 1000.

- **Fields.** Each row has `event_id`, `dedupe_key`, `outcome`, `task_id`,
  `detail` and `created_at`.
- **Lifetime.** The ledger outlives the trigger file and is pruned after 30
  days.
- **Viewing it.** In the TUI Triggers view, `tab` opens the ledger and `enter`
  opens a delivery's task.

| Outcome | Meaning | Look at |
|---|---|---|
| `seeded` | A command source's first poll after arming saw the event. Nothing fired, and the key now counts as delivered | Expected after arming or re-arming. GitHub sources seed a snapshot and write no `seeded` rows |
| `fired` | The replayed route accepted the action. `task_id` is the task created or acted on | Under `propose` the task is `paused` and waits for a human to resume it |
| `deduped` | The dedupe key had already `fired` or been `seeded` | Is the key too coarse? Did the seed cover this event? Do not fix it by changing `dedupe_key` on a live trigger, which would re-fire every delivered event |
| `filtered` | `match:`, `allowed_actors` or `if:` dropped the event | `vincent trigger test` names the stage. `match:` compares values as text, with list semantics, and treats a missing path as a miss. `allowed_actors` matches the item's author, not who acted |
| `rate_limited` | `limits.max_per_hour` fired deliveries were already reached in the trailing hour | The event was dropped, not queued. It fires later only if the source reports it again |
| `refused` | The route answered 4xx, or a reaction found no task on the branch | `detail`. Typical causes: a workflow the project lacks; a state the action does not allow, such as cancelling a `done` task or retrying one that is not `blocked`; a clamped task an adapter cannot run restricted; a bad issue or pull number; no task on the rendered branch |
| `error` | A template did not render, or the route answered 5xx or was unreachable | `detail`. Typical causes: `missingkey=error` on a key the event lacks; a `dedupe_key` or `branch` that renders empty; an `if:` that renders neither `true` nor `false` |

When an event has **no row at all**, check these in turn:

- the trigger was not armed, or its source is failing (steps 1 and 2);
- a command output line was not a JSON object with a string `id`, so it was
  logged and skipped;
- more than 20 events arrived in one poll, and those past 20 were dropped with
  a warning in the log;
- a seed event's key did not render, which is logged at warn;
- an `http` push was refused before judging, for example with a `401`;
- the command's own filter or cursor never returned the event.

`vincent daemon logs` shows what was logged.

## 4. Reproduce without firing

Neither dry run writes a task, a ledger row, a cursor, a poll-health change or
an event. Both work while the trigger or `triggers.enabled` is off.

**`vincent trigger test <id> --event event.json [--json]`**, also available as
the `trigger_test` MCP tool with body `{event}`:

- **Requirements.** It needs a running daemon, a trigger already loaded under
  that id, and a fixture holding one JSON object (`-` reads stdin).
- **Output.** It prints each stage in order:
  - the match result, with the path that missed,
  - the `if:` verdict and its rendered text,
  - the dedupe key, and whether the ledger already holds it,
  - the action's method and path, the target branch and task, and the request
    body,
  - the outcome. A `fired` here means "would be replayed".
- **Exit status.** It exits 1 when the outcome is `error` or the daemon
  refused the request.
- **Fixtures.** Shape the fixture like the source's real events. A GitHub
  event has `action`, `author` and `Issue` or `Pull`. A command event is one
  line of the script's output.

**The `trigger_poll` MCP tool** (`POST /v1/triggers/{id}/poll`) runs the source
once for real and judges everything it returns.

- **Answer.** It holds these fields:
  - `seed`: a real poll now would only seed.
  - `events`: one judgement per event.
  - `truncated`: events past the cap of 20.
  - `refused`: output lines that were not events.
  - `cursor`: the cursor the command printed.
  - `error`: set when the command or listing failed.
- **Status.** A failing poll still answers `200`, and an `http` trigger
  answers `400`.
- **GitHub before seeding.** A GitHub trigger that has not seeded has no
  snapshot to diff, so `events` comes back empty.
- **Side effects.** The command really runs, so anything it does outside
  vincent happens.

The TUI Triggers view has the same two dry runs: `T` judges a sample event and
`X` polls once.

## 5. Fix safely

- **Edit the file.** The daemon reloads it on save. Re-run
  `vincent trigger validate`, then the dry run that showed the problem.
- **Keep identity fixed.** Leave `id`, the file name and `source.project`
  alone. A new id is a new cursor and a new ledger history.
- **Never re-key delivered events.** Keep what `dedupe_key` renders for events
  already delivered.
- **Report what a human must do.** If the fix needs a dangerous switch
  (`enabled: true`, `on_fire: create`, `permission: workflow` or
  `triggers.enabled`), say what it would change and leave it to the user unless
  they asked for exactly that.
