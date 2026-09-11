-- 0029_triggers: runtime state for event triggers (task 096.2, spec §14).
--
-- A trigger's *definition* is YAML under {config_dir}/triggers/ (task 096
-- decision 4); only what the daemon learns while running one lives here — the
-- poll cursor with its health, and the delivery ledger. Both are keyed by the
-- trigger id as text rather than by a row of a triggers table, because there
-- is no such table: the file is the source of truth, and a table mirroring it
-- would be a second one to keep in sync.

-- trigger_cursors is one row per trigger that has polled. A NULL cursor is an
-- *unseeded* trigger: its next poll seeds and fires nothing (decisions 6 and
-- 16). Poll status sits beside the cursor because it has the same lifetime —
-- both follow the file, and both are dropped when the file leaves the
-- registry.
CREATE TABLE trigger_cursors (
    trigger_id      TEXT PRIMARY KEY,
    cursor          TEXT,
    last_poll_at    TEXT,
    last_poll_ok    INTEGER NOT NULL DEFAULT 0,
    last_poll_error TEXT NOT NULL DEFAULT '',
    last_fire_at    TEXT
);

-- trigger_deliveries is the ledger: one row per event the pipeline judged,
-- whatever it decided. It outlives the trigger's file on purpose — deleting a
-- trigger and re-creating the same id must not refire an event that already
-- fired (decision 21) — and is pruned at 30 days by the §17 pass (decision 13).
--
-- task_id is ON DELETE SET NULL rather than CASCADE: a `fired` row is the
-- dedupe record for its key, and deleting the task it created must not make
-- the event fire again. It is the link the view's `enter` follows (decision
-- 26), so it goes null with the task rather than dangling.
--
-- detail holds the §13.1 error envelope of a `refused` delivery, or the error
-- text of an `error` one; empty otherwise.
CREATE TABLE trigger_deliveries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger_id  TEXT NOT NULL,
    event_id    TEXT NOT NULL DEFAULT '',
    dedupe_key  TEXT NOT NULL DEFAULT '',
    outcome     TEXT NOT NULL CHECK (outcome IN
                  ('fired', 'deduped', 'filtered', 'rate_limited', 'refused', 'error')),
    task_id     INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    detail      TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

-- The dedupe lookup: "has this key fired for this trigger".
CREATE INDEX idx_trigger_deliveries_key ON trigger_deliveries(trigger_id, dedupe_key);
-- The rate-limit count, the newest-first ledger read, and the prune scan.
CREATE INDEX idx_trigger_deliveries_created ON trigger_deliveries(trigger_id, created_at);
CREATE INDEX idx_trigger_deliveries_age ON trigger_deliveries(created_at);
