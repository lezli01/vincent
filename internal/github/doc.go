// Package github is the daemon's door to GitHub (task 035, task 052, task
// 069, spec §5.3, §13.2). It answers four questions — is this project's
// `origin` a github.com repository, can this daemon reach it, what does one
// issue look like, and what does one pull request look like — and it performs
// exactly one action: it creates a pull request.
//
// It was read-only until task 069, and that amendment is deliberate and
// narrow. **CreatePull is the only write.** No other method here writes, no
// other `POST` is made and no other mutating `gh` subcommand is run: nothing
// updates, comments on, closes or merges anything, and decision record row
// 11's prohibition on hardcoded merge behaviour is untouched. The write is
// reached only from a human pressing a key in vincent — the route in front of
// it is excluded from the MCP tool surface (§13.4) — and `github.enabled`
// gates it exactly as it gates every read.
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
// Nothing in this package is reachable from the step path: an issue is
// snapshotted onto the task at creation and every later render reads the
// snapshot, so a step render still cannot fail for an external reason (§8.4).
//
// Checks are the third shape, and they are neither: they are fetched on
// demand and never held at all. A check rollup is a fact about a *commit*,
// not about a pull request, so CheckRollup names the ref it is about — a pull
// request that gains a push while a fetch is in flight has checks belonging
// to the previous head, and rendering them under the new one would show a
// green build for code nobody ran.
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
