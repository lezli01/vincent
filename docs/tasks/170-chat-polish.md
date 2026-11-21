# 170 — Chat polish: question-card digits, a status line, shell passthrough, images and vim

**Status:** 🔄 in progress (6/8) · **Opened:** 2026-11-21

Part of the chat UX epic (#1009), tier 3; issue #1024. Seven small-to-large
items for the chat workspace, built under one issue as one task document. The
author chose not to split the large items into issues of their own.

It respects task 124 decision 9 (the composer's text is passed through, never
interpreted), task 160 (conversation focus, which already shipped the
clickable `↓ N new` marker), task 063 decision 2 (chat routes stay off MCP)
and the ownership invariant (the daemon runs everything; the TUI holds nothing
the daemon lacks).

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **170.1** Number keys in the chat's answer card: `1`–`9` pick or toggle
  option N of the question under the cursor, submitting at once only for a
  single single-select question. The card numbers its options. Digits are
  fixed surface-local keys in `keymap.fixed`. The task workspace's popup is
  unchanged. ✓ 2026-11-21
- [x] **170.2** `tui.chat.status_line`: a single-line `text/template` over
  the chat's fields, validated at load (parse, then execute against the zero
  value with `missingkey=error`), in the settings view, hot-reloaded. It
  replaces the header's context gauge and turn count. Empty keeps the shipped
  header. ✓ 2026-11-21
- [x] **170.3** Shell passthrough: `shell_command` (`alt+x`),
  `POST /v1/chats/{id}/shell` and its reads and `attach`, `vincent chat shell
  [ls|attach]`, `chat_shell_runs` (migration 0070), `chat.shell_changed`,
  recovery with the identity guard, the card, opt-in attachment through
  `RunSpec.Preamble`, export, the MCP exclusions, and `scripts/170-gate.sh`
  (group 2, all three platforms). ✓ 2026-11-21
- [x] **170.4** Image paste: `internal/clipimage` (osascript,
  `wl-paste`/`xclip`, PowerShell) behind `ctrl+v` with a text-paste fallback,
  pasted image paths, `POST /v1/chats/{id}/images`, `vincent chat image
  add|ls`, `chat_images` (migration 0070), `[image N]` chips resolved into
  `RunSpec.Images`, and the `ImageAttacher` capability (`supports_images`):
  codex `--image` (0.169.0) and copilot `--attachment`. claude and cursor are
  refused rather than emulated. ✓ 2026-11-21
- [ ] **170.5** Side question. Depends: 167.4 (the session fork). Not built:
  the brief says this item waits for 167.4 rather than adding its own seeding.
- [x] **170.6** Vim mode in the composer (`tui.chat.vim`, default off): a
  layer over the textarea with vim's core set, `esc` peeling insert → normal
  → leave, and `capturesInput` kept true in normal mode. Departures are
  recorded in `internal/tui/chatvim.go`: undo is the layer's own snapshot
  stack, `j`/`k` move by logical line, and the mode is shown by a label rather
  than a block cursor. ✓ 2026-11-21
- [x] **170.7** The clickable `↓ N new` marker. Already delivered by task
  160.2 (decision 5). Nothing was rebuilt. ✓ 2026-11-21
- [ ] **170.8** Screenshots: re-capture the shots of the seeded rate-limit
  chat (`tui-chat`, `tui-chat-handoff`, `tui-chat-copy`, `tui-chat-skills`,
  `tui-chat-files`), whose seed now has a shell card. Needs VHS.

## Decisions

Settled with the author on 2026-11-21, in the brief for #1024. Those marked
*(brief)* were made in the brief and stand unless the author overrules them.

1. **Keymap operations, never composer prefixes.** The shell command, and the
   side question when it is built, are rebindable `internal/keymap`
   operations that open a one-line prompt. A message starting with `!` or
   `/btw` is sent verbatim, so task 124 decision 9 stands unamended.
   *(brief: op names and default keys)*
2. **The daemon runs the shell command, where the chat's turns run.** That is
   the task's worktree or container for a linked chat, under the §8.3 shell
   and §8.5 environment. A run is not a turn and holds no
   `max_parallel_chats` slot. It is allowed in any live state, refused on
   terminal chats, and limited to one per chat. *(brief)* It beat the TUI
   running the command itself, which would break the ownership invariant and
   run on the client's machine rather than in the chat's workspace, and
   allowing it only while the chat is idle, which would take it away exactly
   when the human wants to look at the workspace during a long turn.
3. **Fixed bounds:** a 2-minute timeout and 256 KiB of combined output, with
   truncation marked. *(brief)* It beat configurable bounds (a new
   `config.yaml` key for a quick look at the workspace, when anything longer
   belongs in the human's own terminal) and unbounded output, which a
   runaway command would turn into an unbounded file and an unbounded
   preamble.
4. **Attaching is opt-in.** A finished run's command and output reach the
   agent only when the human marks it. They are delivered as the next turn's
   `RunSpec.Preamble`, never fused into the message, and exactly one turn
   takes a run. *(brief)* It beat sending every run's output to the agent
   automatically, which would put whatever the human glanced at into the
   agent's context unasked, and appending the output to the human's
   message, which would make the stored turn differ from what was typed.
5. **The clipboard is read by the client** (like `internal/nerdfont`), because
   the clipboard belongs to the client machine. With no image, `ctrl+v` falls
   back to a text paste silently. It beat having the daemon read the
   clipboard, which on a remote or service-run daemon is a different
   machine's clipboard, or none.
6. **Images:** PNG, JPEG, GIF or WebP by sniffed content, up to 10 MiB each,
   numbered per chat. *(brief)* An adapter takes them only where captured help
   proves a flag for it. Otherwise the upload or send is
   `422 validation_failed` naming the agent, and the same applies to a linked
   chat in a container. It beat trusting the file name's extension, which a
   renamed file defeats, and emulating the capability on claude and cursor
   (for example by writing the path into the prompt), which the adapter rule
   forbids. *Amended 2026-11-21 (review F2, F4):* only `[image N]` naming an
   image the chat holds is a chip, and any other is sent as typed, beating
   refusing every match, which on claude and cursor could only ever refuse
   the human's own prose; and on an adapter without the capability a pasted
   image path stays text, which the agent can still open, beating refusing
   it, which took away what a dropped file always gave claude.
7. **Vim is a layer, not an editor.** An operation the textarea cannot
   express cleanly is dropped or recorded as a departure, rather than forking
   the component. *(brief)* It beat a new editor component or a fork of
   `bubbles/textarea`, either of which would have to re-earn every
   composer behavior (drafts, pickers, paste) the textarea already has.
8. **The side question waits on 167.4.** It reuses that task's fork and
   seeding (167 decision 1) and does not grow its own. It beat building a
   seeding of its own now, which would be a second way to start a session
   from a conversation, to be replaced the day 167.4 lands.
