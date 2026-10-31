-- 0050_chat_issue: a chat opened on an issue (task 140, issue #885, spec
-- §5.5, §5.6).
--
-- `issue_id` names the issue whose main worktree the chat holds. It is NULL
-- for a free chat and for one linked to a task, and goes NULL when the issue
-- is deleted, as tasks.issue_id does.
--
-- `end_sha` is the commit the chat's work ended on, stamped when it lets go
-- of the issue's main worktree — at close, at archive, or when the next main
-- task receives the directory — exactly as tasks.end_sha is (task 134.12).
ALTER TABLE chats ADD COLUMN issue_id INTEGER REFERENCES issues(id) ON DELETE SET NULL;
ALTER TABLE chats ADD COLUMN end_sha TEXT;
CREATE INDEX idx_chats_issue ON chats(issue_id) WHERE issue_id IS NOT NULL;
