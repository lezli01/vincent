-- 0044_merge_back: the task that merges a finished side task back into its
-- issue's main worktree (task 134.14, issue #761, spec §5.3, §5.6; task 134
-- decisions 11, 12, 134.14-a to c).
--
-- `merge_source_task_id` is the side task a merge-back task merges, and NULL
-- on every other row. ON DELETE SET NULL: deleting the source leaves the
-- merge-back row, which then blocks `merge_source_missing` when it runs. The
-- side branch's name is in the merge-back's own workflow snapshot, so the
-- row never needs the source to know what to merge.
ALTER TABLE tasks ADD COLUMN merge_source_task_id INTEGER
    REFERENCES tasks(id) ON DELETE SET NULL;

-- At most one pending merge-back per source: a follow-up that finishes the
-- side task again while an earlier merge-back is still waiting adds nothing,
-- because the pending one merges the side branch's tip as it is when it runs.
-- Settled and archived merge-backs leave the index, so a later follow-up gets
-- a merge-back of its own.
CREATE UNIQUE INDEX idx_tasks_merge_source_pending ON tasks (merge_source_task_id)
    WHERE merge_source_task_id IS NOT NULL AND archived_at IS NULL
      AND state NOT IN ('done', 'aborted', 'archived');
