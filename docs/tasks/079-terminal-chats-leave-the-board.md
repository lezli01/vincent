# 079 — Terminal chats leave the chats board

**Status:** ✅ done (1/1)
**Issue:** #298
**Amends:** §13.2's `GET /v1/chats` and `POST /v1/chats/{id}/archive` rows,
§5.5's `handed_off` row, §15's chats board and its task 074 amendment
**Follow-up to:** [067](067-chats-in-the-tui.md), which built the board, and
[074](074-chat-handoff.md), which gave it a second terminal state

Archiving a chat took it off nothing. The row stayed on the board, its last
column kept counting up like a live conversation, and pressing `a` on it again
was refused with `a chat with a live turn cannot be archived` — a sentence
about a process neither terminal state can hold.

Three defects with one symptom, two of them the code doing exactly what the
spec said, which is why this is a spec amendment and code rather than code
alone.

## Decisions

### 1. The wire parameter is `?archived=false|true|all`

Verbatim parity with §13.2's task filter — same spelling, same three values,
same default of exclusion — and it hides **both** terminal states, `archived`
and `handed_off` alike, even though its name says only the first. §13.2's row
therefore says so explicitly, because the name alone does not.

`?terminal=` was considered and rejected. It is the more literal name for what
the parameter does, but one vocabulary across both entities is worth more than
a second, more accurate one: a client that knows how to hide archived tasks
now knows how to hide terminal chats without being told.

### 2. A terminal chat's cell renders an absolute stamp, not a duration

The last column means "how long ago this row was last written", which is the
right reading for an idle chat and a misleading one for a chat that can never
be written again. For `archived` and `handed_off` it renders *when* the chat
ended instead. Nothing ticks, and the cell keeps its information.

**No schema change, and task 074 decision 6 is not reopened.** The issue
suggested the row might need an `archived_at` or `finished_at` column. It does
not: a terminal transition is the last write a chat row takes, so `updated_at`
already *is* the moment the chat ended, which is what task 074 decision 6 rests
on. The stamp is date and time, never a relative word like "today" — a cell
that depended on `now` at all would still be a clock, only a slower one.

### 3. The default exclusion lives in the store

`ChatFilter` gains an archived filter — `store.ArchivedFilter`, the task
filter's own type — whose zero value excludes both terminal states, exactly as
`TaskFilter.Archived` does. `ChatFilter`'s doc comment recorded the opposite
decision ("the zero value lists every chat, archived ones included — the caller
decides") and is rewritten to say why it changed rather than quietly deleted.
The reasoning it carried was that a listing which hid archived rows would make
`vincent chat list --all` a lie; the lie it produced instead was the board,
where the caller that was supposed to decide had no way to.

### 4. The refusal names the state, on both sides

Archiving is legal from `idle` alone (§5.5), so every other state reaches the
refusal, and one hardcoded sentence could not be true of all of them. The API
now names the state that actually blocked it, so the message and
`details.state` agree; the three sibling handlers already did this, which is
what makes it a plain bug rather than a design question.

The TUI declines `a` on a terminal row with the same distinction rather than
opening the `archive %q and remove its worktree? (y/n)` prompt. Both halves,
not just the server's: the prompt asks a human to confirm removing a worktree
that is already gone — or, after a handoff, one the task owns now — and a
question that cannot be answered usefully should not be asked.

Two existing tests, `TestChatActionsIn409WhenTheFSMSaysNo` and
`TestHandoffIsTerminal`, exercise this exact path and assert only the status
code. That is how a wrong sentence survived both, and the regression test reads
the sentence.

### 5. The terminal band stays

`sortChats` still sorts `archived` and `handed_off` into the board's last band
(task 067, task 074). Hiding them by default does not make the band dead code:
it is what orders the rows the `s` toggle brings back.

### 6. `s` is the toggle, and hiding with no way back was rejected

`s` cycles the listing — live, then archived and handed-off, then both — which
is view 7's key for the same idea (task 064 decision 9). An archived chat's
transcript is still worth reading, so a filter with no route back would trade
one lost affordance for another. The header names the listing whenever it is
not the default, so an empty board is never mistaken for "no chats".
