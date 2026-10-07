-- 0043_issue_worktree: a task's role in its issue's worktrees (task 134.10,
-- issue #757, spec §5.3, §5.6; task 134 decisions 7, 9).
--
-- `issue_worktree` is `main` for a root task working in the issue's main
-- worktree, `side` for one that asked for its own worktree with
-- `merge_back`, and NULL for everything else: every row that predates this
-- migration, every task with no issue, and every fan-out lane (task 130
-- decision 5). The issue's main branch is derived from its unarchived
-- main-role tasks; nothing is stored on `issues` (task 130 decisions 3, 9).
ALTER TABLE tasks ADD COLUMN issue_worktree TEXT
    CHECK (issue_worktree IN ('main', 'side'));

-- `end_sha` is the commit a main task's work ended on, so successive main
-- tasks on one branch can each be attributed their own commits. Unused until
-- task 134.12 writes it.
ALTER TABLE tasks ADD COLUMN end_sha TEXT;

-- `merge_on_conflict` is a side task's `merge_back.on_conflict`, recorded at
-- creation (task 134 decision 12). NULL on every task that is not a side task.
ALTER TABLE tasks ADD COLUMN merge_on_conflict TEXT
    CHECK (merge_on_conflict IN ('block', 'agent'));

-- The main-branch derivation and the occupancy query both look an issue's
-- role-bearing tasks up; every older row is NULL and stays out of the index.
CREATE INDEX idx_tasks_issue_worktree ON tasks (issue_id, issue_worktree)
    WHERE issue_worktree IS NOT NULL;
