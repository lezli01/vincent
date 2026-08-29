// Package release reads the project's latest **stable** release from GitHub
// and compares it against the running build (task 055, spec §12.1, §12.3,
// §16).
//
// It is a leaf: it imports nothing else from internal/, exactly like
// internal/github, because both the daemon's background poller and the CLI's
// `vincent update` need it and neither may depend on the other.
//
// The call it makes is deliberately the smallest one that answers the
// question: a single unauthenticated GET of
// `/repos/lezli01/vincent/releases/latest`, with a short timeout and no
// identifying header — no token, no telemetry, no user agent that says
// anything about this machine (§16). GitHub's `releases/latest` excludes
// drafts and prereleases server-side, which already honours
// `.goreleaser.yaml`'s `prerelease: auto`; Latest rejects a prerelease tag a
// second time on the client so the guarantee does not rest on one API's
// documented behaviour alone.
//
// Failure is not an error a caller has to handle loudly. Offline, a 403 rate
// limit and a malformed body all degrade to the same thing — no answer — and
// the caller keeps whatever it knew before.
package release
