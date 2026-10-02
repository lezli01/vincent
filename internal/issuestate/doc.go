// Package issuestate is the issue lifecycle state machine of spec §5.6
// (task 130 decision 3). It is pure: no persistence, no I/O, and nothing but
// the standard library. internal/store consults it inside the transaction
// that changes an issue's state, and internal/issues and the API consult it
// to answer "what may happen next" — so, as with internal/taskstate for §6
// and internal/chatstate for §5.5, there is exactly one definition.
//
// It is a third, deliberately separate vocabulary rather than a reuse of
// taskstate or chatstate. An issue has no process and no steps: nothing runs
// in it, nothing is admitted, nothing holds a slot, and it is never parked.
// Whether it is being worked on is derived from its root tasks
// (`!taskstate.Settled`), never stored as a state of its own (decision 3).
// What is left is `open | closed` and the reason an issue was closed — the
// same argument chatstate makes for keeping a chat's lifecycle off §6: folding
// it in would make every task query and every board legend decide whether it
// means issues too.
//
// Two kinds of actor reach the table. A human or an agent asks for `close`
// or `reopen`, and asking for the state an issue is already in is refused —
// the same 409 a task gives for an action outside its valid states. Sync
// (decision 9) reports what the remote now says, and the remote is the
// authority for an imported issue: reporting a state the issue is already in
// is a no-op, never an error, so a poll that re-reads an unchanged issue
// changes nothing. `remote_closed` and `remote_reopened` are sync's alone.
package issuestate
