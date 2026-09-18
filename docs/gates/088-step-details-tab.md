# Task 088 walkthrough — the Step Details tab

**Acceptance (task 088):** for any attempt, the task workspace's Step Details
tab says what the attempt was handed, how each value was resolved, and what it
produced — legibly, in a real terminal, and unchanged by anything done after
the attempt ran.

This walkthrough has **no script**, deliberately. What is judged is whether a
panel is legible, which is the reason M3's surfaces and
[017](017-workflow-graph.md)'s graph have none. The data underneath is already
asserted: the recorded input and its 64 KiB cut in
`internal/store/stepinput_test.go`, the wire between the real API handlers and
the client in `internal/apiclient/stepinput_live_test.go`, and the tab itself
in `internal/tui/taskstepdetails_test.go` and
`internal/tui/stepdetailslive_test.go`. A script would re-assert that over
curl and still not answer the only open question.

The tab's screenshot tape is not part of this walk; it was tracked in
[#414](https://github.com/lezli01/vincent/issues/414) and is now
`scripts/screenshots.sh`'s `tui-task-step-details` (2026-09-18).

## Setup

A throwaway installation, a daemon, and one task on the workflow below. The
fake agent is enough — every leg judges what vincent records, not what the
agent does — and leaving `agents.claude` out of `config.yaml` runs the real
claude CLI instead.

```sh
go build -o bin/ ./cmd/vincent ./cmd/fakeagent
W="$(mktemp -d)"
export VINCENT_CONFIG_DIR="$W/config" VINCENT_DATA_DIR="$W/data"
mkdir -p "$VINCENT_CONFIG_DIR/workflows" "$W/repo"
printf 'agents:\n  claude:\n    path: "%s"\n' "$PWD/bin/fakeagent" > "$VINCENT_CONFIG_DIR/config.yaml"
git -C "$W/repo" init -b main
git -C "$W/repo" commit --allow-empty -m seed
# save the two workflows below into $VINCENT_CONFIG_DIR/workflows/
./bin/vincent daemon start
./bin/vincent project add "$W/repo"
./bin/vincent task add --project <id> --workflow walk-details --title "walk details" --model sonnet
./bin/vincent                                # enter on the task, then 6
```

On Windows the fake agent is `bin/fakeagent.exe`, and `describe` pins
`shell: sh`, which there resolves to whatever `sh` is on `PATH` — Git Bash's.

`walk-details.yaml` — one of everything the tab records:

```yaml
name: walk-details
description: one of everything the Step Details tab records
fields:
  - {name: target, type: string, required: true, default: docs}
defaults:
  agent: claude
steps:
  - id: summarise
    type: agent
    effort: high
    max_retries: 1
    prompt: |
      Summarise {{ .Task.Title }} for {{ index .Task.Fields "target" }}.

      {{ .Task.Description }}
    check: 'exit {{ if eq .Step.Attempt 1 }}1{{ else }}0{{ end }}'

  - id: describe
    type: command
    shell: sh
    run: 'git log -1 --format="{{ .Task.Title }}: %s"'

  - id: guarded
    type: command
    if: '{{ ne (index .Task.Fields "target") "skip" }}'
    run: git rev-parse HEAD

  - id: each
    type: loop
    for_each: ['{{ index .Task.Fields "target" }}', api]
    steps:
      - id: item
        type: command
        run: 'git log -1 --format="{{ .Loop.Item }}: %s"'

  - id: checks
    type: include
    workflow: walk-checks
```

`walk-checks.yaml` — the workflow `checks` splices in:

```yaml
name: walk-checks
steps:
  - {id: status, type: command, run: git status --short}
```

`check:` renders with `.Step.Attempt`, so it fails `summarise`'s first attempt
and passes the retry. `exit N` is the whole body, which `/bin/sh` and `pwsh`
read alike. The task runs seven attempts: `summarise` twice, `describe`,
`guarded`, `item` once per item, and `status`. It lands `done`.

## Legs

| # | Do | Expect |
|---|---|---|
| 1 | From each of the workspace's other tabs, press `6` | Step Details opens every time, on the attempt that was selected |
| 2 | Select `summarise` attempt 1, then `describe` | The prompt reads `Summarise walk details for docs.`, the check `exit 1`, and `describe`'s script `git log -1 --format="walk details: %s"` — the substituted values, never a `{{ … }}` |
| 3 | Select `summarise` attempt 2 | The prompt ends with the `<previous-attempt-failure attempt="1">` block, marked as vincent's rather than the workflow's, and the check reads `exit 0`. Attempt 1 carries no such block |
| 4 | Stay on attempt 2, then select `describe` and `status` | Agent `claude` from the workflow, model `sonnet` from the task, effort `high` from the step, each with the level that supplied it; the permission mode, both timeouts and the working directory. `describe` shows the shell `sh`; `status` shows the include chain naming `walk-checks` |
| 5 | Select `guarded`, then each `item` attempt | `guarded` has no `if: rendered to` line: its guard passed and the step ran, and only a row a guard decides without running — a skip, a stop, a `condition` — records what it rendered to. Each `item` shows its iteration and the total of 2, its item — `docs`, then `api` — and the whole resolved list |
| 6 | Read the outcome of `summarise` attempt 1 and of `item` | Tokens and cost where the agent reported them, durations, exit codes, the `check_failed` reason on attempt 1, and the transcript path |
| 7 | Change the attempt with `↑`/`↓`, then `←`/`→`; press `3`, then `4`, then `6` | Output shows the attempt you selected here; Diff is the task's whole diff and does not follow it; Step Details is still on the attempt |
| 8 | Press `pgdn` and `pgup` on `summarise` attempt 2 | The facts scroll; the selected attempt does not change |
| 9 | Create a second task with a description at the API's 64 KiB cap — `--description "$(head -c 65536 /dev/zero \| tr '\0' x)"` — and open `summarise` | The prompt, which is that description plus the line above it, says it was cut, rather than ending silently. On Windows the command line cannot carry that many bytes; write the description with `e` on New task instead |
| 10 | Open an attempt that ran before vincent recorded any of this — a task run on a build without task 088, such as v0.8.0, then this build on the same data dir | The prompt and script read `not recorded (this attempt predates the record)`, not an empty prompt |
| 11 | Change `defaults.command_timeout` in `config.yaml`, wait for the hot reload, and reopen `describe` | The timeout shown is the one the attempt ran with, not the new value. `describe` has no `check:`, so its check timeout row reads `not recorded` on any build |
| 12 | Press `?` on Step Details | The help lists this tab's keys: `6`, and selecting an attempt with `↑`/`↓` or `←`/`→`, with `pgup`/`pgdn` scrolling the facts |

## Runs

| Date | Version | Platform | By | Result |
|---|---|---|---|---|
| — | — | — | — | not yet walked |

Add a row per walk. A surface that has never been walked on a platform is not
known to read correctly there.
