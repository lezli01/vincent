// Package selfupdate replaces the running vincent binary with a verified
// release archive (task 055, spec §12.1, §16).
//
// **The CLI does this, never the daemon.** That is a stated exception to §4's
// "the daemon owns everything", written into §12.1 beside `daemon restore`'s,
// and it is the same kind of exception for the same two reasons: the operation
// has to work with no daemon running at all, and a daemon cannot cleanly
// rewrite its own running image on Windows. What the daemon keeps is the
// background check and the cached answer (internal/release).
//
// Nothing downloaded is executed before it is verified. The chain is the one
// the release footer already tells a human to run, because there is no
// per-asset signature to check — `.goreleaser.yaml` signs `artifacts:
// checksum`, so the release carries exactly one `checksums.txt.sig` and one
// `checksums.txt.pem`:
//
//  1. Verify `checksums.txt` against its cosign signature and certificate,
//     pinned to the project's identity regexp and OIDC issuer.
//  2. Verify the downloaded archive's SHA-256 against that now-trusted file.
//  3. Extract the binary from the verified bytes and swap it into place.
//
// Step 1 prefers an installed `cosign` and degrades rather than refusing
// (decision 1): with no `cosign` on PATH the checksum check runs alone and the
// command says plainly that the signature was not verified, and
// `--require-signature` makes the absence fatal for a caller who wants the
// guarantee. This is the shape internal/github already uses for `gh` — prefer
// the user's own tool, fall back, never bundle. Embedding sigstore-go loses on
// two counts: a very large dependency tree in a project whose runtime require
// block is fifteen lines, and a second outbound call (a TUF trust-root
// refresh) in a feature whose whole promise is one unauthenticated GET.
//
// A binary a package manager owns is never touched. Channel detection is a
// runtime path heuristic and cannot be a build-time stamp: one archive feeds
// the direct download, the Homebrew cask, Scoop, WinGet and the nfpm packages,
// so no ldflag can tell them apart. An unidentifiable install is treated as
// package-managed — the conservative direction, since the cost of being wrong
// is a printed command rather than a clobbered file a package manager will
// silently revert on its next upgrade.
package selfupdate
