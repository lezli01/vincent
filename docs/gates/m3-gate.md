# M3 phase gate — walkthrough and results (T3.8)

Spec §19 M3 acceptance: *the full loop (register → author workflow\* → run 3
parallel tasks → answer an agent question → approve gate → archive) is doable
without leaving the TUI.*

M1 and M2 are asserted by `curl` in CI. M3 cannot be: its acceptance is a
judgement about a terminal program, and the Phase 3 decision ruled out driving
one from tests. So this is a human walkthrough. `scripts/m3-gate.sh` seeds the
world and prints the launch instructions; everything below is done by hand, on
**Windows 11 and macOS**, both runs against the same build. The shakeout runs
were walked on the PR branches whose fixes they produced; the two recorded runs
were walked on `master` at `40fbffe`, the first commit carrying all of them.

Linux is not hand-run: it executes the full Go suite and both existing gates on
every PR across all three OSes. That is a stated choice, not an omission.

```sh
scripts/m3-gate.sh                     # seed (real claude on the question leg)
VINCENT_GATE_AGENT=fake scripts/m3-gate.sh   # zero-spend rehearsal
scripts/m3-gate.sh clean               # stop the daemon, remove the seeded tree
```

Start `vincent` from the printed launch block in the terminal you mean to
judge — Windows Terminal or pwsh on Windows, not Git Bash, which hands a
native console app a pipe rather than a console.

## Grading

Fixed before the walkthrough starts, so nothing can be argued down after the
fact because the gate is otherwise green:

- **An item that blocks completing the loop fails the gate.** It is fixed in
  this PR, and *both* OS runs are then re-walked on the fixed build — every
  result below names one commit, and two runs against two builds would make
  that record a lie.
- **An item that merely looks or reads wrong does not fail the gate.** It
  becomes a new Phase 4 task in `tasks.md` under the "newly discovered work"
  rule, and its ID goes in the findings table.

## Section A — the acceptance loop (mandatory)

Section A failing is a gate failure. Do it in order; it is one continuous
session, and *leaving the TUI to get something done is itself a failure*.

| ID | Step | Passes when |
|---|---|---|
| **L1** | Launch `vincent` with no daemon running. | The TUI comes up on its own, shows it is starting the daemon, and reaches the board without you starting anything by hand. On the very first launch the full-auto notice appears; `enter` dismisses it. |
| **L2** | Register the app repo: `:` → projects, `a`, paste the seeded app-repo path, `ctrl+s`. | The project appears in the list with its name derived from the directory, and no error. |
| **L3** | Author a workflow: `:` → workflows, select **m3-parallel** on the app project, `e`. | Your `$EDITOR` opens *the project-scoped file* (`.vincent/workflows/m3-parallel.yaml`, 2 steps — not the 1-step global copy). Change the step id `work-shadow` to something you will recognise, save, quit the editor. The row updates **without a restart**. |
| **L4** | Read the shadowing row on the Workflows view. | **m3-parallel** is one row, not two, and says it shadows the global entry. |
| **L5** | Create four tasks on the app project with **m3-parallel**: `n`, title, pick the workflow, override `agent` to `codex`, `ctrl+s`. Repeat. | Four tasks exist. |
| **L6** | Watch the board. | Exactly **three** run at once and the fourth sits `queued`; each running row shows the step name you typed in L3 (proving the shadow ran, not the global copy) and a step count of 2. When the first finishes, the fourth starts on its own — no keypress, no refresh. |
| **L7** | Open one running task (`enter`), watch the output pane. | Output is still arriving while you watch; `f`/`G` re-follows after you scroll away. Press `d` for the diff — it opens on the file list, with `README.md` and its `+n -n` counts; `enter` expands the agent's edit and `C` folds it away again. |
| **L8** | Create a task on the app project with **m3-loop** (agent `claude`, the real CLI). Wait. | The task reaches `awaiting_input` and the TUI alerts you. Jump to it (`!`), open the answer popup (`enter`), pick an option, `enter`. The run resumes in place — no new attempt, no restart — and finishes the agent step. |
| **L9** | The task stops at the gate. Approve it: `a`. | The manual step shows as approved, the publish step runs, and the task reaches `done`. `git -C <bare remote> log --oneline --all` shows the branch with the agent's commit. |
| **L10** | Archive the finished loop task: `A`, confirm. | It asks first, naming the consequence; on `y` the task shows as archived and its worktree is gone from disk. |

## Section B — contents sweep

One item per phrase in §19's M3 **contents** column, quoted, plus the paths
that column implies. Graded by the rule above — findings here do not fail the
gate unless they blocked Section A.

| ID | §19 phrase | Look at |
|---|---|---|
| **S1** | "All six views" | All six §15 surfaces render, none blank or panicking: the home panels plus the four takeovers reached from the `:` palette; `?` lists the keys each surface actually has, straight from the registry. |
| **S2** | "live tail" | Covered by L7; additionally, a tail left open across a task's completion ends cleanly rather than hanging on "following". |
| **S3** | "diff view" | `d` on a task with no commits yet, on a finished task, and on an archived one (worktree removed) — three different situations, three intelligible messages. On a task with a multi-file change (task 012): every file collapsed on arrival, `↑`/`↓` walking the list, `enter` folding one, `O`/`C` the lot, the summary line's totals matching the per-file counts. |
| **S4** | "all actions" | `p` pause/resume a running task · `c` cancel (asks first) · `s` skip a step · `r` retry a blocked task · `E` edit the failing step in `$EDITOR` and retry · `x` reject a gate. Each offered only when valid for that task's state. |
| **S5** | "input-request alerts + answer form" | Covered by L8; additionally `e` in the form types a free-text answer instead of picking, and `esc` leaves without answering and the task stays parked. |
| **S6** | "`$EDITOR` integration" | Covered by L3 (workflow file) and S4 (`E` on a step); also `e` on the description field in the new-task form. The TUI redraws correctly after the editor exits in all three. |
| **S7** | "daemon auto-start" | Covered by L1. |
| **S8** | Reconnect (PR H made connect and reconnect one state) | From a second terminal, `vincent daemon stop --force`. The panels stay on screen marked stale behind the banner (§15 Disconnected), `:` still reaches the daemon view, and `r` brings the connection back without losing where you were. |
| **S9** | Projects view states | The spare repo gives the list a second row; `d` on a project with a running task is refused with a reason; `enter`/`e` edits and saves. |
| **S10** | Workflows view states | **m3-broken** renders as invalid, carrying the registry's own message (unknown step type, line 5); the built-in `adhoc` renders as built-in with no `e`. |
| **S11** | Daemon view | The daemon entry in `:` shows version, uptime, pid, listen address, the three paths, `max_parallel_tasks: 3`, both adapters with their resolved paths, and a log tail that matches what the daemon is actually writing. `R` refreshes all three. |
| **S12** | Destructive confirmation | `A` on one of the **m3-parallel** tasks — its worktree is dirty (the agent edited README.md and nothing committed). The prompt says changes will be lost. Answer `n` first: the task is still there. Then `y`. |
| **S13** | Quit reminder | `q` with tasks still running prints the running-task count after the alt screen tears down, and the line survives in scrollback. |
| **S14** | Mouse (T3.13) | On both Windows hosts (Windows Terminal + pwsh, Git Bash/mintty): click focuses a panel, click selects a task row, the wheel scrolls the focused panel, clicking a footer hint fires it, clicking the output/diff tab switches it. `M` turns it off and native click-drag text selection returns. |
| **S15** | Resize across both floors (T3.13) | Shrink below 80×20 — single-panel mode, `tab` swaps which; below 60×15 — the explicit size line; grow back — three panels return, nothing panics, selection and any committed filter survive. |
| **S16** | Palette reachable and complete (T3.13) | `:` opens on every surface; entries match it — the selected task's valid actions, navigation, the surface's own keys — and running an entry behaves exactly like pressing the key it shows. |
| **S17** | Accordion at 24 rows (T3.13) | On a 24-row terminal focus each panel in turn: the focused band expands, the collapsed band is title + one line, and the task table never shows fewer than five rows. |
| **S18** | `NO_COLOR` (T3.13) | A `NO_COLOR=1` run: focus still discernible (the ▸ glyph), states legible as text, box-drawing intact, nothing invisible. |

## Shakeout findings — before any run is recorded

Both walkthroughs so far are shakeout: they surfaced findings, the findings
were fixed, and no run is recorded against a build that is already superseded.
The recorded runs below judge one build, which is the whole point of the
commit-SHA-per-run rule.

The Windows shakeout (2026-08-10, nine findings) is written up in `tasks.md`
under T3.8. The macOS shakeout follows.

### macOS — 2026-08-10

| | |
|---|---|
| OS / version | macOS 26.5.2 (25F84) |
| terminal host | Ghostty 1.3.1 |
| vincent commit | `3a2d361` |
| `claude --version` | 2.1.226 (Claude Code) |
| resolved `$EDITOR` | `nano` |
| mode | real claude on the question leg |

Section A completed — the §19 loop ran end to end without leaving the TUI —
with three findings. None blocked the loop, so by the grading rule none fail
the gate; all three are fixed here rather than deferred, because two of them
are one-line geometry and one is dead message routing.

1. **Clicking a task row selected the task two rows above it** (S14, L5/L6):
   you had to click a few lines *below* the row you wanted. `updateClick`
   derived the table's first-row line from `board.chromeLines()`, which is a
   *height budget* — it counts the header, the status line **and the action
   line rendered below the table** — so the click origin sat two lines too
   high and every click resolved to the wrong row, or to nothing at all near
   the top. The geometry above the rows is now its own `board.firstRowLine()`,
   and the regression test locates the target line in the **rendered frame**
   instead of recomputing the same arithmetic the code under test uses — the
   old test agreed with the bug because it shared the formula.
2. **Paste did nothing, anywhere** (L2, S5, S6). L2 says "paste the seeded
   app-repo path" and there was no way to do it. Bracketed paste reaches
   Bubble Tea as `tea.PasteMsg`, which is neither a key nor a mouse event, so
   the root took its default route and *broadcast* it to every view; the views
   dispatch on `tea.KeyPressMsg` and dropped it. `ctrl+v` failed differently:
   bubbles' textinput answers it with an unexported message only textinput
   understands, so a root that routes by message type cannot deliver it
   either. Paste now follows the key routing to the one field holding the
   keyboard (palette → active view → nothing), and `ctrl+v` reads the system
   clipboard into the same path for terminals that pass the key through.
   A paste with no field under it is dropped, never replayed as keystrokes —
   pasting a path onto the board would otherwise fire `a`, `c` and `r` as task
   actions.
3. **The gate's own launch block poisoned later `go test` runs.** The banner
   told you to `export FAKEAGENT_DELAY_MS`, `FAKEAGENT_SCENARIO_CODEX` and the
   two `VINCENT_*` dirs; they outlive the walkthrough, and the next
   `go test ./...` in that shell inherits them — `cmd/fakeagent` and
   `internal/agent/codex` then fail with what read exactly like real
   regressions (a 25-second delay and a pinned scenario). The POSIX block is
   now one-shot `env VAR=… vincent`, and the teardown line tells PowerShell
   how to clear its session.

Also fixed while enumerating the fields that can receive a paste: the new-task
form's key/value editor was missing from `capturesInput()`, so typing `q` into
a field name quit the TUI.

## Results

One block per run. A run records the build it judged: any loop-blocking fix
invalidates every earlier run.

<!-- Template — copy per run.

### <OS> — YYYY-MM-DD

| | |
|---|---|
| OS / version | |
| terminal host | |
| vincent commit | |
| `claude --version` | |
| resolved `$EDITOR` | |
| mode | real claude / rehearsal |

Section A: L1 … L10 — pass / fail
Section B: S1 … S13 — pass / fail / n-a

**Findings**

| Item | What | Disposition |
|---|---|---|
| S? | | fixed in PR N / T4.x |

**Verdict:** GATE PASS / GATE FAIL

-->

Both recorded runs judge **`40fbffe`** — the first build carrying every
shakeout fix above (mouse row hit-testing, paste routing, one-shot launch env).
Nothing loop-blocking was found in either, so neither run invalidates the other.

### Windows 11 — 2026-08-10

| | |
|---|---|
| OS / version | Windows 11 |
| terminal host | Windows Terminal (pwsh); Git Bash/mintty for the second S14 host |
| vincent commit | `40fbffe` |
| `claude --version` | 2.1.226 (Claude Code) |
| resolved `$EDITOR` | unset → `notepad` (platform default, `internal/tui/editor.go`) |
| mode | real claude on the question leg |

Section A: L1–L10 — **pass** (the §19 loop completed without leaving the TUI).
Section B: S1–S18 — **pass**.

**Findings**

| Item | What | Disposition |
|---|---|---|
| — | none | — |

Everything surfaced by the Windows shakeout (the nine findings written up under
T3.8 in `tasks.md`) was already fixed in this build and re-checked here: the
task table keeps updating while the new-task form is open, clicks land only on
rendered rows, the Projects table degrades instead of overflowing, `?` shows the
focused surface's keys with its own footer, the palette's sections are ruled,
and the new-task form names the workflow-default agent.

**Verdict:** GATE PASS

### macOS — 2026-08-10

| | |
|---|---|
| OS / version | macOS 26.5.2 (25F84) |
| terminal host | Ghostty 1.3.1 |
| vincent commit | `40fbffe` |
| `claude --version` | 2.1.226 (Claude Code) |
| resolved `$EDITOR` | unset → `vi` (platform default, `internal/tui/editor.go`) |
| mode | real claude on the question leg |

Section A: L1–L10 — **pass** (the §19 loop completed without leaving the TUI).
Section B: S1–S18 — **pass**, with S14 read as its POSIX equivalent: the mouse
items are exercised in Ghostty, not on the two Windows hosts, which the Windows
run above covers.

**Findings**

| Item | What | Disposition |
|---|---|---|
| — | none | — |

The three macOS shakeout findings are fixed in this build and re-checked here:
a click selects the row under the cursor (S14), bracketed paste and `ctrl+v`
both fill the focused field (L2, S5, S6), and the gate banner's launch block no
longer exports `FAKEAGENT_*` into the shell that later runs `go test`.

**Verdict:** GATE PASS

## Friction record dispositions (T3.9)

`tui-friction.md` requires every finding it recorded before the refactor to come
back here carrying **fixed**, **deferred** to a Phase 4 ID, or **won't-fix**.
Judged against the same `40fbffe` walkthroughs; the sweep item that exercised
each one is named, so a disposition is a thing that was looked at, not a thing
the mechanism's PR claimed.

| Finding | Disposition | Seen in |
|---|---|---|
| F1 42 keys, no complete on-screen list | fixed — palette is searchable by intent and shows each entry's direct key | S16 |
| F2 `?` is a full-screen 45-row modal | fixed — `?` is the focused surface's keys plus globals, framed and titled | S1 |
| F3 bindings in three places, already drifted | fixed — one registry feeds palette, footer and `?`; drift is a test failure | S1, S16 |
| F4 `1..6` navigation with no legend | fixed — `1..6` retired; takeovers are palette entries under a **views** section | L2, L3, S16 |
| F5 board↔detail modal round-trip | fixed — one screen; the queue and a live tail are visible together | L6, L7 |
| F6 same key, different meaning per view | fixed — footer names the focused panel's keys | S1, S9 |
| F7 only task actions get a hint | fixed — the action bar generalised into the footer for every panel | S1, S4 |
| F8 no way to *go* to an attention task | fixed — `!` jumps, surfaced only when the count is non-zero | L8 |
| F9 no mouse | fixed — focus, row select, wheel, hints, tabs, `M` to disable | S14 |
| F10 no command surface | fixed — `:` opens on every surface | S16 |
| F11 `esc` overloaded and positional | fixed — explicit popup → takeover → filter → no-op stack; never quits | L8, S5, S15 |
| F12 answer form is a stop in a `tab` cycle | fixed — a popup with a row badge and a footer hint | L8, S5 |

No finding is deferred or won't-fix. The one item T3.8 deferred is not from this
record: naming what "(workflow default)" resolves to for **model and effort** is
**T4.7**, which needs the API to report §8.6 resolution per step — the agent half
was fixed during the Windows shakeout.

### Gate result

**T3.8 passes.** §19's M3 acceptance holds on Windows 11 and macOS against one
build, `40fbffe`. Linux keeps its coverage from CI — the full Go suite plus the
M1 and M2 gates on every PR.
