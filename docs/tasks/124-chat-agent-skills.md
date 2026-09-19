# 124 — Let a chat's human see the agent's skills and invoke one from a message

**Status:** 🔄 in progress (5/19)
**Opened:** 2026-09-19
**Issue:** #496 (parent), #497–#515 (one per item)
**Spec:** §9.1 (`SkillLister`, `SkillInvoker`), §9.6 (`supports_skill_listing`,
`skill_sigil`, `skill_position`), §9.7 (cursor invokes, never lists), §9.8
(where codex and cursor read skills from); later items amend §5.5, §9.4, §13.2,
§15 and §16

## Problem

Chats (§5.5; tasks 063, 067, 072, 074 and 119) let a human talk to an agent
CLI inside a project. Each of those CLIs loads its own **skills** — Agent
Skills directories holding a `SKILL.md`, from the user's home, from the
project in the worktree and, for claude, from plugins — and a vincent chat
gave the human no way to see them. Nobody had checked whether an invocation
typed into a chat message reaches the CLI's skill mechanism under vincent's
argv, stdin framing and prompt wrapping.

The work is done when a human in a chat can see the skills the chat's agent
will actually load there — in the TUI, over the REST API and through
`vincent chat` — and can invoke one, by picking it or by typing the adapter's
own syntax, with the CLI's own mechanism running it and the transcript showing
that it ran wherever the CLI reports that. Where an adapter cannot list, or
cannot report a skill load, vincent says so and does not fake it: §9's rule
that a capability an adapter lacks is documented and ignored, never emulated.

The research behind the breakdown is in #496. It was captured on 2026-09-19 on
macOS against claude 2.1.277, codex-cli 0.154.0 and cursor-agent 2026.09.15 /
2026.09.18; each item says which captures it must commit as a
`testdata/*_<version>.*` fixture.

**Numbering note (2026-09-19).** The issues cite this record as task 123. That
id went to `123-update-global-workflows.md`, which landed in #495 the day #497
was filed, so this record is 124 and its items `124.n`. The issues' titles and
citations are corrected on GitHub by the author; this record is the one to
cite.

## Decisions

Decisions 1–12 are the parent issue's (#496, "Decisions taken in this
breakdown"). 13–19 were settled with the author, or taken in evaluation, when
124.1 was built, 20–27 when 124.2 was, 28 in its follow-up, 29–36 when
124.8 was, and 37–39 when 124.12 was.

1. **2026-09-19 — Each CLI's own listing, never a scan.** claude answers a
   stream-json `initialize` control request (`commands`), codex answers
   `skills/list` on the `app-server --stdio` channel vincent already speaks.
   *Beaten:* scanning `SKILL.md` files on disk. A scan cannot see bundled
   skills, plugin enablement, claude.ai-synced skills, `skillOverrides` or
   codex's `skills.config` tables, so it would be a second implementation of
   three moving rule sets. It would also be the state-file parsing v0 T1.7
   forbids, and this does not move that line: §9.8 reads only a file vincent
   itself published, from a public CLI's documented install location, whereas
   a scan would reconstruct each CLI's loading rules from its private state and
   config — the inference T1.7 refused for authentication.
2. **2026-09-19 — cursor refuses to list in v1, but invokes.** Its `-p`
   dialect has no listing. ACP's `available_commands_update` has one, but it
   takes about 4 s, needs a login and the network, and misses the claude
   plugin skills a `-p` turn loads — not the skills the agent "will actually
   load there". *Beaten:* adopting ACP now. Whether to adopt it is 124.18
   (#514).
3. **2026-09-19 — A chat-scoped route (`GET /v1/chats/{id}/skills`), and no
   pre-creation route.** The worktree exists from `POST /v1/chats` on, so the
   route answers before the first turn and is exact about the directory. The
   new-chat form has no draft to insert into — the first message is typed in
   the workspace, after the chat exists — and the static bits on
   `GET /v1/agents` (decision 13) cover "can this agent list at all" before a
   chat exists. *Beaten:* a DTO field on the chat (`vincent chat send` polls
   the chat every 500 ms); widening `GET /v1/agents` (its cache is keyed on
   the binary alone, and a list also depends on the directory); an
   agent-scoped pre-creation route.
4. **2026-09-19 — Answer 200 with verdicts; 409 only for state.**
   `list_verdict` is `supported`, `unsupported` or `unknown`, meaning what
   `agent.InputVerdict` means. A capability read is information, not a refused
   action. A terminal chat gets `409 invalid_state`; a linked chat whose task
   lost its worktree gets `409 task_has_no_worktree`. *Beaten:* a 4xx for an
   adapter that cannot list.
5. **2026-09-19 — An in-memory cache, with no persistence and no SSE event**,
   in the style of §9.6's catalog cache, keyed on adapter, binary identity and
   working directory. Only a probe answers before the first turn and covers
   codex. *Beaten:* persisting the list, or reading it from a turn's init line,
   which carries names only, only for claude, and only after a message.
6. **2026-09-19 — The route stays out of MCP**, extending task 063 decision 2
   to the whole chat family. *Beaten:* an MCP tool beside `agent_list`.
7. **2026-09-19 — claude's `builtin: true` rows are omitted in v1.** `builtin`
   is the only machine signal, and it marks bundled skills (`simplify`,
   `loop`) and built-in commands (`clear`, `compact`) alike; the two cannot be
   told apart before a turn. Advertising `/clear` is harmful: it resets
   claude's conversation while vincent still shows the history. 124.16 (#512)
   restores the bundled skills once a turn's init line has classified them.
   *Beaten:* listing them flagged and letting the TUI group them.
8. **2026-09-19 — No scope parsing, and no `kind`.** claude's scope exists only
   as a display label inside a description, such as `(project)`. `Skill.Scope`
   carries the CLI's own word (codex's `user|repo|system|admin`) or nothing,
   after the "`ToolResult.Verb` refuses to guess" precedent. *Beaten:* parsing
   the label into a normalized scope, and a `kind` separating skills from
   commands.
9. **2026-09-19 — Pass-through only** (task 025 decision 5). No validation of
   an invocation against the list, no rewrite, no translation. A client-side
   hint in the TUI is allowed; a daemon refusal is not. *Beaten:* refusing an
   unknown name, and translating one adapter's syntax into another's.
10. **2026-09-19 — The skill event is `agent.skill` with
    `by: "human" | "agent"`.** 124.2 (#498) owns the event; the TUI research's
    `invoked_by: user` is folded into it. *Beaten:* a separate `invoked_by`
    field in the TUI's vocabulary.
11. **2026-09-19 — A container-run linked chat is answered `unknown` in v1.**
    Probing on the host would list the wrong skills, which is emulation.
    *Beaten:* a host probe. Probing through the container is 124.17 (#513).
12. **2026-09-19 — The skills key is a fixed chat key, `tab`**, not a `keymap`
    operation: task 118 decision 1 says surface-local rows are fixed, and
    `tab` is inert in the composer today and the terminal-wide convention for
    completion. *Beaten:* a rebindable operation. Confirming `tab` over `f2`
    is open question 5, below.
13. **2026-09-19 — `supports_skill_listing` is a static bool about the
    adapter**, after `supports_resume`. `agent.CanListSkills` means only "this
    adapter implements `SkillLister`": it answers "can this agent list at
    all", not "will the installed build list". A build that cannot surfaces at
    list time, as `ErrSkillsUnsupported`, which 124.9's route turns into
    `list_verdict: unsupported` — for example a claude below whatever floor
    124.7 settles on. That floor stays open and does not reopen this wire
    shape. *Beaten:* a version-aware tri-state `skill_list_verdict` modelled
    on `input_verdict`.
14. **2026-09-19 — Whether a stream reports a skill load stays off the wire in
    v1** (#496's open question 8). There is no `ReportsInvocations` on
    `SkillInvoker` and no `reports_skill_loads` on `GET /v1/agents`; the fact
    lives in `docs/guides/agents.md` only, and nothing is written there before
    124.2 and 124.10 make it true for claude. *Beaten:* publishing the bit
    now, `false` for all three.
15. **2026-09-19 — Dedicated skill stubs, not `StubNonResuming`.** That stub's
    doc pins `SupportsResume() == false` as the entire behaviour under test,
    and a second refusal on it would blur what it is for. `StubNoSkills`
    implements neither capability, under a name no shipped CLI has;
    `StubSkills` lists a scripted `SkillList` or error, counts its calls and
    records its `SkillQuery` values safely under concurrency, and invokes with
    a configurable syntax, so the cache tests (124.9) and the TUI tests
    (124.13, 124.14) share one stub. Nothing in production registers either.
    *Beaten:* widening `StubNonResuming`.
16. **2026-09-19 — `ErrSkillsUnsupported` is a sentinel, never a probe
    failure.** It means "this adapter, or this installed build, can never
    list". A timeout, a malformed answer or a crashed probe is an ordinary
    error; callers tell them apart with `errors.Is`. This is `InputVerdict`'s
    split between `unsupported` and `unknown`, and it is what 124.9 maps to
    `list_verdict`. *Beaten:* one error for both, which would let a flaky
    probe claim a positive no.
17. **2026-09-19 — `Skill` synthesizes nothing**, extending decision 8. Every
    field is the CLI's own words and `""` is unreported; `Skills` keeps the
    CLI's order and names may repeat, since both CLIs document same-name
    entries they do not merge; `SkillProblem` carries codex's `errors[]`.
    *Beaten:* de-duplicating names, and dropping load errors.
18. **2026-09-19 — Invocation syntax is static per adapter.** claude: `/`,
    `leading`, `"/" + name`. codex: `$`, `anywhere`, `"$" + name` for now —
    124.8 adds the linked `[$name](path)` for an ambiguous name, using the
    `among` argument. cursor: `/`, `anywhere`. `SkillPosition` is a string
    type whose values go on the wire unchanged, and `CanInvokeSkills` is true
    exactly when an adapter implements `SkillInvoker`. *Beaten:* a
    version-gated syntax, which no captured build calls for.
19. **2026-09-19 — `vincent agents` notes "no skill listing" only as a
    positive no**, after `TestAgentRowNotesAreBadNewsOnly`. A `null` from an
    older daemon, or one with no registry, adds nothing. Until 124.7 lands,
    claude and cursor carry the note, which is today's truth; codex lost it
    with 124.8.
    Invocation gets no note: every shipped adapter can invoke.
20. **2026-09-19 — The forked kickoff is mapped in 124.2, on a second
    capture.** A `local_agent` `task_started` with no `tool_use_id` and a
    `/`-prefixed description becomes `agent.skill{by:"human", forked:true}`.
    The re-capture on claude 2.1.277 matched the issue's c24: description
    `/fork-probe` with no arguments even though the message passed `zebra`,
    the rendered body under `prompt`, no `tool_use_id`, before `system/init`.
    So `args` is empty. Settled with the author. *Beaten:* moving the kickoff
    to 124.10 (#506), and leaving it unowned until a later build. Either
    would keep the forked skill's body on screen as `agent.raw`, which is the
    defect this item exists to remove.
21. **2026-09-19 — An agent-loaded skill carries its args.** The parser
    remembers each `Skill` tool_use's `input.args` by call id and puts them on
    the matching `agent.skill`, capped to one line. When a transcript range
    starts after the tool_use, the args are empty, a cost stated like task
    109's. Settled with the author. *Beaten:* leaving `args` empty for
    `by: "agent"`. The `Skill` summary reads only `input.skill`, so the args
    would be on no normalized record at all.
22. **2026-09-19 — The pairing is structural and scoped by parent.** A result
    that carries `tool_use_result.commandName` arms a pending skill in its
    `parent_tool_use_id` scope. The next line in that scope claims it if it
    is an `isSynthetic` `user` line and clears it otherwise. The §7.4 control
    lines belong to no scope: the live run answers them before the parser
    sees them and the transcript route does not, and the two paths must
    agree. *Beaten:* strict adjacency in the raw stream, which an
    interleaving async subagent breaks.
23. **2026-09-19 — An agent's refused `Skill` call produces no
    `agent.skill`.** Its `agent.tool_result` (`is_error`) reports the refusal
    and pairs by `call_id`. `Error` is filled only where the CLI reports a
    refusal on a line of its own, which is 124.10's.
24. **2026-09-19 — The whole `SkillInvocation` shape lands now.** 124.12
    draws human invocations and refusals and depends only on 124.2, so the
    wire fields exist before any parser fills them. The mapping of every
    field is tested with constructed events.
25. **2026-09-19 — `agent.input_echo` is a record with no payload and no live
    chunk.** The echoed text is already on screen, as the human's message in
    a chat and as the step's stored rendered prompt in a task. The verbatim
    line is still in `format=raw`. This does not reverse §9.7's raw-lines
    rule, which covers unmodeled lines only.
26. **2026-09-19 — The `Skill` permission summary is the skill's name.** It
    comes from `input.skill`, keyed on the tool name the way
    `AskUserQuestion` is, and falls back to `description`.
27. **2026-09-19 — The fresh captures make both builds tested.** claude
    2.1.277 and cursor 2026.09.18-9a7762b join `testedVersions`, after task
    108 decision 1.
28. **2026-09-19 — 124.12 draws `agent.skill` in the CLI transcript too.**
    `vincent task transcript` and `vincent chat transcript` print nothing for
    a record type they do not name, so a skill load is as invisible there as
    in the pane until 124.12 (#508) draws it. The printer has no levels; for
    the run header and a subagent's records it prints the pane's `normal`
    content (§15), and 124.12 settles whether a skill load follows suit.
29. **2026-09-19 — codex has no listing floor.** A build that cannot answer
    `skills/list` fails like any probe: an ordinary error, `list_verdict:
    unknown`. The method arrived in 0.73.0, and every verified build postdates
    it. No old build's refusal has been captured, so neither a version table
    nor matching the `-32600 "unknown variant"` wording may claim a positive
    no. Settled with the author. *Beaten:* mapping the app-server's method
    refusal to `ErrSkillsUnsupported`; a 0.73.0 floor from a `--version`
    probe.
30. **2026-09-19 — The linked form only for an exact-name duplicate among the
    listed, enabled skills.** codex also refuses a plain `$name` whose
    lowercased form equals an enabled app connector's slug (`selection.rs`),
    and `skills/list` cannot reveal connectors. §9.3 documents that as a known
    case where `$name` selects nothing, rather than making every invocation
    carry a path. Settled with the author. *Beaten:* always emitting
    `[$name](path)`.
31. **2026-09-19 — The fixture is captured on 0.154.0, the owner's installed
    build, and `0.154.0` joins `testedVersions`.** This follows task 108
    decision 1 and decision 27: a fresh capture makes its build tested.
    *Beaten:* installing 0.150.1 to capture on an already verified build. The
    undocumented scopes were observed on 0.154.0, and re-observing them on an
    older build is work that proves less.
32. **2026-09-19 — Disabled rows are dropped.** codex's own name counting
    excludes disabled skills (`name_counts.rs`), so dropping them keeps
    `among` counting the same set codex does. This is not a relaxation of
    decision 17, which forbade dropping load *errors*, and those still become
    `Problems`.
33. **2026-09-19 — The one `data` entry is taken, not matched by cwd.** One
    cwd is sent, so one entry is expected, and its echoed `cwd` is never
    compared, because paths may be normalized, especially on Windows. Any
    other count is malformed.
34. **2026-09-19 — One spawn path for both app-server requests.** Rate limits
    and skills both go through `agent.Launch`. Rate limits pass the host
    launcher, which is a fact about the account and not the directory.
    Skills pass `q.Launcher`. The host launcher is today's spawn exactly,
    including `CREATE_NO_WINDOW`.
35. **2026-09-19 — The fake app-server's default list comes from the
    requested cwd's `.agents/skills`**, which #511 step 4 relies on.
    `FAKEAGENT_CODEX_SKILLS` overrides it verbatim. `unauthenticated` still
    refuses only rate limits, because codex lists without a login, and a new
    `error` mode refuses `skills/list`.
36. **2026-09-19 — The guide's codex cell becomes ✅ now.** The row mirrors
    `supports_skill_listing`, which flips in this change. The prose beside it
    says that no chat surface shows the list until the chat skills route
    (124.9) lands, so the page stays true in the meantime.
37. **2026-09-19 — A skill the human invoked shows at `quiet`.** `by:
    "human"` draws one line at every level, quiet included, after the
    `vincent.input_request` / `vincent.input_response` precedent: an
    acknowledgement of what the human did is visible at quiet. §15's
    definition of quiet gains that third category in a dated note. A skill
    the model loaded (`by: "agent"`) stays hidden at quiet, like every tool
    call, and a load that failed shows at every level. This settles #496's
    open question 6. *Beaten:* hiding it, which would leave a human reading
    at quiet unable to see that their `/tdd` ran.
38. **2026-09-19 — The CLI printer follows the pane within each fetch.** In
    `vincent task transcript` and `vincent chat transcript`, a model-loaded
    skill prints on its call's line as `> skill <name> <args>` whenever the
    call and the load are in the same fetched range, and then prints nothing
    at its own position. Without `-f` that is always the case: the command
    fetches one range. Under `-f` a poll can split the call from its load;
    the `> Skill <name>` line that already printed stays, and the load does
    not print a second time. That split is the only case where a model
    load's args are missing. A load whose call lies outside the fetched range
    prints at its own position. *Beaten:* printing both lines in order, which
    reads as a second call and is the double draw the pane avoids; and
    printing only the call line, which never shows a model load's args.
39. **2026-09-19 — The skill's body is never on screen.** #508's criterion
    "folded into the unrecognized count at compact/normal, raw at verbose"
    predates #498. Since #498 the `isSynthetic` body line *is* the
    `agent.skill` record, and no record carries the body (T4.16; 124.2), so
    no level draws it. It stays reachable through `e`, `--raw` and
    `format=raw`. The lines the parser still leaves unmodeled — the forked
    capture's two leftovers, for one — keep the `agent.raw` rules: a count at
    compact and normal, whole at verbose, no trace at quiet. *Beaten:*
    showing the body at verbose, which would put it on the wire and reverse
    T4.16 and #498's design; that needs its own item.
40. **2026-09-19 — The claude listing floor is 2.1.277, inside the input
    family.** The probe runs only on a build in `[2.1.277, 3.0.0)`: it needs
    `supportsInput`'s family for the control channel, and 2.1.277 is the
    first build with `builtin`, which decision 7's filter depends on — and
    the only build captured. Any other build, below the floor or a 3.x alike,
    answers an error wrapping `ErrSkillsUnsupported` that names the version
    and the floor, the way `InputVerdict` reads a build outside the verified
    family. The floor moves only with re-captured fixtures. Closes open
    question 3. *Beaten:* listing across the whole `[2.1.0, 3.0.0)` family,
    which would advertise `/clear` below 2.1.277; and detecting `builtin` per
    reply, which would wrongly refuse a future build that stopped listing
    built-ins at all.
41. **2026-09-19 — The listing probe suppresses hooks and MCP servers.** It
    passes `--settings {"disableAllHooks":true}` and `--strict-mcp-config`
    with no `--mcp-config`. The measured list was identical (112/112), and the
    probe runs no user code and spawns no user MCP server. What it loses,
    stated in §9.2, is a skill only a SessionStart hook installs; MCP prompts
    are not skills, so they are out either way. Closes open question 4.
    *Beaten:* keeping hooks, or hooks and MCP both, for fidelity.
42. **2026-09-19 — claude's `builtin: true` rows are omitted**, every one of
    them: decision 7, implemented by 124.7. Bundled skills come back in
    124.16 (#512). Closes open question 2.
43. **2026-09-19 — Only a positive no is `ErrSkillsUnsupported`** (decision
    16), for claude's lister too. A binary that cannot be resolved, a failed
    `--version`, a timeout, `subtype: "error"`, malformed JSON, a reply
    without `commands` (or with `null`) and an exit before the reply are all
    ordinary errors, so 124.9 maps them to `unknown`; each carries the
    stderr tail. This follows `InputVerdictWith`'s rule that a binary which
    cannot be found or probed is unknown.
44. **2026-09-19 — The fake's cwd-derived default list is not built in
    124.7.** `cmd/fakeagent` answers `initialize` from
    `FAKEAGENT_CLAUDE_COMMANDS` or a small fixed default; a list read from the
    cwd's `.claude/skills/*/SKILL.md` belongs to 124.15 (#511), which decides
    whether the m14 leg needs it.

## Open questions

Each of the parent's remaining open questions belongs to the item that has to
answer it, so none is lost:

| #496 open question | Owner |
|---|---|
| 1 — Restricted posture: accept that a human-invoked skill's `allowed-tools` widens a restricted claude turn, or pass `--disable-slash-commands` in restricted mode | 124.6 (#502) |
| 2 — Built-in rows: omit every `builtin: true` row, or list them flagged | closed by 124.7 (#503): decision 42 |
| 3 — claude listing floor: 2.1.277 only, or the whole `[2.1.0, 3.0.0)` input family | closed by 124.7 (#503): decision 40 |
| 4 — Hooks in the probe: suppress SessionStart hooks and MCP servers, at the cost of missing hook-installed skills | closed by 124.7 (#503): decision 41 |
| 5 — The skills key: `tab` over `f2` | 124.13 (#509) |
| 6 — The `quiet` level: does a human-invoked skill show there | 124.12 (#508); settled by decision 37 |
| 7 — Cache TTLs: 5 min for a clean list and 1 min for a failed one | 124.9 (#505) |
| 9 — `/clear` under pass-through: should a conversation reset become a visible record | 124.6 (#502) |
| 10 — Containers: should `container.mount_agent_config` also mount `~/.agents` | no owner; out of scope |

Open question 8 is settled by decision 14.

## Work

In the parent's delivery order. An item with no `Depends:` tag has no blocker.

- [x] 124.1 (#497) Open this record. `agent.SkillLister`, `SkillQuery`,
  `SkillList`, `Skill`, `SkillProblem`, `ErrSkillsUnsupported`,
  `CanListSkills`, `SkillInvoker`, `SkillSyntax`, `SkillPosition` and
  `CanInvokeSkills`; `SkillInvoker` on all three adapters; the
  `agenttest.StubNoSkills` and `StubSkills` stubs; `supports_skill_listing`,
  `skill_sigil` and `skill_position` on `GET /v1/agents`, the `apiclient`
  fields and `CannotListSkills`, and the `vincent agents` note; spec §9.1,
  §9.6, §9.7 and §9.8. ✓ 2026-09-19
- [x] 124.2 (#498) `agent.skill` and `agent.input_echo`: claude's
  model-loaded skills stop showing as raw `SKILL.md` JSON, and cursor's prompt
  echo stops counting as unrecognized. `EventSkill`, `SkillInvocation`,
  `EventInputEcho`; the claude and cursor mappings; the `Skill` tool summary
  and §7.4 permission summary; the `agent.skill` chunk and record; the
  `skill-model` fakeagent scenario; fixtures from claude 2.1.277 and
  cursor-agent 2026.09.18-9a7762b, both now tested builds; spec §9.1, §9.2,
  §9.3, §9.7, §13.2 and §13.3. ✓ 2026-09-19
- [x] 124.3 (#499) Send claude's linked turn 1 as two text blocks, so a
  leading `/name` expands. `agent.RunSpec.Preamble` carries a linked chat's
  opening context apart from the message; claude's input line sends it as
  its own text block ahead of the message's, and `RunSpec.JoinedPrompt` is
  the one spelling of the `<message>`-wrapped string every other path sends,
  byte-identical to before. `cmd/fakeagent`'s `echo-prompt` records several
  blocks as an array. Fixture `stream_blocks_context_2.1.277.jsonl`; spec
  §5.5 and §9.2 amended. ✓ 2026-09-19
- [~] 124.4 (#500) Fix the chat composer's newline keys, a pre-existing bug.
  In review as #516.
- [ ] 124.5 (#501) `--message-file` on `chat send` and `chat start`, fixing
  Git Bash's `/name` rewrite, and the quoting rules documented.
- [ ] 124.6 (#502) Amend §5.5, §9.4 and §16 on pass-through and restricted
  skills, and record the owner's posture decision.
- [x] 124.7 (#503) claude lists through `initialize`. `claude.Adapter`
  implements `agent.SkillLister`: one `initialize` control request, only
  `commands` decoded, `builtin` rows dropped, on builds in
  `[2.1.277, 3.0.0)`; hooks and MCP servers suppressed. `cmd/fakeagent`
  answers `initialize` (`FAKEAGENT_CLAUDE_COMMANDS`,
  `FAKEAGENT_CLAUDE_INITIALIZE`, `FAKEAGENT_SKILLS_ECHO_CWD`). Fixtures
  `initialize_2.1.277.jsonl` and `initialize_loggedout_2.1.277.jsonl`; spec
  §9.1, §9.2 and §9.6 amended. Decisions 40–44. ✓ 2026-09-19
- [x] 124.8 (#504) codex lists through `skills/list`. Depends: 124.1.
  The app-server exchange generalized to "handshake, then one request" on
  `agent.Launch`; `codex.Adapter` implements `SkillLister`; `Invocation`
  emits `[$name](path)` for a duplicated name; `cmd/fakeagent` answers
  `skills/list` (`FAKEAGENT_CODEX_SKILLS`, the `.agents/skills` default, the
  `error` mode); fixture `app_server_skills_0.154.0.json`, 0.154.0 now a
  tested build; spec §9.1, §9.3 and §9.6 amended. ✓ 2026-09-19
- [ ] 124.9 (#505) The skill cache, `chatrun.Workspace`,
  `GET /v1/chats/{id}/skills`, the `apiclient` types and the MCP exclusion.
  Depends: 124.1.
- [ ] 124.10 (#506) `--replay-user-messages` on claude chat turns, so a
  human-invoked skill shows as `agent.skill{by:"human"}`. Depends: 124.2.
- [ ] 124.11 (#507) `vincent chat skills <chat-id>`. Depends: 124.9.
- [x] 124.12 (#508) Draw `agent.skill` at each verbosity level, and in
  `vincent task|chat transcript` (decision 28). The pane and the chat body
  draw `▸ skill <name> <args>`, `(forked)` for a forked skill and
  `▸ skill <name> failed: <error>` for a refusal; a model's load is drawn in
  its `Skill` call's place, paired by `call_id`, and no longer splits an
  unrecognized-line count. The printer prints `> skill …` and
  `! skill … failed: …` on the same pairing within each fetch. Decisions
  37–39; spec §15, `docs/guides/tui.md` and `docs/reference/cli.md`
  amended. ✓ 2026-09-19
- [ ] 124.13 (#509) The skill list above the composer, opened with `tab`,
  inserting the picked invocation. Depends: 124.11, 124.7.
- [ ] 124.14 (#510) The same list, opened inline as the human types the
  adapter's sigil. Depends: 124.13.
- [ ] 124.15 (#511) `m14` leg 12, end to end on all three operating systems.
  Depends: 124.11, 124.5, 124.7, 124.8, 124.10, 124.3.
- [ ] 124.16 (#512) Use the claude init line's `skills` to invalidate stale
  lists and restore bundled skills. Depends: 124.9, 124.7.
- [ ] 124.17 (#513) Probe through the task's container instead of answering
  `unknown`. Depends: 124.9, 124.7.
- [ ] 124.18 (#514) Investigate whether ACP should become cursor's listing,
  and record a decision. Depends: 124.1.
- [ ] 124.19 (#515) The `tui-chat-skills.png` tape. Depends: 124.14.

The requirement's two done criteria are met once 124.14, 124.11, 124.10, 124.3
and 124.15 have landed. 124.16, 124.17 and 124.18 widen coverage after that.

## Verification

- 124.1: `TestSkillCapabilitiesComeFromTheInterfaces` proves both
  capabilities true and false against the two stubs, and against no shipped
  adapter. `TestErrSkillsUnsupportedIsTheOnlyPositiveNo` holds decision 16
  through wrapping. `TestStubSkillsRecordsWhatItWasAsked` is the listing
  stub's contract under concurrent calls. Each adapter's `TestSkillSyntax`
  pins its sigil, position and `Invocation` for a plain and a `plugin:skill`
  name. `TestAgentsReportSkillCapabilities` pins the three wire fields
  literally per adapter, both ways through the stubs, and `null` with no
  registry. `TestAgentsCommandAgainstTheRealAPI` decodes them over the real
  handlers, and `TestAgentRowNotesAreBadNewsOnly` holds decision 19.
- 124.2: `TestModelLoadedSkill` pins the claude capture's three lines — the
  `Skill` call summarized `echo-probe`, the result still a result, the body the
  one `agent.skill{by:"agent"}` with name, call and args — and that no event
  field holds the `SKILL.md` body. `TestSkillBodyNeedsItsResult`,
  `TestSkillPairingIsScopedByParent`, `TestSkillArgsNeedTheCall` and
  `TestRefusedSkillCallIsNoLoad` hold decisions 22, 21 and 23;
  `TestSkillPermissionNamesTheSkill` decision 26; `TestForkedSkillKickoff`
  decision 20; `TestSkillLoadsTheSameOnBothPaths` the control-line rule. Each
  adapter's `TestFixtureEventTypesArePinned` pins every capture's per-line
  event types — the pre-existing claude captures unchanged, the cursor ones
  changed only in their `user` line — and cursor's `TestUserLineIsTheInputEcho`
  and `TestNoSkillEvents` state its side positively. `TestSkillChunkShape`,
  `TestInputEchoPublishesNothing`, `TestNormalizeSkillRecordFields` and
  `TestSkillRecordsMatchTheirLiveChunks` pin the wire both ways, and
  `TestSkillChunkMatchesItsRecord` and `TestCursorEchoIsNotRaw` run real chat
  turns on fakeagent over the real handlers, comparing the SSE chunk with the
  refetched record. `TestSkillModelScenario` ties the fake to the real parser.
- 124.3: `TestLinkedFirstTurnKeepsASkillInvocationLeading` reads claude's
  real stdin on a linked chat through the launcher seam: turn 1 is two text
  blocks, the context then the message byte for byte, and turn 2 is the
  message alone. `TestLinkedFirstTurnStdinOffClaudeInputMode` holds claude
  below the input gate, codex and cursor to the old wrapped bytes.
  `TestUserMessageLineKeepsTheMessageLast` pins the line itself,
  `TestFixtureContextBlockStream` parses the 2.1.277 capture, and
  `TestEchoPromptKeepsBlocksApart` the fake CLI's record of the blocks.
- 124.7: `TestInitializeFixtures` decodes both 2.1.277 captures — the
  non-`builtin` rows in the CLI's order, labels, argument hints and aliases
  kept, `Scope`, `Plugin` and `Path` empty, no `account` or `models` text in
  the value. `TestReadInitialize` pins the skipped lines, a hostile `account`,
  kept duplicates, `[]` against a missing or `null` `commands`, and each
  error reply; `TestReadInitializeTakesALongLine` the raised scanner.
  `TestListSkillsAgainstTheFake` pins the argv through `FAKEAGENT_ARGV_FILE`,
  `TestListSkillsRunsInTheWorkDir` the directory,
  `TestListSkillsGoesThroughTheLauncher` the launcher's resolve, probe and
  `Launch`. `TestSupportsSkillListingEdges` and
  `TestListSkillsBelowOrPastTheFloor` hold decision 40, the latter with no
  `initialize` sent; `TestListSkillsFailuresAreUnknown`,
  `TestListSkillsHonorsCancel` and `TestListSkillsUnprobeableBinary` hold
  decision 43; `TestListSkillsKeepsAnAnswerFromALingeringCLI` the reply
  outranking a late exit. `TestClaudeListsSkills` pins
  `CanListSkills(claude)`, and `TestAgentsReportSkillCapabilities` claude's
  `supports_skill_listing: true`.
- 124.8: `TestCodexCanListSkills` flips codex's capability.
  `TestParseSkillsList` holds the 0.154.0 capture: the enabled rows in
  codex's order with name, description, scope and path verbatim against the
  raw bytes, the `skills.config`-disabled row gone, and the broken
  `SKILL.md` as the one `Problem`; `TestParseSkillsListPluginAndEmpty` the
  `pluginId` leg (derived from the capture, where no plugin loads) and an
  empty list; `TestParseSkillsListMalformed` zero, two, non-object and
  non-list answers, none of them `ErrSkillsUnsupported`.
  `TestListSkillsHandsTheLauncherItsSpawn` reads the one `app-server --stdio`
  Command a `RecordingLauncher` saw — `Dir`, `Env`, `StdinPipe` — and its
  stdin: `initialize`, `initialized`, then `skills/list` with
  `cwds == [WorkDir]` and `forceReload: true`. `TestListSkillsAgainstFakeAgent`
  runs a nil launcher on the host over the `.agents/skills` default,
  `FAKEAGENT_CODEX_SKILLS`, `unauthenticated`, and the `error`, `malformed`,
  `hang` (a one-second deadline) and missing-binary failures, each an
  ordinary error with no listing. `TestInvocation` pins the linked form, case
  sensitivity, a path with a space, a Windows path and the no-path fallback.
  The rate-limit tests in `appserver_test.go` pass unmodified on the shared
  exchange, and `TestAgentsReportSkillCapabilities` pins codex's
  `supports_skill_listing: true`.
- 124.12: `TestSkillLevelTable` holds decision 37's table at all four levels
  for a human load, a model load with and without its call, a forked one,
  failures of each kind with and without a name, and a subagent's loads one
  level quieter. `TestSkillLoadDrawnAtItsCall` proves one `skill` line, no
  `Skill` line and the outcome directly under it, the multi-call and quiet
  cases, and a load with no call in the window drawn on its own.
  `TestSkillLoadDoesNotSplitAnUnrecognizedRun` is the one count.
  `TestSkillLiveAndRefetchedAgree` is the `chatverbosity_test.go`
  equivalence, and `TestLiveSkillLoadMatchesItsRefetch` runs fakeagent's
  `skill-model` over the real handlers, comparing the SSE-built frame with
  the refetched one. `TestLiveSkillCapturesNeverShowTheBody` holds decision
  39 over the claude model and fork captures, the copy picker included.
  `TestSkillLineWithoutColour` covers NO_COLOR and escapes in a name, args
  and an error; `TestChatBodyDrawsTheHumansSkill` the chat body at quiet.
  `TestRenderTranscriptSkillForms`, `TestTranscriptPrintsASkillLoadOnce`
  (one fetch, the `-f` split, no call in range, the rail) and
  `TestTranscriptSkillCaptures` (both commands identical over the captures)
  hold decision 38.
