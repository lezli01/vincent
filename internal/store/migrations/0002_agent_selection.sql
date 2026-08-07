-- 0002_agent_selection: task-level agent/model/effort overrides chosen at
-- creation, and the resolved model/effort recorded on every step run
-- (spec §8.6, §14; T1.8).

ALTER TABLE tasks ADD COLUMN agent_override  TEXT;  -- NULL = none
ALTER TABLE tasks ADD COLUMN model_override  TEXT;
ALTER TABLE tasks ADD COLUMN effort_override TEXT;

ALTER TABLE step_runs ADD COLUMN model  TEXT;  -- as passed to the adapter
ALTER TABLE step_runs ADD COLUMN effort TEXT;
