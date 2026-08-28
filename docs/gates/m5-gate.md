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
| 4 | A `restricted` step is refused where cursor's sandbox is unavailable, never downgraded to full-auto; elsewhere the same workflow simply runs (§9.4) | no — asserts vincent's own behavior |

## Prerequisite (Windows, when the daemon is parented by Git Bash)

Two conditions have to be true together, and neither alone is enough:

1. Claude Code hooks are configured (`~/.claude/settings.json`, or a Claude
   plugin hook), **and**
2. **`MSYSTEM` is set in the daemon's environment** — which is true of every
   process descended from Git Bash / MSYS2, and false everywhere else.

Isolated on one machine, same CLI, same hooks, minutes apart — each run a fresh
registry-derived environment plus exactly one addition:

| Environment | `hi.txt` written | `Hook blocked` |
|---|---|---|
| clean default environment | yes | 0 |
| clean + `MSYSTEM=MINGW64` | **no** | **5** |
| clean + Git Bash `\usr\bin` on `PATH`, no `MSYSTEM` | yes | 0 |

So it is not `PATH` and not the mere availability of bash. **`MSYSTEM` alone
decides it**, and `m5-gate.sh` is a bash script, so a gate run from Git Bash
always has it.

**You cannot remove it from inside Git Bash.** The MSYS runtime injects
`MSYSTEM` into the environment block of every child process: `env -u MSYSTEM`
and `unset MSYSTEM` both still leave a *native* Windows child seeing
`MSYSTEM=MINGW64`. Verified — do not spend time on that route. The remedy below
works on the other condition instead.

A daemon started from **PowerShell**, `cmd`, or the §12.1 Scheduled Task has no
`MSYSTEM` and is unaffected. That is the shipped configuration, and it is proven
working: an installed, logon-started daemon ran a cursor workflow to `done` with
28 edit tool calls, **zero** hook blocks and a real commit on the branch.

### Why it happens

**Cursor imports Claude Code's hooks.** Nothing about it lives in Cursor's own
config, which is what made it take two days to find. Cursor's hook log states it
plainly:

```
User config path:        c:\Users\<you>\.cursor\hooks.json
Claude user config path: c:\Users\<you>\.claude\settings.json
No user hooks configuration found       <- no Cursor hooks exist
Loaded Claude user hooks                <- yours are adopted from Claude Code
```

`cursor-agent` does the same (`claudeUserHooks`, `claudeProjectHooks`,
`claudeProjectLocalHooks` in its bundle). It then wraps each imported command in
a **PowerShell** preamble — writing the payload to `%TEMP%\cursor-hooks-XXXXXX\`
— and evaluates that string with **bash**:

```
Hook blocked with message: --: eval: line 1: syntax error near unexpected token `&'
--: eval: line 1: `$OutputEncoding = [System.Text.Encoding]::UTF8; Get-Content -LiteralPath '…payload.json' -Raw | & { $input | … }'
```

Composed for one shell, executed by another, so **every** imported hook errors —
and an erroring hook **blocks** the tool call. The symptom is confusing rather
than obvious: the agent step **succeeds** (cursor exits 0 with a clean `result`)
and the *next* step fails with `nothing to commit, working tree clean`. Scenario
1 detects that shape and says so rather than reporting a bare `nonzero_exit`.

This is a Cursor bug, not a vincent one, and not something you misconfigured.
**Reported upstream 2026-08-12:**
[forum.cursor.com/t/…/168129](https://forum.cursor.com/t/cursor-agent-windows-imported-claude-code-hooks-are-composed-as-powershell-but-executed-with-bash-silently-blocking-every-tool-call/168129).
Check whether it is fixed before following the remedy below — when it is, this
whole section should go rather than survive as ritual.

### The remedy: a scratch home

Give `cursor-agent` a home directory with your real `.cursor` and no `.claude`.
Nothing on the machine changes, and no Claude Code hook has to be disabled:

```powershell
$fh = "$env:TEMP\vincent-gate-home"
New-Item -ItemType Directory -Force $fh | Out-Null
cmd /c mklink /J "$fh\.cursor" "$env:USERPROFILE\.cursor"   # no elevation needed

$env:USERPROFILE = $fh; $env:HOME = $fh
$env:GOPATH = (go env GOPATH); $env:GOMODCACHE = (go env GOMODCACHE); $env:GOCACHE = (go env GOCACHE)
$env:VINCENT_GATE_AGENT = "cursor"; $env:VINCENT_GATE_SCENARIO = "1"
bash ./scripts/m5-gate.sh
```

The junction keeps authentication working — it is the real `~/.cursor`. Pinning
Go's three cache variables is needed because the build runs under the same
relocated home and would otherwise re-resolve every module. Repo git identity is
set per-repo by the gate, so a missing global `.gitconfig` does not matter.

Verify the remedy on its own before blaming anything else — this probe writes
`hi.txt` when hooks are clear and writes nothing when they are not:

```powershell
cursor-agent -p "Create a file named hi.txt containing hello. Nothing else." `
  --output-format stream-json --trust --force --model auto > out.jsonl
Test-Path hi.txt      # False, plus "rejected" entries in out.jsonl, means hooks are blocking
```

`m5-gate.sh` prints `SHELL`, `MSYSTEM` and `USERPROFILE` at the top of every run
— **copy them into the row you record below.** Those two together say whether a
Windows leg was exposed: `MSYSTEM` present is the trigger, a relocated
`USERPROFILE` is the remedy. It reads them with `printenv` rather than
`$SHELL`/`$MSYSTEM`, because a shell variable is not the same thing as what a
native child inherits — only the **exported** environment reaches the daemon and
then the agent.

Also confirm you are logged in — `cursor-agent status` — or every run fails at
the API. Vincent surfaces that as `logged_in: false` before a task is created
(scenario 3), which is the point of the field.

## Recorded runs

Each row is one hand-run of the real-CLI legs. CI covers the fakeagent legs on
every push, so this table only tracks what CI cannot.

| Date | OS | Launched from | CLI version | Scenarios | Result |
|---|---|---|---|---|---|
| 2026-08-11 | Windows 11 (26200) | not recorded | `2026.08.04-aaa8809` | all 4, fakeagent | **pass** |
| 2026-08-11 | Windows 11 (26200) | not recorded | `2026.08.04-aaa8809` | 2, real CLI | **pass** — invalid model exited 1 with no `result` event; the stderr tail reached the step record |
| 2026-08-11 | Windows 11 (26200) | not recorded | `2026.08.04-aaa8809` | 1, real CLI | **blocked, environmental** — `--version`, `status` and the `models` probe all answered from the real binary (`available: true`, `default_model: auto`, zero efforts) and the agent step itself succeeded, but every edit was rejected by a blocked hook, so the commit step found nothing to commit. Not a vincent defect. **Cause identified 2026-08-12** (see the prerequisite above): Cursor imports Claude Code's hooks from `~/.claude/settings.json`, wraps them in PowerShell and evaluates them with bash. Superseded by the row below |
| 2026-08-12 | Windows 11 (26200) | PowerShell; `USERPROFILE`/`HOME` → scratch home (junction to real `.cursor`, no `.claude`) | `2026.08.11-e8db854` | 1, real CLI | **pass** — every assertion ran: catalog reported, task reached `done`, step recorded `agent=cursor` with tokens, no cost and no effort, transcript carried cursor-shaped lines, **and the branch carried the edit** — the assertion that had been failing since 2026-08-11 |
| 2026-08-12 | macOS | — (no `MSYSTEM` on macOS) | `2026.08.11-e8db854` | all 4, fakeagent | **pass** — including scenario 4's *other* half: this is the only place `restricted` genuinely **runs** rather than being refused, which had never been proven anywhere |
| 2026-08-12 | macOS | — | `2026.08.11-e8db854` | 1 and 2, real CLI | **pass** — no prerequisite dance needed; the hook trigger is Windows-only |
| 2026-08-12 | Windows 11 (26200) | Scheduled Task (installed daemon, no `MSYSTEM`) | `2026.08.11-e8db854` | cursor workflow by hand, not the gate | **pass** — task reached `done`, 28 `editToolCall`s, **0** hook blocks, real commit on the branch (4 files, +31/−20). The shipped configuration is unaffected |

Scenario 4's Windows leg is the one that cannot be exercised anywhere else: it
is the only OS where cursor's sandbox is unavailable, so it is the only place
the refusal is real rather than forced. It passed above. The corresponding
"restricted actually runs" leg needs macOS or Linux; CI covers it with
fakeagent on both.

**Amended 2026-08-28 (task 040): where scenario 4's refusal happens moved.**
The Windows refusal is now `POST /v1/tasks` answering `400 validation_failed`
and naming the step and the agent, because the daemon can tell from the adapter
and `GOOS` alone that the step could never run restricted (§9.4). The engine's
`restricted_unsupported` block reason survives as the backstop for a task whose
daemon changed underneath it, so what the walkthrough proves is unchanged in
substance — a restricted step is refused, never downgraded — but the surface it
is proved on is the create call rather than the block reason. The POSIX leg is
untouched: cursor can restrict there, so the task is created and simply runs.

`scripts/m5-gate.sh` has **not** been updated for this: its scenario 4 still
posts the task expecting a `2xx` and then waits for `blocked` with
`restricted_unsupported`, which the new `400` fails on the Windows runner
before any assertion is reached. The Windows leg of this gate should be
re-walked once the script asserts the refusal at creation.

**M5 is complete (2026-08-12).** Scenario 1 passed against the real CLI on
Windows and on macOS; macOS also carried scenario 4's "restricted actually runs"
half, the one leg no other platform can prove. Linux stays covered by the
fakeagent gate in CI.
