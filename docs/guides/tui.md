# Using the TUI

`vincent` with no arguments opens the terminal UI, starting a daemon if none is
running. It is a pure API client: it holds no state the daemon does not have,
and quitting it never affects work.

```sh
vincent          # opens the TUI
```

- [The first run](#the-first-run)
- [Layout](#layout)
- [The board](#the-board)
- [Task detail](#task-detail)
- [Answering a question](#answering-a-question)
- [The takeover screens](#the-takeover-screens)
- [The command palette](#the-command-palette)
- [Every key](#every-key)
- [Mouse, selection and paste](#mouse-selection-and-paste)
- [When the daemon is unreachable](#when-the-daemon-is-unreachable)

---

## The first run

The very first launch shows a one-time notice: **agents run full-auto by
default and can execute arbitrary commands as you**. Acknowledge it once and it
does not return. The acknowledgment is stored in `{data_dir}/tui.json`.

It is worth reading rather than dismissing — [Security model](../security-model.md)
is the longer version.

## Layout

Two of the six views — the board and task detail, the daily loop — share one
persistent screen of three panels:

```
┌─ Tasks ──────────────────────────────────────────────┐
│  #12  api    add rate limiting   running   3/5  …    │   ← always full width
│  #13  web    fix flaky test      ● gate    2/4  …    │
└──────────────────────────────────────────────────────┘
┌─ Timeline ───────────┐┌─ Output │ Diff ──────────────┐
│  1 ✓ plan      1m2s  ││  … live tail …               │
│  2 ▸ implement 4m9s  ││                              │
└──────────────────────┘└──────────────────────────────┘
```

The task table drives everything below it: moving the selection moves the
timeline and the output pane with it. `tab` moves focus between panels;
`shift+tab` goes back.

The other four views — new task, projects, workflows, daemon — are full-screen
takeovers. `esc` closes one layer at a time (popup → screen → filter) and
**never quits**.

## The board

One row per task: id, project, title, state, current step `k/n` with its name,
elapsed, and cost so far. The header shows daemon status, agent availability,
running-versus-cap counts, and how many tasks need a human.

Three behaviors matter:

- **Tasks waiting on a human are pinned to the top** with a distinct badge —
  `awaiting_input`, `awaiting_gate` and `blocked`. `!` jumps to the next one from
  anywhere.
- **The terminal bell rings when a task enters `awaiting_input`**, so most
  terminals flash or badge the window even when it is not focused.
- **A task waiting on a clock says when it resumes.** A task whose agent hit a
  usage limit is `queued` like any other, but its state cell reads
  `queued → 14:20` — the time vincent will try it again, on its own. It holds no
  slot and needs nothing from you; the detail header names the reason in full
  (`queued · usage limit → 14:20`). See
  [Troubleshooting](troubleshooting.md#usage_limit--do-nothing).

`/` filters by id, title, project or state; `tab` commits the filter, `esc`
clears it.

Elapsed on the board is **wall clock** from the task's start. That is
deliberate: a task idle on a human for 35 of its 40 minutes must not read as
"5m" on the board whose job is to flag it. The per-attempt figures in the
timeline are the other measure — active time, with the excluded wait shown
beside it rather than silently subtracted.

## Task detail

`enter` opens the selected task. The **timeline** on the left lists every
attempt of every step with its duration, tokens and cost; the **output pane** on
the right shows the live tail of the running step.

Selecting an attempt in the timeline *is* how you read scrollback — that is why
the two panels are side by side and both always visible.

| Key | Does |
|---|---|
| `]` | Switch the output pane tab: Output ⇄ Diff (`[`, `]` and `d` all work) |
| `f` or `G` | Follow the live output again |
| `v` | More or less detail: compact → normal → verbose (reasoning, then unrecognized lines) |
| `e` | Open this attempt's **whole** transcript in `$EDITOR` |
| `↑`/`↓` | Scroll; scrolling up pauses follow |

### Reading the whole transcript

The output pane holds the **end** of a transcript — the last 256 KB, capped at
5000 records — because a single attempt is allowed to produce gigabytes. When a
step fails, the part you want is often the beginning, which is exactly the part
not on screen. The pane says so when it has dropped something:

```
… earlier output truncated — press e for the whole transcript
```

`e` hands the complete file to your `$EDITOR`, the same way `e` opens a
workflow file in the workflows view. What opens is the raw JSONL on disk —
the lossless record, including lines vincent's parsers do not recognize —
rather than the pane's rendering. It is the same file
`vincent task show <id>` prints the path of.

Two cases answer instead of opening: a step that never wrote a transcript (a
manual gate), and a transcript that retention has already
[pruned](../reference/configuration.md#transcript_retention_days). Neither
opens an empty buffer, because an empty buffer reads as "the step produced
nothing".

**Follow mode belongs to the live attempt.** It is unavailable on a finished
one, and a step advance moves your selection only if the cursor was already on
the live attempt — so reading an old step is never interrupted by a new one
starting.

The **Diff** tab is `git diff` against the merge-base with the base branch,
including uncommitted changes, syntax-highlighted. It is fetched when you
activate the tab and on an explicit refresh, never on every output chunk.

### The action bar

Below the panes, the action bar shows **exactly the actions valid in the
current state** — the daemon computes that list, the TUI renders it.

| Key | Action | Valid from |
|---|---|---|
| `a` | Approve the gate | `awaiting_gate` |
| `x` | Reject the gate | `awaiting_gate` |
| `r` | Retry the blocked step | `blocked` |
| `E` | Edit the step's prompt or command in `$EDITOR`, then retry | `blocked` |
| `s` | Skip the current step | `blocked`, `awaiting_gate` |
| `p` | Pause / resume | `queued`, `running` / `paused` |
| `c` | Cancel the task (asks first — a running step is killed) | most states |
| `A` | Archive (asks first — the worktree is removed) | `done`, `aborted` |

`E` opens the failing step's prompt or command in your editor, and the override
applies **to this task's snapshot only** — the workflow file is untouched.

What each action means in full is in
[Task lifecycle](../reference/task-lifecycle.md).

## Answering a question

When a claude step asks something mid-run, the task enters `awaiting_input` and
gets a badge on its row plus a footer hint. Press `enter` on the row to open the
answer form.

| Key | Does |
|---|---|
| `space` | Pick an option (toggles, for a multi-select question) |
| `e` | Type your own answer — options are suggestions, never a list |
| `enter` | Submit; the run resumes in the same session where it stopped |
| `esc` | Close without answering (what you picked is kept) |

The form is a popup, and it **never steals focus**: auto-opening under a
keystroke is how an answer gets lost. It announces itself and waits for you.

## The takeover screens

Reached from the command palette (`:`), except new task which keeps a direct
key.

### New task — `n`

Opens for the project you are looking at. A guided form: project → workflow
(with its description and step list, flagging steps whose agent is unavailable)
→ title → description → custom fields → base branch → priority → optional
agent/model/effort override.

| Key | Does |
|---|---|
| `enter` | Open the focused field's editor or picker |
| `e` | Edit the description in `$EDITOR` |
| `+` / `-` | Nudge the priority (higher runs first) |
| `R` | Re-probe the adapters (the list is otherwise cache-served) |
| `ctrl+s` | Create the task |

The override pickers are fed by live adapter data, tagged with where each option
came from, and always accept free text. They are windowed and filterable — you
type to narrow, which is what makes cursor's ~180-model catalog usable. Each
resolved field shows **which level won** (step, task, workflow, adapter), so the
form tells you what will actually run rather than what you typed.

### Projects

| Key | Does |
|---|---|
| `a` | Register a repository |
| `enter` or `e` | Edit the selected project |
| `d` | Remove it (asks first; its task rows go with it) |
| `/` | Filter by name or path |
| `ctrl+s` | Save, in the form |

### Workflows

The merged registry with scope badges and validation status.

| Key | Does |
|---|---|
| `enter` | Show the entry's steps |
| `e` | Open the file in `$EDITOR` — the view updates when you save |
| `R` | Re-read the registry |

The view **reads** the registry; it does not author it. New workflow files are
written in your editor and appear on the next reload.

### Daemon

Version, uptime, the config in effect, the adapters detected, and a live tail of
the daemon log. The view reports, it does not act — `vincent daemon stop` owns
stopping the daemon, and a TUI that auto-started one has no business killing it.

An **orphans** line appears in the identity block when the daemon has found
directories under its data dir that no task claims, naming the count and
`vincent gc`. It is a pointer, not a button: for the same reason this view does
not stop the daemon, it does not delete anything either. Nothing shows when the
count is zero.

| Key | Does |
|---|---|
| `R` | Re-read the daemon info, the config and the log |
| `f` or `G` | Follow the end of the log again |
| `↑`/`↓` | Scroll the log |

The log tail is read straight from `{data_dir}/logs/daemon.log` — the one place
the TUI is not a pure API client, because an endpoint cannot serve the log when
the daemon is the thing that died, which is exactly when the log is worth
reading.

## The command palette

`:` opens it. Everything reachable in the TUI is in there by name — navigation
to every screen, and every task action the daemon currently offers. Type to
filter, `enter` to run, `esc` to close.

The palette exists so the four takeover screens do not need memorized number
keys. If you cannot remember a binding, `:` and `?` are the two keys worth
knowing.

## Every key

`?` toggles a help overlay listing every binding for the surface you are on.
The overlay, the palette and the footer all render from **one registry** in the
source, so a key that exists is a key that is documented.

Global bindings — active whenever the focused surface is not capturing text:

| Key | Does |
|---|---|
| `:` | Command palette |
| `?` | Toggle help |
| `tab` / `shift+tab` | Move focus between panels; commits a filter |
| `!` | Jump to the next task needing a human |
| `n` | New task |
| `M` | Toggle the mouse |
| `esc` | Close one layer: popup → screen → filter — never quits |
| `q` | Quit the TUI (the daemon keeps running) |
| `ctrl+c` | Quit |

## Mouse, selection and paste

The mouse is on by default: click to select, scroll to scroll. That takes mouse
events away from your terminal, so **native text selection needs the mouse
off** — press `M`, or hold shift while dragging, which most terminals treat as
"bypass the application".

Paste normally arrives as a bracketed paste from your terminal's own binding
(`Cmd+V`, `Ctrl+Shift+V`, middle click) and lands in the focused field with no
key involved. `ctrl+v` is the fallback for terminals that pass the key through
instead.

## When the daemon is unreachable

The TUI does not pretend. It shows the disconnected state in the header, keeps
the last data it fetched visible rather than blanking the screen, and reconnects
on its own when the daemon comes back — SSE reconnection resumes durable events
from where it left off, so nothing is missed.

The daemon view stays useful throughout: its log tail is read from disk, which
is the one thing still true to show when the daemon has died.

---

## See also

- [Scripting vincent](scripting.md) — everything here, without a terminal.
- [Task lifecycle](../reference/task-lifecycle.md) — what the actions do.
- [Troubleshooting](troubleshooting.md).
