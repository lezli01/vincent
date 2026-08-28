# 048 — A command line for every §6 human action

**Status:** ✅ done (6/6)
**Issue:** [#89](https://github.com/lezli01/vincent/issues/89)
**Spec:** amends §12.1

## Problem

The API exposes every §6 human action. `vincent task` exposed `add`, `ls`,
`show`, `cancel` and `follow-up` — so the entire *recovery* half of the product
was reachable from the TUI, from `curl`, and from nothing else.

That makes the daemon's headline property false in one direction. "The daemon
owns the work and clients are disposable" (§2, `CLAUDE.md`'s ownership
invariants) is true of creating work and untrue of unblocking it: for the
actions a **blocked** task needs, exactly one client could perform them, and it
was the heavyweight interactive one.

The failure mode that makes this more than an inconvenience is an agent auth
outage. `agent_unauthenticated` fails an attempt under the normal retry budget
and blocks the task once that budget is spent (§7.2, task 003); waiting does not
fix it, because a human has to repair the login and then move each task. A board
full of `agent_unauthenticated` blocks was a board that could not be un-blocked
from a script. (`usage_limit` is deliberately *not* this case: it is a
`queued_reason`, never a `block_reason`, and the scheduler re-admits without a
human.)

Secondary gap of the same shape: `DELETE /v1/projects/{id}` has existed with
`?force` since T1.5, and `vincent project` registered only `add` and `ls`. A
project could be created from the CLI and deleted only from the TUI or curl.

## What shipped

Nine subcommands under `vincent task` — `pause`, `resume`, `skip`, `approve`,
`reject`, `retry`, `repair`, `archive`, `answer` — and `vincent project rm`.
`internal/apiclient` already had every typed method, so this is cobra wiring,
flag design, output rendering and documentation: no API, store, scheduler,
`taskstate` or engine change.

All of them take one id, carry `--json`, print the daemon's *post-action* view
of the task rather than a predicted transition, and follow `task cancel`'s error
shape — a 409 from the FSM is exit 1 with the daemon's own wording.

`task show` grew two things the numbering depends on: the pending §7.4 request,
rendered as numbered questions with their options and a multi-select marker (or
the tool and summary of a `permission` request), and an `actions` row carrying
`available_actions`, so a script reads what is legal instead of probing for
409s. `--json` needed no change — `TaskDetail` has carried `pending_input` and
`available_actions` all along; a test pins that rather than code adding it.

## Decisions

1. **2026-08-28 — Nine actions, not eight: `repair` ships too.** The issue
   omits it. §12.1 and task 025 decision 12 name repair in the same breath as
   retry, skip and approve, and `TestDocsClaimsTUIActionsHaveSubcommands` walks
   **every** `taskstate.HumanActionsFrom` result — so restoring the parity
   sentence while `task repair` was absent would turn that drift check red.
   Repair takes `--prompt` (required) with `--prompt-file`, plus the §8.6
   triple, and prints the catalog warnings its selection raised to stderr,
   exactly as `follow-up` does.

2. **2026-08-28 — One id per invocation** (`cobra.ExactArgs(1)`), matching
   `cancel` and `follow-up`. The motivating case — a board of
   `agent_unauthenticated` blocks — is a shell loop over
   `task ls --state blocked --json | jq -r '.[].id'`, the idiom
   `scripting.md` already teaches for `follow-up`. Variadic ids were rejected:
   they force a partial-failure exit code that the documented 0/1/2 contract has
   no room for, and a `--json` array shape no other single-task command emits.

3. **2026-08-28 — `project rm` is flags-only and never prompts.** `--force`
   forwards `?force` and that is the whole confirmation story. The daemon
   already guards the unsafe cases with two distinct 409s — "project has N
   non-archived task(s)" and one naming a `running` task — and both messages
   reach the user intact, because they ask for opposite things. An interactive
   confirm was rejected: it would make this the first interactive subcommand in
   a tree whose stated purpose is scripting.

4. **2026-08-28 — `--answer` is indexed, and the wire format is not.**
   `InputResponse.Answers` is keyed by question *text* (§13.2) and stays that
   way. `--answer <n>=<value>` numbers off what `task show` prints, because a
   question is a sentence and nobody should retype one to answer it. A repeated
   index is a multi-select's several values; values split on the **first** `=`,
   the rule `parseFieldFlags` already documents, so a URL or a regex needs no
   escaping. The command GETs the task, maps the indexes, and runs
   `InputRequest.Validate` locally before posting — a fast fail, never the
   authority, exactly as `Validate`'s own doc comment says.

5. **2026-08-28 — `--body` posts the payload as it stands**, through a new
   `apiclient.AnswerRaw` taking a `json.RawMessage`. Decoding a script's own
   §13.2 document through `InputResponse` and re-encoding it would put this
   client between a caller and a document the daemon is the authority on, for no
   gain. The CLI checks only that it is JSON at all, so prose never leaves the
   process as an answer payload.

6. **2026-08-28 — `archive` must not flatten the dirty-worktree 409.**
   `apiMessage` returns only `Message`; the reason lives in
   `Error.Details["reason"] == "worktree_dirty"` — the discriminator
   `internal/tui/bulkaction.go` already reads — so the command branches on it
   and appends the `--force` hint. Exit 1 with no way out is a riddle.

7. **2026-08-28 — Task 025 decision 12 and the §12.1 note it produced are
   superseded in full.** They made retry, repair, skip and approve TUI-and-API
   only, and said so in the spec. This work does not relitigate that by
   assertion: task 027 decision 11 already named its own successor, closing with
   "filling the gap for every human action is a separate piece of work and out
   of scope here." This is that piece of work, and the §12.1 note is amended in
   place with a dated paragraph rather than deleted — `spec §12.1` citations
   stay stable, and the reasoning that lost is what makes the record worth
   keeping.

8. **2026-08-28 — The drift test compares the CLI's spelling of an action.**
   `taskstate` spells actions in snake_case (`follow_up`) and the command tree
   in kebab-case (`follow-up`), so `TestDocsClaimsTUIActionsHaveSubcommands`
   normalizes before comparing. Renaming the subcommand to match the API was
   rejected: `follow-up` is the published name, and the hyphen is the CLI's
   convention, not a drift.

9. **2026-08-28 — `answer`'s coverage is a stub-daemon test, not the live
   one.** Everything else is asserted end to end against a real daemon through
   the real binary — including the headline, a **blocked** task taken back to
   `running`. Reaching `awaiting_input` for real needs an agent that asks a
   question mid-run, which would make the assertion depend on the fake agent's
   scenario rather than on the command. So the request shapes (index mapping,
   multi-select, first-`=` splitting, the refusals, `--body` pass-through) are
   pinned against a hand-written daemon that records the posted body, which is
   the thing under test. The live suite still covers `answer`'s refusal path on
   a task that is not waiting for input.

## Work

- [x] **048.1 — The five bare actions** (`pause`, `resume`, `skip`, `approve`,
  `reject`) through one shared constructor, plus `taskID` and `printTaskAction`.
  ✓ 2026-08-28
- [x] **048.2 — The flag-carrying actions**: `retry` (`--branch`, the
  `--prompt`/`--run` edit+retry pair and their `-file` twins), `repair`,
  `archive` (`--force` and the `worktree_dirty` hint), `answer`
  (`--answer`/`--allow`/`--deny`/`--body`), and `apiclient.AnswerRaw`.
  ✓ 2026-08-28
- [x] **048.3 — `task show` renders the pending request and the available
  actions.** ✓ 2026-08-28
- [x] **048.4 — `vincent project rm`.** ✓ 2026-08-28
- [x] **048.5 — Tests**: `internal/cli/actions_e2e_test.go` against the real
  binary and a real daemon (blocked → running, the gate trio, the
  `branch_exists` recovery, the dirty-archive 409 and its `--force`, both
  `project rm` 409s, exit 2 with no daemon and no daemon started), and
  `internal/cli/taskanswer_test.go` for `answer` and the `task show` rendering.
  `TestDocsClaimsTUIActionsHaveSubcommands` stops skipping. ✓ 2026-08-28
- [x] **048.6 — Documentation**: §12.1 amended, `docs/reference/cli.md`,
  `docs/guides/scripting.md` (the parity claim restored), `README.md`,
  `docs/features.md`, `docs/getting-started/quickstart.md`, `CHANGELOG.md`, and
  the superseded-by pointers on tasks 025 and 027. ✓ 2026-08-28

## What is still API-only

Out of scope here, and named so the next reader does not mistake this for full
parity: the `PATCH` endpoints, `resolve`, `agents`, `task diff` and
`task transcript`. None of them strands a blocked task, which is what made this
issue urgent and makes those their own work.
