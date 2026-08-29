# Task 055 gate — the release check and `vincent update`

**Acceptance:** a real binary, on a real machine, checks a real published
release feed; `vincent update` verifies a real keyless cosign signature against
Fulcio and Rekor; and the binary it swaps in **starts** with Gatekeeper or
SmartScreen in front of it.

This gate has **no script**, deliberately, for the same reason
[`032`](032-macos-notarization.md) has none: the three things it judges are the
three things a hermetic test cannot produce. Every scripted gate in
`scripts/` drives a real daemon over curl, and nothing in the swap path involves
a daemon at all — the CLI does the download, the verification and the swap
(spec §12.1), which is the whole point of that exception.

## What CI already proves, and what is left

CI covers, on Linux, macOS and Windows:

- parsing a captured `releases/latest` body, and that a prerelease is never
  offered;
- that a 403, a timeout and a malformed body all degrade to "unknown" with no
  caller blocked;
- that with `update.check: false` or `update.poll_interval: 0` the daemon's
  poller never touches an `httptest` feed whose handler fails the test if hit;
- that the checksum leg refuses a flipped byte and leaves the old binary
  byte-identical, that a `cosign` exiting nonzero refuses, and that a missing
  `cosign` degrades with the outcome reported and is fatal under
  `--require-signature`;
- that the swap preserves the mode bits, leaves no staging file, and does not
  touch the destination when it fails — including the Windows rename-aside path,
  which is covered by running the same test on Windows rather than by a separate
  script;
- the channel table, over synthetic paths, for all three `GOOS` values.

What is left for a human is exactly the three legs above: a real feed, a real
signature, and a real operating system deciding whether to run the file.

## Walkthrough

Run this against a **published release**, on a machine where vincent came from
the release archive rather than from a package manager.

1. **The check answers from the real feed.**

   ```sh
   vincent update --check
   echo "exit $?"
   ```

   Expect the current and latest versions and either "You are up to date"
   (exit 0) or "An update is available" (exit 2).

2. **The daemon's cached answer agrees.** With a daemon running:

   ```sh
   vincent doctor | sed -n '/UPDATE/,/^$/p'
   ```

   The `latest release` row names the same version as step 1, or says
   "not checked yet" if the daemon started less than a poll ago. Neither ever
   changes `vincent doctor`'s exit code.

3. **The check is genuinely off when switched off.** Set
   `update:\n  check: false` in `config.yaml`, wait for the hot reload, and
   confirm the daemon makes no request — with a packet capture, or by watching
   `vincent doctor` keep reporting "not checked (update.check is false)" across
   a restart. `vincent update --check` must still answer, because it queries the
   feed itself.

4. **Verification really runs.** With `cosign` installed:

   ```sh
   vincent update --require-signature
   ```

   The swap must succeed against a genuine release and must *not* report
   "the cosign signature was NOT verified". Uninstall `cosign` (or point `PATH`
   away from it) and repeat: the same command must now refuse, and plain
   `vincent update` must proceed with the warning.

5. **The swapped binary starts.** This is the leg no test can reach:

   ```sh
   vincent version
   ```

   On macOS, from a fresh download in the same session — Gatekeeper judges the
   file, not the command. On Windows, from a fresh shell, watching for
   SmartScreen. A binary that verified perfectly and will not launch is a
   failed gate.

6. **The daemon mismatch is visible.** Immediately after the swap, without
   restarting:

   ```sh
   vincent daemon status
   ```

   Expect the "this binary is … — the running daemon is older" line, and exit
   0: nothing is broken.

7. **A package-managed install is untouched.** On a machine where vincent came
   from Homebrew, Scoop, WinGet, mise, a deb/rpm or `go install`:

   ```sh
   vincent update
   echo "exit $?"
   ```

   Expect exit 2, that channel's command printed, and — verified with
   `shasum`/`Get-FileHash` before and after — a byte-identical binary.

## Run record

| Date | Version | Platform | Walked by | Result |
|---|---|---|---|---|
| — | — | — | — | not yet walked; needs a release published after this task landed |
