-- 0059_chat_queue: follow-up messages queued behind a live chat turn (task
-- 156, issue #1010, spec §5.5).
--
-- A queued message exists only behind a live turn. When that turn ends `done`
-- the runner pops the oldest row into a new chat_turns row in one transaction
-- that deletes it, so a message is a queued row or a turn and never both: a
-- started message is a turn, which §12.4 recovery finalizes `interrupted` and
-- never re-runs.
--
-- `position` orders the queue. It is append-only (MAX + 1): the queue has no
-- reorder, and edit and delete leave the others where they were.
CREATE TABLE chat_queued_messages (
    id         INTEGER PRIMARY KEY,
    chat_id    INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    prompt     TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);
CREATE INDEX idx_chat_queued_messages_chat ON chat_queued_messages(chat_id, position);

-- A turn that ends other than `done` — failed, interrupted, or cancelled by a
-- human — pauses the queue instead of draining it. The chat is still `idle`
-- and usable; the queue waits for `resume` or `clear`.
ALTER TABLE chats ADD COLUMN queue_paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN queue_pause_reason TEXT;
