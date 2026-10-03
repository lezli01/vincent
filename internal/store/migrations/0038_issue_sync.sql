-- 0038_issue_sync: the per-project bookkeeping of the GitHub issue import
-- and refresh (task 130.8, issue #667, spec §5.6, §14).
--
-- `issue_sync_state` is one row per project the reconciler has polled or
-- been asked to poll. `watermark` is the newest remote `updated_at` seen,
-- `etag` the last conditional-request validator, `since` the bound the next
-- incremental poll asks for. `ok`/`reason` are the last attempt's outcome —
-- a project with no row is treated as ok, so the first failure is a
-- transition and the first success is not. `import_complete` records that
-- the first full import has finished; `last_full_scan_at` paces the sweep
-- that finds issues which moved or vanished; `rate_limited_until` defers the
-- next attempt; `requested_at` is a human's "sync now", which the reconciler
-- answers ahead of its tick. Times are store.TimeFormat text, NULL for never.
-- ON DELETE CASCADE: the row is meaningless without its project, and
-- DeleteProjectCascade relies on the project row taking it along.
CREATE TABLE issue_sync_state (
    project_id         INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    provider           TEXT NOT NULL,
    repo               TEXT NOT NULL DEFAULT '',
    watermark          TEXT,
    etag               TEXT NOT NULL DEFAULT '',
    since              TEXT,
    last_attempt_at    TEXT,
    last_ok_at         TEXT,
    ok                 INTEGER NOT NULL DEFAULT 1,
    reason             TEXT NOT NULL DEFAULT '',
    import_complete    INTEGER NOT NULL DEFAULT 0,
    last_full_scan_at  TEXT,
    rate_limited_until TEXT,
    requested_at       TEXT
);

-- `remote_status` is what the last sweep learned about a live remote: ''
-- live, 'moved' (transferred out of the project's repo; remote_json carries
-- `moved_to`) or 'missing' (gone, or no longer readable). Neither deletes
-- the issue — the local copy is kept, and a later refresh that matches the
-- node id again resets it to ''.
ALTER TABLE issue_remotes ADD COLUMN remote_status TEXT NOT NULL DEFAULT '';
