// Package github is the daemon's door to GitHub (task 035, task 052, task
// 068, task 069, spec §5.3, §13.2). It answers four questions — is this
// project's `origin` a github.com repository, can this daemon reach it, what
// does one issue look like, and what does one pull request look like — and it
// acts on a pull request when a human asks it to.
//
// It was read-only until task 069 gave it one write, CreatePull, and that
// posture is now dated rather than current: task 068.4 added MergePull,
// ClosePull, ReopenPull, CommentPull and RerunFailedJobs (write.go), which is
// decision record row 11 rewritten — vincent delivers, human-triggered. The
// callers of runGHWrite and restWrite are the whole of what it writes. Every
// write is reached only from a human in vincent — the routes in front of them
// are excluded from the MCP tool surface (§13.4) — `github.enabled` gates them
// exactly as it gates every read, and a 403 on any of them is
// `no_write_scope`, never the read side's `forbidden`. A merge reads before it
// writes: its refusals come from a preflight of the merge state and the live
// check rollup, and the send is pinned to the head commit the human confirmed.
//
// CompareURL is unchanged, and is now the fallback rather than the only path:
// it is string construction over a parsed Repo, nothing is sent when it is
// built, and it is what a client opens when there is no write credential or
// the create call fails.
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
// Nothing in this package is reachable from the step path, the writes
// included: an issue is snapshotted onto the task at creation and every later
// render reads the snapshot, so a step render still cannot fail for an
// external reason (§8.4), and no step type, default workflow or automatic
// behaviour calls a write (task 068 decision 1).
//
// Checks are the third shape, and they are neither: they are fetched on
// demand and never held at all. A check rollup is a fact about a *commit*,
// not about a pull request, so CheckRollup names the ref it is about — a pull
// request that gains a push while a fetch is in flight has checks belonging
// to the previous head, and rendering them under the new one would show a
// green build for code nobody ran.
//
// Listings also serve a poller (task 096 decisions D and E). ListOptions.State
// takes StateAll and ListOptions.Since bounds a listing by update time, on
// both legs, so a caller diffing one listing against the last sees an issue
// close or a pull request merge as a changed row rather than one that quietly
// left an open-only window. Issue carries every assignee and PullRequest its
// labels and requested reviewers for that diff — as live as the rest of a
// listing: nothing here stores one.
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
