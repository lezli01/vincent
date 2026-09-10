// Package skill answers two questions about the agent skills this repository
// publishes (§9.8, task 095): is the one this binary ships installed on this
// machine, and — when it is not — how does it get there.
//
// The two halves are deliberately unequal. **Detection is a filesystem read**
// and nothing else: the `skills` CLI installs one copy into a global store at
// `~/.agents/skills/<name>/` and links it into each selected agent's
// directory, so reading `SKILL.md` out of the store answers the version and
// stat-ing `~/.<agent>/skills/<name>` answers who it is linked into. That is
// what makes `vincent doctor` report the truth on a machine with no node
// installed at all. **Installation shells out** to `npx skills add`, because
// reproducing another tool's install layout — its store, its links, its
// symlink/copy split — is a second implementation of something already
// published (decision 1).
//
// It is a leaf: it imports nothing under internal/, the way gitx and procx do
// not, so both the daemon composing a report and a client with no daemon can
// use it. Its one non-stdlib import inside this module is the `skills`
// package at the repository root, which is where `go:embed` can see the
// published tree at all.
//
// The v0 T1.7 decision ("no state-file parsing") is not reopened here. That
// decision is about inferring another tool's *authentication* from its
// private state; `~/.agents/skills/` and `~/.<agent>/skills/` are the
// documented install locations of a public CLI, and the file read out of them
// is one this repository published.
package skill
