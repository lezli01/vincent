// Package github is the daemon's door to GitHub (task 035, task 052, task
// 068, task 069, task 130.5, spec §5.3, §13.2, §18). It answers whether this
// project's `origin` is a github.com repository, whether this daemon can
// reach it, what an issue and a pull request look like, and — for the issue
// sync — every issue and comment that changed since a watermark. It acts on a
// pull request, and on an issue's state, when a human asks it to.
//
// It was read-only until task 069 gave it one write, CreatePull, and that
// posture is now dated rather than current: task 068.4 added MergePull,
// ClosePull, ReopenPull, CommentPull and RerunFailedJobs (write.go), which is
// decision record row 11 rewritten — vincent delivers, human-triggered — and
// task 130.5 added SetIssueState (issuestate.go), which closes an issue with a
// reason or reopens it (task 130 decisions 9 and 10: state is two-way, and
// the write-back is a human's). The callers of runGHWrite, restWrite and
// apiCall with a mutating method are the whole of what it writes. Every write
// is reached only from a human in vincent — the routes in front of them are
// excluded from the MCP tool surface (§13.4) — `github.enabled` gates them
// exactly as it gates every read, and a 403 on any of them is
// `no_write_scope`, never the read side's `forbidden`. Every write reads
// before it writes: a merge's refusals come from a preflight of the merge
// state and the live check rollup, and the send is pinned to the head commit
// the human confirmed; an issue state write's preflight read is returned to
// the caller for compare-and-set, and reports `gone`, `moved` and `not_found`
// before anything is sent. A write that happened is never reported failed
// because the read after it did not parse.
//
// CompareURL is unchanged, and is now the fallback rather than the only path:
// it is string construction over a parsed Repo, nothing is sent when it is
// built, and it is what a client opens when there is no write credential or
// the create call fails.
//
// Two legs answer every call. The `gh` CLI is preferred, because it carries
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
// behaviour calls a write (task 068 decision 1). internal/taskrun imports this
// package for the Issue type alone; a test holds internal/taskrun and
// internal/workflow to naming no write method.
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
// The durable listings are the sync's (task 130.5, listing.go):
// ListIssuesSince and ListIssueComments speak REST on both legs — `gh api -i`
// and net/http — and parse with one decoder, so the legs agree by
// construction. They walk `rel="next"` in ascending update order up to a page
// cap, keep each issue's node id (the key a synced issue is stored under,
// task 130 decision 2), drop pull request rows, and resume by watermark: a
// capped walk is Truncated, and the caller calls again from ResumeSince and
// dedupes the inclusive boundary by node id. A listing complete in one page
// returns its ETag, and sending it back answers a 304 as Unchanged rather
// than as an error; a multi-page listing returns none, because in ascending
// order a change lands on the last page and a page-1 304 would hide it. No
// request this package makes follows a redirect: a transferred issue is
// `moved` with its new location, and a deleted one `gone` (reason.go).
// GetIssue is the same single-issue read, exported for the sync's daily
// sweep of open issues the open listing no longer carries.
//
// Those listings are standing traffic, not on-demand reads (task 130.8):
// the daemon's reconciler imports and refreshes every GitHub-based
// project's issues on each `github.poll_interval` tick, whether or not
// anyone has opened an issue picker. An idle repository costs one
// conditional request a tick, answered 304; `github.enabled: false` or
// `github.poll_interval: 0` and it costs none — the gate is checked before
// any call, as it is for every other read.
//
// Issues and pull requests are stored differently on purpose. An Issue on a
// task is snapshotted, because a run has to be reproducible and `.Issue` has
// to render offline — and that stays true now that the same shape also
// carries a synced issue's node id, state reason and close time. A pull request is only ever *pointed at* — a PullLink of
// repo and number — because draft, state and merged status are live by
// nature, and a snapshot of them would read exactly like a current one while
// being wrong within minutes.
//
// It is a leaf: it imports nothing else from internal/.
package github
