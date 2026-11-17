-- 0056_jira_projects: a project bound to Jira syncs any number of Jira
-- projects on its site, each with its own optional JQL (task 146, spec
-- §5.1, §5.6).
--
-- jira_projects is a JSON array of {"key": "...", "jql": "..."} objects in
-- the order the binding names them, '' beside every provider but jira. It
-- replaces the single jira_project_key / jira_jql pair, which becomes its
-- first and only entry; the site and the mapping override stay shared by
-- every project the binding names.

ALTER TABLE projects ADD COLUMN jira_projects TEXT NOT NULL DEFAULT '';

UPDATE projects SET jira_projects = CASE
        WHEN jira_jql = '' THEN json_array(json_object('key', jira_project_key))
        ELSE json_array(json_object('key', jira_project_key, 'jql', jira_jql))
    END
WHERE jira_project_key != '';

ALTER TABLE projects DROP COLUMN jira_project_key;
ALTER TABLE projects DROP COLUMN jira_jql;
