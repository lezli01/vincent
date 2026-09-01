-- 0023_chat_handoff: a chat handed off to a task (task 074, spec §5.5, §14).
--
-- One nullable foreign key, on `chats`, and no column on `tasks`. The reverse
-- direction — a task's `source_chat_id` — is a lookup over this index, not a
-- second stored copy, so there is exactly one authoritative edge and no way
-- for the two ends to disagree.
--
-- The column is on `chats` rather than on `tasks` because task 063's posture
-- is that no existing task query changes meaning when chats exist, and the
-- chat is the entity whose lifecycle a handoff changes: a handed-off chat is
-- terminal, and this is the fact that makes it so.
--
-- ON DELETE SET NULL rather than CASCADE: deleting the task must not delete
-- the conversation that produced it. The chat stays `handed_off` — the state
-- is a lifecycle fact, not a rendering of this column — and simply stops
-- naming a task that no longer exists.
ALTER TABLE chats ADD COLUMN handoff_task_id INTEGER REFERENCES tasks(id) ON DELETE SET NULL;

-- Partial, because the only query over it asks for the rows that have one:
-- `WHERE handoff_task_id IS NOT NULL`, read once per task list and turned into
-- a map, never once per rendered task.
CREATE INDEX chats_handoff_task_idx ON chats (handoff_task_id) WHERE handoff_task_id IS NOT NULL;
