-- 0064_step_run_head: the commit range one step run covered (task 164.5,
-- decision 6, spec §5.4).
--
-- head_start is the worktree's HEAD when the attempt started, journaled at the
-- row's insert — before the agent or command runs (crash-first). head_end is
-- HEAD when the attempt ended, written by the same statement that finalizes
-- the row. Together they let GET /v1/tasks/{id}/diff?step_run= diff exactly
-- what one attempt committed (§13.2).
--
-- A sha names commits only: what the attempt left uncommitted in the worktree
-- is not in the range, which is the documented limit of the per-step diff.
--
-- Rows from before this migration hold NULL in both and are never back-filled:
-- no recorded commit says where an old attempt started, and guessing one would
-- attribute somebody else's work to it. §12.4 recovery leaves an interrupted
-- attempt's head_end NULL for the same reason — it did not see the attempt end.
ALTER TABLE step_runs ADD COLUMN head_start TEXT;
ALTER TABLE step_runs ADD COLUMN head_end TEXT;
