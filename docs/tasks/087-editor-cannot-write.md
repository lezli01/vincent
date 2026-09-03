# 087 — The editor cannot change most of a workflow

**Status:** ✅ done (4/4)
**Issue:** #320 (with [086](086-editor-and-graph-misreport.md), which is the
read half)
**Amends:** §15 view 5 — "there is **no delete**: the view gains no destructive
action" is narrowed to *no unconfirmed* destructive action, and the editor's key
table gains `a`, `d`, `K`/`J` and the multi-line pane's `ctrl+s`
**Keeps, without relitigating:** [065](065-workflow-editor-in-the-tui.md)
decision 2 (the wire carries edit operations, not YAML), decision 3 (forms
rendered from the served descriptor), decision 4 (a stale write is refused, so
every operation carries the version the last read handed back) and decision 6
(`e` still means `$EDITOR`; the editor's keys live in `ctxWorkflowEditor` and
never reach the list layer)

Everything the editor could see, it could look at. Committing a `prompt:` or a
`run:` flattened its newlines and wrote the body back as one line; four keys
existed in the editor's context and none of them added, removed or reordered
anything; `internal/workflow/edit.go` had implemented `insert`, `remove` and
`move` in 065.1 and had no caller anywhere; and closed sets — agent, model,
effort, a mapping — were typed as free text, so a `env:` row committed a quoted
scalar over a mapping and the daemon refused the file.

## Decisions

1. **The multi-line pane is the flattening fix, and there is no interim
   guard.** *2026-09-03.* The alternatives — refusing to open a one-line field
   on a multi-line value, or no-op'ing a commit whose text is unchanged — were
   declined: both leave the row unable to do the thing a person opened it to
   do. `prompt:`, `run:` and `instructions:` get a real full-pane editor with
   true newlines, saved as a `|` block scalar. It is a **new component beside**
   `textField`, not a mode on it: that widget's one-line flattening is #299's
   deliberate behaviour and every other caller wants it.

2. **Removing a step or a lane is allowed, and takes a confirmation.**
   *2026-09-03.* §15 view 5's "no delete" is read as *no unconfirmed
   destructive action*, not as "the editor may not remove anything" — the
   sentence was written to refuse deleting a **workflow file**, and it still
   refuses that. `d` prompts; `set`, `insert` and `move` commit on `enter` as
   they always have, because each of those leaves what it replaced in the
   file's history and a removal does not. §15 is amended in place so the next
   reader sees the sentence was narrowed deliberately rather than overrun.

3. **The new keys are `a` / `d` / `K` / `J`, and `a` opens a type picker.**
   *2026-09-03.* `a` matches the list layer's `a` and the projects view's; `K`
   and `J` are capitalised so they cannot be confused with `k`/`j` navigation,
   because a file reordered by a typo is not a trade worth making. On a steps
   list `a` offers only the types the served descriptor marks legal for **that
   path's** context and then writes a skeleton carrying that type's required
   fields — the daemon re-parses the whole file on every PATCH, so a step the
   form itself created must be valid on arrival or the next edit is refused for
   a fault the form introduced. No free-text type row: a type the descriptor
   does not accept is a 400 the form must never offer (065 decision 3).

4. **The overlay owns every key while it is open, its own `esc` included.**
   *2026-09-03.* What "cancel" and "save" mean belongs to the value being
   edited — `enter` is a newline in a pane and a choice in a picker — and the
   layer above has no way to tell which without asking. The alternative,
   handling `esc` centrally, forces every overlay to share one answer to a
   question they genuinely differ on.

5. **The pickers read `GET /v1/agents`, not the descriptor.** *2026-09-03.*
   065 decision 3 notes that the agent, model and effort sets are deliberately
   not in the workflow schema — they are a property of the host's installed
   CLIs, not of §8.2 — so reading them from the descriptor would mean putting
   them there. The new-task form's picker is lifted to a shared component and
   pointed at the same endpoint.

## Phases

- **087.1 — The multi-line pane.** ✅ `textPane` beside `textField`
  (`internal/tui/textpane.go`), the `wfEditorPane` overlay, `FullPane()`
  rendering, and the dirty latch that makes open-then-close write nothing.
  Closes the data loss.
- **087.2 — Reach the unreachable blocks.** ✅ The breadcrumb resolver
  generalised to `lanes[i]`, `lane`, `merge`, `fields[i]`, `defaults` and
  `container` alongside `steps[i]`; `descend` on `fields:` and `defaults:`; the
  lane, merge, declared-field, defaults and container forms
  (`internal/tui/workfloweditorforms.go`).
- **087.3 — Structural edits.** ✅ `a`/`d`/`K`/`J` emitting `insert`/`remove`/
  `move`, the type picker and its skeletons, the confirmation overlay
  (`internal/tui/workfloweditorstructure.go`), and `scripts/m13-gate.sh`
  scenarios 8–11 driving all three over the API and asserting byte fidelity
  including CRLF.
- **087.4 — Richer controls.** ✅ The agent/model/effort picker
  (`internal/tui/workfloweditorpicker.go`), the `map` key/value sub-form, and
  int and duration validation on the row before the PATCH.

## Not in scope

No delete of a workflow **file** or registry entry — decision 2 narrows the
sentence to permit removing a step, not to permit removing the workflow. No
editing from the graph: 065's *"out of scope"* holds and the graph stays a
read-only projection. No CLI counterpart. No change to
`skills/vincent-workflows/SKILL.md` or `internal/workflow/builtin.go`, per 065's
*"deliberately not amended"* — this adds no field, no step type and no
semantics, so CLAUDE.md's built-in rule does not fire.
