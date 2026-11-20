-- 0064_chat_last_viewed: when a person last looked at a chat (task 165).
--
-- The installation has no per-user identity, so there is one value per chat,
-- shared by every client: opening a chat in the TUI clears its unread mark
-- in every other client too. RFC3339 UTC in store.TimeFormat, so TEXT
-- comparison with chat_turns.ended_at is chronological; NULL = never viewed.
-- `unread` is not stored: it is derived on read from the latest terminal
-- turn, so no turn-finishing write path has to remember to set it.
ALTER TABLE chats ADD COLUMN last_viewed_at TEXT;

-- Every chat that existed before this column starts read (review F2): nobody
-- looked at its history "after" anything, and leaving it NULL would light the
-- unread mark on every chat that ever finished a turn, archived ones
-- included, burying the marks the feature is for. The seed is the same
-- instant chatUnreadExpr compares against — the latest terminal turn's end,
-- or its start when recovery left no end — so the derived flag reads false;
-- a chat with no finished turn stays NULL, which already reads as read.
UPDATE chats SET last_viewed_at = (
	SELECT MAX(COALESCE(ct.ended_at, ct.started_at)) FROM chat_turns ct
	WHERE ct.chat_id = chats.id AND ct.state IN ('done', 'failed', 'interrupted')
);
