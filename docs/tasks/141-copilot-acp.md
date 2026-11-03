# 141 — `copilot --acp`: mid-run input and skill listing, investigated

**Status:** ✅ done (6/6) · **Opened:** 2026-11-03

Issue [#889](https://github.com/lezli01/vincent/issues/889). Task 138 shipped
the copilot adapter on the `--output-format json` dialect and left two
capabilities refused in spec §9.9: mid-run input (`InputSupport: never`,
`Respond` always errors) and skill listing (`list_verdict: unsupported`,
because `session.skills_loaded` never reaches stdout in JSON mode, 138.12).
Its open question 6 asked whether `copilot --acp` — the Agent Client Protocol
server, JSON-RPC over stdio — could supply either. This document answers that
question against captures from one named build. It is an investigation: no
adapter code, test, gate or public doc changes here. A **yes** would have
produced a separate build issue; neither verdict below is a yes, so none was
filed.

Conventions for this file are in [the tasks README](README.md).

## Decisions (2026-11-03)

1. **#514 is a template, not a precedent.** Issue #514 (whether ACP should
   become cursor's skill listing) was closed on 2026-10-02 as "deprecated,
   the project is taking a different direction". No decision was recorded,
   and task 124.18 is still `[ ]`. This work borrows #514's research shape —
   real captures, the build pinned, and a check of whether ACP's answer
   agrees with what the run dialect actually loads, which is the check task
   124 decision 2 rejected cursor's `available_commands_update` on. It
   inherits no outcome, and it does not touch 124.18. *Beaten:* treating #514
   as having answered the question for cursor, which it never did.
2. **"Per-run contract" means one subprocess per run, not one dialect.** Task
   138 decision 1 requires one subprocess per run. A run may spawn
   `copilot --acp`, `initialize`, open one session (`session/new`, or
   `session/load` to resume), `session/prompt` it, and exit when the run ends;
   that still meets decision 1. A long-lived session shared across runs or
   chat turns is out of scope, and so is relaxing decision 1. Listing may be
   answered by a short-lived probe subprocess, the way claude's is.
   *Beaten:* reading decision 1 as "the JSON dialect only", which would have
   answered both questions no without a capture.
3. **Record: this task, pinned to the npm `latest` build at capture time.**
   On 2026-11-03 `npm view @github/copilot dist-tags` gave `latest: 1.0.105`
   (and `prerelease: 1.0.106-6`), so every capture is 1.0.105. Captures live
   in [`141/`](141/), named for the build, never as adapter `testdata/`,
   because no adapter consumes them. Task 138's open question 6 is amended in
   place to point here; task 138 stays closed. *Beaten:* capturing a
   prerelease to get the #5269 and #5133 fixes — the adapter pins the npm
   `latest` build (task 138 decision 15), so a prerelease's answer would not
   be one vincent could ship on.

## Method

macOS 27.2 arm64, `copilot` 1.0.105 from the Homebrew cask, logged in through
the keyring, real `$HOME` (task 138.12 showed a relocated `COPILOT_HOME` drops
the skill store, so isolation would have hidden global skills). A scratch git
repository held one project skill, `.github/skills/proj-probe/SKILL.md`; the
`$HOME` held four global ones (`grill-me`, `find-skills`, `vincent-triggers`,
`vincent-workflows`) and no installed plugin.

[`141/acp_probe.py`](141/acp_probe.py) is the client: it spawns
`copilot --acp --no-auto-update [flags]` with `COPILOT_ALLOW_ALL` stripped
from the environment, as the adapter's `childEnv` does; sends `initialize`
(protocol 1, no `fs` or `terminal` client capability); opens one session
(`session/new`, or `session/load` when `LOADSESSION` is set); then either
waits (listing) or sends one `session/prompt`; closes stdin and waits for
exit. It answers `session/request_permission` with `reject_once`, or
`allow_once` under `PERMREPLY=allow`, and any other agent-to-client request
with a JSON-RPC "method not found". Every line in and out is recorded with
its offset in seconds. JSON-dialect comparisons ran the adapter's own argv
shape (`--output-format json --no-auto-update --session-id <uuid>`, the
three `--allow-all-*` flags, prompt on stdin).

| Capture (`141/…_1.0.105.jsonl`) | Dialect | Flags | What it shows |
|---|---|---|---|
| `list_full` | ACP | none | `initialize`, `session/new`, 15 s idle: the command list |
| `acp_list_prompt` | ACP | none | a listing prompt; the run's own `<available_skills>` |
| `json_list` | JSON | full-auto | the same prompt on the JSON dialect |
| `perm_restricted_shell` | ACP | restricted | a shell command asked for under §9.9's tool set |
| `perm_restricted_outside` | ACP | restricted | a write outside the worktree, rejected |
| `perm_default_shell` | ACP | none | a shell command, permission rejected |
| `perm_default_shell_allow` | ACP | none | the same, permission allowed |
| `perm_fullauto_shell` | ACP | full-auto | the same, never asked |
| `ask_user` | ACP | none | a prompt that asks the model to use `ask_user` |
| `json_ask_user` | JSON | full-auto | the same on the JSON dialect |
| `resume_load` | ACP | full-auto | `session/load` of `perm_default_shell_allow`'s session, one more turn |

"Restricted" is §9.9's set exactly: `--available-tools=view,grep,glob,edit,create,apply_patch --allow-tool=read --allow-tool=write`.

## Findings

### Upstream gaps (state on 2026-11-03)

| Issue | State | In 1.0.105? | Bearing |
|---|---|---|---|
| github/copilot-cli#5269 — plugin skills absent from ACP sessions | closed 2026-11-03, completed | **no** — the maintainer's closing comment names **1.0.106-3** (prerelease, 2026-11-02) as the first build with the fix | a parity gap for both questions while `latest` is 1.0.105 |
| #5133 — `includeCoAuthoredBy: false` ignored in ACP | closed 2026-11-03, completed | **no** — first fixed in **1.0.106-5** (prerelease, 2026-11-03) per the closing comment; 1.0.106-3 still had it | a parity gap for moving runs |
| #5198 — mid-turn steering in `--acp` | open | — | **not §7.4.** Steering is the *user* injecting text into a live turn; §7.4 is *agent-initiated* `question`/`permission` requests. It is not counted as a blocker |
| #5047 — assisted approval in ACP | open | — | `--assisted-approval` is out of scope in §9.9 for the JSON dialect too, so it is no parity loss |

### Skill listing

- **`available_commands_update` carries skills, mixed with commands.**
  `list_full`: `initialize` answered in 2.2 s; the first
  `available_commands_update` arrived **0.16 s after `session/new` was
  sent**, before its result, and an identical second one with the result
  (3.6 s from spawn). It holds 37 entries: 32 built-in slash commands
  (`add-dir`, `model`, `skills`, …) and the five skills. Nothing types an
  entry as a skill; only `input.hint` tells them apart — exactly the five
  skills carry `"instructions for the skill"`. It needs a `session/new`, so
  a probe needs whatever the session needs (login, network); neither was
  isolated here, and this run was logged in.
- **Agreement with the JSON dialect: no.** `json_list`'s session
  `system.message` (`~/.copilot/session-state/<id>/events.jsonl`, the one
  record of what a JSON run loads, since `session.skills_loaded` is not on
  stdout) lists **seven** skills in `<available_skills>`: the five above plus
  `customize-cloud-agent` and `github-pr-media`, which ship with the CLI.
  The model's reply listed the same seven. ACP's list has five and omits
  both built-in skills. A list served from ACP would therefore miss skills
  the JSON run loads — the emulation task 124 decision 2 refused for cursor.
  The plugin-skill half of #5269 was not reproduced (no plugin installed);
  upstream records it as a further disagreement on this build.
- **Agreement with the ACP dialect: yes, on this capture.**
  `acp_list_prompt`'s own `system.message` lists the same five skills the
  command list did, and the model's reply named those five. So the list is
  faithful to an ACP run and only to an ACP run: it would become usable only
  if runs moved to ACP (below), and then still without plugin skills until a
  release carries #5269's fix.

### Mid-run input (§7.4)

- **`permission`: the mechanism exists and works mid-turn.** With no
  permission flags, a shell call raises `session/request_permission`
  (`perm_default_shell`): a JSON-RPC *request* naming the `toolCallId`,
  `kind: "execute"`, the exact command in `rawInput.commands`, and three
  options (`allow_once`, `allow_always`, `reject_once`). The turn waits for
  the reply; `reject_once` fails the tool call with `code: "rejected"` and
  the turn ends; `allow_once` (`perm_default_shell_allow`) runs it and the
  turn continues. It is machine-readable, so it would satisfy §7.4's bar,
  and the answer is written back while the turn is live.
- **Full-auto asks nothing.** `perm_fullauto_shell`, with vincent's three
  `--allow-all-*` flags, runs the shell with no request. Full-auto is the
  default (§16), so the default run would never produce `pending_input`.
- **Restricted removes the shell; nothing is requested for it.**
  `perm_restricted_shell`: `bash` is listed among "Disabled tools" and the
  model read `.git/HEAD` and `.git/logs/HEAD` with `view` instead. The
  restriction removes the tool; it does not turn it into a request.
- **Restricted does ask for paths outside the worktree.**
  `perm_restricted_outside`: a `create` of a file outside the worktree
  raised `session/request_permission` titled "Access paths outside trusted
  directories". On the JSON dialect the same refusal is a `denied` tool
  result (138.10, `restricted_write_1.0.105.jsonl`). This is the only
  request a restricted run was seen to raise.
- **`question`: none.** No ask-the-user tool reaches the model in either
  dialect on 1.0.105 (`ask_user`, `json_ask_user`): asked to use
  `ask_user`, the model replied in both that it has no such tool and asked
  in prose. No elicitation or other agent-to-client request arrived; the
  probe's "method not found" fallback never fired. Prose is never §7.4, so
  there is nothing to map to `question`.
- **§7.4's invariants.** One run is one session in one subprocess, so a
  `pending_input` is per task and the step keeps its slot while the CLI
  waits on the reply. Requests are id-keyed JSON-RPC, so two parallel tool
  calls could have two outstanding at once; the adapter would have to hold
  the second until the first is answered to keep one `pending_input`. No
  capture had two outstanding.
- **What `InputSupport` would become:** `detected`, version-gated as
  claude's is, with `permission` only — and only reachable from a restricted
  run's out-of-tree paths, or from a new permission mode vincent does not
  have today.

### Parity cost of moving runs to ACP

Each item is a §9.9 bullet the JSON dialect satisfies today.

| §9.9 today (JSON) | ACP on 1.0.105 | Gap |
|---|---|---|
| Event normalization from `assistant.*`/`tool.*` events | `session/update` with `agent_message_chunk`, `agent_thought_chunk`, `tool_call`, `tool_call_update`, `usage_update`, `config_option_update`, `session_info_update` | a rewrite, not a blocker; but CLI notices ("Info: Disabled tools: …", "Response was interrupted due to a server error. Retrying...") arrive **inside** `agent_message_chunk` text (`perm_restricted_shell`, `perm_fullauto_shell`), indistinguishable from the model's reply |
| Token usage checkpoint | `session/prompt`'s result carries `usage` (input, output, thought, cache read/write) | none |
| Resume by session id (`--resume=<id>`) | `loadSession: true`; `session/load` resumed (`resume_load`) | `session/load` **replays the whole history** as `session/update`s before the new turn, which a normalizer must drop |
| Restricted tool set (`--available-tools`) | the same flags are honoured by `copilot --acp` | none |
| `COPILOT_ALLOW_ALL` stripping | an environment matter; unchanged | none |
| MCP injection (§13.4) via `--additional-mcp-config`, HTTP | `mcpCapabilities: {http, sse}`; servers go in `session/new`'s `mcpServers` | none in principle — §13.4's server is already streamable HTTP with a bearer header. Not exercised here |
| SIGTERM behaviour | — | **not captured** |
| No cost reporting | no cost in ACP either; the JSON `result`'s `premiumRequests` has no ACP counterpart | none (premium requests are not mapped, task 138) |
| Plugin skills load | absent (#5269) | **blocker until a release carries 1.0.106-3's fix** |
| `includeCoAuthoredBy: false` honoured | ignored (#5133) | **blocker until a release carries 1.0.106-5's fix** |

A Windows or Linux capture was not made. The one place the answer could
differ is the `copilot.cmd` shim: ACP would move the MCP JSON out of argv
into `session/new`, which removes the `cmd /c` quoting hazard task 138
review F1 fixed rather than adding one. That is a reason it would not get
worse, not a capture.

## Verdicts

- **Skill listing: no.** `available_commands_update` disagrees with what
  the JSON dialect loads — it omits the CLI's built-in skills
  (`customize-cloud-agent`, `github-pr-media`) and, upstream, plugin skills —
  so serving it while runs stay on JSON is emulation (spec §9.1, task 124
  decision 2). It agrees with an ACP run, so it can only come with moving
  runs, which is not yet.
- **Mid-run input: not yet.** ACP has a working, machine-readable
  `permission` request (no `question`). Taking it means moving runs to ACP,
  and on 1.0.105 that move loses parity on plugin skills (github/copilot-cli
  #5269) and `includeCoAuthoredBy` (#5133). Both are fixed upstream in
  prereleases only; the answer changes when a `latest` release carries
  1.0.106-5's fixes, at which point the remaining work — re-capturing on
  that build, SIGTERM behaviour, separating CLI notices from reply text, and
  the replay on `session/load` — is a build issue's. Even then the request
  is reachable only from a restricted run's out-of-tree paths, which bounds
  what the move buys.

## Items

- [x] **141.1** Re-check github/copilot-cli #5269, #5198, #5047 and #5133 on
  the capture date and record which build carries each fix. ✓ 2026-11-03
- [x] **141.2** Capture ACP's skill list and check it against what the JSON
  and the ACP dialects load. ✓ 2026-11-03
- [x] **141.3** Capture §7.4 permission and question behaviour in
  full-auto, default and restricted modes. ✓ 2026-11-03
- [x] **141.4** List the parity cost of moving runs to ACP against §9.9.
  ✓ 2026-11-03
- [x] **141.5** Amend spec §9.9's "ACP considered and rejected" bullet with
  a dated note citing this task. The "Skills" bullet stays as it is: the
  listing verdict is unchanged. ✓ 2026-11-03
- [x] **141.6** Point task 138's open question 6 here. ✓ 2026-11-03
