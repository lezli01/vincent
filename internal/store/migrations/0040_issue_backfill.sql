-- 0040_issue_backfill: every task 035 GitHub issue snapshot becomes an issue
-- (task 130.11, issue #670, task 130 decision 21, spec §5.3, §14).
--
-- Before 130.11 a task created with `github_issue: N` carried only 0014's
-- `github_issue_json` snapshot. This migration gives each distinct
-- (project, repo, number) among those snapshots one `issues` row, so the
-- issues pillar sees the work that already happened. Because it is pure SQL
-- in the migration transaction, every `store.Open` path runs it: an upgraded
-- database, a whole-database restore of an older backup, and the staged copy
-- task import opens (task 117).
--
-- Grouping is over every task with a snapshot and no issue pointer yet —
-- lanes, archived and never-started tasks included. Lanes carry verbatim
-- copies of their parent's snapshot, so they fall into the parent's group
-- and add no issue. The same number in two repos is two issues.
--
-- Content is the **newest** snapshot's (latest task created_at, ties broken
-- by task id), including its state, even when every linked task is archived:
-- the first sync corrects it, and GitHub wins (decision 21.2). A closed row
-- needs a close reason (internal/issuestate: a closed issue carries one), so
-- it takes the snapshot's `state_reason` when that is `not_planned` and
-- `completed` otherwise — `duplicate` would need the issue it duplicates,
-- which a snapshot cannot name.
--
-- Timestamps are copied, never generated: created_at is the earliest linked
-- task's created_at, updated_at and (for a closed row) closed_at the newest
-- one's. They are store.TimeFormat text already.
--
-- The remote is a placeholder: `remote_key = 'legacy:owner/repo#N'` with
-- synced_at, remote_json and remote_updated_at NULL. The importer re-keys a
-- never-synced placeholder to the node_id on first sight (decision 21.3).
-- When a remote for the same (project, github, repo, number) already exists —
-- imported by 130.8 under its real node_id, or a tombstone — nothing is
-- created: tasks link to that remote's issue (or stay unlinked for a
-- tombstone, decision 6).
--
-- `issue_json` stays NULL — legacy rows render from `github_issue_json`
-- (130.4) — and `github_issue_json` is not touched.
--
-- No `events` rows are written. `issue.created` is emitted by the store's
-- write path, and a migration is not one.
--
-- Issue ids are derived here rather than left to AUTOINCREMENT so the
-- grouping can be joined back to the rows it creates: they continue from the
-- larger of sqlite_sequence's high-water mark and MAX(id), in group order,
-- and inserting them explicitly advances sqlite_sequence the usual way.
CREATE TEMP TABLE issue_backfill AS
SELECT
    (SELECT max(COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'issues'), 0),
                COALESCE((SELECT MAX(id) FROM issues), 0)))
        + ROW_NUMBER() OVER (ORDER BY g.project_id, g.repo, g.number) AS issue_id,
    g.project_id, g.repo, g.number, g.js, g.first_at, g.last_at,
    lower(trim(COALESCE(json_extract(g.js, '$.state'), ''))) = 'closed' AS closed
FROM (
    SELECT r.project_id, r.repo, r.number, r.js, r.first_at, r.created_at AS last_at
    FROM (
        SELECT s.*,
            ROW_NUMBER() OVER (PARTITION BY s.project_id, s.repo, s.number
                               ORDER BY s.created_at DESC, s.task_id DESC) AS rn,
            MIN(s.created_at) OVER (PARTITION BY s.project_id, s.repo, s.number) AS first_at
        FROM (
            SELECT t.id AS task_id, t.project_id, t.created_at, t.github_issue_json AS js,
                json_extract(t.github_issue_json, '$.repo') AS repo,
                json_extract(t.github_issue_json, '$.number') AS number
            FROM tasks t
            WHERE t.github_issue_json IS NOT NULL AND t.issue_id IS NULL
        ) s
        WHERE s.repo IS NOT NULL AND s.repo <> '' AND s.number IS NOT NULL
    ) r
    WHERE r.rn = 1
      AND NOT EXISTS (
        SELECT 1 FROM issue_remotes x
        WHERE x.project_id = r.project_id AND x.provider = 'github'
          AND x.repo = r.repo AND x.number = r.number)
) g;

INSERT INTO issues (id, project_id, title, body, state, close_reason, kind, priority, author,
                    created_at, updated_at, closed_at)
SELECT issue_id, project_id,
    COALESCE(json_extract(js, '$.title'), ''),
    COALESCE(json_extract(js, '$.body'), ''),
    CASE WHEN closed THEN 'closed' ELSE 'open' END,
    CASE WHEN NOT closed THEN NULL
         WHEN lower(COALESCE(json_extract(js, '$.state_reason'), '')) = 'not_planned' THEN 'not_planned'
         ELSE 'completed' END,
    '', 0,
    COALESCE(json_extract(js, '$.author'), ''),
    first_at, last_at,
    CASE WHEN closed THEN last_at END
FROM issue_backfill
ORDER BY issue_id;

INSERT INTO issue_remotes (issue_id, project_id, provider, remote_key, repo, number, url)
SELECT issue_id, project_id, 'github', 'legacy:' || repo || '#' || number, repo, number,
    COALESCE(json_extract(js, '$.url'), '')
FROM issue_backfill
ORDER BY issue_id;

INSERT OR IGNORE INTO labels (project_id, name, source)
SELECT b.project_id, j.value, 'github'
FROM issue_backfill b, json_each(b.js, '$.labels') j
WHERE j.type = 'text' AND trim(j.value) <> ''
ORDER BY b.issue_id, j.key;

INSERT OR IGNORE INTO issue_labels (issue_id, label_id)
SELECT b.issue_id, l.id
FROM issue_backfill b, json_each(b.js, '$.labels') j
JOIN labels l ON l.project_id = b.project_id AND l.name = j.value
WHERE j.type = 'text';

UPDATE tasks SET issue_id = (
    SELECT x.issue_id FROM issue_remotes x
    WHERE x.project_id = tasks.project_id AND x.provider = 'github'
      AND x.repo = json_extract(tasks.github_issue_json, '$.repo')
      AND x.number = json_extract(tasks.github_issue_json, '$.number')
    ORDER BY x.issue_id IS NULL, x.id
    LIMIT 1)
WHERE github_issue_json IS NOT NULL AND issue_id IS NULL;

DROP TABLE temp.issue_backfill;
