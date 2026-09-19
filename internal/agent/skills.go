package agent

import (
	"context"
	"errors"
)

// This file is the seam through which the daemon asks an adapter two
// questions about its CLI's skills (spec §9.1, task 124): which skills it
// would load in a given directory, and how a message names one.
//
// Both are optional interfaces rather than Adapter methods, for the reason
// QuotaReporter is (quota.go): a capability an adapter lacks is stated in
// §9.x and ignored at run time, never emulated, and an optional interface
// says "cannot" by being unsatisfied. A method on Adapter would force every
// adapter that cannot answer to grow a stub saying so.
//
// **Nothing is synthesized** (task 124 decisions 8 and 17). Every value a
// lister returns is the CLI's own word: no normalized scope, no kind, nothing
// parsed out of a description, and no de-duplication of names the CLI
// repeated. A field the CLI did not report stays "".

// ErrSkillsUnsupported is returned by SkillLister.ListSkills when this
// adapter, or the installed build of its CLI, can never list the skills it
// would load (§9.1, task 124 decision 16). It is a positive no, the
// InputUnsupported of listing, and it is the only error a caller may read
// that way.
//
// A probe that timed out, crashed or answered something unparseable is an
// ordinary error, never this one: it is "nobody can say", the InputUnknown of
// listing, and a caller tells the two apart with errors.Is. The chat skills
// route (#505) maps this sentinel to `list_verdict: unsupported` and every
// other error to `unknown`.
var ErrSkillsUnsupported = errors.New("this agent CLI cannot list the skills it would load")

// SkillLister is the optional capability an adapter implements when its CLI
// can report, without starting a conversation, the skills a person could
// invoke by name in a run started in a given place (§9.1, task 124).
//
// Implementing it is a claim about the adapter, not about the installed
// build: an adapter whose older builds cannot list still implements it, and
// answers ErrSkillsUnsupported from ListSkills on such a build (task 124
// decision 13).
type SkillLister interface {
	// ListSkills reports the skills the CLI would load for a run in
	// q.WorkDir, in the CLI's own order and words. It returns
	// ErrSkillsUnsupported, wrapped or not, for a build that can never
	// answer, and an ordinary error for a probe that failed.
	ListSkills(ctx context.Context, q SkillQuery) (SkillList, error)
}

// SkillQuery is where a skill listing is asked about (§9.1, task 124). A list
// is a function of the directory and of the machine the CLI runs on, not of
// any session, so those are all it carries.
type SkillQuery struct {
	// WorkDir is the directory the next run would start in: a chat's own
	// worktree, or its linked task's.
	WorkDir string
	// Launcher is where the CLI would run. nil means the host, the same
	// convention RunSpec.Launcher follows.
	Launcher Launcher
	// Env is the CLI's environment. nil means the daemon's, the same
	// convention RunSpec.Env follows.
	Env []string
}

// SkillList is one listing as the CLI reported it (§9.1, task 124).
type SkillList struct {
	// Skills is in the CLI's order. Names may repeat: both claude and codex
	// document same-name entries that are not merged, and vincent does not
	// merge them either.
	Skills []Skill
	// Problems are entries the CLI found but could not load — codex's
	// `errors[]`. nil when the CLI reported none or has no such report.
	Problems []SkillProblem
}

// Skill is one entry as the CLI reported it (§9.1, task 124 decision 17).
// Nothing is synthesized: every field is the CLI's own words, and "" (or nil)
// means the CLI did not report it.
type Skill struct {
	// Name is what an invocation names, verbatim — `plugin:skill` for a
	// plugin's skill on both claude and codex.
	Name string
	// Description is the CLI's own text. claude's carries a display label
	// such as `(project)`; it is left in place and never parsed.
	Description string
	// ArgumentHint is claude's `argumentHint`. codex has no such field.
	ArgumentHint string
	// Aliases are other names the CLI accepts for the same entry.
	Aliases []string
	// Scope is the CLI's own word for where the skill came from — codex's
	// `user|repo|system|admin`. It stays "" for claude, whose scope exists
	// only inside Description, and is never normalized across CLIs.
	Scope string
	// Plugin is the plugin the skill belongs to, as the CLI named it.
	Plugin string
	// Path is the skill's location on disk, when the CLI reported it.
	Path string
}

// SkillProblem is one entry the CLI could not load: codex's `errors[]` item,
// its path and message verbatim (§9.1, task 124).
type SkillProblem struct {
	Path    string
	Message string
}

// CanListSkills reports whether a implements SkillLister (§9.1, task 124).
//
// It answers "can this agent list at all", which is a fact about the adapter.
// It does not answer "will the installed build list": a build that cannot
// surfaces only at list time, as ErrSkillsUnsupported (task 124 decision 13).
//
// The false leg is proven against agenttest.StubNoSkills, not against
// whichever shipped adapter lacks the capability today. The Resumer lesson
// applies: a refusal proven against a real CLI inverts itself the day that
// CLI gains the capability.
func CanListSkills(a Adapter) bool {
	_, ok := a.(SkillLister)
	return ok
}

// SkillInvoker is the optional capability an adapter implements when a
// message can name one of its CLI's skills (§9.1, task 124). vincent passes
// the message through verbatim (task 124 decision 9); this is what a client
// uses to write the invocation into it, and nothing validates or rewrites it
// on the way to the CLI.
type SkillInvoker interface {
	// SkillSyntax is how this adapter's CLI recognizes an invocation. It is
	// static: the same for every build and every directory.
	SkillSyntax() SkillSyntax
	// Invocation is the exact text a client inserts to invoke s, given the
	// whole list s came from — codex needs the list to tell an ambiguous
	// name. It is built from the CLI's own Name; nothing is translated.
	Invocation(s Skill, among []Skill) string
}

// SkillSyntax is how a message names a skill (§9.1, task 124 decision 18).
type SkillSyntax struct {
	// Sigil is the character an invocation starts with: "/" or "$".
	Sigil string
	// Position is where in a message the CLI recognizes an invocation.
	Position SkillPosition
}

// SkillPosition is where in a message an invocation is recognized (§9.1,
// task 124 decision 18). Its values go on the wire unchanged, as
// `skill_position` on GET /v1/agents (§9.6).
type SkillPosition string

// Skill positions (task 124 decision 18).
const (
	// SkillLeading is an invocation the CLI expands only at the start of the
	// message — claude's `/name`.
	SkillLeading SkillPosition = "leading"
	// SkillAnywhere is an invocation the CLI recognizes as a token anywhere
	// in the message — codex's `$name`, cursor's `/name`.
	SkillAnywhere SkillPosition = "anywhere"
)

// CanInvokeSkills reports whether a implements SkillInvoker (§9.1, task 124).
// Like CanListSkills, its false leg is proven against agenttest.StubNoSkills,
// never against a shipped adapter: all three implement it today.
func CanInvokeSkills(a Adapter) bool {
	_, ok := a.(SkillInvoker)
	return ok
}
