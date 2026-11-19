-- 0060_chat_turn_context: a chat turn's cache counts and its context figures
-- (task 157, issue #1015, spec §5.5, §9.2, §14).
--
-- cache_read_tokens and cache_creation_tokens are the turn's own cache spend,
-- which the turn heading adds to input_tokens to show what the turn actually
-- processed (task 157 decision 3). On a cumulative-usage adapter (copilot)
-- they are this turn's share, computed in internal/chatrun the way
-- input_tokens is (task 138.11 decision 1), from the raw session totals in
-- session_cache_read_tokens and session_cache_creation_tokens.
--
-- context_tokens is the prompt size of the run's last top-level model call,
-- and context_window the window the run reported for its model (decisions 1
-- and 2). They feed the chat's context gauge.
--
-- Every column is NULL when the adapter did not report it, and on turns
-- written before this migration: NULL is "not reported", never zero.
ALTER TABLE chat_turns ADD COLUMN cache_read_tokens INTEGER;
ALTER TABLE chat_turns ADD COLUMN cache_creation_tokens INTEGER;
ALTER TABLE chat_turns ADD COLUMN context_tokens INTEGER;
ALTER TABLE chat_turns ADD COLUMN context_window INTEGER;
ALTER TABLE chat_turns ADD COLUMN session_cache_read_tokens INTEGER;
ALTER TABLE chat_turns ADD COLUMN session_cache_creation_tokens INTEGER;
