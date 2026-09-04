-- 0027_step_input: what one attempt was actually *given* — the rendered input
-- the step handed its process, and the run-time resolution behind it (§5.4,
-- issue #323).
--
-- Nothing recorded it. `prompt_override`/`run_override` (migration 0003) hold
-- only text a human typed at edit+retry, so they are empty on the ordinary
-- attempt; the claude adapter passes the prompt on stdin, so not even the
-- `debug: true` argv note carries it. A human asking "what did this attempt
-- get?" had the workflow's *template* in the snapshot and the §8.4 values
-- nowhere — and the template is not the answer when the render is the thing
-- that went wrong.
--
-- These are recorded rather than re-derived on read, and the difference is
-- not cosmetic. `config.yaml` hot-reloads (§12.3), so a timeout or a shell
-- default can move under a row that already ran; a task's agent/model/effort
-- overrides are patchable after the fact, so re-running the resolver later
-- can name a different level than the one that actually supplied the value.
-- A derived answer would quietly disagree with what the attempt got.
--
-- **`rendered_if` is display-only, like every other column here.** Task 015
-- decision 10 says a guard is re-evaluated every time it is reached and is
-- never sticky, and nothing in the engine reads any of these columns back to
-- decide anything. This extends that decision's own closing clause — the
-- mitigation is visibility — by recording what the guard *rendered to*, beside
-- the raw template `result_summary` already carries.
--
-- **Written once, at render, and never updated.** The input is known before
-- the process starts and must already be on the row while the attempt is
-- `running` and after §12.4 recovery finalizes it `interrupted` — which is
-- exactly the attempt a human opens this record for. That is why the writer is
-- a narrow, additive one (RecordStepRunInput) and why UpdateStepRun does not
-- carry these columns: an actor's stale struct would erase them.
--
-- NULL on a rendered column means no input was recorded: every row written
-- before this migration, and every field the step type has no input for. An
-- empty string is distinct and meaningful — a render that produced nothing.
-- 0 on `input_truncated`, `timeout_ms` and `check_timeout_ms`, and "" on the
-- source columns, mean the same "not recorded" a pre-0027 row reads, following
-- `loop_total`'s precedent (migration 0026): readers fall back to what they
-- did before, so a task in flight over the upgrade renders as it did.
--
-- Each field is bounded at store.StepInputLimit (64 KiB) bytes on a rune
-- boundary, and `input_truncated` says a cut happened. Nothing prunes
-- step_runs and the database ships whole in `vincent daemon backup`, so the
-- ceiling is the record's cost control, not a display concern.
--
-- No index: these are read only through rows already selected by task and step.
ALTER TABLE step_runs ADD COLUMN rendered_prompt TEXT;   -- NULL = none recorded
ALTER TABLE step_runs ADD COLUMN rendered_run TEXT;      -- NULL = none recorded
ALTER TABLE step_runs ADD COLUMN rendered_check TEXT;    -- NULL = none recorded
ALTER TABLE step_runs ADD COLUMN rendered_if TEXT;       -- NULL = none recorded; display only
ALTER TABLE step_runs ADD COLUMN rendered_for_each TEXT; -- NULL = none recorded; JSON array
ALTER TABLE step_runs ADD COLUMN input_truncated INTEGER NOT NULL DEFAULT 0;
ALTER TABLE step_runs ADD COLUMN agent_source TEXT;      -- agent.Source: step|task|workflow|adapter
ALTER TABLE step_runs ADD COLUMN model_source TEXT;
ALTER TABLE step_runs ADD COLUMN effort_source TEXT;
ALTER TABLE step_runs ADD COLUMN permission_mode TEXT;   -- full-auto | restricted
ALTER TABLE step_runs ADD COLUMN timeout_ms INTEGER NOT NULL DEFAULT 0;       -- 0 = not recorded
ALTER TABLE step_runs ADD COLUMN check_timeout_ms INTEGER NOT NULL DEFAULT 0; -- 0 = not recorded
ALTER TABLE step_runs ADD COLUMN shell TEXT;             -- resolved shell of a command step
ALTER TABLE step_runs ADD COLUMN work_dir TEXT;
