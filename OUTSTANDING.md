# Outstanding — actions that need you

Everything in this file needs hardware, privileges, or judgement I do not have
from a session on your Windows box. Nothing else is blocking.

**How to use it:** fill in the blanks inline — dates, pass/fail, pasted output,
free-text notes. When you are done (or done with any one section), tell me and
I will fold the results into `docs/versions/v0/tasks.md`,
`docs/versions/v0/m5-gate.md` and the spec, then delete this file.

The four sections left are independent — take them in any order.

> **Done: the release tag.** `v0.1.0-rc1`, 2026-08-11,
> [run 31484132218](https://github.com/lezli01/vincent/actions/runs/31484132218).
> Verified and recorded against **T4.5, now closed**. §1 below is the direct
> beneficiary: the artifacts its clock starts from exist at
> [the release page](https://github.com/lezli01/vincent/releases/tag/v0.1.0-rc1).

---

## 1. T4.6 — M4 acceptance: fresh machine to first completed task

**Why you:** needs a clean VM per OS with **no Go toolchain** — that is what
proves the released artifact self-sufficient.

**The clock** starts at downloading the release artifact and stops at the first
completed task. It **includes** Gatekeeper/SmartScreen friction (vincent's own
cost) and **excludes** installing and authenticating the agent CLI (a
documented prerequisite). Target: **under 10 minutes**.

Walkthrough: download → unpack → `vincent project add` → copy an example
workflow → `vincent task add` → watch it finish. The README quickstart is the
script; deviating from it is itself a finding.

| OS | Clean VM | Time taken | Under 10 min? | Notes |
|---|---|---|---|---|
| Windows 11 |  |  | ☐ yes ☐ no |  |
| macOS |  |  | ☐ yes ☐ no |  |
| Linux |  |  | ☐ yes ☐ no |  |

Where the time actually went (this is the useful part — I can only act on
specifics):

```

```

---

## 2. T4.1 — service install matrix

**Why you:** requires a reboot per OS, and an elevated prompt on Windows.

```sh
vincent service install        # Windows: from an Administrator prompt
vincent service status
# reboot
vincent service status         # must still report running
vincent task ls                # must reach the daemon with no manual start
vincent service uninstall
vincent service status         # must report nothing installed
```

| OS | Installed | Survived reboot | Uninstalled cleanly | Notes |
|---|---|---|---|---|
| Windows 11 | ☐ | ☐ | ☐ |  |
| macOS | ☐ | ☐ | ☐ |  |
| Linux | ☐ | ☐ | ☐ |  |

Linux only — did `loginctl enable-linger` succeed automatically, or did the
installer print the manual command?

```

```

If anything failed, the exact error:

```

```

---

## 3. T5.7 — M5 gate, the real-`cursor-agent` legs

### 3a. Fix the Cursor hooks on this machine (blocks scenario 1)

Two `pretooluse` hooks are registered in **PowerShell** syntax while
`cursor-agent` runs hooks through **bash**, so every hook errors — and a hook
that errors *blocks* the tool call. `cursor-agent` can currently think and read
on this box but cannot edit or run anything.

```
Hook blocked: --: eval: line 1: syntax error near unexpected token `&'
`$OutputEncoding = … | & { $input | npx -y context-mode hook cursor pretooluse }'
`$OutputEncoding = … | & { $input | rtk hook claude }'
```

They come from the `context-mode` plugin and `rtk`, and are generated at
runtime rather than stored in a config file I could point at.

Confirm they are fixed:

```sh
cd "$(mktemp -d)" && git init -q && echo hi > readme.txt
printf 'Create a file named hi.txt containing hello. Nothing else.' \
  | cursor-agent -p --output-format stream-json --trust --force --model auto \
  | grep -o '"rejected":{[^}]*}' | head
```

Any output means hooks are still blocking.

- Hooks fixed: ☐ yes ☐ no — how: `____________________________________`
- Probe above returns nothing: ☐ yes ☐ no

Then re-run scenario 1 against the real CLI:

```sh
VINCENT_GATE_AGENT=cursor VINCENT_GATE_SCENARIO=1 ./scripts/m5-gate.sh
```

- Result: ☐ pass ☐ fail → output:

  ```
  
  ```

### 3b. macOS legs

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
`tool_call/completed` payloads: the hooks in §3a rejected every tool call
during capture. The `started` lines — the only ones the adapter normalises —
are verbatim, and the file says so in its test header.

Worth re-capturing once §3a is fixed?

- ☐ Yes, re-capture from a clean run
- ☐ No, the reconstruction is documented and sufficient

---

## Anything else you want picked up

```




```
