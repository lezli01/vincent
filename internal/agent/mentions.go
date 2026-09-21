package agent

// This file is skills.go's twin, and deliberately so (spec §9.1, task 126
// decision 1): it is the seam through which the daemon asks an adapter how a
// message names a file, so that the quoting rule has exactly one definition
// and it lives in the adapter rather than in each client.
//
// It is an optional interface rather than an Adapter method, for the reason
// SkillInvoker and Resumer are: a capability an adapter lacks is stated in
// §9.x and ignored at run time, never emulated, and an optional interface
// says "cannot" by being unsatisfied.
//
// **Nothing is validated and nothing is rewritten** (task 124 decision 9,
// carried by task 126 decision 1). The string FileMention returns is the
// adapter's own word: no later stage checks that the path exists, cleans it,
// or translates it between adapters. An empty path yields a bare sigil, which
// is what pass-through means here — a guard returning "" would be the daemon
// judging a path.
//
// **The input contract is workspace-relative, forward-slash** (task 126
// decisions 2 and 21): git's own bytes, exactly as worktree.Manager.ListFiles
// returned them, in git's order, on every platform. Nothing in this package
// calls filepath.FromSlash, and nothing here knows about a separator. §5.5
// records why relative is safe — claude resolves a mention against the
// process cwd, and Command.Dir is always a repository toplevel — and records
// that Windows is unobserved (#557), which is why no separator handling is
// written ahead of the observation.

// FileMentioner is the optional capability an adapter implements when a
// message can name a workspace file (§9.1, task 126). vincent passes the
// message through verbatim; this is what a client uses to write the mention
// into it.
//
// Implementing it is a claim that the CLI recognizes the syntax, not a claim
// that the CLI expands it. Whether the file is put in front of the model by
// the CLI itself, or merely hinted at for the model to read with a tool, is
// FileMentionSyntax.Expands — and §5.5 records that with tools allowed
// `codex exec` and `cursor-agent -p` each read a mentioned path in one tool
// call, so a mention is worth sending on all three (task 126 decision 6).
type FileMentioner interface {
	// FileMentionSyntax is how this adapter's CLI recognizes a mention. It is
	// static: the same for every build and every directory.
	FileMentionSyntax() FileMentionSyntax
	// FileMention is the exact text a client inserts to mention relPath,
	// which is workspace-relative and forward-slash-separated. It is built
	// from those bytes; nothing is validated, cleaned or translated.
	FileMention(relPath string) string
}

// FileMentionSyntax is how a message names a file (§9.1, task 126).
type FileMentionSyntax struct {
	// Sigil is the character a mention starts with: "@" on all three shipped
	// adapters.
	Sigil string
	// Position is where in a message the CLI recognizes a mention.
	Position MentionPosition
	// Expands is whether the CLI itself puts the file in front of the model,
	// before the model sees the message and without a tool call. It is the
	// honest capability statement — the difference is guaranteed context
	// against a hint (§5.5) — and it is a static per-adapter fact, with no
	// "unknown" leg, which is why it is a bool (task 126 decision 23).
	Expands bool
}

// MentionPosition is where in a message a mention is recognized (§9.1, task
// 126 decision 22). It is its own type rather than a reuse of SkillPosition:
// §5.5 records that `@` and `/` do not share a positional rule on claude —
// the mention is anywhere, the skill invocation is leading — so a shared type
// would invite a reader to assume the two move together.
type MentionPosition string

// Mention positions (task 126 decision 22).
const (
	// MentionLeading is a mention the CLI recognizes only at the start of the
	// message. No shipped adapter answers it today; it exists because the
	// position is the adapter's fact to state, not an assumption.
	MentionLeading MentionPosition = "leading"
	// MentionAnywhere is a mention the CLI recognizes as a token anywhere in
	// the message — all three shipped adapters, observed in §5.5. It is what
	// task 126 decision 8 keys the picker's open rule on.
	MentionAnywhere MentionPosition = "anywhere"
)

// CanMentionFiles reports whether a implements FileMentioner (§9.1, task
// 126). Like CanInvokeSkills, it answers interface satisfaction and nothing
// more, and its false leg is proven against agenttest.StubNoMentions, never
// against a shipped adapter: all three implement it today, and a refusal
// pinned to a real CLI inverts itself the day that CLI changes — the lesson
// recorded on Resumer.
func CanMentionFiles(a Adapter) bool {
	_, ok := a.(FileMentioner)
	return ok
}
