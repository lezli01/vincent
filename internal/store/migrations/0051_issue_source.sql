-- 0051_issue_source: a project's explicit issue-source binding (task 142.6,
-- issue #902, spec §5.1/§5.6). issue_provider '' derives the source from
-- origin exactly as before this migration — github when origin is a
-- github.com remote, none otherwise — so every existing project keeps its
-- behaviour. 'none', 'github' and 'jira' pin it. The jira_* columns are set
-- only beside 'jira'; the API clears them for every other value.
--
-- No column here holds a secret: the Jira credential comes from the
-- environment only (task 142 decision 4). The recorded Jira project id and
-- the sticky comparison belong on issue_sync_state and arrive with their
-- first writer (#908).

ALTER TABLE projects ADD COLUMN issue_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN jira_site TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN jira_project_key TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN jira_jql TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN jira_mapping_json TEXT NOT NULL DEFAULT '';
