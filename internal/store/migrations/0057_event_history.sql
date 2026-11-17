-- 0057_event_history: an identity for the event history (spec §13.3, issue
-- #1001). An event id names an event only within one history: `vincent daemon
-- restore` puts an older vincent.db in place, whose sequence then climbs back
-- over ids a client already saw from the replaced one. The generation is what
-- tells a reconnecting client its Last-Event-ID belongs to another history.
--
-- One row. A fresh installation starts at generation 1; the daemon bumps it
-- on its first start after a restore (internal/backup's marker file), because
-- restore itself opens no database (§4).

CREATE TABLE event_history (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    generation INTEGER NOT NULL
);

INSERT INTO event_history (id, generation) VALUES (1, 1);
