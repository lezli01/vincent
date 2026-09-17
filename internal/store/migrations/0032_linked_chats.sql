-- 0032_linked_chats: a chat linked to a task (task 119, spec §5.5, §14).
--
-- `linked_task_id` is the one authoritative edge, on `chats`, for the reason
-- 0023 put `handoff_task_id` there (task 074 decision 2): the lock a linked
-- chat places on its task, and the task's `open_chat_id`, are this column read
-- backwards over the index below, never a second stored copy that could
-- disagree with it.
--
-- A linked chat stores no `worktree_path`. The task keeps sole ownership of
-- the §10 claim, so gc's claim sets need no change and no chat-side removal
-- has anything to act on (task 119 decision 1); chatrun resolves the task's
-- path at the start of each turn.
--
-- ON DELETE CASCADE rather than 0023's SET NULL: a linked chat is the task's
-- history, not the origin of it, so permanently deleting the task (task 092)
-- takes its closed chats with it.
--
-- `opening_context` is the task context taskrun assembled when the chat was
-- opened, prepended to the first turn's prompt and to no later one. The task
-- cannot move while the chat is open, so the snapshot is exactly the context
-- at the first send.
--
-- There is no state CHECK on `chats`, so the new `closed` state needs no table
-- rebuild.
ALTER TABLE chats ADD COLUMN linked_task_id INTEGER REFERENCES tasks(id) ON DELETE CASCADE;
ALTER TABLE chats ADD COLUMN opening_context TEXT;

CREATE INDEX chats_linked_task_idx ON chats (linked_task_id, state) WHERE linked_task_id IS NOT NULL;
