# 128 — Keymap upgrade tolerance

Issue [#590](https://github.com/lezli01/vincent/issues/590). Spec §12.3
(`tui.keys`) and §15 (Keys). Amends task 118 decision 3 in part; keeps task
118 decision 6 (replace, not alias) and decision 9 (a refused keymap keeps the
one in force).

## What this is

`tui.keys` was checked by one strict `keymap.Build` everywhere it was read, so a
`config.yaml` valid on release N stopped the daemon on release N+1 in two
cases: N+1 retired an operation id the user named, or N+1 added a default or
fixed key the user had already bound. Neither had happened yet; this makes sure
neither ever stops a daemon.

## Decisions

Settled with the author on 2026-09-28.

### 1. Retired ids are dropped with a warning and never aliased

`internal/keymap` carries `retiredOps`, beside `fixedNames`: a retired id and a
note for the human. Both builds skip such an id and the lenient one returns a
warning naming it and the note. It is never mapped onto another operation, so
task 118 decision 6 stands. An id that is in none of the catalog, `retiredOps`
or `fixedNames` is still an error everywhere, writes included. The map ships
empty: nothing has been retired yet.

### 2. Lenient on load, strict on write

A build cannot tell a clash an upgrade introduced from one the user is writing
now, so the rule is split by caller.

- **Load paths are lenient** — daemon start, hot reload, and the TUI's
  `applyKeys`. An override that lands on a key a non-overridden operation's
  default holds, or a fixed key holds, wins: the default becomes unbound, the
  fixed key is shadowed, and a warning says so (`keymap.BuildLenient`,
  `config.Load`, `Config.KeyWarnings`).
- **Write paths stay strict** — `PATCH /v1/config`, and through it
  `vincent config set` and the TUI's config editor (`keymap.Build`,
  `config.Decode`). They refuse such a clash exactly as before, with the file
  byte-identical. A PATCH decodes the whole candidate file, so after an upgrade
  that made a clash, any PATCH is refused until `tui.keys` is fixed; the error
  names the key.
- **Still hard on load:** two overrides on one key, bad key syntax, the §15
  text-field rules (task 118 decision 5), an unknown id — and, as built, a clash
  with one of the root's own fixed keys (`tab`, `esc`, `ctrl+c`, `ctrl+v`): the
  layer stack and the way out of the TUI must work on every surface.
- **Hand edits** reach the daemon through a reload and get the lenient
  treatment with a logged warning. Intended.

*Note (2026-10-05, task 132 decision 50):* the whole-file posture is narrowed
for one other key. The deprecated `project` level of `tui.board.group_by`
(task 132.9) is stripped on every decode, `config.Decode` included, and a
PATCH is refused for it only when the patch's own `group_by` value contains
it: almost every upgraded file still carries the bootstrapped
`[project, workflow]`, and refusing the whole candidate would block every
unrelated PATCH. The `tui.keys` posture above is unchanged.

This supersedes task 118 decision 3 in part, for load-time collisions between a
user key and a default key only; the amendment is written into 118's record,
`internal/keymap/doc.go` and the `TUI.Keys` comment in `internal/config`.

### 3. A fixed key yields on its surface

The effective `Keymap` carries the shadowed fixed entries and answers
`Shadowed(surface, key)`; an unbound operation's `Key` is `""`, which every
renderer and the dispatcher treat as "no key". The root consults `Shadowed`
before delegating a key: on a surface where the key's fixed meaning is
shadowed, the key reaches the view only if the operation that owns it now is
answered there, and is dropped otherwise. `?` and the palette list a shadowed
row or an unbound operation as `unbound`, the footer drops its hint, and an
unbound palette entry does nothing when picked.

*As built:* where the operation that took the key **is** answered on the same
surface as the fixed meaning it shadowed (`refresh: g` on the workflows
takeover, whose fixed `g` draws the graph), the key reaches the view and that
view's switch order decides. Making each literal case consult `Shadowed` is the
follow-up if it ever matters; it is not needed for the upgrade case, where the
new fixed key and the user's operation are on different surfaces by
construction of release N.

### 4. Where warnings go

The daemon logs each at `Warn` ("keymap warning") at start and on every accepted
reload. `vincent doctor` lists them under `paths.keymap_warnings` — a warning
row, never a problem, so the exit code is unchanged. The TUI raises a one-time
line under the header whenever its own lenient build returns a warning set it
has not raised before; the next key clears it. That covers version skew (a new
TUI against an old daemon). `applyKeys` still keeps the old keymap on a hard
error.

## Tasks

- [x] 128.1 (#590) Lenient load and strict write for `tui.keys`, retired ids,
  unbound defaults and shadowed fixed keys, the warnings in the log, doctor and
  the TUI, and the §12.3/§15 amendments.
