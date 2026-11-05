-- 0053_issue_archive_cursor: the auto-archive worker's place in the events
-- table (task 143.3, issue #928, spec §5.6, §12.4).
--
-- The worker archives a closed issue's settled tasks by reading the durable
-- events after this cursor, in the style of trigger_cursors (0029): a
-- restart resumes from here, so a close committed while the daemon was down
-- is still acted on. One row, pinned by the CHECK.
--
-- It is seeded here, at the newest event the upgrade finds — or 0 on an
-- empty database — so an upgrade archives no history (task 143 decision 7:
-- no backfill). Issues already closed when this runs keep their tasks until
-- something on them moves again.
CREATE TABLE issue_archive_cursor (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    cursor INTEGER NOT NULL
);

INSERT INTO issue_archive_cursor (id, cursor) SELECT 1, COALESCE(MAX(id), 0) FROM events;
