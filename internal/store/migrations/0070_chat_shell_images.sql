-- 0070_chat_shell_images: a chat's shell runs and its image attachments
-- (task 170, issue #1024, spec §5.5).
--
-- chat_shell_runs is one command a human ran in the chat's workspace through
-- the daemon. It is not a turn: it holds no max_parallel_chats slot and its
-- output reaches the agent only when the human marks the finished run for
-- the next prompt (`attach`), which the first turn to start then takes,
-- recording its seq in attached_turn and clearing the mark.
--
-- state is one of running, exited, timed_out, interrupted, canceled, failed.
-- pid and proc_identity are journaled as a turn's are (0013's PID-reuse
-- guard), so recovery can prove an orphan before killing it. output_path is
-- the file beside the chat's transcripts that holds the capped combined
-- output; truncated says the cap was reached. after_turn is the seq of the
-- last turn that existed when the run started (0 for none), which is where a
-- client draws the run in the conversation.
--
-- chat_images is one image a client uploaded, numbered per chat from 1; the
-- composer's `[image N]` chip names it and a turn resolves the chip to path.
--
-- Times are store.TimeFormat. ON DELETE CASCADE: both go with the chat row
-- on a permanent delete, as chat_hunk_reviews' rows do (0069); the files are
-- removed with the chat's transcript directory, which holds them.
CREATE TABLE chat_shell_runs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id       INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    command       TEXT NOT NULL,
    state         TEXT NOT NULL,
    exit_code     INTEGER,
    started_at    TEXT NOT NULL,
    ended_at      TEXT,
    output_path   TEXT NOT NULL DEFAULT '',
    output_bytes  INTEGER NOT NULL DEFAULT 0,
    truncated     INTEGER NOT NULL DEFAULT 0,
    pid           INTEGER,
    proc_identity TEXT,
    attach        INTEGER NOT NULL DEFAULT 0,
    attached_turn INTEGER,
    after_turn    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX chat_shell_runs_chat ON chat_shell_runs(chat_id, id);

CREATE TABLE chat_images (
    chat_id    INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    number     INTEGER NOT NULL,
    media_type TEXT NOT NULL,
    bytes      INTEGER NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    path       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (chat_id, number)
);
