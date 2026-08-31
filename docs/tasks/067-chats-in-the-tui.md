# 067 — A chats view in the TUI: a chats board beside the task board

**Status:** ✅ done (1/1)
**Issue:** #269
**Closes:** [063.2](063-free-chat.md) and [063.3](063-free-chat.md)

Task 063 landed the chat entity end to end — the tables, the runner, the route
family, `vincent chat`, recovery and gc — and deliberately stopped short of two
things: the TUI view, and three gaps its own documentation audit found. This
task closes both, in one piece of work, because the 063.3 gaps are what make a
chat drivable without curl and the view is the client that proves it.

## Decisions

**1. A chat turn gets §7.2/§7.4's clocks verbatim.** `agent_timeout` (60m by
default) bounds a running turn; `input_timeout` (24h) bounds `awaiting_input`.
Expiry fails the turn — `ReasonTimeout` and `ReasonInputTimeout`, the existing
snake_case constants, not new strings — kills the process tree, returns the chat
to `idle` and releases its `max_parallel_chats` slot.

No new `defaults.*` key. A chat is bounded by the same numbers a step is, and
inventing `chat_agent_timeout` would make §12.3 carry two vocabularies for one
clock. There is no per-turn override either, because §8.2's `timeout` and
`input_timeout` are *workflow step fields* and a chat has no workflow.

This is the option that closes the hole §11 named in its own words —
live-but-uncounted agent CLIs, here a slot held forever by a chat whose human
walked away — so it is that amendment extended once more, not excepted.

*Alternative rejected:* a chat-specific pair of keys. It would have documented
two names for one number and left an operator asking which one applied.

**2. The per-turn transcript is served over the API.** Adding only the SSE route
would leave a reopened TUI showing finished turns as `ChatTurn.ResultText` and
nothing else, and would leave §13.3's catch-up seam approximate for chats while
it is exact for tasks. So `GET /v1/chats/{id}/turns/{seq}/transcript` lands
beside `GET /v1/chats/{id}/events`, mirroring the step route: same
`?offset=`/`?tail=` mutual exclusion, same `X-Next-Offset`.

The seam needs no `run_id` analogue — a chat turn *is* its run, and the chunks
`internal/chatrun` already publishes carry `turn_id` and `offset`, which is the
pair. Both new routes join the §13.4 exclusion list, as *exclusions* rather than
under the SSE carve-out: the rule that keeps chats off the tool surface is "a
human drives a chat", and one rule for the whole family is what stops a later
chat route being classified by which sentence it happens to match.

**3. One PR.** 063.2 and 063.3 land together, for the reason above.

**4. Attention is the chats board's own.** An `awaiting_input` chat is pinned
and badged **on the chats board only**. `!` and the home board's
needs-attention header stay task-only, keeping decision row 29's "never appears
on the task board" literal in the client as well as in the API. Task 063
decision 7 is untouched: it governs the daemon's outward `notify:` hook, not a
client's bell, and nothing here changes `internal/config`'s imports.

**5. The gate is `m14`, not `m13`.** Task 063 planned `scripts/m13-gate.sh`;
task 065 took that filename for the workflow editor and wired it into
`ci.yml`. 063.2's row is corrected rather than left naming a file that means
something else.

**6. The chats board groups by project and by nothing else.**
`tui.board.group_by`'s workflow scopes are meaningless for a chat, so the `g`
cycle is not offered. Folds persist in `{data_dir}/tui.json` under their own
`chat_folds` key rather than sharing `board_folds`: the two boards group by the
same project names, and one list would make folding a project on one board fold
it on the other — one fold set pretending to be two.

**7. The §7.4 answer popup is reused, not forked.** `answerForm` grew
`updateWith`, which takes a submitter; the task path passes a closure that posts
to the task and the chat path one that posts to the chat. Structured options,
multi-select and the permission allow/deny distinction come along because the
request is the same request — `internal/chatrun` stores what the adapter
emitted, unaltered.

**8. An open new-chat draft captures input on every row.** *(Added 2026-08-31,
issue #279.)* PR L's rule is that a form captures input "only while a text row
is in edit mode", and `newTask.capturesInput` follows it verbatim. This form is
the one recorded exception: `newChatForm.capturesInput` returns true for all six
focus positions, so no keystroke escapes an open draft to the global handler.

The exception is scoped to *this form* and the rule is not changed globally —
doing that would make `?`, `:`, `!` and `M` unreachable from every form in the
TUI, which is a product change this issue does not license. Declaring the
colliding keys in `ctxNewChat` does not work either: `panelOwnsKey` guards `n`
alone, while `root.updateKey` quits on `q` unconditionally. What justifies the
exception here is the shape of the form — four of six rows are text fields, and
a live draft sits behind the two that are not, so `q` on the project row quit
the TUI with the draft. `esc` is still the way out, and it is already this
form's layer of §15's esc stack.

**9. The agent picker filters on a published `supports_resume`, and `n`
refuses an empty registry.** *(Added 2026-08-31, issue #279.)* `applyFields`
claimed to default to "the first adapter that can resume", but the wire type
could not express it: `apiclient.Agent` carried `supports_input` and no resume
field, while `agent.CanResume` is what `POST /v1/chats` has always gated on.
`GET /v1/agents` now publishes it, following the `input_verdict` precedent
exactly — an absent field from an older daemon means no judgement, and nothing
is filtered out on the strength of a field that was never sent. The daemon's
`agent_cannot_resume` refusal is untouched and still rendered by
`applyFailure`; it simply stops being reachable from this form.

For the same reason the form is not opened at all when there is no project to
create in: `ctrl+s` could only answer `pick a project`, and no field on the form
accepts one. Only a positive answer refuses — a board that has not listed the
projects, or whose listing failed, opens the form as before.

**10. The new-chat form uses the picker every other create form uses.**
*(Added 2026-08-31, issue #281.)* Project, agent, model and effort became
`picker` rows; title and base stayed text. The component was already there when
this form was written, and this was the only create surface in the TUI not
using it — filtering, the bounded window, the `cli`/`curated` provenance note
and `allowFree` were all being re-decided by hand here, badly. Four decisions
qualify the move:

- **`←`/`→` stay, as a fast in-place step.** `enter` opens the list; `←`/`→`
  continue to step the project and agent rows without opening it. This is the
  enum-row precedent from the new-task fields editor verbatim
  (`internal/tui/newtaskpicker.go`): left/right step the members the way a
  boolean cycles, enter opens the list, which is the only workable control for
  a long one. The issue proposed retiring the cyclers; the cycler was never the
  defect — the *absence of a list* was — and two adapters are quicker to step
  than to open. They are not added to the model and effort rows, which are
  catalogs of a hundred and more (§9.7) where "next" answers nothing.

- **`base` shows the project's real default branch as a placeholder, and
  submits empty.** The row reads `main (the project's default)` — the branch
  name, not the phrase — re-derived whenever the project row changes, while
  `CreateChatRequest.BaseBranch` stays empty so `handleChatCreate` resolves
  `base = project.DefaultBranch` as it does today. Seeding the input's *value*
  was rejected: it pins a branch at draft time and needs a rule for re-seeding
  after a project change over text the human may have typed. No
  `GET /v1/projects/{id}/branches`: the new-task form has the same free-text
  base row, and the two should be decided together rather than one of them
  smuggled in here.

- **Decision 9 is unchanged.** The agent list keeps hiding adapters that do not
  publish `supports_resume`; `resumableAgents` is untouched and the daemon's
  `agent_cannot_resume` refusal stays the authority. A picker *can* now show a
  row disabled with its reason — the new-task precedent from tasks 010 and 013
  — and that was considered and declined: an adapter that cannot hold a
  conversation is out of reach for every chat, not for this one draft, which is
  the case that precedent covers.

- **TUI only; the daemon is not touched.** The issue's rationale is wrong on
  one fact and it does not change the outcome. `POST /v1/chats` never validates
  `model` or `effort` — it stores what it is sent, with no catalog check and no
  warning, unlike task create, repair and follow-up, which run
  `checkTaskCatalog`. A typo therefore does not come back as a rejection; it
  comes back as a failed first turn when the CLI refuses the model, which is a
  worse failure and a stronger argument for the picker. Giving chat creation
  §8.2's warnings is a real gap, with its own DTO, apiclient, `docs/reference/api.md`
  and spec amendments — and is not this issue.

Decision 8 is unchanged in substance and its arithmetic is not: the form now
has *two* live text rows, not four, and an open list adds a filter row and a
free-text row on top of them, so `capturesInput` stays unconditionally true for
a stronger reason than before.

## Sub-tasks

| ID | What | Status |
|---|---|---|
| 067.1 | The two API routes, `internal/apiclient`'s `StreamChat` and `ChatTurnTranscript`, the two clocks and the transcript cap in `internal/chatrun`, archived-chat pruning, `vincent chat answer`/`cancel`/`--json`, the chats board and the chat workspace in the TUI, `scripts/m14-gate.sh`, and the spec and docs amendments | ✅ done |

## What the tests prove

- **The lists are the daemon's catalogs** *(issue #281)* — `internal/tui`'s
  `newchat_test.go` at form level and `newchatlive_test.go` against the real
  handlers: the project list offers every registered project with its path, the
  agent list only the resumers, the model and effort lists the selected
  adapter's served catalog with its `cli`/`curated` tags, an `(agent default)`
  row naming that adapter's default and a free-text row; changing the agent
  re-scopes both and clears what was chosen under the previous one; `←`/`→`
  still step the two short rows without opening a list; the base row's
  placeholder follows the project and an untouched row submits empty, so the
  daemon resolves the project's default branch; `enter` never creates and
  `ctrl+s` creates from any row; `esc` with a list open closes the list and
  leaves the draft.
- **The stream is a stream** — `internal/apiclient`'s `*live_test.go` against the
  real handlers: a chat's durable `chat.*` events arrive filtered to that chat,
  another chat's and a task's do not leak onto it, `Last-Event-ID` resumes the
  durable events, and live chunks carry `chat_id`/`turn_id`/`offset`.
- **The seam is exact** — a transcript fetch followed by a resume from its
  `X-Next-Offset` reproduces the appended records with none duplicated and none
  dropped; `?offset=` with `?tail=` is a 400 and an unknown turn is a 404.
- **The clocks bite** — `internal/chatrun`: a turn past `agent_timeout` fails
  `timeout`, a chat past `input_timeout` in `awaiting_input` fails
  `input_timeout`, both return the chat to `idle` and free the slot (asserted
  through `CountChatsHoldingProcess`), and an answer inside the window stops the
  input clock rather than carrying it into the rest of the turn.
- **The cap applies** — a turn's transcript stops at `transcript_max_bytes` and
  the turn fails `transcript_limit`, which only the task engine used to do.
- **Retention** — `internal/taskrun`: an archived chat's transcript directory is
  reclaimed on the same pass as an archived task's, and a chat that was never
  archived keeps its transcripts however old the cutoff.
- **Refusals render as refusals** — the workspace's `409 chat_cap_reached` path
  shows a refusal and creates no turn row; the create form renders
  `400 agent_cannot_resume` as the typed reason.
- **The answer popup is the same popup** — an `awaiting_input` chat opens
  `answerForm` from its own pending request, keeps the multi-select shape, and
  closes when the daemon stops saying `awaiting_input`.
- **Separation holds both ways** — a `task.*` event does not reload the chats
  board, and the chats board's attention badge is its own.
- **MCP** — the tool surface equals `Routes()` minus the five admin routes, the
  workflow writes and the now nine-route chat family.
- **`bindings.go`** — every new context has its probe and its palette row; the
  existing `bindings_test.go` and `palette_test.go` invariants were extended,
  never exempted.

## Acceptance

`scripts/m14-gate.sh` drives a chat end to end over curl against
`cmd/fakeagent`: create, send, resume continuity, an `awaiting_input` answer,
the `max_parallel_chats` `409`, cancel, archive, the per-chat event stream and
the transcript route's offset seam. It has no workflow `run:` bodies to spell in
the sh∩pwsh intersection — a chat has no workflow — but its own bash obeys the
two standing rules: `| tr -d '\r'` on any multi-line `jq` capture, and never
`| grep -q`.

**Not in this task:** the screenshots. `scripts/screenshots.sh` is the only
source of `docs/assets/tui-*.png` and it is not run in CI; capturing the two new
panels is a seeded VHS run on a macOS or Linux workstation, and this task
deliberately does not ship a drawing in the meantime. `docs/guides/tui.md`
describes both panels in prose and key tables until that run happens.
