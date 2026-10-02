// Package fakeissues is the fake GitHub's issue corpus and REST answers
// (issue #661, part of #658). It is **test infrastructure**: nothing in the
// daemon imports it, and nothing it says is a claim about vincent's own
// behaviour — only about what GitHub answers.
//
// It exists as a package rather than inside cmd/fakegh because the fake has
// two legs that must agree. cmd/fakegh, the `gh` stand-in, turns a Response
// into gh's argv/stdout/exit-code shape; the `net/http` leg mounts the same
// Store.Serve behind an httptest handler. One definition of the fake serves
// both, so a test cannot pass on one leg for a reason the other does not
// share. It cannot live in internal/github/githubtest, whose contract is that
// only _test files import it — cmd/fakegh is a main.
//
// What it holds:
//
//   - The corpus: a JSON array of REST-shaped rows in a file
//     (FAKEGH_ISSUES_FILE), re-read on every call and written back by every
//     write through a temp file and a rename, so the issues can change
//     between two polls of one running daemon. Issue rows, pull request rows
//     (they carry `pull_request`, and the issues listing includes them, as
//     GitHub's does) and comment rows (they carry `issue_url`) share the one
//     array. Unset, the built-in corpus answers — the two open issues the
//     porcelain has always served — and a set but missing file is seeded
//     from it.
//   - The REST routes: `repos/{o}/{r}/issues` with state, since, sort,
//     direction, per_page and page, a Link header across pages and a weak
//     ETag over the body that turns a matching If-None-Match into a 304;
//     `repos/{o}/{r}/issues/{n}`, GET and PATCH (state, state_reason,
//     duplicate_issue_id); and `repos/{o}/{r}/issues/comments`.
//   - The per-issue faults: a row carrying `"_fake": {"transferred_to":
//     "owner/other#12"}` answers 301 with a Location, one carrying `"_fake":
//     {"deleted": true}` answers 410, and both are left out of every listing.
//     A marker is a corpus edit, not a scenario, so a gate can break one
//     issue among good ones. `_fake` never reaches an answer.
//   - The scenarios that reach this surface: `read-only` (writes 403),
//     `rate-limited` (every call 403 with the rate-limit headers) and
//     `unreachable` (ErrUnreachable — there is no HTTP answer at all).
//     Scenario resolves the scenario for one invocation, with
//     FAKEGH_SCENARIO_FILE over FAKEGH_SCENARIO, so a gate can flip one
//     between two polls without restarting anything.
//   - The porcelain projection: List and Issue answer `gh issue list` and
//     `gh issue view` from the same rows, so a write made over REST shows on
//     every surface.
package fakeissues
