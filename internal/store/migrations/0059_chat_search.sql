-- 0059_chat_search: a full-text index over every chat (task 156, #1022,
-- spec §5.5, §14).
--
-- What is indexed is a chat's title, each turn's prompt and each turn's
-- final answer (`result_text`) — never a transcript, which is tool output and
-- which §12.2's retention deletes. `result_text` is a column, not a file, so
-- an answer stays searchable after its transcript is gone.
--
-- Tokenizer: trigram (task 156 decision 2). Matching is by substring, so
-- `429` finds `status429Body` and `body_format` finds a path that contains
-- it; the unicode61 default would split neither. The cost is a larger index
-- and that a term under three characters cannot use it — the API refuses
-- such a query with `query_too_short` rather than scanning.
--
-- Two tables rather than one with a kind column. An external-content FTS5
-- table (`content=`) reads its text back from exactly one content table by
-- rowid, and the text lives in two: `chats.title` and `chat_turns`. A single
-- table would have to store its own copy of every prompt and answer to hold
-- both, doubling the text; two external-content tables store only the index.
-- `snippet()` works on an external-content table — it reads the column back
-- from the content table by rowid — so nothing here needs a copy. A title
-- match finds a chat that has no turns because the title table is keyed by
-- the chat itself.
--
-- Triggers keep both in step. An external-content table is told about a
-- removed row with the 'delete' command and the row's *old* values, so the
-- update triggers delete the old text and insert the new one. They are
-- narrowed with WHEN to a change of the indexed text: `UpdateChatTurn`
-- rewrites every column on each state write and `updateChat` rewrites the
-- title on every chat write, and re-indexing unchanged text on each of those
-- would be pure churn. `result_text` is written when a turn finishes, after
-- its row was inserted, which is the case the turn update trigger exists for;
-- a renamed chat (#1016) is the title one's.
--
-- Deleting a chat cascades to its turns (0022), and SQLite fires a child's
-- DELETE triggers for a cascaded delete, so permanent delete (task 092) and
-- project deletion leave nothing behind in either index.
CREATE VIRTUAL TABLE chat_title_fts USING fts5(
    title,
    content = 'chats',
    content_rowid = 'id',
    tokenize = 'trigram'
);

CREATE VIRTUAL TABLE chat_turn_fts USING fts5(
    prompt,
    result_text,
    content = 'chat_turns',
    content_rowid = 'id',
    tokenize = 'trigram'
);

CREATE TRIGGER chat_title_fts_ai AFTER INSERT ON chats BEGIN
    INSERT INTO chat_title_fts (rowid, title) VALUES (new.id, new.title);
END;

CREATE TRIGGER chat_title_fts_ad AFTER DELETE ON chats BEGIN
    INSERT INTO chat_title_fts (chat_title_fts, rowid, title) VALUES ('delete', old.id, old.title);
END;

CREATE TRIGGER chat_title_fts_au AFTER UPDATE OF title ON chats
WHEN old.title IS NOT new.title BEGIN
    INSERT INTO chat_title_fts (chat_title_fts, rowid, title) VALUES ('delete', old.id, old.title);
    INSERT INTO chat_title_fts (rowid, title) VALUES (new.id, new.title);
END;

CREATE TRIGGER chat_turn_fts_ai AFTER INSERT ON chat_turns BEGIN
    INSERT INTO chat_turn_fts (rowid, prompt, result_text) VALUES (new.id, new.prompt, new.result_text);
END;

CREATE TRIGGER chat_turn_fts_ad AFTER DELETE ON chat_turns BEGIN
    INSERT INTO chat_turn_fts (chat_turn_fts, rowid, prompt, result_text)
        VALUES ('delete', old.id, old.prompt, old.result_text);
END;

CREATE TRIGGER chat_turn_fts_au AFTER UPDATE OF prompt, result_text ON chat_turns
WHEN old.prompt IS NOT new.prompt OR old.result_text IS NOT new.result_text BEGIN
    INSERT INTO chat_turn_fts (chat_turn_fts, rowid, prompt, result_text)
        VALUES ('delete', old.id, old.prompt, old.result_text);
    INSERT INTO chat_turn_fts (rowid, prompt, result_text) VALUES (new.id, new.prompt, new.result_text);
END;

-- Backfill every existing row. 'rebuild' re-reads an external-content
-- table's whole content table, which is exactly the backfill.
INSERT INTO chat_title_fts (chat_title_fts) VALUES ('rebuild');
INSERT INTO chat_turn_fts (chat_turn_fts) VALUES ('rebuild');
