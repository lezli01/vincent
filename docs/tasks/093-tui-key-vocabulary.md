# 093 — A key vocabulary for the TUI, enforced by the registry

**Status:** ✅ done (6/6)
**Issue:** #353
**Amends:** §15 — the key vocabulary table and its three clauses; view 5's `e`
sentence narrowed to name its `enter`-alias exception; view 7's create key
`c` → `a`; view 10's date window `d` → `s`; the chats board's key list.
Decision record row 31.
**Keeps, without relitigating:**
[011](011-bulk-actions.md), [025](025-repair-with-an-agent.md) and
[027](027-follow-up-runs.md) — the §6 action letters `p a x r E R s c A F` are
live on the board and on every tab of the task workspace, and **not one of them
moves**. That constraint is what decides every contested case below.
[025](025-repair-with-an-agent.md) in particular: `R` is repair in the task
workspace, "free in the task workspace, where the takeover screens that use it
for re-probing never are". That partition is not undone; it becomes clause 2.
[049](049-command-palette.md) — retiring `1..6` without substituting new
memorized keys, untouched. [067](067-chats-in-the-tui.md) — `n` is §15's one
deliberate two-meaning key, untouched.

## The problem

`internal/tui/bindings.go` is the single source `?`, the footer and the palette
all render from, and that is precisely how the drift stayed invisible. The help
was never wrong; the *vocabulary* was. It faithfully advertised four different
keys for "refresh" — `R` on daemon, projects, pull requests, workflows, the
graph, the editor and the adapter probe; `r` on chats, archived chats and the
task workspace's Pull Request tab — and two of those hints were the same word
in two cases, `R reload` beside `r reload`.

§15 already stated the principle: "a key that means two unrelated things
depending on the row is worse than one that sometimes does nothing", and the
structured editor took its own context because sharing "would give one key two
meanings depending on the view". Both were applied per view. Nothing applied
them across views, so every new surface guessed, and the registry grew one
guess at a time.

## Decisions

**1 (2026-09-10). The rule is three clauses, not one.** The issue asked that
"each vocabulary operation uses exactly one key registry-wide". Taken literally
that breaks task 025's recorded partition of `R`. So:

1. A key may be shared **only** when it means the same operation. Archive is
   `A` whether it is a §6 action on a task or the chats board's own key — that
   is the vocabulary working, not a collision.
2. A key may mean two different things **only** where the registry can prove
   the two surfaces never co-exist. A takeover offers no `available_actions`,
   so `a` may add on projects while `a` approves a gate; the task workspace is
   **not** disjoint from the action set, so nothing there may reuse an action
   letter.
3. A key already carrying a vocabulary term may not be given a second meaning
   on a new surface.

Beat: the literal one-key rule, which would have forced `R` or repair to move
and relitigated a design session. Clause 2 is task 025's finding promoted from
an accident to the rule, and it still resolves all four of the issue's groups.

**2 (2026-09-10). The §6 letters decide the contested cases.** `R` won refresh
because it already held seven surfaces and §15 already said "`R` re-reads a
registry or the daemon blocks". `A` won archive because it is the action
letter. `D` is destruction of something persisted and `d` is an edit to an
unsaved draft — so the projects view's remove goes `d` → `D`, while the
workflow editor's and the new-task Fields editor's stay `d`: those drop a row
from a draft, which is undone by not saving. After the inversion `d` never
touches anything persisted and `D` always does, which is what makes pressing
`d` on an archived board safe.

Beat: moving an action letter, which decision 1 rules out; and leaving `d` on
the date window, which is the dangerous case the issue named.

**3 (2026-09-10). The date window is `s`, not a new term.** `s` already means
"cycle what this list is showing" on the chats board and the pull-request list.
A date window is that gesture on a third list, so the table loses a row instead
of gaining one. `s` is skip as a §6 action and the surfaces are provably
disjoint: `archived` is never a `from` state in `taskstate`'s transition table,
so `HumanActionsFrom` returns nothing for an archived row and no action key
fires on either board. The test derives that rather than asserting it.

**4 (2026-09-10). The Pull Request tab loses its refresh key outright.** `R` is
repair there and does not move; the tab already re-reads on its own timer,
which its own label said. Dropping the key is what removes the complaint rather
than relocating it — and it is half of a real bug: `updatePullTabKey`
intercepted `r` and `c` before falling through to `t.detail.update(msg)`, whose
own comment claims "task actions stay reachable from here". They were not.
**Retry and cancel were unreachable from that tab**, while the root handed the
footer `t.target()` so the footer advertised `r retry` and `c cancel` on it.
The check moved to `enter` — the cursor is on a check, and `enter` opens what
the cursor is on — and both rebinds together close it.

**5 (2026-09-10). Free text is `t`, a new term.** `e` means `$EDITOR` on every
form a picker can be raised over, so the two meanings had to be told apart by
key rather than by which layer happened to be open. `t` was unused as a bare
key. It lands on the answer form and on all four contexts a picker offering a
free-text row can be open in — and the picker's offer, printed on screen since
it was written, gets a registry row for the first time.

Beat: leaving `e` polysemous and disambiguating by layer, which is exactly the
"one key, two meanings depending on the view" the workflow editor's own context
was created to avoid.

**6 (2026-09-10). The vocabulary term lives on the registry row.** The
alternative was a test that reads each row's *label* and infers the operation
from its wording — which makes every future label edit a test edit, and infers
wrongly the first time a label says "archived" while meaning "listing". A
`term` field on `binding` is one word per row, is what the failure message
prints, and makes "does this row perform a shared operation?" a fact the
registry states rather than one a regex guesses.

A row with no term is surface-local, and that is deliberate: `link this pull
request`, `fork the entry`, `group the tasks` appear once each, and a
vocabulary that named them would be a list of every key rather than a rule
about the shared ones. Fold and page are in §15's table but carry no term for
the opposite reason — each is a *set* of keys by design, which is not a shape
"one key per term" can describe.

**7 (2026-09-10). The Fields editor becomes a registered context.**
`ctxNewTaskFields`, for the reason `ctxWorkflowEditor` has one: nothing the
form underneath offers means the same thing inside it. Its `a` and `d` were
handled, hinted inline and documented in the guide, and the registry had never
heard of them — so no registry test could see them and `?` did not list them.
The inline hint line goes with the registration, because the footer then
renders from the registry like every other surface.

## Tasks

- [x] **093.1** The vocabulary in `internal/tui/bindings.go`: the table as a
      comment citing §15, the `vocabularyTerm` type and the `term` field, and
      every row that performs a shared operation tagged with it.
- [x] **093.2** The rebinds and their dispatch sites: `chats.go` (`a`→`A`,
      `r`→`R`, `d`→`s`), `board.go` (`d`→`s`), `projects.go` (`d`→`D`),
      `pullrequests.go` (`c`→`a`), `taskpulltab.go` (`c`→`enter`, `r` dropped),
      `answerform.go` and `newtaskpicker.go` (`e`→`t`).
- [x] **093.3** `ctxNewTaskFields`: the context, its six rows, `newTask`'s
      `bindingContext()`, the root's dispatch, and the inline hint removed from
      `newtaskrender.go`.
- [x] **093.4** The three enforcement tests in `bindings_test.go`, beside
      `TestEveryPanelKeyIsHandled`: one key per operation, no row spelling its
      operation a second way, and shadowing only on surfaces proven disjoint
      from `taskstate.HumanActionsFrom`.
- [x] **093.5** Probes for every new and rebound key, including the four
      free-text `t` probes and the six Fields-editor rows.
- [x] **093.6** The docs: §15's Keys section, views 5, 7 and 10, decision
      record row 31, and `docs/guides/tui.md`'s key tables.

## What this does not do

No user-configurable keymap in `config.yaml`. It is much larger scope and does
not answer the question — it moves the choice of a consistent default onto
every user. Worth its own issue; it is not a substitute for this one.

No aliasing. Keeping every current key and adding a standard second one doubles
what `?` has to explain and leaves `d` still cycling the window on the archive,
which is the dangerous case.

The converse of 093.5 — proving every key a view *handles* is registered — is
still out of reach, because a handler cannot be enumerated from the registry
side. The allow-listed vim aliases are the known residue; registering the
Fields editor closed the one surface where the gap was hiding a whole
vocabulary pair.
