-- 0039_issue_outbox: the crash-safe write-back of an imported issue's state
-- to GitHub (task 130.10, issue #669, spec §12.4, §14).
--
-- A human's close or reopen of an issue with a live GitHub remote inserts
-- one `set_state` row here in the same transaction as the state change and
-- its issue.state_changed event, so the change and the promise to send it
-- commit together or not at all (crash-first). `desired_json` is the GitHub
-- value to reach — {state, state_reason, duplicate_of} — and `base_json` the
-- value vincent last saw there, which the drain compares against before it
-- writes: remote == base sends, remote == desired is done with no write, and
-- anything else is a true conflict GitHub wins.
--
-- `status` is pending until the drain settles it: done, failed (terminal,
-- `last_reason` says why), conflict, or superseded by a newer pending row
-- for the same issue, so close→reopen→close sends at most one write.
-- `origin` is the actor that asked. Times are store.TimeFormat text;
-- `next_attempt_at` is when a pending row is next due. CASCADE: a deleted
-- issue has nothing left to send.
CREATE TABLE issue_sync_outbox (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id        INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    op              TEXT NOT NULL DEFAULT 'set_state',
    desired_json    TEXT NOT NULL,
    base_json       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    last_reason     TEXT NOT NULL DEFAULT '',
    origin          TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX issue_sync_outbox_due_idx ON issue_sync_outbox (status, next_attempt_at);
CREATE INDEX issue_sync_outbox_issue_idx ON issue_sync_outbox (issue_id, id);
