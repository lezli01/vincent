-- 0033_trigger_overrun: `overrun:` and its backlog (task 122, spec §14).
--
-- Two ledger outcomes and one ledger column, plus the table the two queue
-- modes hold events in.
--
--   * `superseded` — an event `overrun: skip` dropped, an event a newer one
--     replaced under `queue_coalesce`, a held event discarded when its trigger
--     disarmed, or the oldest held event dropped at the per-trigger cap.
--     Deliberately not `deduped`: a suppressed event is a distinct event
--     deliberately dropped, not a duplicate, and conflating them means the
--     ledger stops explaining itself.
--   * `queued` — held in trigger_backlog, will be judged again and fire when
--     its group empties. Not "delivered": the dedupe lookup keeps counting
--     `fired` and `seeded` alone, so a second identical event arriving while
--     one is held reaches the overrun step rather than being swallowed
--     (task 122 decision 8).
--
-- concurrency_key is the rendered `concurrency_key:` the event was grouped
-- under, "" for a trigger that declares no `overrun:`. The in-flight group is
-- found through this column joined to tasks.state, so it has to be on the row:
-- a group whose key were re-rendered at read time would move whenever the
-- template did, and would be unrecoverable for an event long since consumed.
--
-- superseded_task_id is the supersede link `cancel_previous` writes: the task
-- this delivery's task replaced. ON DELETE SET NULL like task_id — the chain
-- is readable while both tasks exist and does not pin a deleted one.
--
-- SQLite cannot alter a CHECK constraint in place, so the table is rebuilt the
-- way 0030 did: move the old one aside, create the new one under the real
-- name, copy every row with its id, drop the old one, recreate 0029's three
-- indexes. 0029 and 0030 are not edited — migrations are append-only.

ALTER TABLE trigger_deliveries RENAME TO trigger_deliveries_0030;

CREATE TABLE trigger_deliveries (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger_id          TEXT NOT NULL,
    event_id            TEXT NOT NULL DEFAULT '',
    dedupe_key          TEXT NOT NULL DEFAULT '',
    concurrency_key     TEXT NOT NULL DEFAULT '',
    outcome             TEXT NOT NULL CHECK (outcome IN
                          ('fired', 'seeded', 'deduped', 'filtered', 'rate_limited',
                           'refused', 'error', 'superseded', 'queued')),
    task_id             INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    superseded_task_id  INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    detail              TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL
);

INSERT INTO trigger_deliveries (id, trigger_id, event_id, dedupe_key, outcome, task_id, detail, created_at)
SELECT id, trigger_id, event_id, dedupe_key, outcome, task_id, detail, created_at
FROM trigger_deliveries_0030;

DROP TABLE trigger_deliveries_0030;

CREATE INDEX idx_trigger_deliveries_key ON trigger_deliveries(trigger_id, dedupe_key);
CREATE INDEX idx_trigger_deliveries_created ON trigger_deliveries(trigger_id, created_at);
CREATE INDEX idx_trigger_deliveries_age ON trigger_deliveries(created_at);
CREATE INDEX idx_trigger_deliveries_group ON trigger_deliveries(trigger_id, concurrency_key);

-- trigger_backlog is the durable hold for `queue_coalesce` and
-- `queue_serial`. In the manager's memory it would not survive a restart,
-- which is the crash-first invariant: a trigger that silently loses held work
-- across a daemon restart has lost the event nobody can re-send.
--
-- event_json is the raw event, replayed through the whole of judge() at drain
-- (decision 6) rather than a frozen verdict: a rate cap met in the meantime is
-- honoured, and a reaction re-resolves its branch against the tasks that exist
-- now. The row carries no trigger definition — the file is the definition, and
-- the one on disk at drain time is the one that applies.
CREATE TABLE trigger_backlog (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger_id      TEXT NOT NULL,
    concurrency_key TEXT NOT NULL,
    event_id        TEXT NOT NULL DEFAULT '',
    event_json      TEXT NOT NULL,
    created_at      TEXT NOT NULL
);

CREATE INDEX idx_trigger_backlog_group ON trigger_backlog(trigger_id, concurrency_key, id);
CREATE INDEX idx_trigger_backlog_age ON trigger_backlog(created_at);
