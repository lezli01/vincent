# Poll scripts for `type: command`

A `type: command` trigger is the whole integration with a system vincent has no
support for: the daemon runs your argv every `poll_interval`, reads events from
its stdout, and knows nothing about the vendor. This is the contract that argv
must keep. The repository's `docs/guides/triggers.md` is authoritative if
vincent has evolved since this skill was installed.

## Where the script lives

- **`{config_dir}/trigger-scripts/<name>`.** This is a convention, not a path
  the daemon enforces. It sits *beside* `{config_dir}/triggers/`, never inside
  it, so a script never shares a directory with the files the registry
  watches.
- **Never in a repository.** A script a repository carries can be changed by
  anyone with merge rights, and it runs as you on every poll. That is the same
  supply-chain hole that makes triggers global-only.
- **POSIX:** owner-only. `chmod 700` the directory and the script, give the
  script a shebang line, and name it by absolute path in `command:`.
- **Windows:** a `.ps1` file is not an executable, and nothing runs the argv
  through a shell, so name the interpreter yourself:
  `command: [pwsh, -NoProfile, -File, <absolute path>.ps1]`.
- **No shell.** `command:` is executed directly. `|`, `&&`, `>`, `$VAR`, `~`
  and globs are passed as literal arguments, never interpreted. A pipeline goes
  inside the script.

## Credentials

A credential comes from the **daemon's inherited environment**, never from the
script, the trigger file or an argv:

- The script reads `$JIRA_API_TOKEN` (or the vendor's equivalent) at run time.
  The variable must be set in the environment the daemon started with, and the
  `environment:` policy in `config.yaml` (`inherit`, `unset`, `set`) must let it
  through. A daemon running as a login service does not inherit the shell that
  installed it.
- `environment.set` is not a secret store. It holds literal values in a plain
  file.
- Keep secrets out of every argv, including the argv of a child the script
  starts. Process listings show argv. Pass a secret on stdin or in a header
  read from stdin (`curl -H @-`), not as `-u user:token`.
- An authenticated CLI or an OS credential store the script calls is an equally
  good source.
- Never echo a credential. Stderr goes to the daemon log.

## The contract

**Stdout is NDJSON, one event per line.**

- Each event line is a JSON object with a **string `id`**. The whole object
  becomes `.Event`, verbatim, with no normalization, so a Jira event carries
  Jira's field names.
- `id` is the event's identity, and the trigger's default `dedupe_key`. Make it
  change when the event is genuinely new, for example by including the item's
  update time. If "once" means something coarser, such as once per ticket, set
  the trigger's `dedupe_key` rather than weakening the id.
- Blank lines are skipped. A line that is not an object, or has no string
  `id`, is logged and skipped. A dry-run poll reports how many there were as
  `refused`.
- Emit identifiers as strings. A JSON number reaches `.Event` as a float, so a
  template needs `{{ printf "%.0f" .Event.number }}` to render it as digits.

**An optional last line** holds only `{"cursor": "<string>"}`.

- The daemon stores the string and hands it back on the next run in
  `$VINCENT_TRIGGER_CURSOR`. It is empty on the first run after arming.
- The string is opaque to vincent. It can be a timestamp, an action id or a
  build number.
- A run that prints no cursor line leaves the stored one unchanged. A seed run
  with no cursor line stores `""`.

**The exit status is the health signal.**

- Exit 0 with no event lines means "no events".
- A failed fetch must exit **non-zero**. The poll then counts as failed, the
  cursor does not advance, and the trigger's poll status reads failing with the
  exit status and the tail of stderr. A script that swallows an HTTP error and
  exits 0 hides the outage as silence.
- Send diagnostics to **stderr**, which goes to the daemon log. Stdout is
  reserved for events.

**Fixed bounds.**

- A poll gets a one-minute timeout, after which its whole process tree is
  killed.
- Stdout over 8 MiB fails the poll rather than being cut short.
- After the seed, at most **20 events** of one poll are judged. Events past
  that are dropped, not deferred, and a cursor that moved past them will not
  show them again. Keep each page small, the query narrow, and the interval
  short enough that a normal poll stays under 20.

**Seeding.** The first poll after arming records one `seeded` ledger row per
event it printed, keyed by the rendered `dedupe_key`, and fires nothing. So the
first run may safely return a wide window: nothing in it can start work.

## A Jira-style example

The poller below prints Jira issues in "Ready for Dev". Its cursor is the Unix
time at which the previous run started, which keeps it independent of time
zones. Each run looks back to that moment plus two minutes of overlap. The
overlap is harmless, because a re-seen issue carries the same id and is
`deduped`.

It needs `JIRA_BASE`, `JIRA_USER` and `JIRA_API_TOKEN` in the daemon's
environment. Adjust the endpoint and JQL to your instance: Jira Cloud's API v3
returns `description` as a document object rather than text, which is why the
example fetches only `summary` and `updated`.

### POSIX (`sh`, `curl`, `jq`)

```sh
#!/bin/sh
# {config_dir}/trigger-scripts/jira-ready-for-dev: prints NDJSON for vincent.
set -eu
now=$(date -u +%s)
since=${VINCENT_TRIGGER_CURSOR:-$((now - 86400))}
minutes=$(( (now - since) / 60 + 2 ))
auth=$(printf '%s:%s' "$JIRA_USER" "$JIRA_API_TOKEN" | base64 | tr -d '\n')
resp=$(printf 'Authorization: Basic %s\n' "$auth" |
  curl -sSf -H @- -G "$JIRA_BASE/rest/api/3/search/jql" \
    --data-urlencode "jql=project = VIN AND status = 'Ready for Dev' AND updated >= -${minutes}m" \
    --data-urlencode 'fields=summary,updated' \
    --data-urlencode 'maxResults=20')
printf '%s\n' "$resp" |
  jq -c '.issues[] | {id: ("jira:" + .key + ":" + .fields.updated), key, fields}'
printf '{"cursor":"%s"}\n' "$now"
```

Some details are load-bearing:

- `set -eu` turns an unset credential into a non-zero exit.
- `resp=$(...)` takes curl's exit status, so a failed request fails the poll.
  POSIX `sh` has no `pipefail`, so curl's output is captured before it reaches
  `jq` rather than piped straight in.
- `printf` is a shell builtin, so the token never appears in any argv.
- `now` is taken before the query, so nothing updated during the request is
  skipped next time.

Install it owner-only:

```sh
mkdir -p "<config_dir>/trigger-scripts" && chmod 700 "<config_dir>/trigger-scripts"
chmod 700 "<config_dir>/trigger-scripts/jira-ready-for-dev"
```

### Windows (`pwsh`)

```powershell
# <config_dir>\trigger-scripts\jira-ready-for-dev.ps1: prints NDJSON for vincent.
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$now = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
$since = if ($env:VINCENT_TRIGGER_CURSOR) { [long]$env:VINCENT_TRIGGER_CURSOR } else { $now - 86400 }
$minutes = [long][math]::Floor(($now - $since) / 60) + 2
$pair = "$($env:JIRA_USER):$($env:JIRA_API_TOKEN)"
$auth = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($pair))
$jql = "project = VIN AND status = 'Ready for Dev' AND updated >= -${minutes}m"
$uri = "$($env:JIRA_BASE)/rest/api/3/search/jql?maxResults=20&fields=summary,updated&jql=$([uri]::EscapeDataString($jql))"
$resp = Invoke-RestMethod -Uri $uri -Headers @{ Authorization = "Basic $auth" }
foreach ($i in $resp.issues) {
  [ordered]@{ id = "jira:$($i.key):$($i.fields.updated)"; key = $i.key; fields = $i.fields } |
    ConvertTo-Json -Compress -Depth 20
}
@{ cursor = "$now" } | ConvertTo-Json -Compress
```

- With `$ErrorActionPreference = 'Stop'`, a failed request is a terminating
  error, and `pwsh -File` then exits non-zero.
- Setting the output encoding keeps non-ASCII summaries intact when the daemon
  reads the redirected stdout.
- A missing variable does not stop the script by itself. Check each one
  explicitly if an empty value would still reach Jira.

### The trigger that runs it

```yaml
# {config_dir}/triggers/jira-ready-for-dev.yaml
id: jira-ready-for-dev
enabled: false
source:
  type: command
  project: 1
  poll_interval: 5m
  command:
    - /home/me/.config/vincent/trigger-scripts/jira-ready-for-dev
  # Windows:
  # command: [pwsh, -NoProfile, -File, 'C:\Users\me\AppData\Roaming\vincent\trigger-scripts\jira-ready-for-dev.ps1']
action:
  type: create_task
  workflow: feature-pr
  title: '{{ .Event.key }} {{ .Event.fields.summary }}'
  fields:
    ticket: '{{ .Event.key }}'
dedupe_key: 'jira:{{ .Event.key }}:ready-for-dev'
limits:
  max_per_hour: 3
  max_task_cost_usd: 4
```

The paths shown are examples. Use the real absolute path of your `{config_dir}`.
The id includes `updated`, so every edit to a Ready-for-Dev ticket is a new
event, and the `dedupe_key` is what makes one ticket start one task.

## Checking a script before wiring it up

1. Run it by hand with the environment the daemon will have and an empty cursor:
   `VINCENT_TRIGGER_CURSOR= /abs/path/script; echo "exit $?"`. In pwsh, set
   `$env:VINCENT_TRIGGER_CURSOR = ''`, run
   `pwsh -NoProfile -File C:\abs\path\script.ps1`, then read `$LASTEXITCODE`.
2. Confirm that every event line is one object with a string `id`, that the
   last line is the cursor, and that nothing else is on stdout.
3. Break the credential on purpose and confirm the exit status is non-zero.
4. Rerun with the printed cursor and confirm the overlap repeats ids exactly.
5. Once the trigger file validates, the `trigger_poll` MCP tool
   (`POST /v1/triggers/{id}/poll`) runs the real command once and judges its
   events without firing, ledgering or moving the cursor. The command's own
   effects outside vincent still happen.
