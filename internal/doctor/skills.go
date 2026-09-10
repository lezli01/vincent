package doctor

import "github.com/lezli01/vincent/internal/skill"

// Skill is one published skill's installation state (§9.8, task 095). It is
// an alias rather than a copy for the reason internal/apiclient aliases the
// whole report: internal/skill owns the vocabulary, and a second declaration
// of it here would be a second definition of the same document.
//
// The group sits **beside** the agents group rather than inside it. Task 041
// closed the §9.5 facet vocabulary at five, and a skill is a property of the
// machine's agent configuration rather than of an adapter binary — a machine
// with no adapter installed can still hold the skill, and one copy in the
// store serves every agent at once (decision 4).
//
// Like GitHub, Container and Update it is a **row, not a problem**: a missing,
// stale or unreadable skill leaves everything working — the built-in
// `create-workflow` and `update-workflows` prompts carry the skill's text
// themselves — so none of it moves `vincent doctor`'s exit code (decision 5).
type Skill = skill.Status

// DetectSkills reads the installation state off the filesystem. It runs no
// subprocess and opens no socket, which is what lets it answer identically on
// the daemon and in a client with no daemon (decision 9).
func DetectSkills() []Skill {
	return skill.Detect(skill.Options{})
}
