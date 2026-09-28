// Package reasons is the plain-language catalogue of task and step reasons
// (task 127, issue #593): what a `block_reason`, a step run's
// `failure_reason` or `skip_reason`, or a task's `queued_reason` means to a
// human, which §6 actions answer it, and where the documentation explains it.
//
// The reasons themselves are §18's snake_case vocabulary, shared between
// internal/taskrun and internal/worktree so a reason means the same thing
// wherever it originated (T1.5/T1.6 decision). They land on the fields §5.3
// (tasks) and §5.4 (step runs) define, and the actions this package suggests
// are §6's human actions, spelled as plain strings.
//
// It is a leaf: it imports nothing internal and spells every reason out
// rather than importing the constants, the way internal/tui already does,
// because the wire string is the identity and a client that consumes this
// package must not come to depend on the engine. What keeps the two
// spellings from drifting is a test, not an import: an external test package
// parses internal/taskrun and internal/worktree for every Reason* constant
// and requires each to be explained here or excluded on purpose, and a
// second test holds docs/reference/task-lifecycle.md's "Failure reasons"
// tables to exactly this package's key set (task 127 decisions 1 and 2).
//
// It covers task and step reasons only. GitHub's reasons have their own
// renderer (github.Message), and chat reasons stay with internal/chatrun.
// An unknown reason explains as itself, so an older client talking to a
// newer daemon falls back to the raw snake_case it showed before.
package reasons
