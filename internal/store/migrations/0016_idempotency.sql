-- 0016_idempotency: replay protection for task creation (task 040, issue #146).
--
-- `POST /v1/tasks` is the one route in §13.2 where a replayed request produces
-- a second side effect: it inserts a row, claims a branch and wakes the
-- scheduler, and none of that is a compare-and-swap. A client that times out
-- *after* the commit and re-sends gets a second task, a second worktree and a
-- second agent run against the same repository. Every other mutating route is
-- already safe — the §6 actions are a CAS on the state the request read
-- (amended 2026-08-24, issue #127), `POST /v1/projects` refuses a duplicate
-- path outright, and the PATCH/DELETE routes are desired-state operations that
-- reach the same end state when re-sent.
--
-- The primary key is `(method, path, key)` rather than `key` alone so a later
-- route joins the table without a migration, even though today only
-- `POST /v1/tasks` writes it.
--
-- `request_sha` is a digest of the *decoded* request, canonically re-marshalled
-- — not of the raw bytes, so whitespace and JSON key order cannot manufacture a
-- conflict — and taken before the GitHub issue prefill mutates the request, so
-- an edited issue title cannot turn two identical sends into a 409.
--
-- `task_id` is a reference, not a stored response body. A replay re-reads the
-- task and renders today's 201 from it; persisting the rendered JSON would put
-- the workflow snapshot and a 4 MiB body bound (§13.1) into a table that grows
-- with every create. The visible consequence is stated rather than hidden: a
-- replay shows the task as it is **now**, so an already-admitted task replays
-- as `state: running` under a `201`.
--
-- ON DELETE CASCADE because a key whose task has been destroyed has nothing
-- left to replay. Force-deleting a project deletes its tasks, and foreign keys
-- are enforced on every connection (`_foreign_keys=1`), so the key goes with
-- it and a replay inside the remaining window creates a fresh task. That beat
-- ON DELETE SET NULL plus a 410, which adds a status and an error code for a
-- case only a deliberate destructive act inside a 24-hour window can reach.
CREATE TABLE idempotency_keys (
    method      TEXT NOT NULL,
    path        TEXT NOT NULL,
    key         TEXT NOT NULL,
    request_sha TEXT NOT NULL,
    task_id     INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (method, path, key)
);

-- The prune scan is a range over created_at and nothing else; without this it
-- is a full table scan on every 24-hour pass.
CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys(created_at);
