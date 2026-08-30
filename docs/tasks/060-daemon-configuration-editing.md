# 060 — Edit the daemon configuration from the TUI

Issue [#244](https://github.com/lezli01/vincent/issues/244).

Every key in `config.yaml` was hand-edited. There was no `vincent config`
subcommand, the TUI's daemon view rendered a read-only digest, and that digest
could only show what `GET /v1/config` served — eleven keys of the twenty-four in
`config.Config`. `branch_template`, `debug`, `environment`, `parallel`,
`fan_out`, `loop`, `include`, `github`, `update`, `mcp` and `notify` were
invisible from every client, so changing `max_parallel_tasks` or turning on the
`notify:` hook meant leaving the TUI, finding the platform-native config dir and
editing YAML by hand.

Scope is `config.yaml` only. Projects are DB rows with their own form; workflow
YAML opens in `$EDITOR` from the workflows view. Neither is touched.

## Status

| # | Work | Status |
|---|---|---|
| 060.1 | `GET /v1/config` serves every key; a drift test fails when one is added and not served | [x] |
| 060.2 | `PATCH /v1/config`: validate, comment-preserving atomic write at 0600, apply before answering | [x] |
| 060.3 | `internal/config`'s document editor (`Apply`, `WriteFile`) and `defaultConfigYAML`'s six missing blocks | [x] |
| 060.4 | The TUI's navigable config list and typed editor, with the four-key confirmation | [x] |
| 060.5 | `vincent config get\|set` | [x] |
| 060.6 | MCP: `PATCH /v1/config` excluded, `config_get` redacted | [x] |
| 060.7 | `scripts/m11-gate.sh`, wired into `ci.yml` on all three platforms | [x] |
| 060.8 | Spec amendments (§12.3, §13.2, §13.4, §15, §16) and the derived pages | [x] |

## Decisions

**1. The two read-only negatives are reversed, with dated in-place
amendments.** v0 PR N recorded *"View 6 reports, it does not act"* — the same
reasoning that withholds `vincent gc` and `POST /v1/daemon/stop` from that
screen — and §12.3 recorded *"The API exposes it read-only (`GET /v1/config`)"*.
Both are superseded. The distinction that carries the reversal: stopping or
garbage-collecting acts on the **process supervising the TUI**, while a config
edit changes a **file the daemon owns and already hot-reloads**, which a human
may edit by hand at any moment anyway. The negative for stop and gc stands
unchanged; only configuration is admitted.

**2. The full file is served, including `environment.set` values and
`notify.command`.** This supersedes §16's task-046 note, which says in as many
words *"it is why `notify` is not served on `GET /v1/config`"*, and relaxes
§12.3's "names, never the values, at any level" so that it governs the **log**
and step transcripts, which is where that rule was earned, rather than the API.
The reasoning admitted: the endpoint is loopback-only behind a 0600 bearer
token, which is the same trust boundary as the 0600 file — anyone who can call
`GET /v1/config` can already `cat` it. The logging rule is not weakened: the
daemon still logs variable names only.

**3. `config_get` is redacted on the MCP path only.** `internal/mcp/tools.go`
derives its tool surface from §13.2's route table, so decision 2 would otherwise
hand an agent step the user's literal `environment.set` values and
`notify.command` argv, into its context and its transcript. The MCP rendering of
`GET /v1/config` masks those two; the HTTP rendering does not. A test asserts
the two bodies differ in exactly those fields and nowhere else.

**4. `PATCH /v1/config` is excluded from the MCP tool surface.** Settled already
by task 057 decision 4, whose wording anticipates this: *"An agent must not be
able to stop, garbage-collect or **reconfigure** the daemon supervising it."*
The route joins `mcp.Excluded`. The existing route-table parity test fails on
either an unexposed or a silently exposed route, so this cannot drift.

**5. PATCH applies synchronously.** The `config.Watch` callback in `daemon.Run`
was the daemon's only applier, on a 100 ms debounce, so a GET issued right after
a 200 could still read the old values and every gate assertion would need a
retry loop. Instead the callback body is a named function, and PATCH calls it
after the write and before responding; the watcher's later fire re-reads
identical bytes and is a harmless no-op. This is also the answer to the issue's
"the write must not fight its watcher": there is one applier function with two
callers, and it is idempotent.

**6. Serialize and re-read; last writer wins.** A mutex around the
read-modify-write, and the file is read fresh at patch time rather than from a
cached copy, so concurrent PATCHes cannot interleave. A hand-edit racing a patch
is last-writer-wins and undetected — the same posture `PATCH /v1/projects/{id}`
already has. No ETag/`If-Match`: a precondition concept no other endpoint in
this API carries, for a race between a human and themselves.

**7. `defaultConfigYAML` is extended first, so the writer's normal path is
edit-in-place.** The issue assumed "most keys are commented out"; in fact only
`notify:` was, and `branch_template`, `parallel`, `fan_out`, `loop`, `include`
and `mcp` were absent from the template entirely — six keys with no documented
block to uncomment. Every missing key is added to the template as a commented-out
documented block, matching the style of the `notify:` block. Appending a key to
the end of a file is the rare fallback, reached only by a `config.yaml` written
by an older version.

**8. `vincent config get|set` ships in the same work.** The issue's problem
statement opens on the missing subcommand but its solution never adds one. The
CLI is a thin API client, consistent with task 048's precedent of a command line
for every human action, and it is what lets the acceptance gate exercise PATCH
without curl-ing a JSON body by hand.

**9. The editor is line-oriented, not a marshal round trip.** `config.Apply`
walks the file's lines with an indentation stack and applies dotted-path
assignments: an active key is edited in place, a key that exists only as a
commented-out block is uncommented where it stands, and a key with no block at
all is appended. Regenerating the file from the `Config` struct would have been
much simpler and would have flattened the commented template that is most
users' only documentation of what the keys are. Values are rendered on one line
— scalars, flow sequences, flow mappings — so a value can be swapped without
disturbing what surrounds it.

**10. The daemon view's config block keeps its digest and gains a list.** The
full table is thirty-odd rows, taller than the log pane it shares the view with,
and the log is why that view is the one that renders while the daemon is
unreachable. So the block renders the digest it always did until `tab` moves the
arrows onto it, and then becomes the full scrolling list. Nothing that was
visible stopped being visible, and every key became reachable.

**11. The block says "differs from the default", not "set in the file".**
`GET /v1/config` serves effective values and carries no provenance — it cannot
distinguish a value the file pinned from the identical built-in. Adding
provenance to the endpoint was rejected as scope the issue does not need. So the
row shows the built-in default beside a value that differs from it and claims
nothing more than that.

## What the tests prove

- **Round-trip fidelity** (`internal/config/edit_test.go`). Comments, blank
  lines, key order and trailing comments survive; the `notify:` block is
  uncommented in place and appears exactly once; a key with no block is
  appended once, with its parent reused by the second key under it.
- **Nothing on failure** (`internal/api/config_patch_test.go`). Seven invalid
  patches — including a `branch_template` that does not compile — answer the
  snake_case envelope and leave the file **byte-identical**, asserted on the
  bytes.
- **No drift between the struct and the wire.** One test fails when a field is
  added to `config.Config` and not to `configResponse`; another when a key the
  read serves is not reachable by `configPatch`.
- **Read-after-write.** `GET` immediately after a 200 shows the new value, with
  no sleep.
- **Serialization.** Twelve concurrent patches; the file parses afterwards.
- **0600 survives**, including on a file that was `0644` when the patch arrived
  (POSIX only).
- **MCP.** `config_get` over a real MCP client masks `environment.set` and
  `notify.command` while the HTTP body does not, and the two bodies are
  otherwise equal.
- **TUI** (`internal/tui/configlive_test.go`), against the real handlers over
  `httptest`: the editor writes through end to end, a validation error renders
  against its field with the refused value still on screen, and each of the four
  dangerous keys refuses to apply without the confirmation.
- **Gate m11.** Real daemon, real file, over curl: every key served, a patch in
  force without a restart, comments and key order intact, an invalid patch
  leaving the bytes alone, 0600 on POSIX, `vincent config get|set`, and
  `config_patch` absent from the MCP tool list.

## Not done here

- **Provenance on `GET /v1/config`** — see decision 11.
- **A `vincent config edit` that opens `$EDITOR`.** The workflows view's
  pattern would work, but it is the alternative the issue rejected: no typed
  fields and no validation before the file hits disk.
- **Per-project settings.** They are DB rows with their own form (§14).
