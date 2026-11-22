-- 0074_chat_compares: one prompt answered by two agents side by side (task
-- 171, issue #1020, spec §5.5).
--
-- A compare forks a chat twice from the state before turn seq — once per
-- agent — and sends the same prompt to both forks. This row is what pairs
-- them: the TUI's split view reads it, and keeping one side (task 171
-- decision 8) records kept_chat_id here.
--
-- left_* and right_* are each side's chat, its agent and the permission mode
-- it actually runs under. A side runs restricted where its agent can keep a
-- chat restricted, and full-auto where it cannot (decision 7); mode_note
-- then says why, so every surface states what each side ran under rather
-- than what was asked for.
--
-- source_chat_id has no foreign key, as forked_from_chat_id has none (0071):
-- the source may be permanently deleted and the pair still means something.
-- The sides' chat ids are ON DELETE SET NULL, so deleting one fork leaves
-- the other's record of the comparison; the row goes with its project.
--
-- created_at is store.TimeFormat.
CREATE TABLE chat_compares (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_chat_id   INTEGER NOT NULL,
    seq              INTEGER NOT NULL,
    prompt           TEXT NOT NULL,
    left_chat_id     INTEGER REFERENCES chats(id) ON DELETE SET NULL,
    left_agent       TEXT NOT NULL,
    left_mode        TEXT NOT NULL,
    left_mode_note   TEXT NOT NULL DEFAULT '',
    right_chat_id    INTEGER REFERENCES chats(id) ON DELETE SET NULL,
    right_agent      TEXT NOT NULL,
    right_mode       TEXT NOT NULL,
    right_mode_note  TEXT NOT NULL DEFAULT '',
    kept_chat_id     INTEGER,
    created_at       TEXT NOT NULL
);

CREATE INDEX chat_compares_left ON chat_compares(left_chat_id);
CREATE INDEX chat_compares_right ON chat_compares(right_chat_id);
