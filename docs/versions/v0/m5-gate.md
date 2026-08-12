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

## Prerequisite (Windows): hide `~/.claude` from Cursor

**Cursor imports Claude Code's hooks.** On a machine that has Claude Code
configured, this is what stands between a real-CLI run and a passing scenario 1,
and it took two days to find because nothing about it lives in Cursor's own
config. Cursor's hook log states it plainly:

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

This is a Cursor bug, not a vincent one, and not something you misconfigured —
having Claude Code hooks at all is enough to trigger it. **Reported upstream
2026-08-12:**
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
— **copy them into the row you record below**, since a relocated `USERPROFILE`
is exactly what distinguishes a passing real-CLI leg from a blocked one. It
reads them with `printenv` rather than `$SHELL`, because bash assigns itself the
user's login shell when it inherited none and does not export it; only the
**exported** environment reaches the daemon and then the agent.

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
| 2026-08-11 | Windows 11 (26200) | not recorded | `2026.08.04-aaa8809` | 1, real CLI | **blocked, environmental** — `--version`, `status` and the `models` probe all answered from the real binary (`available: true`, `default_model: auto`, zero efforts) and the agent step itself succeeded, but every edit was rejected by a blocked hook, so the commit step found nothing to commit. Not a vincent defect. **Cause identified 2026-08-12** (see the prerequisite above): Cursor imports Claude Code's hooks from `~/.claude/settings.json`, wraps them in PowerShell and evaluates them with bash. Superseded by the row below |
| 2026-08-12 | Windows 11 (26200) | PowerShell; `USERPROFILE`/`HOME` → scratch home (junction to real `.cursor`, no `.claude`) | `2026.08.11-e8db854` | 1, real CLI | **pass** — every assertion ran: catalog reported, task reached `done`, step recorded `agent=cursor` with tokens, no cost and no effort, transcript carried cursor-shaped lines, **and the branch carried the edit** — the assertion that had been failing since 2026-08-11 |
| | macOS | | | 1, 2, 4 | **not run** — needs a macOS box |

Scenario 4's Windows leg is the one that cannot be exercised anywhere else: it
is the only OS where cursor's sandbox is unavailable, so it is the only place
the refusal is real rather than forced. It passed above. The corresponding
"restricted actually runs" leg needs macOS or Linux; CI covers it with
fakeagent on both.

Scenario 1 against the real CLI **has now passed on Windows** (2026-08-12), so
the second half of that promise is met. **M5 is not complete until the macOS row
is filled.**
