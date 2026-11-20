-- 0066_chat_checkpoints: a checkpoint of the chat's worktree per turn (task
-- 165.2, issue #1017, spec §5.5, §12.4).
--
-- checkpoint_sha is the snapshot commit written as
-- refs/vincent/chats/<chat id>/turn-<seq> before the turn's agent process
-- starts: the worktree as `git add -A` saw it, parented on head_before. It is
-- stored, together with head_before, before adapter.Start, so a crash after
-- the spawn finds both already committed (crash-first, §12.4).
--
-- head_after is the worktree's HEAD when the turn ended. The pair is what the
-- shared-worktree rewind rule (task 165 decision 3) compares between
-- consecutive turns.
--
-- Rows from before this migration, and turns that never got as far as the
-- checkpoint, hold NULL: no checkpoint, nothing to rewind to.
ALTER TABLE chat_turns ADD COLUMN checkpoint_sha TEXT;
ALTER TABLE chat_turns ADD COLUMN head_before TEXT;
ALTER TABLE chat_turns ADD COLUMN head_after TEXT;
