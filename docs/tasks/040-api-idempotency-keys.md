# 040 — Idempotency keys for task creation

**Issue:** [#146](https://github.com/lezli01/vincent/issues/146) (P2, API
correctness). Parent review [#135](https://github.com/lezli01/vincent/issues/135).

**Spec:** §13.1 (transport, amended 2026-08-28), §13.2 (`POST /v1/tasks`), §14
(`idempotency_keys`), §17 (retention).

## The problem

A mutating request that commits and then loses its response leaves the client
unable to tell "not processed" from "processed, response lost". For most of
vincent's API that distinction does not matter, because re-sending is harmless.
For one route it does.

`POST /v1/tasks` inserts a row, claims a branch and wakes the scheduler. None of
that is a compare-and-swap on state the request read, so a client that times out
after the commit and re-sends gets a second task, a second worktree, and a second
agent run against the same repository.

The issue proposes covering every mutating route. Most of them do not need it:

- **The §6 action routes** — `cancel`, `pause`, `resume`, `retry`, `repair`,
  `skip`, `approve`, `reject`, `answer`, `archive`, `follow_up` — are applied as
  a compare-and-swap on the state the request read (§6, amended 2026-08-24,
  issue #127). A transport retry re-reads a state the action is no longer valid
  from and gets `409` with `details.state`. `follow_up` is the one that could in
  principle duplicate, since it is repeatable and returns the task to
  `done`/`aborted` — but only if the whole follow-up run finished between the
  original request and the retry, which no transport retry window reaches.
- **`POST /v1/projects`** refuses an already-registered path with a `400` naming
  the existing project, and `projects.name` is `UNIQUE`.
- **The `PATCH` routes, `DELETE /v1/projects/{id}`, `POST /v1/maintenance/gc`
  and `POST /v1/doctor/fix`** are desired-state operations: re-sending one
  reaches the same end state.

So: one route, one header, one table.

## Tasks

- [x] 040.1 — `idempotency_keys` migration, keyed `(method, path, key)`, with
  the `created_at` index the prune scan needs ✓ 2026-08-28
- [x] 040.2 — `internal/store/idempotency.go`: lookup, transactional insert,
  prune; `CreateTaskWithKey` writes the key in the task's own transaction
  ✓ 2026-08-28
- [x] 040.3 — `internal/api/idempotency.go`: header validation, canonical
  digest, replay; wired into `handleTaskCreate` ✓ 2026-08-28
- [x] 040.4 — the 24-hour prune rides `taskrun.TranscriptPruner` ✓ 2026-08-28
- [x] 040.5 — tests: no-header regression, sequential replay, digest
  insensitivity to formatting, conflict, concurrent duplicates, atomicity,
  cascade, bounds, prune, doctor ✓ 2026-08-28
- [x] 040.6 — `scripts/m1-gate.sh` leg over curl ✓ 2026-08-28
- [x] 040.7 — spec §13.1/§13.2/§14/§17 amendments and
  `docs/reference/api.md` ✓ 2026-08-28

## Decisions

**1. Scope is `POST /v1/tasks` alone** (2026-08-28). Not the action routes, not
the project routes, not the `PATCH`es. The action routes were the real
candidate: their replay `409` is ambiguous between "another actor moved this
task" and "my own retry already applied this". That ambiguity is real, but
resolving it costs a key check in every action handler for a case a human on
loopback settles by looking at the board. The schema is keyed by
`(method, path, key)` so a later route joins without a migration.

**2. A key row stores a reference, not a response body** (2026-08-28). The row
records the created task's id; a replay re-reads the task and renders today's
`201` from it. *Beat:* persisting the rendered JSON and replaying it verbatim,
which reads the issue's "returns the original result" literally — but a task
response carries the workflow snapshot's steps and the route's body bound is
4 MiB (§13.1), so keys would become a storage-growth surface of exactly the kind
issue #98 was opened about. The consequence is stated rather than hidden: a
replay shows the task **as it is now**, so an already-admitted task replays as
`state: running` under a `201`. That is the honest answer — the task exists, and
this is it.

**3. Server-side only; no client changes** (2026-08-28). The issue's criterion
"CLI/TUI retries preserve the key; a new explicit user action generates a new
one" describes machinery that does not exist. `internal/apiclient` has no REST
retry: `client.go` sets a 10-second timeout and returns the error to the caller,
where the TUI or CLI shows it and a human decides. Only the SSE subscription
reconnects, and it is a `GET`. Adding an automatic retry would be a behaviour
change in its own right — today a timeout on task creation is reported to the
person, who can look at the board first — and minting a key per composed form
fights the issue's own rule that a new explicit user action is a new operation:
a human pressing enter a second time may well mean "yes, make another one".

**4. Retention is a fixed 24 hours with no config knob** (2026-08-28). Pruned by
the existing 24-hour pass (`taskrun.PruneInterval`), fixed the way §13.1's body
bounds are fixed. A key exists to cover a transport retry, which happens in
seconds; 24 hours is three orders of magnitude of headroom, and a knob here is
config surface to document and defend forever for a number nobody will tune. It
is independent of `transcript_retention_days`, including that setting's "zero
keeps everything" escape.

**5. A key conflict is `409 invalid_state` with `details.reason`, not a new
code** (2026-08-28). The issue asks for `409 idempotency_conflict`.
`internal/api/errors.go` records the opposite rule — the code is always
`CodeInvalidState`, the specific reason travels in `details` — and
`docs/reference/api.md` publishes it. The convention wins: `details.reason =
"idempotency_key_reused"`. One 409 code stays true, clients that branch on
`code` are unaffected, and no published rule needs amending.

**6. A key row cascades away with its task** (2026-08-28). `task_id` carries
`REFERENCES tasks(id) ON DELETE CASCADE`, and foreign keys are enforced on every
connection. Force-deleting a project deletes its tasks, and a key whose task has
been destroyed has nothing left to replay — so the key goes with it and a replay
inside the remaining window creates a fresh task. *Beat:* `ON DELETE SET NULL`
plus a `410`, which adds a status and an error code for a case only a deliberate
destructive act inside a 24-hour window can reach.

**7. The digest is taken over the request as received** (2026-08-28). Canonical
re-marshal of the decoded `taskCreateRequest`, hashed, **before**
`applyIssuePrefill` mutates it. Prefill reads GitHub; hashing after it would
turn an edited issue title into a spurious `409` on a request the caller sent
identically twice. Hashing the decoded struct rather than the raw bytes means
whitespace and key order do not manufacture a conflict either.

## Acceptance criteria from the issue that this does not build

Stated rather than quietly dropped.

- *"CLI/TUI retries preserve the key"* — dropped. There is no client retry to
  preserve a key across (decision 3).
- *"Keys are not logged as secrets"* — nothing to build. `logMiddleware` logs
  method, path, status and duration; no header reaches the log, and a
  client-chosen key is not a credential.
- *"Scope keys by … authenticated vincent instance, and logical caller"* — there
  is one daemon, one token (§13.1) and no caller identity to scope by. The
  `(method, path, key)` scope is the whole of what exists.
- *Coverage of the action routes and the project routes* — out of scope by
  decision 1, on the evidence above.

`internal/api/doctor.go` needed no change: `store.TableRows` enumerates the
schema, so `idempotency_keys` appears in `database.table_rows` on
`GET /v1/doctor` with no code — which is what the issue's "exposed through
doctor/storage metrics" asks for.
