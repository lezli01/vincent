-- 0041_issue_comment_sync: the read-only mirror of GitHub issue comments
-- (task 130.16, task 130 decision 24, issue #675, spec §5.6, §14).
--
-- The comment pass keeps its own bookkeeping beside the issue pass's, on the
-- same per-project row: `comment_watermark` is the newest remote comment
-- `updated_at` seen, `comment_since` the bound the next incremental comment
-- poll asks for, `comment_etag` that poll's last validator. Separate columns
-- because the two passes list different endpoints and advance independently
-- — a failed comment poll must not move the issue watermark, nor the other
-- way round. Times are store.TimeFormat text, NULL for never.
ALTER TABLE issue_sync_state ADD COLUMN comment_watermark TEXT;
ALTER TABLE issue_sync_state ADD COLUMN comment_since TEXT;
ALTER TABLE issue_sync_state ADD COLUMN comment_etag TEXT NOT NULL DEFAULT '';

-- A mirrored comment is keyed by (issue_id, remote_key): a re-list updates
-- the row in place instead of appending a copy. Per issue, not global — two
-- projects sharing an origin hold two issue sets (task 130 open question 3),
-- so one GitHub comment legitimately lands under two issues. A local comment
-- stores remote_key NULL (AddIssueComment writes nullString("")), which the
-- predicate leaves out, so local comments never collide; '' is excluded too
-- so a stray empty key can never make two local rows clash.
CREATE UNIQUE INDEX issue_comments_remote_key
    ON issue_comments (issue_id, remote_key)
    WHERE remote_key IS NOT NULL AND remote_key <> '';
