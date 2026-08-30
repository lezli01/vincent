// Package chatrun runs chat turns (spec §5.5, task 063). It is to a chat what
// internal/taskrun is to a task, and it holds the same actor invariant: one
// goroutine per running turn, and that goroutine is the sole writer of the
// chat's §5.5 state and of its `chat_turns` row.
//
// Three things make it a separate package rather than a mode of the engine:
//
//   - Admission. A turn never goes through internal/scheduler and is never
//     `queued`. It starts when the human sends it, or it is refused with 409
//     because `max_parallel_chats` is reached (§11, decision 1). The
//     scheduler's "the only place queued → running happens" invariant is
//     untouched because a chat turn is never in that state.
//   - Continuity. A turn resumes the agent CLI's own session (§7.3, amended
//     for chats), which is the one place in vincent that does not start a
//     fresh one. That is a per-adapter capability, and an adapter without it
//     is refused at chat creation rather than emulated (§9.3, §9.7).
//   - Recovery. A turn the daemon died under is finalized `interrupted` and
//     is **not** re-run (§12.4, decision 5): re-running would re-send the
//     human's message, and the session it was resuming died with the process.
//
// What it does share is extracted, not copied: the transcript writer is
// internal/transcript, and rendering goes through the same normalized event
// stream every adapter already emits.
package chatrun
