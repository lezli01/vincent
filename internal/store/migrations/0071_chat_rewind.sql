-- 0071_chat_rewind: rewinding a chat to a turn's checkpoint, and forking a
-- new chat from one (task 167.5, 167.6, issue #1041, spec §5.5).
--
-- A rewind never deletes a turn. rewound_at and rewind_id mark the turns a
-- rewind took out of the conversation (every turn from the target on), one
-- rewind_id per rewind so a later surface can group them; they stay in the
-- transcript and in every cost and token total (§17), and a new turn's seq
-- continues after them.
--
-- seeded marks a turn that started a fresh agent session carrying a bounded
-- replay of the earlier conversation (task 167 decision G) rather than
-- resuming the agent's own session, so every surface can say "seeded, not
-- resumed". resume_anchor is the claude assistant-message uuid a turn
-- resumed at with --resume-session-at (decision A); NULL is "none".
ALTER TABLE chat_turns ADD COLUMN rewound_at TEXT;
ALTER TABLE chat_turns ADD COLUMN rewind_id TEXT;
ALTER TABLE chat_turns ADD COLUMN seeded INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chat_turns ADD COLUMN resume_anchor TEXT;

-- The chat's resume plan for its next turn, written by a rewind or a fork:
-- resume_at is the anchor the next turn resumes the session at, and
-- pending_preamble the seeded context it starts a fresh session with
-- instead. The turn that consumes the plan clears it.
--
-- forked_from_chat_id and forked_from_seq name the chat and turn a fork was
-- cut from. There is deliberately no foreign key: the source may later be
-- permanently deleted (task 092), and the fork must outlive it.
ALTER TABLE chats ADD COLUMN resume_at TEXT;
ALTER TABLE chats ADD COLUMN pending_preamble TEXT;
ALTER TABLE chats ADD COLUMN forked_from_chat_id INTEGER;
ALTER TABLE chats ADD COLUMN forked_from_seq INTEGER;
