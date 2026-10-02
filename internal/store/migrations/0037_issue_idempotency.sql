-- 0037_issue_idempotency: replay protection for issue creation, and an
-- issue's MCP provenance (task 130.3, issue #662, spec §13.1).
--
-- `POST /v1/issues` is the second route where a replayed request produces a
-- second side effect: it inserts a row a re-send would insert again. 0016
-- expected a later route to join `idempotency_keys` without a migration, and
-- its `(method, path, key)` primary key does scope one; what it cannot do is
-- point at an issue, because its `task_id` is NOT NULL and references
-- tasks. A nullable second reference would let a row name neither, so the
-- issue route gets a table of the same shape instead — task 130 decision 11
-- records the departure. The digest, the 24-hour window and the replay rule
-- are 0016's, shared rather than copied (internal/api/idempotency.go).
--
-- ON DELETE CASCADE for 0016's reason: a key whose issue has been deleted has
-- nothing left to replay, so a re-send inside the window creates a fresh one.
CREATE TABLE issue_idempotency_keys (
    method      TEXT NOT NULL,
    path        TEXT NOT NULL,
    key         TEXT NOT NULL,
    request_sha TEXT NOT NULL,
    issue_id    INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (method, path, key)
);

CREATE INDEX idx_issue_idempotency_keys_created_at ON issue_idempotency_keys(created_at);

-- The task whose agent step created the issue over MCP, as `tasks` records it
-- for a task (§13.4, task 057 decision 7). NULL for every other caller. SET
-- NULL because deleting that task leaves the issue it filed.
ALTER TABLE issues ADD COLUMN created_by_task_id INTEGER REFERENCES tasks(id) ON DELETE SET NULL;
