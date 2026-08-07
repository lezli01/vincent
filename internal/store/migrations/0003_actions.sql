-- 0003_actions: state the human-action endpoints need to persist
-- (spec §6, §14; T2.6).
--
-- pause_requested   — a pause accepted while the task was running but not yet
--                     taken effect. Persisted so it survives a crash, which
--                     re-queues the task (§12.4) without clearing the request.
-- retry_cursor_at   — the last human `retry`. The retry budget counts failed
--                     attempts after this point, which is how `retry` resets it
--                     (§6, §7.2).
-- pending_override_json — edit+retry text handed from the action handler to the
--                     actor. The handler runs while the task is blocked, before
--                     the next step_run exists, and the actor is the sole writer
--                     of step_run rows; this column is the bridge. The actor
--                     drains it onto the attempt it creates and clears it.

ALTER TABLE tasks ADD COLUMN pause_requested       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN retry_cursor_at       TEXT;
ALTER TABLE tasks ADD COLUMN pending_override_json TEXT;

-- The override as the human typed it, recorded on the attempt that used it.
-- Two columns, not one: the request body is { prompt_override?, run_override? }
-- and a command override filed under `prompt_override` reads as a bug.
ALTER TABLE step_runs ADD COLUMN prompt_override TEXT;
ALTER TABLE step_runs ADD COLUMN run_override    TEXT;
