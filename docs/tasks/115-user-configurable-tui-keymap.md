# 115 — A user-configurable TUI keymap

**Status:** ✅ done (6/6)
**Issue:** [#412](https://github.com/lezli01/vincent/issues/412)
**Spec:** amends §12.3 (`tui` example and prose), §15 Keys; decision record row
35 added, row 31 narrowed
**Amends:** [046](046-notify-hook.md) decision 4 — `internal/config`'s one
internal import becomes two (decision 3 below)
**Keeps, without relitigating:** [093](093-tui-key-vocabulary.md) decision 2 —
the §6 action letters `p a x r E R s c A F` do not move **as defaults**. That
decision governs the shipped keymap, and nothing here changes it. 093's "No
aliasing" is carried over as decision 6.

## Problem

Task 093 standardized the TUI's keys — one operation, one key, enforced by the
binding registry — and deliberately left a keymap in `config.yaml` out: "much
larger scope … Worth its own issue; it is not a substitute for this one"
([093](093-tui-key-vocabulary.md), "What this does not do"). Decision record row
31 said the same. Neither rejected it; both **deferred** it, and named #412 as
its home. Nothing implemented it.

The objection 093 recorded — a keymap "moves the choice of a consistent default
onto every user" — is answered by the design below rather than set aside: an
override rebinds an *operation* everywhere that operation appears, and the same
three §15 clauses that hold the defaults hold the override.

What makes this large is not the config key. The registry in
`internal/tui/bindings.go` only **rendered** — `?`, the footer and the palette
read it — while every view dispatched on hard-coded strings: about 440
`case "R":` sites across 41 files, and no `key.Matches` anywhere. A keymap that
the help advertises and the handlers ignore would recreate exactly the bug 093
closed on the Pull Request tab, where the footer advertised `r retry` and
`c cancel` and neither key reached the task.

## Decisions

### 1 (2026-09-17). An operation is a vocabulary term, a §6 action, or a global

The rebindable set is:

- the twelve §15 vocabulary terms: `refresh`, `archive`, `delete`,
  `draft_remove`, `add`, `editor`, `free_text`, `browser`, `open_row`, `scope`,
  `filter`, `lane`;
- the §6 actions: `pause` (one id for pause and resume, which are one key
  today), `approve`, `reject`, `retry`, `edit_retry`, `repair`, `skip`,
  `cancel`, `follow_up`. `archive` is one id shared with the term, which is
  clause 1 working;
- the global chrome: `palette`, `palette_alt`, `help`, `help_alt`,
  `next_attention`, `mouse`, `quit`, `new`.

`new` also drives the chats board's `n`, because §15's one deliberate
two-meaning key is the same gesture — "make a new one here" — and must not split
under a keymap. One override rebinds its operation on **every** surface that
carries it, so a user's keymap still obeys "one operation, one key".

Fixed, and refused by name if they appear in `tui.keys`: the surface-local rows
that carry no term (`g` group, `L` lanes, `m` merge, `X`, `i`, `u`, `P`, `U`,
`S`, …); the multi-key sets 093 decision 6 already kept out of the terms (fold
`←`/`→` `C`/`O` `space`, page `<`/`>`, `↑`/`↓`, `K`/`J`, `[`/`]`); `esc`, which
is the layer stack; `ctrl+c`; `ctrl+v`; `tab`/`shift+tab`; the popups' `y`/`n`
confirmations; and the unregistered vim aliases (`h j k l f b u G`, and the
output tab's `d`).

As built (115.1), a name a reader might reasonably try for a fixed key —
`group`, `lanes`, `merge`, `fold`, `fold_all`, `page`, `select`, `views`,
`back`, `esc`, `close`, `interrupt`, `paste`, `tab`, `confirm`, `decline`,
`yes`, `no`, `resume`, `follow` — is refused as "not rebindable" with the
reason; any other unknown name is refused as an unknown operation, listing the
ids that exist.

### 2 (2026-09-17). The registry becomes the dispatch source

Handlers stop matching string literals and ask the registry for the effective
key of an operation in their context — a Go expression switch
(`case km.key(opRefresh):`) or an equivalent matcher; the exact helper is an
implementation choice.

*Beaten:* a translation layer at the root that rewrites a pressed key into the
default before any view sees it. That would be a second source of truth for
which context is live, and every popup that owns the keyboard would have to be
modelled twice. Dispatching from the registry also brings 093's "the converse …
is still out of reach" — proving every *handled* key is registered — within
reach for the rebindable operations, and the refactor adds that test for them.

### 3 (2026-09-17). The catalog lives in a new leaf package, and the daemon refuses a bad keymap

`internal/keymap` (with a `doc.go` citing §12.3 and §15) holds the operation
ids, their default keys, the surfaces each is answered on, which surfaces have
§6 actions live beside them, which surfaces capture text, the fixed keys and
aliases, the recorded exceptions, the key-string parser, the clause checker and
the effective-keymap builder. It imports nothing internal.

`internal/config` imports it to validate `tui.keys` at load, on hot reload (a
rejected reload keeps the last good configuration, as today), and on
`PATCH /v1/config` — and therefore on `vincent config set`. The file stays
byte-identical on a refusal, exactly the `tui.board.group_by` precedent.
`internal/tui` builds its registry rows on the catalog's ids and defaults.

This **explicitly amends task 046 decision 4** ("config imports `taskstate`,
and only `taskstate`"): `config` now imports `taskstate` and `keymap`, both
leaves, so the dependency direction stays one-way. The amendment is written
where decision 4 is cited — `internal/config/doc.go`, the comment on
`Notify.validate` in `internal/config/config.go`, CLAUDE.md's Architecture
paragraph, §12.3's `notify:` comment — and in the task 046 record, not
silently.

The clause-2 disjointness the archived boards rely on stays **derived from
`taskstate.HumanActionsFrom`** in `bindings_test.go`. The catalog carries the
per-surface "actions live" fact as data and that test proves the data against
the FSM, so `keymap` does not need to import `taskstate`.

*Beaten:* validating only in the TUI. `vincent config set` and a hand edit
would then accept a keymap no TUI can honour, and the TUI would have to guess
what to do with it.

### 4 (2026-09-17). The validator is the three §15 clauses, one checker for defaults and overrides

The checker the registry tests run over the shipped defaults is the same
function config runs over the effective keymap — the defaults with the
overrides applied. An override is refused when its key collides with anything
live beside it on any surface the operation appears on (globals, §6 actions on
the task surfaces, fixed panel rows, reserved aliases), or already carries a
different meaning on any surface, even one never open at the same time (clause
3). The default registry's recorded exceptions are catalog data, so the
defaults pass their own checker. Unknown operation ids and fixed operations are
refused by name. Error messages name the operation, the key, and the meaning it
collides with, in the house style of `tui.board.group_by: …`.

As built (115.1), `keymap.Check` indexes every key the catalog and the fixed
list carry and refuses a key that already means anything else anywhere — which
subsumes "live beside it" — unless a recorded exception on that exact key names
both operations or, for a fixed meaning, allows one. The exceptions are tied to
the key, not to the operations, so **an exception does not travel with a moved
operation**: `refresh: r` is refused naming retry, although `r` carries both
retry and retry-connecting by default. The recorded exceptions are `R`
(refresh and repair, task 025), `a` (add and approve), `s` (scope and skip),
`l` (lane beside link and vim-right), `e` (editor beside `enter/e` on the
projects and daemon screens), `d` (draft_remove beside the output tab's alias),
`n` (new beside the popups' no), `r` (retry beside retry-connecting) and
`enter` (open_row beside every surface's own `enter`). Fixed keys are not
checked against each other — none of them can be moved, so no override can make
a new collision among them. Every problem in one map is reported at once,
sorted and joined with `; `, and `internal/config` prefixes the result with
`tui.keys: `.

### 5 (2026-09-17). A key the composer owns is refused

For any operation that appears in a context that captures text — the chat
workspace's composer, the new-chat draft, the popups' text rows — a printable
key (no ctrl or alt modifier, not a function or navigation key) is refused. The
text-field escape hatches `palette_alt` (`ctrl+p`) and `help_alt` (`f1`) may be
rebound only to non-printable keys, because their whole reason to exist
([076](076-reader-actions-on-assistant-markdown.md) decision 7,
[114](114-help-in-text-fields-and-list-wheel.md) decision 1) is to work while a
text field has the keyboard. `ctrl+v`, `esc` and `ctrl+c` are fixed (decision
1).

As built (115.1), "printable" is one character, or `space`, with neither `ctrl+`
nor `alt+`. The two escape hatches carry a `Typing` mark that refuses a
printable key for them wherever they are answered, which covers every text
field they work in, the popups' included. The catalog also marks the chat
workspace and the new-chat form as capturing text — the surfaces whose field
owns every printable key for as long as they are up — and refuses a printable
key for any other operation answered there. None is today, so that half of the
rule guards the next operation to be given a key on those surfaces.

### 6 (2026-09-17). An override replaces the default; it is not an alias

The vacated default key stops working for that operation and is free for
another override in the same file, so two operations can swap keys in one edit
(`pause: x` with `reject: p`). This is 093's "No aliasing" carried over: `?`
explains one key per operation.

*Beaten:* keeping the default beside the override, which doubles what `?` has
to explain and leaves a key the user moved away from still acting.

### 7 (2026-09-17). Syntax

`tui.keys` is a map of operation id → one key string, in Bubble Tea v2's
key-string form (`R`, `ctrl+r`, `f5`, `shift+tab`). An empty map, the default,
is the shipped keymap. Mapping an operation to its own default is accepted and
is a no-op.

As built (115.1): modifiers are `ctrl+`, `alt+` and `shift+`, in that order;
the named keys are `enter`, `tab`, `esc`, `space`, `backspace`, `delete`,
`insert`, `home`, `end`, `pgup`, `pgdown`, `up`, `down`, `left`, `right` and
`f1`–`f20`; anything else must be a single character. A shifted character is
written as itself, the way the terminal reports it — `shift+r` is refused with
a message saying to write `R`. A string no key press produces is refused rather
than stored.

### 8 (2026-09-17). Every place that names a rebindable key renders the effective one

Not only the footer, the palette and `?`: the hints (`"R reload"` becomes key +
word, composed at render), labels that name other keys in prose (`"enter/e"`,
`"ctrl+t again, or esc"`, `"S brings it back"`), the key lines popups print
themselves, the disconnected banner's `r to retry`, and the chat's
collapsed-content hint `(ctrl+r)` — fixed today, but the rule must hold if it
ever becomes rebindable. A screen that names a key the handler no longer honours
is the 093 bug class; a test renders every registered surface under a
non-default keymap and asserts that no default key string for a rebound
operation survives.

### 9 (2026-09-17). When it applies

The TUI applies the keymap wherever it already reads `tui:` — `board.configCmd`
on connect and on every reconnect, and the daemon view's config fetch — and
immediately after its own config editor's successful `PATCH` of `tui.keys`. The
daemon publishes no config event (§13.3), and that does not change, so an edit
made with `vincent config set` or in the file reaches a running TUI the next
time it reads the configuration.

### 10 (2026-09-17). Docs cannot render a user's bindings

`docs/guides/tui.md` keeps the default tables and gains a keymap section: the
operation ids with their defaults, what is fixed and why, the composer rule, and
replace-not-alias. `docs/reference/configuration.md` gains `tui.keys` with its
refusals. `?`, the footer and the palette are where a user's own bindings are
shown.

## Work

115.1 and 115.3 are deliberately behaviour-neutral, so the large mechanical
refactor lands and is reviewed before any keymap can change a key.

- [x] **115.1 — `internal/keymap`**: the catalog, the defaults, the fixed keys,
  the exceptions, `ParseKey`, `Check` and `Build`, and `doc.go`;
  `bindings_test.go`'s clause tests moved onto the shared checker with no
  behaviour change. Tests in `keymap_test.go`:
  `TestDefaultsPassTheirOwnChecker`, `TestBuildRefusals` (an unknown id, a
  fixed name, a malformed key, a live global, a §6 action, a fixed panel row, a
  vim alias, a second meaning on a disjoint surface, an exception that does not
  travel, and a printable key for either escape hatch — each error naming the
  operation, the key and the conflicting meaning), `TestBuildAccepts` (a free
  ctrl key, a function key, the default as a no-op, a swap) and
  `TestCatalogIsWellFormed`. ✓ 2026-09-17
- [x] **115.2 — Config, API and apiclient**: `TUI.Keys` (`yaml:"keys"`),
  validated through `keymap.Build` at load, on hot reload and on
  `PATCH /v1/config`; `tui.keys` served on `GET /v1/config` as an object, never
  `null`, and accepted on `PATCH`; `ConfigTUI.Keys` and its patch field; the
  task 046 decision 4 amendment in `internal/config`. ✓ 2026-09-17
- [x] **115.3 — Dispatch refactor**: every handler of a rebindable operation
  asks the registry for its key; the converse test that every rebindable
  operation a handler matches is registered. Defaults only — behaviour
  byte-identical. Tests: `TestEveryMatchedKeyIsRegistered`,
  `TestRegistryAgreesWithTheCatalog`, `TestActionsLiveIsTheFSMs`. ✓ 2026-09-17
- [x] **115.4 — Rendering**: hints, labels, popup key lines and banners from the
  effective keymap; the test that renders every registered surface under a
  non-default keymap (`TestReboundKeysRender`). ✓ 2026-09-17
- [x] **115.5 — Applying the keymap in the TUI**: the config fetch sites and the
  editor's `PATCH`; the `tui.keys` row in the daemon view's config editor;
  probes under a non-default keymap (the rebound key fires, the vacated one does
  nothing); a live test that a keymap set through the editor is picked up
  without reconnecting. Tests: the `rebound` walk in
  `TestEveryPanelKeyIsHandled`, `TestReboundKeyReplacesTheDefault`,
  `TestConfigEditorRebindsAKeyWithoutReconnecting`,
  `TestConfigEditorKeepsTheKeymapTheDaemonRefuses`, and `m11` scenario 8. ✓
  2026-09-17
- [x] **115.6 — Documentation**: §12.3 (the `tui.keys` example line, a dated
  amendment, and the dated note on the `notify:` comment's import claim); §15
  Keys amended, dated; decision record row 35 and a dated narrowing of row 31;
  dated notes in the [046](046-notify-hook.md) and
  [093](093-tui-key-vocabulary.md) records; CLAUDE.md's Architecture paragraph
  and package map; `docs/guides/tui.md`, `docs/reference/configuration.md`,
  `docs/reference/cli.md`, `docs/features.md` and `CHANGELOG.md`. No `docs/assets/tui-*.png` changes,
  because the defaults do not. ✓ 2026-09-17

## What this does not do

No aliases. An override replaces the default (decision 6); there is no way to
give one operation two keys.

No per-surface binding. An operation has one key on every surface that carries
it, which is what keeps a user's keymap inside §15's "one operation, one key".

No key sequences or chords: one operation, one key string.

The fixed keys stay fixed (decision 1). `esc`, `ctrl+c`, `ctrl+v`, `tab`, the
multi-key sets, the surface-local rows, the popups' confirmations and the vim
aliases are part of how the TUI is navigated rather than operations a reader
names, and an override may not shadow any of them.

The shipped keymap does not change. 093 decision 2's action letters are still
the defaults, and so no screenshot is re-captured.

No config event. The daemon still publishes none (§13.3); a keymap changed
outside the TUI's own editor waits for the TUI's next configuration read.

No new command, route or MCP tool. `vincent config get` and `vincent config set`
reach `tui.keys` through the existing path, and `config_get` serves it through
the same handler.
