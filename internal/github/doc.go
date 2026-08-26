// Package github is the daemon's read-only door to GitHub issues (task 035,
// spec §5.3, §13.2). It answers three questions and nothing else: is this
// project's `origin` a github.com repository, can this daemon reach that
// repository's issues, and what does one issue look like.
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
// Everything here happens at pick time and create time. Nothing in this
// package is reachable from the step path: an issue is snapshotted onto the
// task at creation and every later render reads the snapshot, so a step
// render still cannot fail for an external reason (§8.4).
//
// It is a leaf: it imports nothing else from internal/.
package github
