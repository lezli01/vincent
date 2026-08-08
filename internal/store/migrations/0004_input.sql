-- 0004_input: interactive input requests (spec §7.4, T2.12).
--
-- tasks.pending_input_json holds the normalized InputRequest while the task
-- is awaiting_input; TransitionTask clears it on any transition out of that
-- state, so "non-null iff awaiting_input" is enforced in one place.
--
-- step_runs.input_wait_ms accumulates the time an attempt spent waiting for
-- a human answer; excluded from duration metrics (§17).

ALTER TABLE tasks ADD COLUMN pending_input_json TEXT;
ALTER TABLE step_runs ADD COLUMN input_wait_ms INTEGER NOT NULL DEFAULT 0;
