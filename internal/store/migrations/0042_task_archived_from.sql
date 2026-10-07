-- 0042_task_archived_from: the state a task was archived from (task 134.3,
-- issue #750, spec §6, §14).
--
-- `archived` is reachable only from `done` or `aborted` (taskstate.Settled),
-- and `finished_at` is stamped for both, so nothing on the row told the two
-- apart once the task was archived. An issue's lane (#751) needs exactly that
-- distinction in SQL — finished work goes to hand_off, cancelled work back to
-- open — so it becomes a column. NULL on every row that is not archived;
-- transitionTaskTx writes it on `→ archived`.
ALTER TABLE tasks ADD COLUMN archived_from TEXT
    CHECK (archived_from IN ('done', 'aborted'));

-- Backfill from the newest task.state_changed event that archived the task,
-- whose payload carries the state it left. Events survive until their project
-- is deleted, so one is normally there. When none is — or its `from` is not
-- one of the two settled states, which the CHECK would refuse — the row reads
-- `done`. That is the safe error: it puts the issue in hand_off, prompting a
-- human to close it, rather than hiding finished work in open.
UPDATE tasks SET archived_from = COALESCE((
    SELECT json_extract(e.payload_json, '$.from')
    FROM events e
    WHERE e.task_id = tasks.id
      AND e.type = 'task.state_changed'
      AND json_extract(e.payload_json, '$.to') = 'archived'
      AND json_extract(e.payload_json, '$.from') IN ('done', 'aborted')
    ORDER BY e.id DESC
    LIMIT 1
), 'done')
WHERE state = 'archived';
