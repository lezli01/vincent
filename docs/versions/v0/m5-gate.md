# M5 phase gate — Cursor adapter (T5.7)

**Acceptance (spec §19 M5):** a workflow whose steps name `agent: cursor` runs
unattended to completion against the real `cursor-agent`, on each OS.

Most of this gate is scripted. `scripts/m5-gate.sh` drives a real daemon over
curl and asserts four things; CI runs it against the committed fakeagent on
all three OSes, and it is the same script you run by hand against the real CLI:

```sh
./scripts/m5-gate.sh                        # fakeagent — what CI runs
VINCENT_GATE_AGENT=cursor ./scripts/m5-gate.sh   # the real cursor-agent
VINCENT_GATE_SCENARIO=2 ./scripts/m5-gate.sh     # one scenario, for debugging
```

| # | Asserts | Real CLI? |
|---|---|---|
| 1 | A `agent: cursor` workflow runs to done; the step records `agent=cursor`, tokens, **no** cost and **no** effort; the transcript carries cursor-shaped lines; the branch carries the edit | yes |
| 2 | A model the CLI rejects fails with the **stderr tail** in the step record — cursor emits no `result` event on that path (§9.7, §18) | yes |
| 3 | An installed-but-unauthenticated CLI reports `logged_in: false` while staying `available: true`, on both `/v1/agents` and `/v1/info`; adapters that cannot tell report `null` (§9.5) | no — it would mean signing you out |
| 4 | A `restricted` step is refused with `restricted_unsupported` where cursor's sandbox is unavailable, and **no process is spawned**; elsewhere the same workflow simply runs (§9.4) | no — asserts vincent's own behavior |

## Prerequisite: record the launch environment

The daemon inherits the environment of the shell that starts it and hands it to
the agent process unchanged — `RunSpec.Env` is never populated for agent steps
(`internal/taskrun/steps.go`), the adapter overrides only a non-nil one
(`internal/agent/cursor/cursor.go`), and the detached spawn sets none of its own
(`internal/daemon/spawn.go`). The shell you type `vincent` into therefore
reaches `cursor-agent`, and everything `cursor-agent` spawns.

On Windows that is not academic. Git Bash exports `SHELL` to native children as
a real Windows path — it path-converts on the way out, so a downstream process
sees `C:\Program Files\Git\bin\bash.exe`, a path that exists and is honored.
PowerShell leaves `SHELL` unset, as does the §12.1 Scheduled Task backend.
Tooling in the hook chain both **chooses the shell it runs a hook with** and
**chooses the syntax it writes that hook in** from that one variable. Get the
two answers from different launches and a hook written for one shell is
evaluated by the other:

```
Hook blocked with message: --: eval: line 1: syntax error near unexpected token `&'
--: eval: line 1: `$OutputEncoding = [System.Text.Encoding]::UTF8; Get-Content -LiteralPath '…payload.json' -Raw | & { $input | … }'
```

A hook that errors **blocks** the tool call, and the symptom is confusing rather
than obvious: the agent step **succeeds** — cursor exits 0 and emits a clean
`result` — and the *next* step fails with `nothing to commit, working tree
clean`. Scenario 1 detects this shape and says so rather than reporting a bare
`nonzero_exit`.

`m5-gate.sh` prints `SHELL`, `MSYSTEM` and `COMSPEC` at the top of every run.
**Copy them into the row you record below.** A leg recorded without its launch
context cannot be reproduced, and one has already been written up as a blocker
when the launching shell was the variable.

On Windows, prefer **PowerShell** for a real-CLI run: it is also how the daemon
runs once installed through `vincent service install`, which is the
configuration that ships.

Then check hooks — if a run still fails after the above:

```sh
cd "$(mktemp -d)" && git init -q && echo hi > readme.txt
printf 'Create a file named hi.txt containing hello. Nothing else.' \
  | cursor-agent -p --output-format stream-json --trust --force --model auto \
  | grep -o '"rejected":{[^}]*}' | head
```

Any output means hooks are blocking tools; the gate cannot pass until they are
fixed or removed. Run it from **both** shells before concluding that: if the
two disagree, the environment is the cause and there is no hook to fix.

Also confirm you are logged in — `cursor-agent status` — or every run fails at
the API. vincent surfaces that as `logged_in: false` before a task is created
(scenario 3), which is the point of the field.

## Recorded runs

Each row is one hand-run of the real-CLI legs. CI covers the fakeagent legs on
every push, so this table only tracks what CI cannot.

| Date | OS | Launched from | CLI version | Scenarios | Result |
|---|---|---|---|---|---|
| 2026-08-11 | Windows 11 (26200) | not recorded | `2026.08.04-aaa8809` | all 4, fakeagent | **pass** |
| 2026-08-11 | Windows 11 (26200) | not recorded | `2026.08.04-aaa8809` | 2, real CLI | **pass** — invalid model exited 1 with no `result` event; the stderr tail reached the step record |
| 2026-08-11 | Windows 11 (26200) | not recorded — **suspect Git Bash** | `2026.08.04-aaa8809` | 1, real CLI | **blocked, environmental** — `--version`, `status` and the `models` probe all answered from the real binary (`available: true`, `default_model: auto`, zero efforts), and the agent step itself succeeded; the edit was rejected by local `pretooluse` hooks whose PowerShell syntax was evaluated by bash, so the commit step found nothing to commit. Not a vincent defect. **Re-run from PowerShell before doing anything else** — 2026-08-12 found no hooks registered in `~/.cursor/cli-config.json`, no `~/.cursor/hooks.json` and no project `.cursor/`, so the launching shell is the better explanation than a stale registration |
| | macOS | | | 1, 2, 4 | **not run** — needs a macOS box |

Scenario 4's Windows leg is the one that cannot be exercised anywhere else: it
is the only OS where cursor's sandbox is unavailable, so it is the only place
the refusal is real rather than forced. It passed above. The corresponding
"restricted actually runs" leg needs macOS or Linux; CI covers it with
fakeagent on both.

**M5 is not complete until the macOS row is filled and scenario 1 passes
against the real CLI on at least one OS.**
