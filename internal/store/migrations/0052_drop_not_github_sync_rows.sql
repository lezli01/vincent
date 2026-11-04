-- 0052_drop_not_github_sync_rows: a project whose issue source resolves to
-- none has no issue_sync_state row (task 142.7, issue #903, spec §5.6).
--
-- Until now the importer wrote a 'not_github' row for every project whose
-- origin was not a github.com remote, so the API could report why it was not
-- syncing. With explicit bindings (0051) and one sync loop per bound
-- provider, a project resolving to none is simply not polled: the importer
-- writes no row for it and the API derives 'not_github' when there is none.
-- The rows written before this migration would otherwise outlive that rule —
-- and a stale one would read as a failed GitHub sync after the project is
-- rebound — so they go. Any other reason is a real attempt's outcome and
-- stays.

DELETE FROM issue_sync_state WHERE reason = 'not_github';
