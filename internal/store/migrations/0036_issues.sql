-- 0036_issues: vincent-owned issues per project (task 130 decisions 1-6,
-- spec §5.6, §14).
--
-- `issues.id` is the one global identity (decision 1): there is no
-- per-project number, so there is no counter to keep and no second key to
-- disambiguate. `parent_issue_id` is the seam for sub-issues and is never
-- written in v1.
--
-- There is no state CHECK on `issues`, for the reason 0032 gave for chats:
-- a later state (the handoff pillar's `waiting`, say) needs a row in
-- internal/issuestate, not a table rebuild (decision 3). Whether an issue is
-- being worked on is not a column at all — it is read from the root tasks
-- that point at it, so it cannot disagree with them.
--
-- `priority` is Linear's scale, 0 none, 1 urgent … 4 low, which is inverted
-- relative to a task's (decision 4). `kind` is an unconstrained token.
--
-- `issue_remotes` holds an imported issue's link to its source (decision 2):
-- for GitHub `remote_key` is the node_id, which survives a transfer and a
-- rename; `repo` and `number` are for display and the API calls. The key is
-- unique **per project**, deliberately not globally: two projects sharing an
-- origin each import their own issue set, and deleting an issue in one never
-- tombstones it in the other. A row whose `issue_id` is NULL is a tombstone —
-- the issue was deleted, and the next poll must not import it again
-- (decision 6) — which is why the FK is SET NULL rather than CASCADE.
--
-- Labels are one catalogue per project, unique case-insensitively
-- (decision 4); `source` is `local` or the provider a label was imported
-- from.
--
-- `tasks.issue_id` is the pointer and `tasks.issue_json` the snapshot
-- (decision 5): templates render the snapshot, offline and reproducibly, as
-- they render 0014's; the pointer answers "which tasks came from this issue"
-- read backwards over the partial index. SET NULL because deleting an issue
-- leaves its tasks, and their snapshots, exactly as they were (decision 6).
-- An FK column added by ALTER must default to NULL, which this one does.
CREATE TABLE issues (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id            INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title                 TEXT NOT NULL,
    body                  TEXT NOT NULL DEFAULT '',
    state                 TEXT NOT NULL DEFAULT 'open',
    close_reason          TEXT,
    duplicate_of_issue_id INTEGER REFERENCES issues(id) ON DELETE SET NULL,
    kind                  TEXT NOT NULL DEFAULT '',
    priority              INTEGER NOT NULL DEFAULT 0,
    author                TEXT NOT NULL DEFAULT '',
    parent_issue_id       INTEGER REFERENCES issues(id) ON DELETE SET NULL,
    version               INTEGER NOT NULL DEFAULT 1,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    closed_at             TEXT
);

CREATE INDEX issues_project_state_idx ON issues (project_id, state, updated_at);

CREATE TABLE issue_remotes (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id          INTEGER UNIQUE REFERENCES issues(id) ON DELETE SET NULL, -- NULL = tombstone
    project_id        INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider          TEXT NOT NULL,
    remote_key        TEXT NOT NULL,
    repo              TEXT NOT NULL DEFAULT '',
    number            INTEGER,
    url               TEXT NOT NULL DEFAULT '',
    remote_json       TEXT,
    remote_updated_at TEXT,
    synced_at         TEXT, -- NULL = never synced
    suppressed        INTEGER NOT NULL DEFAULT 0,
    UNIQUE (project_id, provider, remote_key)
);

CREATE TABLE labels (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL COLLATE NOCASE,
    color       TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT 'local',
    UNIQUE (project_id, name)
);

CREATE TABLE issue_labels (
    issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (issue_id, label_id)
);

CREATE TABLE issue_comments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    author     TEXT NOT NULL DEFAULT '',
    body       TEXT NOT NULL,
    remote_key TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

ALTER TABLE tasks ADD COLUMN issue_id INTEGER REFERENCES issues(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN issue_json TEXT; -- NULL = not created from an issue

CREATE INDEX tasks_issue_idx ON tasks (issue_id) WHERE issue_id IS NOT NULL;
