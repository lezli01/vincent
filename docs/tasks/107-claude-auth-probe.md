# 107 — Probe claude's login state with `claude auth status`

**Status:** ✅ done (5/5)
**Issue:** [#398](https://github.com/lezli01/vincent/issues/398)
**Spec:** amends §9.2, §9.5, §9.6 (example), §18

## Problem

claude was the one adapter still reporting `logged_in: null`. Task 006 decision
1 said claude had no auth surface and, in its own out-of-scope list, said to
"revisit if the CLI ever grows a non-interactive auth surface". Task 026 noted
that it had one and asked for its own issue. This is that revisit, not a
reversal.

Checking the claim turned up that it was wrong from the start rather than out
of date. `internal/agent/claude/testdata/help_2.1.224.txt` is 38 lines — the
Options section of `claude --help`, with no Commands list — and the Claude Code
changelog records "Added `claude auth login`, `claude auth status`, and
`claude auth logout` CLI subcommands" under **2.1.41**. So 2.1.224 already had
the command; the conclusion was drawn from a cut-down fixture.

What the real CLI answers, verified on 2.1.268:

- `claude auth status --help` documents `--json  Output as JSON (default)` and
  `--text`.
- Logged in: exit 0, stdout `{"loggedIn":true,"authMethod":"claude.ai",…}` with
  the account's email, organization and subscription alongside. stderr empty.
- Logged out: exit **1**, stdout `{"loggedIn":false,"authMethod":"none",…}`,
  stderr empty. Reproducible without signing anyone out, by running with
  `CLAUDE_CONFIG_DIR` pointed at an empty directory and `ANTHROPIC_API_KEY`
  unset — which is how the fixture was captured.
- `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` and the Bedrock/Vertex/Foundry
  switches all report `loggedIn: true`: the CLI says whether credentials are
  configured, not whether they work.
- The call is local and about 0.1 s, within §9.6's "always dynamic, never
  slow".

The v0 **T1.7 decision** (no state-file parsing) is untouched: this reads an
official subcommand's stdout and never `~/.claude*`, the keychain or
`.credentials.json`. **Task 003 decision 4** (no pre-flight refusal on
`logged_in: false`) is kept, and `internal/doctor`'s closed Problem set is
unchanged — a claude `false` is a visible row, not a problem.

## What shipped

- `internal/agent/claude/auth.go`: `authStatusSupported` (the version gate),
  `loggedIn` (gate → `agent.Probe(ctx, versionTimeout, path, "auth", "status",
  "--json")` → parse) and `parseAuthStatus`, a pure function over stdout and
  the probe's error. `Detect` sets `LoggedIn` from it.
- `testedVersions` gains `2.1.268`, the capture build.
- Fixtures, personal fields replaced with placeholders (`fake@example.com`, the
  nil UUID, `Example Organization`, neutral paths), every other field as
  captured: `auth_status_logged_in_2.1.268.json`,
  `auth_status_logged_out_2.1.268.json`, `help_auth_status_2.1.268.txt`.
  `help_2.1.224.txt` is not recaptured — the option probe it serves is
  unaffected — and §9.5 now names it as cut down.
- `cmd/fakeagent` answers `auth status` before dialect dispatch, with
  `FAKEAGENT_CLAUDE_LOGGED_OUT`, `FAKEAGENT_CLAUDE_AUTH_UNKNOWN` and
  `FAKEAGENT_CLAUDE_AUTH_HANG`, and gains `FAKEAGENT_ARGV_FILE` (fakegh's
  `FAKEGH_ARGV_FILE`, for the same purpose) so a test can prove a probe was
  never spawned.
- Nothing in `internal/agent/catalog.go` changes: `staleAuth` already re-asks
  any entry whose `logged_in` is non-nil after `authTTL`, and `redetect` keeps
  the previous answer when a re-probe returns nil. A claude that answers joins
  that refresh on its own; an old one costs nothing extra.

## Decisions

All dated 2026-09-16, settled with the author, and binding.

### 1. Only the JSON field decides

The probe passes `--json` explicitly, so a future change of default cannot
change the format under the parse. Only a JSON object on stdout whose
`loggedIn` is a boolean gives an answer, and that boolean is the answer
**whatever the exit code**. Everything else is `nil`: exit 1 without readable
JSON, `loggedIn` missing or not a boolean, a timeout, a cancellation, a failure
to spawn.

This intentionally differs from the §9.5 rule codex and cursor share ("non-zero
exit is `false`"). That leg exists because their logged-out wording has never
been captured. claude's has, as structured JSON, and exit 1 is also what any
ordinary CLI error returns — an unknown option on some intermediate build, the
2.1.139-era `forceRemoteSettingsRefresh` deadlock, a config error. Reading that
as "not authenticated" is the false accusation T4.22 forbids.

**Beat:** reusing the codex/cursor layering, which would turn every claude CLI
error into a logged-out warning.

### 2. Version gate `2.1.41 ≤ version < 3.0.0`

The floor is the changelog's introduction version. The ceiling is the family
`supportsInput` already uses (`major == 2 && minor >= 1`), tightened by the
patch floor. Outside the range, or when the version does not parse, `Detect`
spawns nothing and reports `nil` — which is how older CLIs stay `null`.

A pre-2.1.41 CLI could treat `auth status` as a prompt and start an interactive
session with no TTY, hanging until the 20 s probe timeout on every re-probe, or
exit non-zero. Not spawning it is the only safe probe. A 3.x CLI stays `nil`
until someone verifies it, the conservative choice `supportsInput` made; `nil`
is harmless because it is what every claude reported before this.

**Beat:** probing unconditionally and relying on the parse to reject what comes
back, which bounds the answer but not the cost of asking.

### 3. `true` means "credentials configured", passed through as-is

vincent does not second-guess the CLI's boolean by `authMethod`: API-key and
Bedrock/Vertex/Foundry setups report `true` just as they will run. The docs
state the limit plainly for every adapter — codex's `login status` has the same
property — rather than implying claude alone is weaker.

**vincent reads only `loggedIn`.** The email, organization id and name,
subscription type and the rest are never decoded into a struct field, logged,
stored or sent over the API. The parse struct has exactly one field, and a test
holds it there. Exposing `auth_method` on the API is out of scope.

**Beat:** mapping `authMethod: none` to `false` and everything else to `true`,
which duplicates the CLI's own verdict with a second, unverified vocabulary.

## Work

- [x] **107.1 — The probe**: `internal/agent/claude/auth.go`, `Detect` wiring,
  `testedVersions`, the three fixtures. ✓ 2026-09-16
- [x] **107.2 — The fake**: `cmd/fakeagent/claude_auth.go` (`auth status` and
  its three legs, `FAKEAGENT_ARGV_FILE`), the pre-dialect switch and the header
  table in `main.go`. ✓ 2026-09-16
- [x] **107.3 — Tests**: `parseAuthStatus` table-driven against the fixtures;
  the one-field struct and the fixtures' placeholders; `authStatusSupported`;
  `Detect` logged in, logged out and unknown against the fake; the timeout leg;
  below the floor, no `auth` argv reaches the fake; `internal/api` claude
  `true`/`false` on `/v1/agents` and `/v1/info`; the catalog re-asking a claude
  entry after `authTTL` and keeping its boolean over a `nil` re-probe; `m5`
  scenario 3 asserting claude's definite `true` beside cursor's `false` on the
  same fake binary. ✓ 2026-09-16
- [x] **107.4 — Comments that made the stale claim**: `internal/agent/agent.go`,
  `internal/doctor/doctor.go`, `internal/api/server.go`,
  `internal/cli/doctor.go`, `internal/apiclient/daemon.go`,
  `internal/agent/catalogauth_test.go`. ✓ 2026-09-16
- [x] **107.5 — Documentation**: §9.2, §9.5, the §9.6 example and §18 amended,
  dated; `docs/reference/api.md`, `docs/guides/agents.md`,
  `docs/guides/troubleshooting.md` (both places), `docs/reference/cli.md`,
  `docs/gates/m5-gate.md`, `CHANGELOG.md`; the superseded note on task 006
  decision 1 and pointers from task 006's out-of-scope list and task 026. No
  `docs/assets/tui-*.png` changes: the TUI draws `true` and `null` alike and
  only a `false` adds anything, and the seeded fake claude reports `true`.
  ✓ 2026-09-16
