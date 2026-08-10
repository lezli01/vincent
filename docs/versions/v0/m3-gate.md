# M3 phase gate — walkthrough and results (T3.8)

Spec §19 M3 acceptance: *the full loop (register → author workflow\* → run 3
parallel tasks → answer an agent question → approve gate → archive) is doable
without leaving the TUI.*

M1 and M2 are asserted by `curl` in CI. M3 cannot be: its acceptance is a
judgement about a terminal program, and the Phase 3 decision ruled out driving
one from tests. So this is a human walkthrough. `scripts/m3-gate.sh` seeds the
world and prints the launch instructions; everything below is done by hand, on
**Windows 11 and macOS**, both runs on the PR branch before it merges.

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
| **L7** | Open one running task (`enter`), watch the output pane. | Output is still arriving while you watch; `f`/`G` re-follows after you scroll away. Press `d` for the diff — it shows the agent's README.md edit. |
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
| **S3** | "diff view" | `d` on a task with no commits yet, on a finished task, and on an archived one (worktree removed) — three different situations, three intelligible messages. |
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
Section B: S1 … S18 — pass / fail / n-a

**Findings**

| Item | What | Disposition |
|---|---|---|
| S? | | fixed in PR N / T4.x |

**Verdict:** GATE PASS / GATE FAIL

-->

### Windows 11 — 2026-08-10

| | |
|---|---|
| OS / version | Windows 11 Enterprise 10.0.26200 |
| terminal host | Windows Terminal + pwsh |
| vincent commit | `3a2d361` |
| `claude --version` | 2.1.226 (Claude Code) |
| resolved `$EDITOR` | `notepad` (`VISUAL` and `EDITOR` both unset, so `editorCommand()` took its Windows fallback) |
| mode | real claude |

Section A: L1 … L10 — **pass**. The §19 loop completed without leaving the TUI.

Section B: S1 … S18 — **pass.** S14 failed on the first pass, was fixed in this
PR, and was re-checked on the fixed build.

- **S14 failed, then passed.** Clicking a task row selected a row **two above**
  the one clicked; reaching a given task meant clicking two lines below it, and
  the top two rows were unreachable entirely (the move clamps). Fixed in this
  PR — see the findings table — and re-checked at the terminal on the fixed
  build, where a click now lands on the row under the pointer.
- **S14 coverage stays partial.** Mouse was judged in Windows Terminal + pwsh
  only, before and after the fix. The second Windows host the item names —
  Git Bash/mintty — has not been walked, so a non-console host's mouse
  reporting is still unobserved.

**Findings**

| Item | What | Disposition |
|---|---|---|
| S14 | Click-to-select landed two rows above the click. `shell.updateClick` skipped `board.chromeLines()` to find the first table row, but that is the table's *vertical budget* — it counts the action line and the shell's blank **below** the table as well as the header above it. The offset needed only the lines above. Split into `headerLines()` (what is drawn above the table) with `chromeLines()` now derived from it, so the two cannot drift apart again. | fixed in this PR; re-checked at the terminal on the fixed build, passes |

Why the existing tests did not catch it: `TestShellClickSelectsRow` and
`TestShellClickBelowTheRowsIsIgnored` built their click coordinate from
`chromeLines()` — the same expression `updateClick` subtracts — so the error
cancelled itself and the assertions held against a broken screen. Both now use
`headerLines()`, and a new `TestShellClickLandsOnTheRowUnderTheCursor` reads
each row's position **off the rendered frame** and clicks there, which is the
only formulation that could have failed. It fails by exactly two rows against
the old code.

This is the second defect in the same click path: the earlier unrecorded
walkthrough found clicks below the last row selecting the last task. A mapping
from pixels to rows has no natural test that does not restate it, which is why
the new one asserts against rendered output instead.

The nine findings from the unrecorded first Windows walkthrough (see T3.8 in
`tasks.md`) were all fixed before this run, so this run judges the fixed build
and they are not repeated here.

**Verdict:** GATE PASS (Windows). S14's failure did not block Section A — the
§19 loop is keyboard-driven throughout — so under the grading rule it never
threatened the gate, and it is fixed and re-checked regardless. Not yet a gate
pass overall: the gate is Windows **and** one POSIX OS, and macOS has not been
walked.

Three notes on the record itself:

- **The S14 fix moved the build, and only S14 needed re-checking.** The grading
  rule re-walks *both* OS runs only for a loop-blocking fix; this was not one.
  The fix is also provably narrow: `chromeLines()` returns exactly what it
  returned before (`headerLines() + 2`), so the table's height budget and every
  rendered frame are unchanged — only the click offset moved. So the Windows
  run above stands as recorded, S14 was re-checked at the terminal and passes,
  and the macOS run judges this same fixed commit.

- The "both runs on the branch before it merges" rule in the header is no longer
  literally satisfiable: T3.9–T3.13 merged first, so this run judges `master` at
  `3a2d361` rather than a PR branch. The rule's substance — *both runs judge one
  build* — still binds, so the macOS run must be against `3a2d361`, and anything
  that lands before it re-walks both.
- Section A passing on one OS is not the gate. T3.8 stays `[~]`.


