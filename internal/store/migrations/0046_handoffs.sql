-- 0046_handoffs: handoffs and their items (task 137.4, issue #837, spec
-- §5.7, §14; task 137 decisions 1, 2, 12 and 14).
--
-- A handoff is the integration step's own record: the one task that does the
-- integrating, and the issues it claimed. A task is a handoff task because a
-- `handoffs` row points at it (decision 1), so no `tasks` column is added and
-- the task carries no `issue_id` — it never moves an issue's lane (task 134
-- decision 2). `trigger_id` names the trigger that created it, when one did,
-- and `created_by` is the origin: `human`, `agent` or `trigger`. `grant_json`
-- is the issue-write grant 137.7 gives meaning to.
--
-- Deleting the task deletes its handoff and, with it, its items: a handoff
-- without its task has no state to derive.
CREATE TABLE handoffs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id     INTEGER NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
    trigger_id  TEXT,
    grant_json  TEXT NOT NULL DEFAULT '[]',
    created_by  TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

-- One item per claimed issue. `delivery_task_id` and `delivery_json` are the
-- issue's latest delivery snapshotted at claim time, so a later follow-up on
-- the delivering task cannot change what the handoff was handed. `outcome` is
-- NULL while the item is unresolved (claimed) and otherwise `delivered`,
-- `returned`, `waiting` or `released`. There is no CHECK on it, for the
-- reason 0032 and 0036 give: a later outcome needs a row in Go, not a table
-- rebuild.
CREATE TABLE handoff_items (
    handoff_id        INTEGER NOT NULL REFERENCES handoffs(id) ON DELETE CASCADE,
    issue_id          INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    delivery_task_id  INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    delivery_json     TEXT,
    outcome           TEXT,
    outcome_at        TEXT,
    outcome_by        TEXT,
    note              TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (handoff_id, issue_id)
);

-- The claim (decision 2): at most one live item — unresolved or `waiting` —
-- names an issue. The store re-checks eligibility before an insert, and this
-- index is what makes the check race-free.
CREATE UNIQUE INDEX idx_handoff_items_claim ON handoff_items (issue_id)
    WHERE outcome IS NULL OR outcome = 'waiting';

-- Serves eligibility's "already delivered" clause.
CREATE INDEX idx_handoff_items_issue_outcome ON handoff_items (issue_id, outcome);
