-- 0011_agent_quota: the daemon's last first-hand observation of an adapter's
-- usage window (task 026, spec §14).
--
-- Task 003 already learns this fact and then throws it away: a quota stop
-- writes a step_runs row and an effective reset, but the reset lands only in
-- the held task's `admit_not_before`, which any transition out of `queued`
-- clears (task 003 decision 1). The observation therefore survives exactly as
-- long as the hold, which is precisely when nobody needs to be told.
--
-- One row per adapter, not per stop. History on step_runs would keep more, but
-- every read is then a scan-and-pick-latest per adapter, and current state is
-- what all three surfaces (§15 board header, daemon view, new-task form) want.
--
-- No `used_percent` or `window` column. Both exist on the wire (§9.6) as
-- permanent nulls so clients are written once against the final shape, but no
-- CLI vincent ships can report either, and a column with no writer is dead
-- schema in an append-only migration set.
CREATE TABLE agent_quota (
  agent              TEXT PRIMARY KEY,   -- adapter name, not a binary path
  observed_at        TEXT NOT NULL,      -- when the stop was seen
  resets_at          TEXT NOT NULL,      -- the effective reset the engine acted on
  resets_at_reported INTEGER NOT NULL,   -- 1 = the CLI named it; 0 = usage_limit_recheck_interval supplied it
  source             TEXT NOT NULL       -- 'observed'; the seam a probe would fill
);
