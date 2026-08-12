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

> **Done: the Windows real-CLI legs.** Scenario 1 **passed** against the real
> `cursor-agent` on 2026-08-12, recorded in `docs/versions/v0/m5-gate.md`. Your
> `fail` is what got us there: `SHELL=<not exported>` and it still failed
> identically, which killed my "it's the launching shell" theory and sent me
> looking properly.
>
> **The cause, at last — and it was never on the Cursor side.** Cursor
> **imports Claude Code's hooks**. Its own log says so: it looks for
> `~/.cursor/hooks.json`, finds none, then loads `~/.claude/settings.json`, and
> `cursor-agent` does the same. It wraps each imported command in a PowerShell
> preamble and then evaluates that string with **bash**, so every one errors —
> and an erroring hook blocks the tool call. Your two blockers were `rtk hook
> claude` (from your Claude settings) and `npx -y context-mode hook cursor
> pretooluse` (a Claude plugin hook). That is why neither of us could find a
> registration: there wasn't one. A Cursor bug on Windows, not yours and not
> vincent's.
>
> **The remedy disables nothing.** Run the daemon with `USERPROFILE` and `HOME`
> pointed at a scratch dir holding a junction to the real `.cursor` and no
> `.claude`. Confirmed twice — the bare CLI probe went from 3 rejections and no
> file written to **0 rejections and `hi.txt` created**, and the full gate then
> passed end to end. The procedure is written up in `m5-gate.md`.
>
> One thing I would still take: **is it worth reporting upstream to Cursor?**
> You have the reproduction, and the fix is theirs to make — right now every
> Windows user with Claude Code hooks has a silently crippled `cursor-agent`.

### 3a. macOS legs — the only thing left on T5.7

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
the blocked hooks §3 is about. The `started` lines — the only ones the adapter
normalises — are verbatim, and the file says so in its test header.

This is now cheap to redo: the scratch-home procedure in `m5-gate.md` gives a
run whose tool calls actually complete, which is exactly what the fixture is
missing. Worth re-capturing?

- ☐ Yes, re-capture from a clean run
- ☐ No, the reconstruction is documented and sufficient

---

## Anything else you want picked up

```




```
