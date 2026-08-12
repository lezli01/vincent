# Outstanding — actions that need you

Everything in this file needs hardware, privileges, or judgement I do not have
from a session on your Windows box. Nothing else is blocking.

**How to use it:** fill in the blanks inline — dates, pass/fail, pasted output,
free-text notes. When you are done (or done with any one section), tell me and
I will fold the results into `docs/versions/v0/tasks.md`,
`docs/versions/v0/m5-gate.md` and the spec, then delete this file.

The two sections left are independent — take them in any order.

> **Done: the release tag.** `v0.1.0-rc1`, 2026-08-11,
> [run 31484132218](https://github.com/lezli01/vincent/actions/runs/31484132218).
> Verified and recorded against **T4.5, now closed**.

> **Done: §1, the M4 acceptance gate.** Clean VM per OS against those
> artifacts: **Windows 11 5:00, macOS 4:30, Linux 3:35** — all under half the
> ten-minute budget, no deviations from the README quickstart reported.
> Folded into **T4.6, now closed** and into spec §19. **M4's acceptance is
> met.** If you remember where the time actually went, tell me and I will add
> it — the totals are recorded, the breakdown is the part I can act on.

> **Done: §2, the service install matrix.** Windows re-walked against the
> Scheduled Task backend and reported clean on all three legs, 2026-08-12. That
> logon is also the leg **T4.21** was waiting for — the cold terminal start whose
> ordering defeated T4.20's hide — so both close: **T4.1 and T4.21 are done**,
> with macOS and Linux already recorded. One thing I would still take if you
> remember it: on Linux, did the installer run `loginctl enable-linger` itself or
> print the manual command? That is the difference between surviving **logout**
> and only surviving logon, and no other check asks.

---

## 3. T5.7 — M5 gate, the real-`cursor-agent` legs

### 3a. Re-run scenario 1 from PowerShell (do this first — it may be the whole fix)

**The premise of the old §3a was wrong, and it is worth a paragraph because it
cost a day.** The blocker was written up as "two `pretooluse` hooks registered
in PowerShell syntax while `cursor-agent` evaluates them through bash, and they
are generated at runtime rather than stored in a config file I could point at."
The error message says exactly that — but there is nothing to go and fix: as of
2026-08-12 there are **no** hooks in `~/.cursor/cli-config.json`, no
`~/.cursor/hooks.json`, and no project-level `.cursor/`.

What does vary between runs is **the shell the daemon was launched from**.
vincent hands its own environment to `cursor-agent` unchanged — `RunSpec.Env`
is never populated for agent steps (`internal/taskrun/steps.go`), the adapter
overrides only a non-nil one (`internal/agent/cursor/cursor.go`), and the
detached spawn sets none of its own (`internal/daemon/spawn.go`). Git Bash
exports `SHELL` to native children as a **real Windows path** (it path-converts
on the way out, so a downstream Node process sees `C:\Program
Files\Git\bin\bash.exe`, finds it exists, and honors it); PowerShell leaves
`SHELL` unset. The hook chain picks both the shell it *runs* a hook with and
the syntax it *writes* that hook in from that one variable — so a mismatched
pair is what a Git Bash launch produces and a PowerShell launch does not.

That also explains why the same workload works when vincent is started from
PowerShell, and why it will work under `vincent service install` (a Scheduled
Task has no `SHELL` either) — i.e. in the configuration that actually ships.

So, from a **PowerShell** window:

```powershell
vincent daemon stop
$env:VINCENT_GATE_AGENT="cursor"; $env:VINCENT_GATE_SCENARIO="1"
bash ./scripts/m5-gate.sh
```

The gate is a bash script either way; what matters is that PowerShell rather
than Git Bash is the **parent**, so `SHELL` never enters the environment the
daemon inherits. The script now prints its launch environment as its first
line — confirm it reads `SHELL=<unset>`. If it doesn't, the parent leaked one:
re-run the last line as `env -u SHELL bash ./scripts/m5-gate.sh`.

- Launch environment the script printed: `SHELL=__________ MSYSTEM=__________`
- Result: ☐ pass ☐ fail → output:

  ```
  
  ```

### 3b. Only if 3a still fails: check hooks for real

```sh
cd "$(mktemp -d)" && git init -q && echo hi > readme.txt
printf 'Create a file named hi.txt containing hello. Nothing else.' \
  | cursor-agent -p --output-format stream-json --trust --force --model auto \
  | grep -o '"rejected":{[^}]*}' | head
```

Any output means something is still blocking tool calls. Run it from the *same*
shell that failed, then from the other one — if the two disagree, it is the
environment and not a registration, and §3a is the answer rather than hook
surgery.

- Probe returns nothing: ☐ yes ☐ no
- If it differs between PowerShell and Git Bash: ☐ yes ☐ no

### 3c. macOS legs

Scenarios 1, 2 and 4 on a Mac. Scenario 4 matters most: it is the **only**
place `restricted` genuinely runs rather than being refused, and the refusal
half has only ever been proven on Windows.

```sh
./scripts/m5-gate.sh                              # all four, fakeagent
VINCENT_GATE_AGENT=cursor ./scripts/m5-gate.sh    # real CLI (1 and 2)
```

| Leg | Result | Notes |
|---|---|---|
| All four, fakeagent | ☐ pass ☐ fail |  |
| Scenario 1, real CLI | ☐ pass ☐ fail |  |
| Scenario 2, real CLI | ☐ pass ☐ fail |  |
| Scenario 4 — restricted actually runs | ☐ pass ☐ fail |  |

cursor-agent version used: `________________`

---

## 4. T4.4 — read the quickstart cold

**Why you:** I wrote it, so I cannot judge it. Ideally someone who has never
seen vincent; you are the next best thing.

Read [README.md](README.md) from **Install** through **Quickstart** and follow
it exactly, without filling gaps from what you already know.

- Could you get to a completed task without leaving the README? ☐ yes ☐ no
- Where did you have to guess, backtrack, or look elsewhere?

  ```
  
  ```

- Did the full-auto warning land *before* you ran an agent? ☐ yes ☐ no
- Anything that read as filler or as obviously written by someone who already
  knew the answer:

  ```
  
  ```

---

## 5. Two decisions I would like confirmed

Neither blocks anything. Both are cheap to reverse **now** and awkward later.

### 5a. `--model auto` overwrites your saved Cursor model

Cursor persists `--model` to `~/.cursor/cli-config.json`, so vincent always
passes one (defaulting to `auto`) to keep runs reproducible. The cost: every
cursor step resets the model you picked in your own interactive
`cursor-agent` sessions.

- ☐ Keep it (reproducible runs, your interactive default gets reset)
- ☐ Change it (pass `--model` only when §8.6 resolves one; a task's recorded
  model then may not be what actually ran)
- Notes:

  ```
  
  ```

### 5b. Re-capture the contaminated test fixture

`internal/agent/cursor/testdata/tools_2026.08.04.jsonl` has **reconstructed**
`tool_call/completed` payloads: every tool call was rejected during capture by
the blocked hooks §3a is about. The `started` lines — the only ones the adapter
normalises — are verbatim, and the file says so in its test header.

If §3a turns out to be the launching shell, this capture is cheap to redo from
PowerShell. Worth re-capturing?

- ☐ Yes, re-capture from a clean run
- ☐ No, the reconstruction is documented and sufficient

---

## Anything else you want picked up

```




```
