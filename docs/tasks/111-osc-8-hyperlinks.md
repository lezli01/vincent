# 111 — Opt-in OSC 8 hyperlinks in the output pane

**Status:** ✅ done (4/4)
**Issue:** [#404](https://github.com/lezli01/vincent/issues/404)
**Spec:** amends §12.3 (`tui` example and prose), §15 (task 075's link bullet),
§16 (terminal injection)

## Problem

[075](075-rich-markdown-blocks.md) decision 3 is titled "No OSC 8 hyperlink is
emitted, in this task **or by default**". It says OSC 8 "becomes its own issue
if it is wanted, with the escaping rules and the gating as its subject", and it
links #404. This is that issue. It does not relitigate decision 3: the default
stays "no OSC 8", and each of the decision's three reasons is answered rather
than set aside.

1. **No reliable capability probe.** Nothing is probed. The human turns the
   setting on, and the default stays off.
2. **The payload is an agent-supplied URL inside an escape sequence (§16).** A
   strict sanitizer decides that. A link it rejects renders exactly as it does
   today, and the reference block stays on screen, so the real destination is
   always visible as text.
3. **A link across a wrap boundary would need to be closed and reopened per
   line, under the wrap-plain-then-style invariant.** The link is a *style
   attribute* (`lipgloss.Style.Hyperlink`, lipgloss v2.0.6). `wrapLine`,
   `wrapPre` and the table's `wrapSegments` already style each produced line's
   runs after layout, so each line opens and closes its own link. Wrapping
   still sees only plain text. `sameStyle` compares renders of a probe string,
   and that render includes the link, so a run already splits at a link
   boundary.

The renderer path was checked: Bubble Tea v2.0.9 renders through ultraviolet,
which reads OSC 8 into `cell.Link` (only the first two `;` delimit, so a `;`
inside a URL is safe) and emits it again. The escape reaches the terminal
instead of being dropped at the cell buffer.

## What shipped

- `tui.hyperlinks`, a bool in `config.yaml`, default `false`, served and written
  on `GET`/`PATCH /v1/config`, with a row in the daemon view's config editor
  and a field line under "task grouping".
- `internal/tui/hyperlink.go`: `hyperlinkTarget` (the sanitizer), the id
  builder, `hyperlinkHolder` (the session value), and `separatorStyle`, which
  keeps the space in front of a link out of the link.
- `internal/tui/markdown.go`: `mdRefs` carries the setting and the document id;
  `mdRefs.linked` is the one place a style gains a link. A link's label
  segments, its `[n]` and its reference-line destination go through it.
- The root adopts the setting from `boardConfigMsg`, `daemonConfigMsg` and
  `configSavedMsg`; the task pane and the chat body rebuild when the holder's
  value differs from the one they last built with. `mdCacheKey` carries it.

## Decisions

### 1. The setting is `tui.hyperlinks`, a bool in config.yaml, default false

It goes in the existing `tui:` section, which the daemon validates,
hot-reloads, serves on `GET /v1/config` and otherwise ignores — where
`tui.board.group_by` lives, for the reason `internal/config/config.go` gives:
the TUI is a pure API client and reads no configuration from disk
([009](009-configurable-tasks-view.md) decision 1). There is no env-var or
per-terminal override. When the setting is off, the pane's output is
byte-identical to what it was.

The TUI gets it the way it gets `group_by`: fetched with the rest of the config
on connect and reconnect, and applied again after the TUI's own config editor
saves. It is held as one session value that both workspaces read, shaped like
`levelHolder`/`rawHolder`, so the task workspace and the chat workspace never
disagree.

### 2. The label and the reference-line destination both carry the link

When the setting is on and a destination passes the sanitizer, two pieces of
text become clickable: the link's label, including its dim `[n]`, and the
destination text in that reference's `[n] dest` line. The reference block is
**never dropped** when hyperlinks are on. It is the defence against a label
that looks like one URL and opens another: the true destination stays printed
on screen, and it is itself the clickable copy.

Every piece of one link carries an `id=` parameter, so a terminal treats a label
wrapped across lines as a single link on hover. vincent generates the id from
the document's identity (a digest of its source) and the reference number,
using only `[A-Za-z0-9-]`. No agent byte goes into it.

### 3. Scope: Markdown inline links and inline images only

That means the `[label](url)` and `![alt](src)` constructs task 075 parses, in
both the task workspace's and the chat workspace's output pane. An image's alt
text and its source line are linked on the same terms, and nothing is fetched.

Out of scope:

- Bare URLs, autolinks, reference links and titled links stay literal, as 075
  decision 4 says, and that decision is not amended.
- GitHub URLs vincent shows itself (PR tab, issue picker) are not linked by this
  setting.
- Raw mode (`ctrl+o`, task 076) shows source and emits no links.
- Clipboard payloads (076 decisions 4–5) are built from segment text, not
  styles, so they carry no OSC 8. A test holds that.

### 4. The sanitizer is stricter than the issue's list

One function, the only door to a link: `hyperlinkTarget(dest string) (string,
bool)`. A destination becomes a link only if **all** of these hold:

- its length is at most **2048 bytes**;
- every byte is printable ASCII, **0x21–0x7E**. That rejects C0/C1 controls,
  DEL, ESC, BEL, the 8-bit ST (0x9C), space, and every non-ASCII byte.
  Non-ASCII is refused rather than percent-encoded: that blocks look-alike
  Unicode hosts, and it matches what OSC 8 expects of a URI;
- `net/url.Parse` succeeds;
- the scheme is `http` or `https`, compared after `url.Parse` lowercases it;
- `Host` is non-empty;
- `User` is nil, which refuses a user@ part like `https://github.com@evil.example`.

The URI inside the escape is the **validated original string**, not
`u.String()`. That keeps the printed reference and the link target
byte-identical. A destination that fails any check renders exactly as it did,
with no link, no marker and no notice. `openURLCmd`'s http/https rule is left
alone. It is a separate door, and the renderer still never reaches it.

## Work

- [x] **111.1 — Config**: `config.TUI.Hyperlinks` and the template's
  `hyperlinks: false`; `configTUI`/`tuiPatch` in `internal/api`;
  `ConfigTUI`/`ConfigTUIPatch` in `internal/apiclient`; the `tui.hyperlinks`
  entry in `vincent config`'s key table. Tests: default off and
  decode, the template edit round trip, `TestConfigPatchRoundTripsTUIHyperlinks`.
  ✓ 2026-09-17
- [x] **111.2 — Renderer**: `hyperlink.go`, `mdRefs.linked`, the label, marker
  and reference-line segments, `separatorStyle` in `wrapLine` and
  `wrapSegments`, `lineOpts.hyperlinks`, `assistantBlockLines` and
  `mdCacheKey`. Tests in `hyperlink_test.go`: off is today's render, on links
  exactly the label, `[1]` and destination under one id, wrapping opens and
  closes per line, the hostile-destination table (direct and rendered), the
  `;` URL, id shape, tables, raw mode and clipboard payloads, the cache.
  ✓ 2026-09-17
- [x] **111.3 — Delivery**: `hyperlinkHolder` built by the root and handed to
  both workspaces; the root adopts the value from the three config messages;
  both panes rebuild on a change. The config editor row and the daemon view's
  field line. Tests: `TestHyperlinkSettingIsOneSessionValue` and
  `TestConfigEditorTogglesHyperlinks` over the real API. ✓ 2026-09-17
- [x] **111.4 — Documentation**: §12.3, §15 and §16 amended, dated;
  `docs/reference/configuration.md` (template, `tui` block, new
  `tui.hyperlinks` entry), `docs/security-model.md`, `docs/features.md`,
  `docs/guides/tui.md`, `CHANGELOG.md`; the follow-up pointers on 075's
  decision 3 and "Not done here". No `docs/assets/tui-*.png` changes: the
  default is off and the seeded text contains no link. `internal/tui` is
  covered by no gate script, so the tests are the whole assurance, as they were
  for 073, 075 and 076. ✓ 2026-09-17
