-- 0030_trigger_seeded: the `seeded` delivery outcome (task 096 decision 31B,
-- spec §14).
--
-- A cursor-less `type: command` source would otherwise flood on its second
-- poll: the seed poll fires nothing, and the second poll sees the same events
-- with nothing in the ledger to say they were already there. So the first poll
-- after arming records one `seeded` row per event it was shown, keyed by the
-- event's dedupe key, and the dedupe lookup treats `seeded` like `fired`.
--
-- SQLite cannot alter a CHECK constraint in place, so the table is rebuilt:
-- move the old one aside (its indexes go with it), create the new one under
-- the real name, copy, drop the old one, and recreate the three indexes 0029
-- made. 0029 itself is not edited — migrations are append-only.

ALTER TABLE trigger_deliveries RENAME TO trigger_deliveries_0029;

CREATE TABLE trigger_deliveries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger_id  TEXT NOT NULL,
    event_id    TEXT NOT NULL DEFAULT '',
    dedupe_key  TEXT NOT NULL DEFAULT '',
    outcome     TEXT NOT NULL CHECK (outcome IN
                  ('fired', 'seeded', 'deduped', 'filtered', 'rate_limited', 'refused', 'error')),
    task_id     INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    detail      TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

INSERT INTO trigger_deliveries (id, trigger_id, event_id, dedupe_key, outcome, task_id, detail, created_at)
SELECT id, trigger_id, event_id, dedupe_key, outcome, task_id, detail, created_at
FROM trigger_deliveries_0029;

DROP TABLE trigger_deliveries_0029;

CREATE INDEX idx_trigger_deliveries_key ON trigger_deliveries(trigger_id, dedupe_key);
CREATE INDEX idx_trigger_deliveries_created ON trigger_deliveries(trigger_id, created_at);
CREATE INDEX idx_trigger_deliveries_age ON trigger_deliveries(created_at);
