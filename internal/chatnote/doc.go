// Package chatnote is the text a rejected hunk leaves at the top of a chat's
// draft (spec §5.5 "Diff and hunk review", task 169 decision 8): one block,
// headed Heading, with one line per reverted hunk, that the next message
// carries to the agent.
//
// It is one definition for two writers. The daemon puts the line into the
// draft it holds when a reject lands. A client whose composer holds an edit
// the daemon has not seen has to put the same line into its own text, or its
// next save overwrites the daemon's note with a draft that lacks it — and a
// second reject extends the block in place rather than prepending, so the
// note cannot be recovered by comparing the two drafts (review F5 of #1037).
// The reject's answer therefore names the line, and both sides call Add.
//
// Pure, and a leaf: the standard library only.
package chatnote
