-- 0022_chats: chats and their turns (task 063, spec §5.5, §14).
--
-- Two new tables rather than a `kind` column on `tasks`. The alternative was
-- considered and rejected in the issue and again in the brief: a chat has no
-- workflow snapshot, no step ledger and no §6 lifecycle, so a chat row in
-- `tasks` would force every existing query — the board, admission, the §17
-- aggregates — to decide whether it means chats too, and a chat carrying a
-- `current_step` would be a lie to every reader of the snapshot.
--
-- `chat_turns` is deliberately not `step_runs` with a nullable `task_id`, for
-- the same reason: `step_runs.task_id` stays NOT NULL, so every query and
-- every §17 aggregate over it keeps exactly its current meaning. The columns
-- are the accounting half of `step_runs` (tokens, cost, duration, pid, exit
-- code, proc identity) because that is the gap the issue named — talking to an
-- agent outside vincent has no transcript and no cost record.
--
-- `session_id` on `chats` is the whole of §7.3's chat-only amendment: the
-- agent CLI's own conversation id, handed back as `--resume` on the next turn.
-- It also rides on each turn row, so a reader can see which session a given
-- turn actually ran in — claude may hand a resumed conversation a new id, and
-- a turn that failed `session_lost` names the id that was refused.
--
-- ON DELETE CASCADE mirrors tasks': `DELETE /v1/projects/{id}` drops a
-- project's chats, and gc reconciles the directories that leaves behind (§10).
CREATE TABLE chats (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title           TEXT    NOT NULL,
    state           TEXT    NOT NULL, -- §5.5: idle | running | awaiting_input | archived
    agent           TEXT    NOT NULL, -- adapter name; must be one that can resume (§9.1)
    model           TEXT,             -- NULL/'' = CLI default (§8.6)
    effort          TEXT,
    permission_mode TEXT    NOT NULL DEFAULT 'full_auto',
    branch          TEXT    NOT NULL, -- vincent/{id}-{slug}, as a task's (§10)
    base_branch     TEXT    NOT NULL,
    base_sha        TEXT,             -- the fetched commit the branch starts at, when one resolved
    worktree_path   TEXT,             -- the §10 claim; NULL once archived
    session_id      TEXT,             -- the agent CLI's own session; NULL before the first turn
    pending_input   TEXT,             -- the §7.4 request awaiting an answer, as JSON; NULL otherwise
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

CREATE INDEX chats_project_idx ON chats (project_id);
CREATE INDEX chats_state_idx ON chats (state, id);

CREATE TABLE chat_turns (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id       INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    seq           INTEGER NOT NULL, -- 1-based position in the conversation
    prompt        TEXT    NOT NULL, -- the human's message
    state         TEXT    NOT NULL, -- running | done | failed | interrupted
    fail_reason   TEXT,             -- the shared snake_case vocabulary; session_lost lives here
    error_message TEXT,
    result_text   TEXT,             -- the agent's final answer
    session_id    TEXT,             -- the session this turn ran in
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL,             -- NULL = the adapter reports none (§9.3, §9.7)
    exit_code     INTEGER,
    pid           INTEGER,          -- while running, for §12.4's orphan kill
    proc_identity TEXT,             -- the 0013 PID-reuse guard, same contract
    started_at    TEXT    NOT NULL,
    ended_at      TEXT,
    duration_ms   INTEGER
);

CREATE UNIQUE INDEX chat_turns_seq_idx ON chat_turns (chat_id, seq);
CREATE INDEX chat_turns_state_idx ON chat_turns (state);
