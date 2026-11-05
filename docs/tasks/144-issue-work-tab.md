# 144 — An `Issue | Work` tab strip on the issue detail

**Status:** ✅ done (1/1) · **Opened:** 2026-11-05

The Work tab stream of [#925](https://github.com/lezli01/vincent/issues/925),
filed as [#933](https://github.com/lezli01/vincent/issues/933): the issue
detail (§15 view 13) gets an `Issue | Work` strip, and the Work tab lists
every root task and every chat of the issue in every state, archived
included, each of which `enter` opens. The auto-archive stream of #925 is
task 143; this one does not depend on it.

Conventions for this file are in [the tasks README](README.md).

## Tasks

- [x] **144.1** ([#933](https://github.com/lezli01/vincent/issues/933)) The
  strip, the Issue tab's Work line, the Work tab's Tasks and Chats groups,
  the chat listing in `loadCmd` with its degraded state, the live refresh on
  listed chats' events, the `issue` key rows, the §15 view 13 amendment and
  the guides. `internal/tui` only. ✓ 2026-11-05

## Decisions

Confirmed by the author before 144.1.

1. **The Work line is a row.** On the Issue tab the "N tasks, M chats" line
   is selectable, and `enter` on it switches to the Work tab.
2. **No digit keys.** The strip switches with `tab`/`shift+tab`, `]`/`[`
   and a click only, so `1` and `2` stay unclaimed in the `issue` key
   context.
3. **Client-only and unpaged.** The Work tab reuses #931's
   `GET /v1/chats?issue_id=` and the existing
   `GET /v1/tasks?issue_id=&archived=all`; there is no API, store or daemon
   change, and the lists stay unpaged, as the issue accepts.
4. **A separate record.** The work is task 144, apart from task 143's
   auto-archive stream of #925.

Screenshots of the new tab are out of scope here (#934).
