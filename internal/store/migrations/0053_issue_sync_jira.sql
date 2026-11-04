-- 0053_issue_sync_jira: the importer bookkeeping a Jira source needs (task
-- 142.12, issue #908, spec §5.6, §14).
--
-- remote_project_id is the provider's stable id of the bound project,
-- recorded on the first pass that resolves it (task 142 decision 13.6): a
-- Jira project key can be renamed or reassigned to another project, so the
-- sticky comparison is on the id, and a pass that finds a different one
-- stops with binding_changed. NULL is "never resolved" — every GitHub row,
-- whose sticky key stays repo. A rebind deletes the row, which resets it.
--
-- import_cursor is the opaque resume point of an initial import whose
-- listing is ordered by id rather than by update time (Jira's
-- `ORDER BY id ASC` resumed with `id > cursor`): '' when no import is under
-- way or the source resumes by time, as GitHub's does.

ALTER TABLE issue_sync_state ADD COLUMN remote_project_id TEXT;
ALTER TABLE issue_sync_state ADD COLUMN import_cursor TEXT NOT NULL DEFAULT '';
