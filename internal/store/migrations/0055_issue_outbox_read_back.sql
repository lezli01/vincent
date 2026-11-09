-- 0055_issue_outbox_read_back: when the importer first adopted a read of
-- an issue after its set_state write settled done (task 142.22, #953,
-- review F1 on #956).
--
-- A post-function that rewrites the resolution a transition chose shows on
-- the first read the importer adopts after the write, and only there: a
-- later read with another reason is someone re-resolving the issue in the
-- tracker, not the write being overridden. read_back_at is stamped on that
-- first adopted read, whatever it found, and a stamped row is never
-- re-marked resolution_overridden again. NULL is "not read back yet" — every
-- row that is not done, and a done row whose next refresh is still to come.
-- Rows settled before this migration start NULL; the importer then also
-- requires the issue to still hold the row's desired value, so one whose
-- re-resolve upstream was adopted already is not flagged on upgrade.

ALTER TABLE issue_sync_outbox ADD COLUMN read_back_at TEXT;
