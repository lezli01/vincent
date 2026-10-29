-- 0049_chat_turn_session_usage: the raw session totals a chat turn's agent
-- reported, kept beside the turn's own figures (task 138.11, issue #863,
-- spec §9.9, §14; task 138.11 decision 1).
--
-- Copilot's usage checkpoint is the whole session's running total, so a
-- resumed turn's input_tokens/output_tokens are computed in internal/chatrun
-- as these columns minus the same session's previous turn's. NULL on every
-- turn of an adapter that reports per-run figures, and on turns written
-- before this migration.
ALTER TABLE chat_turns ADD COLUMN session_input_tokens INTEGER;
ALTER TABLE chat_turns ADD COLUMN session_output_tokens INTEGER;
