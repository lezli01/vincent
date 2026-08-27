# 035 — Select a GitHub issue when creating a task

**Status:** ✅ done (12/12)
**Opened:** 2026-08-26
**Issue:** [#200](https://github.com/lezli01/vincent/issues/200)

Most work in a GitHub-hosted repository starts life as a GitHub issue, and
vincent knew nothing about GitHub. Creating a task for an existing issue meant
opening it in a browser, copying the title into the form, copying the body into
the description, and re-typing anything the workflow declared as a field. The
number and URL survived only if the human remembered to paste them, and the
agent that eventually ran the workflow never saw the issue at all — a prompt
could not say "fix the bug described below", because there was nothing in the
§8.4 context to interpolate.

This adds the **reading** half of that: a task can be created *from* an issue,
the pick prefills the task, and the issue reaches templates as `.Issue`.
Checking on pull requests is the intended next piece and is deliberately not
built here.

## Decisions

### 1. `gh` first, an environment token second (2026-08-26)

The daemon prefers `gh issue list/view --json …` and falls back to a `net/http`
call against `api.github.com` using `GITHUB_TOKEN`/`GH_TOKEN` from its inherited
environment when `gh` is absent or logged out. The two are **alternatives, not
a chain that stops at the first disappointment**: an installed-but-logged-out
`gh` falls through to the token rather than failing.

**Beat:** a single REST path that mints its own credential with `gh auth token`.
That is one implementation instead of two, but it throws away exactly what makes
`gh` worth driving — the user's own host, enterprise and SSO configuration — and
driving an external CLI is what the daemon already does everywhere else.

The cost is accepted knowingly: two listing implementations, two error mappings,
two sets of fixtures, all on three platforms. It is funded by two constraints
rather than by hope. Both legs answer into **one** normalized `Issue`, proven by
a test that reads the same real issue through both captured shapes and compares
the results; and both map their failures onto **one** reason vocabulary, so
neither leg's own text — `gh` stderr, an HTTP body — ever reaches a client. Those
strings go to the daemon log and nowhere else.

### 2. The daemon owns the prefill; the TUI previews it (2026-08-26)

`GET /v1/projects/{id}/github/issues?workflow=…` returns each issue plus a
computed `prefill` — title, description with its link line, and the declared
fields the mapping filled. The TUI drops that into its editable rows, so every
guess is visible before creation. `POST /v1/tasks` carrying `github_issue`
recomputes exactly the same prefill server-side and lets any explicitly-supplied
value win.

One mapping implementation is what makes "`--github-issue N` produces the same
stored task as the TUI path" a **testable claim rather than a coincidence**, and
it is why the CLI flag is resolved daemon-side rather than by the CLI fetching
and expanding the issue itself.

Precedence keys on **presence** for `fields` and on **blank/absent** for title
and description. A `fields` key sent with an empty value is a row a human
cleared on purpose, and re-filling it would make the form's "nothing is locked"
promise false; there is no such thing as deliberately creating a task with an
empty title, and an absent description is how every client already spells "you
decide". The TUI therefore sends its description and its declared field rows
explicitly — empties included — whenever a draft is linked to an issue.

### 3. The snapshot is one nullable JSON column (2026-08-26)

`ALTER TABLE tasks ADD COLUMN github_issue_json TEXT`; NULL means no linked
issue. Direct precedent: 0012's `pending_follow_up_json`. Labels stay a real
list instead of a joined string, and the shape can grow when PR checking arrives
without a second migration. Nothing queries inside it — no index, no generated
column — so a linked task costs the same as any other on every board query.

The issue's own "no new `Project` column and no migration" stands as written: it
was about deriving *repo identity* from `origin` at the point of use, which
decision 5 keeps. The task column is new work the issue implied but never
priced.

### 4. Availability is its own probe (2026-08-26)

`GET /v1/projects/{id}/github` returns `{enabled, repo, available, reason,
message, via}` with a short daemon-side TTL. The TUI calls it once when a
project is chosen in the new-task form; `vincent doctor` and `vincent github
status` read the same machinery for their rows.

**Beat:** putting it on the project DTO — the board lists projects constantly,
and that would probe `gh auth` per project on every refresh. **Beat:** inferring
it from a failed issue list — that surfaces the reason only after the call it is
supposed to prevent.

The gate stops at the first "no", which is what makes "with the integration
disabled, or on a non-GitHub project, no GitHub call is made" true by
construction rather than by intent. It is asserted that way too: the tests run
a fake `gh` that logs its argv, and check the log is empty.

### 5. Derived facts, decided at the point of use (2026-08-26)

"GitHub-based" is parsing the `origin` remote for a github.com host; "enabled" is
the config toggle. Neither is stored. A project whose origin is an SSH alias, or
whose GitHub remote is not named `origin`, is simply **not GitHub-based for
now** — the known narrowness the issue's first alternative ("store `github_repo`
on `Project`") is held in reserve for. PR checking, which needs a durable repo
identity, is the expected trigger to revisit it.

### 6. Config defaults to enabled (2026-08-26)

`github: { enabled: true }`. The issue frames the toggle as "a user who does not
want the daemon reading GitHub can turn it off", which is an opt-*out*. It is
inert on every non-GitHub project and makes no call until a human opens the
picker or names an issue, so on-by-default costs nothing unasked for. Read per
use, so a hot reload reaches the next one, like the rest of §12.3.

There is deliberately no token key in `config.yaml`: that would make vincent a
secret store, which §2 declines.

### 7. Prefill rules, stated so they are testable (2026-08-26)

- **Title** ← the issue title, truncated only by the same rules any typed title
  gets — which live in §13.1's bounds check, applied to the prefilled request so
  the two cannot diverge. *Amended 2026-08-27:* prefixed with the issue's own
  number — `#42 daemon leaks the lock file`. It is for the humans reading a
  board row and for the slug reading a branch name; no workflow parses it back
  out, which is what the `issue` field below is for. A title that already
  carries the prefix is left alone rather than doubled.
- **Description** ← the issue body, followed by a link line appended as its own
  trailing block: a blank line, then `GitHub issue #N: <url>`. It is plain text
  in an editable row — a human may delete it — and a task read on its own still
  points back. An empty body yields the link line alone rather than a leading
  blank line, and CRLF bodies from GitHub's web editor are normalized, because
  otherwise every prompt interpolating the description carries stray carriage
  returns.
- **Declared fields** (§8.1.2) are filled by **exact name match only**, against
  the names `issue`, `labels`, `assignee`, `milestone`. No aliases, no fuzzy matching, no
  case folding: a guess that has to be reviewed is cheap, a guess that is hard to
  predict is not. A value is offered only if the declaration's `type` and
  `pattern` would accept it — a declared `integer` named `milestone` gets the
  milestone *number*, a `string` gets its title, and anything that would fail
  validation is left empty rather than pre-filling a value the create call would
  400 on. `labels` renders comma-joined, because §8.1.2 values are strings
  everywhere; the structured list stays on `.Issue.Labels`.
- **Undeclared names are never invented.** Issue metadata reaches templates
  through `.Issue`, which is what the "flatten into `Task.Fields`" alternative
  was rejected for.

*Amended 2026-08-27 — `issue`, the fourth name.* A declared `issue` field is
filled with the issue **number**: bare decimal for a `string` declaration, the
same digits for `integer` and `number`, nothing for a `boolean`. It exists
because the number had nowhere else to go that a `command` step could reach.
Step bodies receive §8.5's environment, not §8.4's template context, so
`.Issue.Number` is unreadable from a `run:` — before this, a workflow acting on
an issue had to parse the id back out of `VINCENT_TASK_TITLE`, which made the
title a machine-readable contract and the first token of it unwritable. The
field moves the contract to where §8.1.2 already validates it, and hands the
title back to the humans. `.vincent/workflows/github-resolve-issue.yaml` is the
first consumer: it declares `issue` `required: true` and its `fetch` step reads
`{{ index .Task.Fields "issue" }}`.

This does not weaken "undeclared names are never invented": a workflow that
declares no `issue` field still gets none, and the number remains available to
templates as `.Issue.Number` either way.

The validation is `workflow.FieldDefinition.Validate`, exported for this and
used by `ValidateTaskFields` itself, so "what the prefill offers" and "what the
create call accepts" cannot drift apart.

### 8. `.Issue` is zero-valued when absent (2026-08-26)

Same convention as `.Loop`'s `Index: 0`: `{{ if .Issue.Number }}` distinguishes
the two, and a template shared between linked and unlinked tasks renders on
both. Rendering stays pure and offline — the snapshot is read from the task row,
never from the network — which is what keeps §8.4's promise that a step render
cannot fail for an external reason.

### 9. Fan-out children inherit the parent's issue snapshot (2026-08-26)

Lane child tasks already inherit the parent's `Fields`, and a lane prompt that
could read `.Task.Fields` but not `.Issue` would be an arbitrary hole. Children
copy the parent's snapshot verbatim; no lane re-fetches anything. The copy is
deep, because the two rows are independent tasks and sharing a label slice
between them is not a property worth relying on.

### 10. Out of scope, deliberately (2026-08-26)

Pull requests — checking, listing, or reporting on them — are the intended next
piece and are not built here. Nothing in this design forecloses them: the
normalized types live in their own package, the capability probe already answers
"is this project GitHub-based and reachable", and the JSON column can carry a PR
shape later. Also out: writing anything to GitHub, non-`origin` remotes, GitHub
Enterprise hosts beyond whatever `gh` itself resolves, re-fetching a stale
issue, and any GitHub call from a client process.

## Tasks

- [x] **035.1** `internal/github`: `doc.go`, the normalized `Issue`, `Repo` and
  `ParseRemote`, the reason vocabulary and `Error`, the prefill mapping.
  A leaf package — it imports nothing else from `internal/`. ✓ 2026-08-26
- [x] **035.2** The `gh` leg: `gh auth status`, `gh issue list/view --json`,
  the argv, the parse, and the stderr → reason mapping. `hideConsole` on
  Windows, for the reason `gitx` has it. ✓ 2026-08-26
- [x] **035.3** The REST fallback leg: `GITHUB_TOKEN`/`GH_TOKEN`, the request
  headers, the parse, the status → reason mapping, and the pull-request filter
  that makes the two legs return the same list. ✓ 2026-08-26
- [x] **035.4** `internal/config`: the `github:` block, defaulted on, exposed on
  `GET /v1/config`'s file and documented in the generated `config.yaml`.
  ✓ 2026-08-26
- [x] **035.5** Migration `0014_github_issue.sql`, `Task.GitHubIssue`, and the
  column through `taskColumns`/insert/scan. ✓ 2026-08-26
- [x] **035.6** `internal/api`: the two endpoints, `Deps.GitHub`, the gate, the
  prefill computation, and `github_issue` on `POST /v1/tasks` with the
  explicit-wins rule. ✓ 2026-08-26
- [x] **035.7** `internal/apiclient`: `GitHubStatus`, `GitHubIssue`,
  `GitHubPrefill`, the two calls, and `github_issue` on the create request and
  the task detail. ✓ 2026-08-26
- [x] **035.8** `internal/cli`: `--github-issue` on `task add` (with `--title`
  no longer unconditionally required), `vincent github issues` and
  `vincent github status`. ✓ 2026-08-26
- [x] **035.9** `.Issue` in `workflow.RenderContext`, populated by
  `taskrun.renderContext` from the task row, and the snapshot copied onto
  fan-out lanes. ✓ 2026-08-26
- [x] **035.10** `internal/doctor`: the GitHub row — toggle, `gh` presence,
  version and login state, token variable *name*, and readability. Composable
  without a daemon, and never a Problem. ✓ 2026-08-26
- [x] **035.11** `internal/tui`: the conditional `ntIssue` row, the picker, the
  prefill application, cursor skipping, and the guided layout's Task-details
  stage. ✓ 2026-08-26
- [x] **035.12** Tests, docs and the spec amendments: `cmd/fakegh` and
  `internal/github/githubtest`, captured `gh` and REST fixtures, the API/TUI/CLI
  suites, and the §3/§5.3/§8.4/§12.1/§12.3/§13.2/§14/§15/§17/§20 amendments.
  ✓ 2026-08-26

## Verification

- `go test ./...` — 2105 tests, all packages green.
- `go run mage.go lint` plus a host-built linter run for `GOOS=windows`,
  `darwin` and `linux` — 0 issues on each.
- No test reaches the network and none reads the user's real config or data
  dirs. The `gh` leg is exercised through `cmd/fakegh` on all three platforms;
  the REST leg through `httptest`. The fixtures under
  `internal/github/testdata/` are **captured**: `gh_2.98.0_*.json` from
  `gh 2.98.0 --json`, `rest_*.json` from the REST API, including a listing that
  really does mix pull requests into `/issues`.
