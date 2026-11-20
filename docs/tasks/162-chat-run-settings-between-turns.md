# 162 — Change a chat's model, effort and permission mode between turns

**Status:** ✅ done (6/6) · **Opened:** 2026-11-20 · **Issue:** #1011 (chat UX epic #1009)

§5.5 said a chat's `model`, `effort` and `permission_mode` were "resolved once
at creation, not per turn", and task 072 decision 1 made the permission mode
non-requestable on purpose: `codex exec resume` has no `--sandbox`, and the
guard was the absence of a mode field. Yet every turn is already a new process
resuming the stored session, and every adapter already passes its model and
effort flags beside its resume flag — only the *same* value had ever been
passed.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **162.1** Migration 0062: `chat_turns.model`, `effort`,
  `permission_mode` and the same three on `chat_queued_messages`;
  `store.TurnSettings`, `UpdateChatSettings`, the turn and queue writers
  resolving and recording the settings, and `chat.updated`. ✓ 2026-11-20
- [x] **162.2** `agent.ResumeSettingsChanger` / `CanChangeOnResume`, claude's
  `true` pinned to `testdata/resume_settings_2.1.329.jsonl`; the runner launches
  each turn from what its row recorded. ✓ 2026-11-20
- [x] **162.3** `PATCH /v1/chats/{id}`, send and queue-edit overrides,
  `permission_mode` on `POST /v1/chats`, the refusals, the MCP exclusion and
  `supports_resume_settings_change` on `GET /v1/agents`; apiclient
  `PatchChat` / `SendChatWith`. ✓ 2026-11-20
- [x] **162.4** `vincent chat set` and override flags on `vincent chat send`.
  ✓ 2026-11-20
- [x] **162.5** The TUI: the composer chip, `alt+p` picker, `shift+tab` toggle
  with its full-auto confirmation, the turn heading's model; chat export's
  per-turn model in all three formats. ✓ 2026-11-20
- [x] **162.6** Spec amendments, docs, and `m14` leg 15. ✓ 2026-11-20

## Decisions

Agreed with the author before implementation (issue #1011's brief).

1. **Task 072 decision 1 is superseded, explicitly.** A chat's permission mode
   is requestable. The guard it relied on — no mode field — is replaced by a
   refusal where the combination is chosen: `restricted` on a chat whose
   adapter cannot keep a resumed turn restricted (`!agent.CanResumeRestricted`,
   codex) or cannot restrict on this host (cursor on Windows, §9.4) is
   `400 agent_cannot_restrict_resume`, on `POST /v1/chats`, `PATCH`, a turn's
   override and a queued message's. `Start`'s fail-closed refusal stays as the
   backstop. `TestChatsAreAlwaysFullAuto` is rewritten to guard this rule and
   the `full-auto` default.
2. **The vocabulary does not grow.** §9.4's two modes; `shift+tab` toggles
   between them and says why when only one is legal.
3. **A linked chat's clamp holds.** A chat linked to a restricted task is never
   raised to `full-auto` — `409` with `linked_task_id`.
4. **Raising to full-auto confirms first in the TUI**; the API takes the field
   directly; the CLI needs `--yes` or an interactive confirmation.
5. **`PATCH /v1/chats/{id}`** carries `title`, `model`, `effort`,
   `permission_mode`; legal on `idle`, `running`, `awaiting_input`; a live
   turn is untouched; a terminal chat is `409`; the agent is fixed; it joins
   decision row 29's MCP exclusions; a change publishes `chat.updated`.
   Model and effort go through `Catalogs.Check`, the function task creation
   uses: a value from another adapter's catalog is `400`, and one in no
   catalog passes with a warning, as on task creation (T2.11).
6. **Per-turn overrides** on the send body, stored on a queued message's row
   until it drains, editable with the queued message; `/compact` sends none.
7. **Every turn records what it ran with**, resolved in the transaction that
   writes the row; old rows are null, "not recorded", never back-filled.
8. **Model and effort changes are gated per adapter on a capture.** claude
   yes, from a 2.1.329 capture whose resumed run's `init` and assistant records
   name the changed model. codex no: its JSONL never names the model a run
   used, so no capture can show the change. cursor and copilot no: not yet
   captured. Before the first turn any value is legal.
9. **TUI**: `chat_model` (`alt+p`) and `chat_mode` (`shift+tab`, a recorded
   keymap exception — the global and form `shift+tab` never reach the chat
   surface, which captures input). A per-turn override from the TUI is not
   offered.

## Notes

- The TUI turn heading names the model only when the turn recorded one; a turn
  older than migration 0062 shows nothing there rather than "not recorded".
  The export says "not recorded" for it.
- The six `docs/assets/tui-chat*.png` that show the composer were re-captured
  with `scripts/screenshots.sh` for the chip.
