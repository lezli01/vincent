# 171 — Ask another agent: fork a chat onto a different agent, and compare two answers

**Status:** 🔄 in progress (6/7) · **Opened:** 2026-11-21

Issue #1020, built on task 167's checkpoint, rewind and fork (#1041). It
amends two task 167 decisions deliberately, with the author's agreement:
decision 1 (what a seeded context holds) and decision 11 (a fork copies its
source's agent). Both carry dated amendment notes pointing here.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **171.1** Cross-agent fork: `agent` (with `model`, `effort`,
  `permission_mode`) on `POST /v1/chats/{id}/fork`, the `chat_fork` MCP tool
  and `vincent chat fork --agent`; always seeded; `chats.forked_from_agent`
  (migration 0073) served as `forked_from.agent`. ✓ 2026-11-21
- [x] **171.2** Tool-call summaries in every seeded context, read from each
  kept turn's normalized transcript through a `TurnTools` seam the daemon
  wires to `api.TurnToolCalls`; tool blocks dropped first when over the
  bound; the intro rewritten; the source agent named on a cross-agent seed.
  ✓ 2026-11-21
- [x] **171.3** The seeded context as turn 0: `pending_preamble` on the chat
  DTO, `PATCH /v1/chats/{id}` with `pending_preamble` (`409
  chat_preamble_not_pending`), `vincent chat preamble`, `vincent chat show`'s
  turn-0 block, and the chat workspace's turn-0 block and fork provenance.
  ✓ 2026-11-21
- [x] **171.4** The rest of turn 0's surfaces: a TUI edit action through the
  task 163 editor, an agent choice in the TUI's fork picker (filtered by
  `supports_resume`), `forked from #N (claude)` on the chats board card and
  the chat header, turn 0 in `vincent chat transcript` and in the export.
  ✓ 2026-11-22

  *How (2026-11-22):* the edit is a key-less palette row, "edit turn 0…",
  offered while the chat is seeded-pending and holds no process, through
  `editTextCmd`; unlike the composer (task 163 decision 3) an emptied file
  is sent, because empty means "clear" here (decision 4). The fork picker's
  `tab` cycles the agent, since a fork has no rewind mode; the list is the
  source's own agent, then every other adapter that is available and says
  `supports_resume: true`. A fork's card gives its title's second line to the
  provenance. A consumed turn 0 needed a home, since the runner clears the
  chat's copy once the turn reports its session: it is written on that
  turn's transcript as the `context` of the `vincent.seeded` note — no
  migration, and the transcript is already the turn's complete record (§17).
  `vincent chat transcript` prints it at the head of the turn, `--turn 0`
  prints a pending one, and the export carries `seeded_context` on the turn,
  `pending_preamble` and `forked_from` on the chat.
- [x] **171.5** Compare: `POST /v1/chats/{id}/compare` (`{turn, prompt,
  agents: [a, b]}`), the pairing in a migration, all-or-nothing capacity,
  restricted where a side can be and labelled `full-auto` where it cannot
  (decision 7), `vincent chat compare`; not an MCP tool. ✓ 2026-11-22

  *How (2026-11-22, design agreed with the author before work started):*
  `prompt` is optional and defaults to turn `turn`'s own prompt — a fork
  exists only before a turn, so "how would each agent have answered turn
  N" is the natural compare; `agents` are two different agents, left then
  right. Each side is made by the fork path (`forkChat`, extracted from the
  fork handler). A side's mode is probed with task 162's check before
  anything is created: `agent_cannot_restrict_resume` makes that side
  `full-auto` with the refusal's message as its `mode_note`, and any other
  refusal refuses the compare; `permission_mode: full-auto` skips the probe.
  All or nothing: `Runner.CheckCapacity(2)` before the forks, then
  `Runner.SubmitPair`, which takes both chats' start locks in id order,
  re-checks the cap for two and writes and launches both turns; a failure
  after the first fork force-deletes what was made and its branch (still at
  the commit it was cut from). The pairing is `chat_compares` (migration
  0074): per side its chat (`ON DELETE SET NULL`), agent, mode and note, and
  `kept_chat_id` for 171.6. `GET /v1/chats/{id}/compare` reads it from
  either side. Both routes join the MCP exclusion list.
- [x] **171.6** Keep one: archive the other fork with `delete_branch=true`,
  refused while either side is live; the TUI split view with keep left/right.
  ✓ 2026-11-22

  *How (2026-11-22):* a chat archive never deletes a branch, so keeping is
  its own route, `POST /v1/chats/{id}/compare/keep`: the other side goes
  through `archiveChat` (extracted from the archive handler) forced past a
  dirty worktree, then `git branch -D` on a branch vincent cut, then its
  checkpoint refs; `409 compare_kept` once kept; `vincent chat keep`. The
  TUI's compare screen (`viewCompare`) draws each side with the chats
  board's preview pane (`previewFrom` shared with it) and the side's agent
  and actual mode, re-reading every second while a side holds a process —
  polling rather than two SSE subscriptions, since a compare is two turns
  watched, not typed into. Keys are fixed (`tab`, `enter`, `[`, `]`) plus
  `refresh`. Entry points: "compare side by side" in a compare side's
  palette (the chat view asks `GET …/compare` once, for forks only), and
  "compare two agents from turn…", the fork picker in a compare mode whose
  `tab` picks the second agent and whose prompt is the composer's draft or
  the turn's own.
- [ ] **171.7** `scripts/171-gate.sh`, wired into the lightest `gate-group`,
  and the screenshot seed gaining a cross-agent fork and a compare pair.

## Decisions

The brief's decisions, settled with the author, are binding:

1. **Scope.** Both halves — the cross-agent fork with an editable turn 0, and
   Compare with its split view — belong to this task.
2. **Fork takes an optional `agent`** (amending task 167 decision 11). Absent
   or equal to the source's, nothing changes. Otherwise the agent must exist
   and resume (`400`), the fork is always seeded (a session cannot cross
   adapters), model and effort are the request's or the adapter's defaults —
   never the source's — and the source's permission mode goes through task
   162's check unless the caller passes `permission_mode`. The fork starts no
   process, so it stays an MCP tool. A `model` or `effort` on a same-agent
   fork is `400`, since that fork copies the source's.
3. **Tool-call summaries in every seeded context** (amending task 167
   decision 1): a `<tools>` block of `chatexport.ToolSummary` lines under each
   turn's prompt, before its reply; the `seededLines`/`seededBytes` bound
   holds and whole tool blocks go first; the intro says output is excluded;
   a cross-agent seed names the source agent.
4. **The preamble is turn 0**, visible and editable until the first turn
   runs. Empty text clears it. A natively resumed fork has none. The edit is
   sent alone on `PATCH /v1/chats/{id}`, which reads the large body tier for
   it, under the chat's hold so no turn consumes the plan mid-edit.
5. **Card label** `forked from #N (claude)`. The source's agent is recorded
   on the fork (migration 0073) rather than read from the source, because the
   source may be permanently deleted.
6. **Compare is a daemon route**, all-or-nothing on capacity, not an MCP tool.
7. **Compare runs restricted where it can, and says so where it cannot.**
8. **Keep one** archives the other fork with its branch and refs.
9. **TUI split view** of the two answers, live, with keep left/right.
