// Package github is the daemon's read-only door to GitHub (task 035, task
// 052, spec §5.3, §13.2). It answers four questions and nothing else: is this
// project's `origin` a github.com repository, can this daemon reach it, what
// does one issue look like, and what does one pull request look like.
//
// It is **read-only**, and task 052 did not change that: no method here
// writes, no `POST` is made and no mutating `gh` subcommand is run. The one
// thing that points at GitHub's own write surface is CompareURL, which is
// string construction over a parsed Repo — nothing is sent when it is built,
// and a human presses GitHub's button (decision record row 11).
//
// Two legs answer the last two. The `gh` CLI is preferred, because it carries
// the user's own host, enterprise and SSO configuration and driving an
// external CLI is what the daemon already does everywhere else; a plain
// net/http call against api.github.com with GITHUB_TOKEN/GH_TOKEN from the
// daemon's inherited environment is the fallback when `gh` is absent or
// unauthenticated. Both legs answer into **one** normalized Issue and one
// reason vocabulary (reason.go), so a client can never tell which one ran —
// and neither leg's own error text is ever handed to a client (decision 1).
//
// vincent stores no credential of its own, which is what keeps spec §2's
// "secret management (daemon inherits the user's environment)" non-goal
// intact.
//
// Nothing in this package is reachable from the step path: an issue is
// snapshotted onto the task at creation and every later render reads the
// snapshot, so a step render still cannot fail for an external reason (§8.4).
//
// Issues and pull requests are stored differently on purpose. An Issue is
// snapshotted, because a run has to be reproducible and `.Issue` has to
// render offline. A pull request is only ever *pointed at* — a PullLink of
// repo and number — because draft, state and merged status are live by
// nature, and a snapshot of them would read exactly like a current one while
// being wrong within minutes.
//
// It is a leaf: it imports nothing else from internal/.
package github
