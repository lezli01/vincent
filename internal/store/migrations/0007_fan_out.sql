-- 0007_fan_out: the parent↔child link a `type: fan_out` step creates
-- (task 014, spec §7.6/§14).
--
-- A lane is a real task, not a row shape of its own: it has its own worktree,
-- branch, scheduler slot, gates, blocks, transcripts and recovery, and these
-- four columns are the entire difference between it and a hand-created task
-- (decision 1). Everything else about fan-out is behaviour over rows that
-- already existed.
--
-- All four are NULL for a root task, and set together for a lane. They are not
-- one JSON column because parent_task_id is walked by a recursive CTE (the
-- §13.2 children rollup) and lane_order is an ORDER BY — both of which want
-- real columns.
--
-- lane_order is the *declared* order of the lane in its step, kept because the
-- join merges in that order rather than in completion order: a re-run must
-- conflict identically for recovery to be idempotent (decisions 7, 9). It
-- cannot be derived from created_at, which is spawn order and coincides only
-- by luck.
--
-- No foreign key from lane_id or parent_step_index back to the snapshot: the
-- snapshot is text, and the child is deliberately indistinguishable from a
-- hand-created task once it exists (decision 4).

ALTER TABLE tasks ADD COLUMN parent_task_id INTEGER REFERENCES tasks(id);
ALTER TABLE tasks ADD COLUMN parent_step_index INTEGER;  -- the fan_out step's index in the parent
ALTER TABLE tasks ADD COLUMN lane_id TEXT;               -- the lane's id in that step
ALTER TABLE tasks ADD COLUMN lane_order INTEGER;         -- declared position, the merge order

-- The list filters and the subtree CTE both walk children by parent
-- (decision 13); without this each level of a tree is a table scan.
CREATE INDEX idx_tasks_parent ON tasks(parent_task_id, lane_order);
