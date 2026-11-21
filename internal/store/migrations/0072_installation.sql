-- 0072_installation: an identifier for this installation's database (task
-- 167, PR #1044 review F4, spec §10).
--
-- A chat's checkpoint refs live in the project repository's shared ref
-- namespace, which every vincent installation that registered the same
-- repository sees — a dev build's, a screenshots seed's, another user's.
-- Chat ids are only unique within one database, so the refs are scoped by
-- this id (refs/vincent/<install id>/chats/<chat id>/...): one
-- installation's orphan sweep never lists, let alone deletes, another's,
-- and two installations' chat 7 never share a ref.
--
-- It belongs to the database rather than the data directory because the chat
-- ids it scopes are the database's: a database copied elsewhere keeps both.
-- A singleton, minted once here and never changed: 16 hex characters of
-- randomness, which is a valid ref path component.
CREATE TABLE installation (
  id          INTEGER PRIMARY KEY CHECK (id = 1),
  install_id  TEXT NOT NULL
);
INSERT INTO installation (id, install_id) VALUES (1, lower(hex(randomblob(8))));
