# 138 — GitHub Copilot CLI as a fourth agent adapter

**Status:** 🔄 in progress (10/17)

Issue [#852](https://github.com/lezli01/vincent/issues/852), the epic, and
its seventeen children below. Spec §9.9 is opened as a stub by this
document's pull request (138.1) and filled bullet by bullet by the pull
request of each item whose code makes a bullet true; §2, §9, §12.3 and §19's
three-adapter prose is amended by 138.15.

## What this is

vincent drives agent CLIs through one interface, `agent.Adapter` (spec §9.1),
with three shipped implementations: `claude` (§9.2), `codex` (§9.3) and
`cursor` (§9.7). This task adds a fourth, `copilot`, which runs the standalone
GitHub Copilot CLI (`copilot`, npm `@github/copilot`) headlessly as a workflow
step and as a chat on Linux, macOS and Windows. It follows the rule the other
three follow: every capability the CLI really has is wired and pinned to
fixtures captured from a named build, and every capability it lacks is
*stated* in §9.9 and refused, never emulated.

Non-goals, from #852: the retired `gh copilot` extension, Copilot in IDEs, the
Copilot coding agent in GitHub Actions; an ACP (`copilot --acp`) integration
(recorded in §9.9 as a considered alternative only); any new cross-adapter
feature — premium requests are **not** mapped onto `CostUSD` and no
`RunResult` field is added for them; changing claude, codex or cursor except
where a shared helper is generalized; vincent performing or storing Copilot
authentication; billing or quota management beyond the existing quota surface.

## Decisions

Copied from #852 as settled on 2026-10-28; binding until superseded here with
the new reasoning.

| # | Question | Decision | Why (and the alternative it beat) |
|---|---|---|---|
| 1 | Integration surface | `copilot --output-format json` subprocess per run, not ACP | Same shape as all three adapters; ACP is a long-lived RPC session no adapter speaks and has open parity gaps (github/copilot-cli #5269, #5198, #5047) |
| 2 | Prompt carrier | **stdin**, never `-p` | `RunSpec` requires it (Windows argv limit); verified working; avoids cmd.exe quoting through an npm `.cmd` shim and fakeagent's argv-walk hazards |
| 3 | Session id | the adapter generates a UUID and passes `--session-id` on new runs; `--resume=<id>` on resume | A killed run may never write `result`; an unknown `--resume` id is refused with a capturable error, an unknown `--session-id` UUID is silently adopted (both captured, below) |
| 4 | Full-auto | `--allow-all-tools --allow-all-paths --allow-all-urls`, never `COPILOT_ALLOW_ALL=true` | The env spelling also trusts the worktree and loads its hooks and MCP servers (`help_environment_1.0.105.txt`) |
| 5 | Version pin | `--no-auto-update` on every spawn, probe included | Otherwise the launcher can run a newer cached build than detection reported |
| 6 | MCP carriage | inline JSON in `--additional-mcp-config` | No worktree file, no new recovery sweep; the token is covered by the existing `redactMCPToken`; an `@file` fallback only if Windows quoting breaks it |
| 7 | MCP before registration | 138.8 (`mcp`) lands before 138.9 (registration and gate) | An interim `ErrMCPUnsupported` would fail every copilot step by default (`mcp.wire_steps` defaults true) |
| 8 | Restricted | the `--available-tools` read-only set plus git, CLI-enforced; refused per GOOS where unproven | §9.4 promises the filesystem and shell are bounded; never a weaker "deny a few tools" set |
| 9 | Models | no enumeration (codex-style); ids free-form, `auto` the default | `help config` lists models the account cannot use — every explicit `--model` tried at capture was refused, below |
| 10 | Login | `logged_in: null` by design | No status command; reading `~/.copilot` is ruled out by T1.7; the invalid-model probe is a text-matching hack |
| 11 | Skill install | no `~/.copilot/skills` link row by default | copilot already loads `~/.agents/skills` (the store) with `source:"personal-agents"`; a link would duplicate what it reads |
| 12 | Registries | one shared constructor for daemon, doctor and `workflow validate` | Makes "fourth adapter ⇒ one registry edit" true; config stays typed (the untyped alternative crosses the `config`-is-a-leaf rule) |
| 13 | Gate | a new `scripts/138-gate.sh` plus m14 loop entries, not a generalized m5 | m5 asserts cursor facts; recent gates are task-numbered |
| 14 | Milestone | task only, no §19 milestone row | Recent features are tasks; the author may override |

Decisions taken by 138.1 (2026-10-28):

15. **Pinned build: 1.0.105**, the npm `latest` dist-tag at capture time
    (`prerelease` was `1.0.105-1`, `unstable` older). Every capture ran that
    exact package — `npm install @github/copilot@1.0.105` into a throwaway
    prefix, invoking its `@github/copilot-darwin-arm64` native binary
    directly — with `--no-auto-update` and `COPILOT_AUTO_UPDATE=false`, after a
    check that `--version` printed `GitHub Copilot CLI 1.0.105.`. The version in
    each file name is that string minus its trailing period.
16. **Refresh rule** (the issue's open question, closed): fixtures are
    re-captured only when the adapter's `testedVersions` is bumped, by whoever
    bumps it, with the same discipline — pinned package, `--no-auto-update`,
    stdin prompt, throwaway repo, isolated `COPILOT_HOME`, scrub and grep — and
    a new provenance row per file. Fixtures of the old build stay beside the new
    ones, as claude's and cursor's do, so a parser change is proven against
    both. The alternative, a scheduled refresh, would chase near-daily releases
    and spend premium requests for no verdict change.
17. **The event schema is not vendored.** It is GitHub's package content. Its
    location and type count are recorded below, with the drift it shows.

## Version findings

**The `--help` format switched at 1.0.84.** Bisected over the npm versions
between 1.0.61 and 1.0.105, running only `--help` (no auth, no spend) with an
empty `COPILOT_HOME` and `--no-auto-update`:

| Build | Help style |
|---|---|
| 1.0.61, 1.0.83 | commander: `Usage: copilot [options] [command]`, `(choices: …)` |
| 1.0.84, 1.0.85, 1.0.88, 1.0.91, 1.0.94, 1.0.100, 1.0.105 | clap: `Usage: copilot [OPTIONS] [COMMAND]`, `[possible values: …]` |

The same release dropped the `--effort` alias (1.0.83 lists
`--effort, --reasoning-effort <level>`; 1.0.84 only `--reasoning-effort`) and
added `--auto-tier`, `--dynamic-retrieval` and `--fleet`. Between 1.0.84 and
1.0.105 the flags vincent needs — `--output-format`, `--session-id`,
`--resume`, `--available-tools`, `--additional-mcp-config`,
`--usage-output-file`, `--no-auto-update`, `--reasoning-effort` — are all
present and unchanged in spelling; 1.0.105 adds `--trust-folder`,
`--sync-session`, `--mcp-github-auth`, `--disable-client-telemetry` and the
`--host-*` trio, and drops `--remote`/`--remote-export` and their `--no-`
forms.

**Proposed minimum supported version: 1.0.84.** Below it, a parser written for
1.0.105's clap help cannot read the help at all, and the `--effort` spelling
differs, so `Detect` cannot establish what the build accepts — `incompatible`
with that reason. From 1.0.84 every flag the adapter passes exists. The JSONL
stream between 1.0.84 and 1.0.104 was **not** captured, so those builds are
`untested` (advisory), not `tested`; 1.0.105 is the one `tested` build. The
alternative, a minimum of 1.0.105, would mark builds incompatible on no
evidence beyond release age, and the stream's tolerance of unknown events
(#852 risks) is what covers the drift between them. Input to 138.5's version
verdict and 138.6.

## Fixtures (138.1)

All under `internal/agent/copilot/testdata/`, captured 2026-10-28 on
**darwin/arm64** from **1.0.105**. Windows and Linux captures are 138.3.

Common to every run unless the row says otherwise:

- `COPILOT_HOME=<isolated>` — a fresh, initially empty directory, never
  `~/.copilot`; `COPILOT_AUTO_UPDATE=false`; `copilot --no-auto-update …`.
- The prompt piped on **stdin**; `-p` was never used.
- The working directory a throwaway git repository with one commit
  (`notes.txt` = `hello`, `README.md`), shown as `/tmp/copilot-fixture/repo`
  after scrubbing.
- `J` below stands for `--output-format json --allow-all-tools
  --allow-all-paths --allow-all-urls`. `COPILOT_ALLOW_ALL` was never set.
- Authentication: the isolated `COPILOT_HOME` held nothing. copilot found the
  operator's stored login outside it without being given a token in the
  environment; only the logged-out capture removed it (row below).
  `copilot version` (a network request) was never run.

| File | Command line (after the common prefix) | Prompt (stdin) | Exit |
|---|---|---|---|
| `help_1.0.105.txt` | `--help` | — | 0 |
| `help_config_1.0.105.txt` | `help config` | — | 0 |
| `help_environment_1.0.105.txt` | `help environment` | — | 0 |
| `version_1.0.105.txt` | `--version` | — | 0 |
| `simple_1.0.105.jsonl` | `J` | `Reply with exactly the word: pong` | 0 |
| `shell_1.0.105.jsonl` | `J --usage-output-file <out>/usage.json` | ``Run the shell command `git log --oneline` and tell me how many commits it printed.`` | 0 |
| `usage_1.0.105.json` | the `--usage-output-file` written by the `shell` run | — | — |
| `edit_1.0.105.jsonl` | `J` | `In notes.txt, change the word hello to goodbye. Do nothing else.` | 0 |
| `session_id_1.0.105.jsonl` | `J --session-id <uuid S>` (a fresh UUID) | `Remember this codeword for later: tangerine. Reply only with OK.` | 0 |
| `resume_1.0.105.jsonl` | `J --resume=<uuid S>`, same repo and `COPILOT_HOME` | `What was the codeword I gave you? Reply with only the codeword.` | 0 |
| `session_id_unknown_1.0.105.jsonl` | `J --session-id <uuid U>` (never used before) | `Reply with exactly the word: pong` | 0 |
| `restricted_1.0.105.jsonl` | `--output-format json --available-tools=view,grep,glob` | ``Run the shell command `ls -la` and then read notes.txt. Report what you could and could not do.`` | 0 |
| `denied_1.0.105.jsonl` | `J --deny-tool=shell` | ``Run the shell command `git status` and report its output.`` | 0 |
| `mcp_1.0.105.jsonl` | `J --additional-mcp-config '{"mcpServers":{"vincent":{"type":"http","url":"http://127.0.0.1:<port>/mcp","headers":{"Authorization":"Bearer <elided>"},"tools":["*"]}}}'` against a live vincent daemon | `Call the vincent MCP server health tool and report what it returned.` | 0 |
| `sigterm_1.0.105.jsonl` | `J`, SIGTERM to the process 12 s after start | ``Write a 2000-word essay about the history of version control into essay.md, one section at a time, running `sleep 5` in the shell between sections.`` | 0 |
| `reasoning_1.0.105.jsonl` | `J` (model `auto`; see unreachable runs) | `This is a hard reasoning problem; think it through carefully before answering. Prove that there is no largest prime number, then compute the 10th prime that is congruent to 3 mod 4. Do not use any tools.` | 0 |
| `subagent_1.0.105.jsonl` | `J` | `Use the task tool to delegate to an explore subagent the question: which file in this repository contains the word hello? Then report its answer.` | 0 |
| `skill_1.0.105.jsonl` | `J`, with `<COPILOT_HOME>/skills/fixture-greeting/SKILL.md` (replies `FIXTURE GREETING OK`) | `Give me a fixture greeting using the fixture-greeting skill.` | 0 |
| `logged_out_1.0.105.txt` (stderr) | `env -i PATH=/usr/bin:/bin HOME=<empty dir> COPILOT_HOME=<empty dir> COPILOT_AUTO_UPDATE=false TERM=dumb copilot --no-auto-update J` — no `gh` on `PATH`, no token env | `Reply with exactly: pong` | 1 |
| `unavailable_model_1.0.105.txt` (stderr), `.jsonl` (stdout) | `J --model gpt-5-mini` | `Reply with exactly the word: pong` | 1 |
| `unsupported_effort_1.0.105.txt` (stderr), `.jsonl` (stdout) | `J --model auto --reasoning-effort low` | `Reply with exactly the word: pong` | 1 |
| `resume_unknown_1.0.105.txt` (stderr) | `J --resume=<uuid never used>` | `What did I ask you before?` | 1 |

**Scrubbing.** The throwaway repo, `COPILOT_HOME` and output directories are
replaced by `/tmp/copilot-fixture/{repo,home,out}`. Session ids, event ids and
tool-call ids are random per run and identify no account, so they are kept
verbatim — which also keeps `session_id`/`resume` correlated on the same id.
After scrubbing, the whole tree was grepped for the operator's username, home
path, hostname, `gho_`/`ghp_`/`ghu_`/`github_pat_` and the vincent MCP token,
with zero hits; every `.jsonl` parses line by line under `jq -c .`.

**Observations the later items build on.**

- `result` is the last line of every completed run: `{type, timestamp,
  sessionId, exitCode, usage:{premiumRequests, totalApiDurationMs,
  sessionDurationMs, codeChanges:{linesAdded, linesRemoved, filesModified}}}`.
  It carries no `data` and no `id`.
- `--session-id` with an unknown UUID is adopted silently: `result.sessionId`
  is the UUID passed. `--resume=<unknown>` exits 1 before any JSONL with
  `Error: No session, task, or name matched '<id>'.` and a remediation block.
- The model and effort refusals print two or three JSONL lines (MCP startup)
  on stdout *before* the stderr error and exit 1.
- SIGTERM on 1.0.105 exits **0** and ends with `agent.interrupted`, `abort`,
  `assistant.turn_end`, `assistant.idle` and `result` (`exitCode: 0`). The
  `session.info {infoType:"cancellation"}` seen during the 1.0.61 research does
  **not** appear on this build.
- `--deny-tool=shell` yields `tool.execution_complete` with `success: false`
  and `error: {code: "denied", message: "Permission to run this tool was denied
  due to the following rules: `shell`"}`, exit 0. Without the allow flags a
  read-only `git status` ran unprompted — so "denied" needs an explicit rule to
  reproduce.
- `--available-tools=view,grep,glob` emits `session.info
  {infoType:"configuration"}` listing every disabled tool; the run had no shell
  and said so.
- The edit tool's `tool.execution_complete.data.result.detailedContent` holds a
  unified `diff --git` patch; the `view` before it holds an empty one.
- The built-in `github-mcp-server` loads in every run (two
  `session.mcp_server_status_changed`), including the restricted one, where
  its tools are listed as disabled. The vincent MCP tool appears as
  `vincent-health` in `tool.execution_start.data.toolName`.
- The shell tool is named `bash` on macOS; its Windows and Linux names are
  138.3's to capture.
- The task tool's first call in `subagent` failed validation (`"prompt":
  Required`) and was retried; the `subagent.*` events carry the explore agent's
  name, model and tool list.
- `skill` shows a `skill` tool call that loads the skill, then the reply.

### Store captures (138.12, 2026-10-29)

Three more runs of the same pinned 1.0.105 on **darwin/arm64**, same discipline
(npm package in a throwaway prefix, its native binary invoked directly,
`--no-auto-update`, `COPILOT_AUTO_UPDATE=false`, stdin prompt, throwaway repo,
same scrub and grep, zero hits). What differs: `HOME` is a throwaway directory
too, holding one fixture skill in the store,
`<HOME>/.agents/skills/fixture-greeting/SKILL.md` (replies `FIXTURE GREETING
OK`). Because that hides the operator's `gh` login, the operator's token was
passed as `GH_TOKEN` in the child's environment only and appears in no file.
Prompt `Give me a fixture greeting using the fixture-greeting skill.`, command
line `J`, every run exit 0.

| File | `COPILOT_HOME` | `~/.copilot/skills/fixture-greeting` | `skill` tool telemetry |
|---|---|---|---|
| `skill_store_1.0.105.jsonl` | unset (so `<HOME>/.copilot`) | — | `found: "true"`, `skillSource: "personal-agents"` |
| `skill_store_linked_1.0.105.jsonl` | unset | symlink to the store copy | `found: "true"`, `skillSource: "personal-copilot"` |
| `skill_store_relocated_1.0.105.jsonl` | a fresh directory elsewhere | — | `found: "false"` |

**`session.skills_loaded` is not emitted** in `--output-format json` mode on
this build — not in these runs and not in 138.1's `skill` fixture — so the
evidence is the `skill` tool's `toolTelemetry.properties` on
`tool.execution_complete`. Two unrecorded runs of a listing prompt (`Without
calling any tool, list the name and source location of every skill available
to you, …, including any duplicates.`) settled duplication: store plus link
listed `fixture-greeting` **once**, and so did a relocated `COPILOT_HOME`
holding the link. copilot **dedupes**, and the copilot directory wins the
source. A fourth unrecorded run set `COPILOT_HOME=<x>/.copilot` with the store
copy at `<x>/.agents/skills` and an empty `HOME`: also `found: "false"`, so the
store is not resolved from `COPILOT_HOME`'s parent either. On 1.0.105,
relocating `COPILOT_HOME` drops the store entirely — which is also why 138.1's
isolated-`COPILOT_HOME` discipline could never have observed
`personal-agents`. §9.8 records it; detection does not follow `COPILOT_HOME`
(decision 2 of #864), and no link is added to work around it.

**Unreachable or not produced.**

- **Explicit `--model`.** Every explicit model tried — `gpt-5-mini`,
  `claude-sonnet-5.5`, `gpt-5.5`, `gpt-6-luna` — was refused "not available",
  including `gpt-6-luna`, which `auto` itself routed the shell run to. So no run
  with an explicit model and `--reasoning-effort` exists; the reasoning-heavy
  run is under `auto` (which refuses any effort), and it shows
  `assistant.reasoning` / `assistant.reasoning_delta` all the same.
- **Rate limit and quota.** No throttled account was available, so neither was
  captured. The texts are known only from issue reports and are **unverified**:
  `Sorry, you've hit a rate limit …` (github/copilot-cli #2696, #2336) and
  `402 … "quota_exceeded"` (#3431). Whether `--output-format json` emits a
  `session.error` for either (the schema's `session.error` has `errorType`,
  `statusCode` and `eligibleForAutoSwitch`) was not observed.
- Nothing was captured on Windows or Linux (138.3).

### `--model` side-effect captures (138.6, 2026-10-29)

Two runs of the pinned 1.0.105 on **darwin/arm64** with 138.12's install
discipline (npm package in a throwaway prefix, its native binary invoked
directly) and 138.1's run discipline (a fresh, initially empty `COPILOT_HOME`,
`--no-auto-update`, `COPILOT_AUTO_UPDATE=false`, stdin prompt `Reply with
exactly the word: pong`, throwaway repo, the operator's own login found
outside `COPILOT_HOME`). `COPILOT_HOME`'s file list and its `config.json` /
`settings.json` were snapshotted before and after each run; the operator's
`~/.copilot` was never touched. Same scrub and grep, zero hits.

| File | Command line (after the common prefix) | Exit |
|---|---|---|
| `model_persist_1.0.105.txt`, run 1 | `J --model gpt-5-mini` (refused, no request spent) | 1 |
| `model_persist_1.0.105.txt`, run 2 | `J --model auto` (accepted, `premiumRequests: 1`) | 0 |

Neither run wrote a model anywhere outside its own session: `config.json`
holds only the `firstLaunchAt` the first run of any kind writes, no
`settings.json` was created, and the rest is logs, session state and the
session store. §9.9 records it.

### Invocation and mention captures (138.13, 2026-10-29)

Three more runs of the pinned 1.0.105 on **darwin/arm64**, with 138.12's
install discipline (npm package in a throwaway prefix, its native binary
invoked directly) and 138.1's run discipline (`--no-auto-update`,
`COPILOT_AUTO_UPDATE=false`, stdin prompt, command line `J`). `HOME` and
`COPILOT_HOME` were both throwaway directories, the latter holding
`<COPILOT_HOME>/skills/fixture-greeting/SKILL.md` (replies `FIXTURE GREETING
OK`); the operator's token was passed as `GH_TOKEN` in the child's environment
only. The throwaway repo's `README.md` begins `Zebra marmalade lighthouse
4417`. No run wrote a local path to stdout, so nothing needed scrubbing; the
same grep found zero hits and every `.jsonl` parses under `jq -c .`. One
premium request each.

A fourth run, `mention_tools_1.0.105.jsonl`, was added after review (PR #878,
F1) to observe the tools-allowed fallback rather than assume it: the same
discipline, an empty `COPILOT_HOME`, and the same repo. It is the one run here
that wrote a local path — the `view` call's argument and its diff — which was
scrubbed to `/tmp/copilot-fixture/repo`, the 138.1 spelling; the grep then
found zero hits.

| File | Prompt (stdin) | Exit |
|---|---|---|
| `skill_invoke_leading_1.0.105.jsonl` | `/fixture-greeting` | 0 |
| `skill_invoke_mid_1.0.105.jsonl` | `Please run /fixture-greeting now.` | 0 |
| `mention_1.0.105.jsonl` | `What is the first line of @README.md? Do not use any tools.` | 0 |
| `mention_tools_1.0.105.jsonl` | `What is the first line of @README.md?` | 0 |

**Observations.**

- `/name` on stdin is **not** recognized by the CLI, leading or mid-message.
  `user.message.data.transformedContent` is the prompt as typed behind the
  `<current_datetime>` preamble, with no skill content. The skill ran in both
  only because the model called its `skill` tool
  (`arguments: {skill: "fixture-greeting"}`, telemetry `skillSource:
  "personal-copilot"`, `found: "true"`) — the same model-chosen call
  `skill_1.0.105.jsonl` shows for a prompt that names the skill in prose. So
  copilot is not a `SkillInvoker` (decision B), and §9.9 says why.
- No `skill.invoked` and no `session.skills_loaded` on stdout, so nothing maps
  to `EventSkill` (decision E) and copilot is not a `SkillLister` (decision D).
- `@README.md` is **not** expanded: `transformedContent` carries the mention
  as typed and none of the file, no tool ran, and the answer was "I can't
  determine the first line of @README.md without reading the file". So
  copilot is a `FileMentioner` with `Expands: false` (decision C), `@`
  anywhere, never quoted.
- With tools allowed (`mention_tools_1.0.105.jsonl`), `transformedContent`
  again carries `@README.md` as typed and none of the file; the model made
  one `view` call on `/tmp/copilot-fixture/repo/README.md` with `view_range
  [1, 1]` and answered `Zebra marmalade lighthouse 4417`. That is §5.5's
  fallback on copilot, observed: an unexpanded mention is still worth
  sending (task 126 decision 6). `TestMentionFallbackReadsThePath` pins it.
- No new event types: all four carry only types the inventory below lists.

## Event inventory

Every distinct `type` across the `.jsonl` fixtures, with its total count and
the fixtures that show it. Input to 138.4 (the fakeagent dialect) and 138.7
(normalization).

| Type | Count | Fixtures |
|---|---|---|
| `abort` | 1 | sigterm |
| `agent.interrupted` | 1 | sigterm |
| `assistant.idle` | 13 | every completed run |
| `assistant.message` | 33 | every completed run |
| `assistant.message_delta` | 286 | every completed run but sigterm |
| `assistant.message_start` | 13 | every completed run but sigterm |
| `assistant.reasoning` | 6 | denied, reasoning, restricted, resume, subagent |
| `assistant.reasoning_delta` | 417 | denied, reasoning, restricted, subagent |
| `assistant.tool_call_delta` | 873 | denied, edit, mcp, restricted, shell, sigterm, skill, subagent |
| `assistant.turn_end` | 33 | every completed run |
| `assistant.turn_start` | 33 | every completed run |
| `model.call_final_result` | 33 | every completed run |
| `model.call_finished` | 33 | every completed run |
| `model.call_start` | 33 | every completed run |
| `prompt_cache_break` | 1 | edit |
| `result` | 13 | every completed run |
| `session.auto_mode_resolved` | 13 | every completed run |
| `session.background_tasks_changed` | 66 | denied, shell, sigterm, subagent |
| `session.indexed_search` | 4 | shell, simple |
| `session.info` | 1 | restricted |
| `session.mcp_server_status_changed` | 32 | every run, the two refusals' stdout included |
| `session.mcp_server_tools_loaded` | 16 | every run, the two refusals' stdout included |
| `session.mcp_servers_loaded` | 13 | every completed run |
| `session.tools_updated` | 13 | every completed run |
| `session.usage_checkpoint` | 13 | every completed run |
| `subagent.completed` | 1 | subagent |
| `subagent.configured` | 1 | subagent |
| `subagent.selected` | 1 | subagent |
| `subagent.started` | 1 | subagent |
| `tool.execution_complete` | 19 | denied, edit, mcp, restricted, shell, sigterm, skill, subagent |
| `tool.execution_partial_result` | 2 | shell |
| `tool.execution_start` | 20 | denied, edit, mcp, restricted, shell, sigterm, skill, subagent |
| `tool.shell_output` | 1 | shell |
| `user.message` | 14 | every completed run (subagent has two: the prompt and the subagent's) |

"Every completed run" is the thirteen exit-0 `.jsonl` fixtures: denied, edit,
mcp, reasoning, restricted, resume, session_id, session_id_unknown, shell,
sigterm, simple, skill, subagent.

**Against the package's schema.** 1.0.105 ships
`schemas/session-events.schema.json` in its package (here
`~/Library/Caches/copilot/pkg/darwin-arm64/1.0.105/schemas/`), whose
`SessionEvent` is an `anyOf` of **164** event types. All 33 captured session
event types are in it. **Drift:** `result`, the terminal line, is the one
captured type the schema does not define — it is written by the `-p` JSON
output, not the session, so its shape is pinned by the fixtures alone.

## Risks

From #852:

- **Windows pipe-stdout bug** (github/copilot-cli #3188): if it still breaks
  at the pinned build, real Windows use is blocked; 138.3 decides.
- **Schema drift and release churn.** Exact-string version verdicts mean most
  users see `untested`. Fixtures are pinned to one build and parsers must pass
  unknown events through as `unknown`.
- **User-global MCP servers.** copilot augments `~/.copilot/mcp-config.json`
  with no strict switch, so a vincent step may load servers nobody in the repo
  chose — and the built-in `github-mcp-server` loads in every run (above).
  Stated in §9.9.
- **Real-run cost.** Every real-CLI leg spends the operator's premium
  requests.
- **Token in the environment.** `GH_TOKEN`/`GITHUB_TOKEN` in the daemon's
  environment silently authenticate copilot.

## Tasks

In #852's delivery order. Each item amends the spec bullets and public pages
its code makes true, in its own pull request.

- [x] **138.1** ([#853](https://github.com/lezli01/vincent/issues/853)) Pinned real-CLI captures of 1.0.105 on macOS, the
  clap-help bisection, this document and its index row, and the §9.9 stub.
  ✓ 2026-10-28
- [x] **138.2** ([#854](https://github.com/lezli01/vincent/issues/854)) One shared adapter-registry constructor for the daemon,
  doctor and `workflow validate` (PR #871). ✓ 2026-10-28
- [ ] **138.3** ([#855](https://github.com/lezli01/vincent/issues/855)) Windows and Linux runs of the pinned build: pipe stdout,
  the shell tool's name, restriction. Depends: 138.1.
- [x] **138.4** ([#856](https://github.com/lezli01/vincent/issues/856)) A Copilot dialect in `cmd/fakeagent`, selected by
  `--output-format json`. Depends: 138.1. ✓ 2026-10-29
- [x] **138.5** ([#857](https://github.com/lezli01/vincent/issues/857)) `internal/agent/copilot` running a full-auto step over
  stdin JSONL, unregistered. Depends: 138.1, 138.4. Usage normalization
  (#857's "usage" bullet) moved to 138.7: on 1.0.105 the token counts live
  only on `session.usage_checkpoint` and `assistant.turn_end`, so 138.5 emits
  no usage event and leaves tokens and cost unreported. ✓ 2026-10-29
- [x] **138.6** ([#858](https://github.com/lezli01/vincent/issues/858)) The effort catalog, the stated model and login gaps,
  failure classification. Depends: 138.5. Efforts are probed from clap
  `--help` over a curated floor; models stay free-form; `logged_in` stays
  `null` (open question 3); `unauthenticated` is classified, quota and rate
  limit are not. ✓ 2026-10-29
- [x] **138.7** ([#859](https://github.com/lezli01/vincent/issues/859)) Run header, thinking, patches, subagents and usage
  normalized — usage taken over from 138.5, `RunResult` tokens included; the
  absent events pinned against every fixture. Depends: 138.5. There is no
  run header: 1.0.105's stdout carries no cwd or tool list, and the spawn
  spec is not the CLI's report. Tokens come from the root
  `session.usage_checkpoint`, never `--usage-output-file`, and are session
  totals; durations from `result.usage`; `NumTurns` stays 0. Shell output
  and edit patches ride on their `tool_result`; a subagent's lines carry its
  spawning call as `ParentCallID`. ✓ 2026-10-29
- [x] **138.8** ([#860](https://github.com/lezli01/vincent/issues/860)) The per-step vincent MCP server through
  `--additional-mcp-config`. Depends: 138.5. Decision 6 settled on the
  inline carrier alone. `TestStartMCPCallbackThroughCmdShim` launches the
  fake through a `copilot.cmd` shim in a spaced directory on the Windows leg.
  Its first run failed (review F1): with more than two `"` on the line,
  `cmd /c` drops the last one, which split the shim's path. `@file` would
  have failed the same way on a spaced temp path. So the fix went into the
  host spawn instead, which wraps a batch file's line in one outer pair of
  quotes (`procx.BatchCmdLine`, §9.1). No `@file` fallback, temp directory
  or sweep was built. `--allow-tool=vincent` rides beside it for 138.10. ✓ 2026-10-29
- [~] **138.9** ([#861](https://github.com/lezli01/vincent/issues/861)) Registration in the daemon, config, TUI and CLI, the
  container mount, the engine test and `scripts/138-gate.sh` in CI.
  Depends: 138.5, 138.8. Everything is delivered except the real-CLI walk:
  `builtin.New` registers copilot fourth, `agents.copilot.path` reaches
  every config surface, `~/.copilot` is mounted, and the gate (scenarios 1,
  2, 3 and 5; 4 is 138.10's) runs in `gate-group` group 3 on all three OSes.
  `VINCENT_GATE_AGENT=copilot` against a real, authenticated CLI is
  human-only and recorded in `docs/gates/138-copilot-gate.md`, still empty.
- [ ] **138.10** ([#862](https://github.com/lezli01/vincent/issues/862)) §9.4 restricted mode over `--available-tools`, or a
  per-OS refusal; gate scenario 4. Depends: 138.9, 138.3. Hand-off from
  138.11: `SupportsRestrictedResume()` returns false, so a linked chat on a
  restricted task is refused at open. Flip it to true here only if a
  capture proves `--available-tools` holds on a `--resume=` run.
- [x] **138.11** ([#863](https://github.com/lezli01/vincent/issues/863)) `Resumer`, session-lost classification, m14 leg 2b.
  Depends: 138.9. A resume is `--resume=<id>` with no `--session-id`
  (`resume_1.0.105.jsonl`), and an unknown id is `session_lost`
  (`resume_unknown_1.0.105.txt`). Decision 1: a resumed chat turn records
  its own spend. Copilot reports session totals (`agent.CumulativeUsage`),
  so chatrun stores the raw totals on `chat_turns` (migration 0049) and
  records totals minus the same session's previous turn's. A new session or
  a shrunk total takes the raw figures. Decision 2:
  `SupportsRestrictedResume()` is false until 138.10. m14 leg 2b walks a
  two-turn copilot chat. ✓ 2026-10-29
- [x] **138.12** ([#864](https://github.com/lezli01/vincent/issues/864)) Published-skill reporting for copilot: a store-read
  `Link` shown as `copilot (reads store)`, a slugless copilot row in the
  install table, `skill.PlanInstall` for `--agent copilot`, and the store
  captures above. ✓ 2026-10-29
- [x] **138.13** ([#865](https://github.com/lezli01/vincent/issues/865)) `SkillInvoker`, `FileMentioner`, m14 legs 12 and 13.
  Depends: 138.11. The captures above decided both: `/name` on stdin is
  prose, so copilot is neither a `SkillInvoker` nor a `SkillLister` and
  produces no `agent.skill`; it is a `FileMentioner` with `@`, `anywhere`,
  `Expands: false`. m14 leg 12f pins the two `unsupported` verdicts and a
  verbatim `/gate-skill`; leg 13f the mention payload on copilot's stdin. ✓ 2026-10-29
- [ ] **138.14** ([#866](https://github.com/lezli01/vincent/issues/866)) The `skills/vincent-workflows` runtime text and the
  built-in re-read. Depends: 138.10, 138.6.
- [ ] **138.15** ([#867](https://github.com/lezli01/vincent/issues/867)) Complete §9.9 and amend the three-adapter prose.
  Depends: the capability items.
- [ ] **138.16** ([#868](https://github.com/lezli01/vincent/issues/868)) README, guides, references and SEO data. Depends: the
  capability items.
- [ ] **138.17** ([#869](https://github.com/lezli01/vincent/issues/869)) `docs/gates/138-copilot-gate.md` real runs on three OSes,
  the TUI screenshots, the task marked done. Depends: 138.16, 138.15, 138.14.

## Open questions

Carried from #852; each is decided in the item named.

1. **Windows if #3188 persists** — a stated refusal, waiting for upstream, or a
   file-redirect workaround. 138.3.
2. **Minimum version** — closed by 138.5 (2026-10-29): exact strings only,
   tested `{1.0.105}`, incompatible empty, so every other build judges
   `untested` (task 041's design rules out ranges). 1.0.84 is recorded in §9.9
   as the *documented* minimum and is not enforced in code.
3. **Env-token login inference** — closed by 138.6 (2026-10-29): no, stay
   `null`. A token in `COPILOT_GITHUB_TOKEN`, `GH_TOKEN` or `GITHUB_TOKEN` may
   be a classic `ghp_` PAT, which Copilot rejects, so its presence proves
   nothing.
4. **Folder trust** — `--trust-folder` cannot be combined with `--session-id`
   or `--resume`, so a project's `.mcp.json`, `.github/mcp.json` and repo hooks
   never load in vincent runs. Recommendation: accept as stated behaviour.
   138.15.
5. **Release bar** — recommendation: all three real legs. 138.17.
6. **ACP follow-up** — an investigation into `copilot --acp` after this ships.
7. **Hoisting `tailWriter`/`readLoop`** into `internal/agent` as a separate
   refactor afterwards.
8. **Codex skills drift** (not copilot) — vercel-labs/skills#2067 moved codex's
   global skills to `~/.agents/skills`; to be filed as its own bug, never fixed
   in passing by 138.12.
