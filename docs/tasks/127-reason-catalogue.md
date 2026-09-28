# 127 — A plain-language catalogue of task and step reasons

Issue [#593](https://github.com/lezli01/vincent/issues/593). Spec §5.3 and
§5.4 (the fields a reason lands on), §6 (the actions it suggests), §18 (the
reason vocabulary). Related: the T1.5/T1.6 shared-vocabulary decision, task
025 decision 8 (every block reason offers repair), task 119 (chat on a
stopped task). The TUI renders the catalogue in #596 and #600; this task
only builds it.

## What this is

A leaf package, `internal/reasons`, that turns a reason string —
`block_reason`, a step run's `failure_reason` or `skip_reason`, a task's
`queued_reason` — into something a human can read:

```go
type Explanation struct {
	Title     string   // "check failed"
	Meaning   string   // one sentence
	Actions   []string // §6 action ids, most useful first
	DocAnchor string   // "guides/troubleshooting.md#worktree_dirty"
}

func Explain(reason string) Explanation
func Known() []string
```

An unknown reason returns `Explanation{Title: reason}` and nothing else, so an
older client talking to a newer daemon falls back to the raw snake_case it
shows today — the shape `github.Message` already has. It is not served over
the API (no `GET /v1/reasons`); the raw fallback is enough for version skew.
The CLI, the MCP server, GitHub reasons and chat reasons are unchanged.

## Decisions

Settled with the author when the issue was planned, 2026-09-28.

1. **Task and step reasons only.** The catalogue covers every `Reason*`
   constant in `internal/taskrun` and `internal/worktree` that can end up on
   a task or a step run, plus `store.SkipReasonCondition` (`condition`). Six
   worktree constants never do and are left out: `push_rejected`,
   `push_no_credential` and `push_failed` (API errors from opening a pull
   request, task 069), `dirty_unknown` (orphan reclaim, task 005), and
   `workspace_path_missing` and `repo_operation_in_progress` (the chat API).
   The completeness test names these six in an explicit set, so every
   constant is either explained or excluded on purpose and a new one cannot
   slip through by default. `retry_backoff` and `usage_limit` stay: they
   appear as `queued_reason` or `block_reason`.

   *Alternative rejected:* also cataloguing GitHub and chat reasons. GitHub
   already has `github.Message`, and chat reasons belong to `internal/chatrun`.

2. **The lifecycle reference is tables, and a test holds it to the
   catalogue.** `docs/reference/task-lifecycle.md` § "Failure reasons" turns
   its worktree-layer prose sentence into table rows and adds `condition`.
   The test parses the first column of every table in that section and
   requires it to be exactly the catalogue's key set. The doc's wording stays
   its own, and may say more than the catalogue's one sentence; only the set
   is checked.

3. **A new leaf, `internal/reasons`.** It imports nothing internal and spells
   out its own reason strings, as `internal/tui/detail.go` already does, so
   the wire string stays the identity. `Actions` are plain strings. The
   completeness test lives in an external test package, whose imports do not
   make the production package non-leaf, and it enumerates the constants with
   `go/parser` rather than a hand-written list, which would not fail when a
   new constant ships.

## Tasks

- [x] 127.1 `internal/reasons` with `Explain` and `Known`; the completeness,
  exclusion, human-action, ordering, doc-anchor, doc-drift and leaf tests;
  the lifecycle reference's tables; spec §18; `CLAUDE.md`'s leaf list and
  package map.
