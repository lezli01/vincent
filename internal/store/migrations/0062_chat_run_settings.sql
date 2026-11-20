-- 0062_chat_run_settings: what each chat turn ran with, and a queued message's
-- per-turn overrides (task 162, issue #1011, spec §5.5, §14).
--
-- A chat's model, effort and permission mode may now change between turns
-- (task 162, superseding the creation-only rule of §5.5 and task 072
-- decision 1), so the chat row no longer says what an old turn ran with.
-- chat_turns records the value resolved for the turn — its override, else the
-- chat's setting at the moment the turn row was written.
--
-- Rows from before this migration hold NULL, which renders as "not recorded":
-- they are never back-filled from the chat's current values, which may not be
-- what those turns ran with.
ALTER TABLE chat_turns ADD COLUMN model TEXT;
ALTER TABLE chat_turns ADD COLUMN effort TEXT;
ALTER TABLE chat_turns ADD COLUMN permission_mode TEXT;

-- A message queued behind a live turn (task 156) keeps its own overrides until
-- it drains. NULL is "no override": the chat's setting at drain time applies.
ALTER TABLE chat_queued_messages ADD COLUMN model TEXT;
ALTER TABLE chat_queued_messages ADD COLUMN effort TEXT;
ALTER TABLE chat_queued_messages ADD COLUMN permission_mode TEXT;
