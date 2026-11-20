-- 0063_chat_drafts: a chat's unsent draft and its one stash slot (task 164,
-- #1012, spec §5.5).
--
-- Columns on the chat rather than a table of their own: each chat has exactly
-- one draft and one stash, both are read with the chat, and a separate table
-- would only add a join to every chat read.
--
-- `draft_updated_at` is NULL until the first write. It is what the API echoes
-- back so a client can tell its own write's echo from another client's.
--
-- Both are cleared in the transaction that moves a chat to a terminal state
-- (archive, close, hand-off), as the queue is (task 156 decision 6): nothing
-- can ever send a draft from a finished chat. Permanent delete removes the row.
ALTER TABLE chats ADD COLUMN draft TEXT NOT NULL DEFAULT '';
ALTER TABLE chats ADD COLUMN draft_updated_at TEXT;
ALTER TABLE chats ADD COLUMN stash TEXT NOT NULL DEFAULT '';
