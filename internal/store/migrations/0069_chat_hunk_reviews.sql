-- 0069_chat_hunk_reviews: the hunks of a chat's diff a reader has accepted
-- (task 169.2, issue #1018, spec §5.5).
--
-- All review state lives in the daemon (task 169 decision 2), so a mark
-- survives a restart and is the same from every client. A mark is keyed by
-- the chat, the file's path (always /-separated, as a patch spells it) and a
-- hash of the hunk's content: nothing ever invalidates a row — once the hunk's
-- content changes its hash does too, and the old mark simply stops matching.
--
-- A rejected hunk has no row: a reject removes the mark, if there is one, and
-- leaves a note in the chat's draft (decision 8).
--
-- accepted_at is store.TimeFormat (RFC3339 UTC, fixed-width nanoseconds).
--
-- ON DELETE CASCADE: the marks go with the chat row (decision 9), on a
-- permanent delete of the chat and on its project's, as chat_queue's rows do
-- (0059). Foreign keys are enforced on every connection (`_foreign_keys=1`).
CREATE TABLE chat_hunk_reviews (
    chat_id     INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    hunk_hash   TEXT NOT NULL,
    accepted_at TEXT NOT NULL,
    PRIMARY KEY (chat_id, path, hunk_hash)
);
