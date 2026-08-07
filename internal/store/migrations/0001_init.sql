-- 0001_init: initial schema (spec §14). schema_migrations itself is owned by
-- the migration runner, not by migration files.

CREATE TABLE projects (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  name                TEXT NOT NULL UNIQUE,
  path                TEXT NOT NULL,
  default_branch      TEXT NOT NULL,
  default_workflow    TEXT,
  max_parallel_tasks  INTEGER,                -- NULL = unlimited (global cap still applies)
  created_at          TEXT NOT NULL,          -- RFC3339 UTC throughout
  updated_at          TEXT NOT NULL
);

CREATE TABLE tasks (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id          INTEGER NOT NULL REFERENCES projects(id),
  title               TEXT NOT NULL,
  description         TEXT NOT NULL DEFAULT '',
  fields_json         TEXT NOT NULL DEFAULT '{}',
  workflow_name       TEXT NOT NULL,
  workflow_snapshot   TEXT NOT NULL,          -- full YAML at creation (incl. any edit+retry overrides)
  base_branch         TEXT NOT NULL,
  branch_name         TEXT NOT NULL,
  worktree_path       TEXT,
  priority            INTEGER NOT NULL DEFAULT 0,
  state               TEXT NOT NULL,          -- spec §6
  current_step        INTEGER NOT NULL DEFAULT 0,
  block_reason        TEXT,                   -- set while state='blocked'
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  started_at          TEXT,
  finished_at         TEXT,
  archived_at         TEXT
);
CREATE INDEX idx_tasks_sched ON tasks(state, priority DESC, created_at);

CREATE TABLE step_runs (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id             INTEGER NOT NULL REFERENCES tasks(id),
  step_index          INTEGER NOT NULL,
  step_id             TEXT NOT NULL,
  step_type           TEXT NOT NULL,          -- agent | command | manual
  attempt             INTEGER NOT NULL,       -- 1-based
  state               TEXT NOT NULL,          -- running | succeeded | failed | interrupted
                                              -- | approved | rejected | skipped
  agent               TEXT,                   -- adapter name, agent steps only
  pid                 INTEGER,                -- while running
  proc_started_at     TEXT,
  exit_code           INTEGER,
  check_exit_code     INTEGER,
  failure_reason      TEXT,
  result_summary      TEXT,                   -- agent result text / command stdout tail
  transcript_path     TEXT,
  input_tokens        INTEGER,
  output_tokens       INTEGER,
  cost_usd            REAL,                   -- NULL when the agent doesn't report cost
  started_at          TEXT NOT NULL,
  finished_at         TEXT
);
CREATE INDEX idx_step_runs_task ON step_runs(task_id, step_index, attempt);

CREATE TABLE events (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,   -- SSE Last-Event-ID cursor
  ts            TEXT NOT NULL,
  type          TEXT NOT NULL,
  task_id       INTEGER,
  project_id    INTEGER,
  payload_json  TEXT NOT NULL
);
CREATE INDEX idx_events_task ON events(task_id, id);
