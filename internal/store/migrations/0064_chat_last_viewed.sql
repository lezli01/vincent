-- 0064_chat_last_viewed: when a person last looked at a chat (task 162).
--
-- The installation has no per-user identity, so there is one value per chat,
-- shared by every client: opening a chat in the TUI clears its unread mark
-- in every other client too. RFC3339 UTC in store.TimeFormat, so TEXT
-- comparison with chat_turns.ended_at is chronological; NULL = never viewed.
-- `unread` is not stored: it is derived on read from the latest terminal
-- turn, so no turn-finishing write path has to remember to set it.
ALTER TABLE chats ADD COLUMN last_viewed_at TEXT;
