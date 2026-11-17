-- 0058_global_pause: the daemon-wide pause (task 149, spec §6, §11).
--
-- global_pause is a singleton: a row means "paused", no row means "active".
-- It is durable on purpose — the pause exists for maintenance, and the most
-- common maintenance is restarting or upgrading the daemon, which must come
-- back still paused rather than admitting the whole queue on its first walk.
-- `reason` is the optional sentence a human gave when pausing; '' is none.
CREATE TABLE global_pause (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  paused_at  TEXT NOT NULL,
  reason     TEXT NOT NULL DEFAULT ''
);

-- tasks.global_pause marks a task whose pause — pending (pause_requested) or
-- taken (state = paused) — was the global pause's rather than a human's on
-- that task alone. `continue` resumes exactly these, so a task a human had
-- paused by hand before the global pause stays paused after it. Every human
-- action on the task clears it, the way every human action but `pause`
-- clears pause_requested (§6).
ALTER TABLE tasks ADD COLUMN global_pause INTEGER NOT NULL DEFAULT 0;
