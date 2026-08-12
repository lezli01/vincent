# Outstanding — actions that need you

Everything in this file needs hardware, privileges, or judgement I do not have
from a session on your Windows box. Nothing else is blocking.

**How to use it:** fill in the blanks inline — dates, pass/fail, pasted output,
free-text notes. When you are done (or done with any one section), tell me and
I will fold the results into `docs/versions/v0/tasks.md`,
`docs/versions/v0/m5-gate.md` and the spec, then delete this file.

The three sections left are independent — take them in any order.

> **Done: the release tag.** `v0.1.0-rc1`, 2026-08-11,
> [run 31484132218](https://github.com/lezli01/vincent/actions/runs/31484132218).
> Verified and recorded against **T4.5, now closed**.

> **Done: §1, the M4 acceptance gate.** Clean VM per OS against those
> artifacts: **Windows 11 5:00, macOS 4:30, Linux 3:35** — all under half the
> ten-minute budget, no deviations from the README quickstart reported.
> Folded into **T4.6, now closed** and into spec §19. **M4's acceptance is
> met.** If you remember where the time actually went, tell me and I will add
> it — the totals are recorded, the breakdown is the part I can act on.

---

## 2. T4.1 — service install matrix

**Why you:** requires a reboot per OS. (No longer an elevated prompt on
Windows — see the note below.)

```sh
vincent service install        # no elevation needed on any OS since T4.19
vincent service status
# reboot, and log back in
vincent service status         # must still report running
vincent task ls                # must reach the daemon with no manual start
vincent                        # and the TUI must NOT say "starting daemon…"
vincent service uninstall
vincent service status         # must report nothing installed
```

| OS | Installed | Survived reboot | Uninstalled cleanly | Notes |
|---|---|---|---|---|
| Windows 11 | ✓ | ☐ | ✓ | **re-walk owed** — your ✓✓✓ was against the old service backend, which T4.19 replaced. Install and uninstall re-verified in-session 2026-08-12; only the reboot leg is open |
| macOS | ✓ | ✓ | ✓ | **folded, 2026-08-11** — three legs clean; the no-CLIs finding fixed and re-verified as T4.15 |
| Linux | ✓ | ✓ | ✓ |  |

> **Windows: your finding was a real defect, and a worse one than it looked.**
> "Install and uninstall work but the TUI still starts its own daemon" was the
> SCM running the service as **LocalSystem** (event 7045 confirms it). That
> daemon resolved `LOCALAPPDATA` to the SYSTEM profile, so its database and
> `daemon.json` lived in `C:\Windows\System32\config\systemprofile\` and no
> client in your session could ever find them — hence a second daemon every
> time. Pinning the directories would have hidden it behind something worse:
> §16's full-auto agents running as SYSTEM, without your agent-CLI logins or
> git identity. The backend is now a **Scheduled Task triggered at logon**,
> running as you, needing no elevation (**T4.19**).
>
> Two things change for this walkthrough. Install and uninstall no longer want
> an Administrator prompt — if yours *does* ask, that is the old service still
> registered and the error says so; remove it elevated once. And the middle
> column now means "starts at the next **logon**", the same promise a
> LaunchAgent and a non-lingering systemd user unit make, so log back in after
> the reboot before checking.

> **macOS: recorded, and your finding was a real defect.** A launchd agent runs
> with launchd's `PATH` (`/usr/bin:/bin:/usr/sbin:/sbin`) — no Homebrew, no npm
> prefix, no `~/.local/bin` — and §9.5 resolves adapters with `exec.LookPath`,
> so an installed service could see none of them while the same daemon started
> by hand saw them all. `service install` now bakes the installing shell's PATH
> into the plist and the systemd unit, the way it already did for the config
> and data dirs (**T4.15**). Linux had the same latent bug; Windows did not need
> the fix once T4.19 moved it into your logon session, where it already has
> your `PATH`. `agents.<name>.path` in `config.yaml` stays the answer anywhere
> a CLI still will not resolve.
>
> **Re-verified by you against a real launchd, 2026-08-11:** an installed
> service now sees the agent CLIs. T4.15 is closed. T4.1 itself stays open for
> the Windows and Linux rows.

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
