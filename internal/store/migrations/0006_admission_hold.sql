-- 0006_admission_hold: a per-task hold on scheduler admission, and the reason
-- a queued task is waiting (task 003, spec §11/§14).
--
-- The pair is deliberately generic rather than usage-limit-specific. The
-- scheduler reads one timestamp and clients render one string, so the next
-- wait-shaped case — an agent-wide hold, a git backoff — costs no second
-- migration and no second branch in the admission walk.
--
-- `block_reason` was not overloaded for this. §14 says it is "set while
-- state='blocked'", and clients key off the API's `block_reason` field to mean
-- exactly that; a queued task carrying one would break them.
--
-- No index accompanies these. ListAdmissible already returns the whole queued
-- set in §11 order and the hold is evaluated during the walk, next to the
-- caps — never in SQL, because a task whose pause was requested while it ran
-- must still be parked while it is held (see internal/scheduler).

ALTER TABLE tasks ADD COLUMN admit_not_before TEXT;  -- NULL = admissible now
ALTER TABLE tasks ADD COLUMN queued_reason TEXT;     -- NULL = waiting for a slot
