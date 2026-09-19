# 124 — Let a chat's human see the agent's skills and invoke one from a message

**Status:** 🔄 in progress (2/19)
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
124.1 was built.

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
    older daemon, or one with no registry, adds nothing. Until 124.7 and 124.8
    land, all three adapters carry the note, which is today's truth.
    Invocation gets no note: every shipped adapter can invoke.

Decisions 20–28 are 124.2's (#498). 20–22 were settled with the author, and
23–28 were taken in evaluation.

20. **2026-09-19 — The forked-skill kickoff is left out, and handed to 124.10
    (#506).** claude's `system/task_started` with `task_type: "local_agent"`,
    no `tool_use_id` and a `/`-prefixed `description` stays `agent.raw`. It
    rests on one capture, taken without `--replay-user-messages`; once 124.10
    turns that flag on, the same forked load may also replay a command-tag
    line, which would produce two `agent.skill` records for one load. 124.10
    maps it, if at all, from one capture showing both lines, and no fork
    fixture is committed here. `Forked` is still defined (decision 23),
    because 124.12 draws `(forked)`, but nothing sets it yet. *Beaten:*
    mapping it now, pinned by the single capture.
21. **2026-09-19 — A model-loaded skill's record carries its arguments.** The
    parser remembers each `Skill` tool_use's `input.args`, keyed by the call's
    id, and puts it on the `agent.skill` record as `Args`, cut to one line and
    capped at `agent.ToolSummaryMax`. 124.12 hides the `▸ Skill` tool_use when
    an `agent.skill` shares its `call_id`, and the tool_use summary is the name
    only, so without `Args` the arguments would appear nowhere in the pane.
    `Args` is empty when the transcript range opened after the tool_use, the
    same stated cost the subagent memory carries. *Beaten:* `name` and
    `call_id` only, as the issue first wrote it.
22. **2026-09-19 — The CLI transcript printer is unchanged, and 124.12 (#508)
    is widened to cover it.** `vincent task transcript` and
    `vincent chat transcript` (`renderTranscriptRecord`) print nothing for
    `agent.skill` or `agent.input_echo`; `--json` prints the records as
    before. A model-loaded skill still prints, as `> Skill echo-probe`, through
    the new tool_use summary. The CLI's `agent.skill` line needs the same
    don't-draw-twice rule against the `Skill` tool_use that the pane gets, so
    it belongs with the drawing work. **Action for the author:** widen #508 on
    GitHub to include `internal/cli/transcript.go`. *Beaten:* adding the arm
    here, or filing a separate item.
23. **2026-09-19 — All six `SkillInvocation` fields are defined now,** although
    124.2 sets only `Name`, `Args`, `By` and `CallID`. 124.12 depends only on
    124.2 and draws `error` and `(forked)`, so the fields must exist first.
    `Error` is first set by 124.10's stderr replay. A model calling an unknown
    skill is already an `is_error` `agent.tool_result` that pairs with its
    tool_use, and produces no `agent.skill`. Every key is omitted on the wire
    when empty, so an unset field costs nothing.
24. **2026-09-19 — `agent.input_echo` is a transcript record with no live
    chunk,** after `agent.result`'s precedent. The transcript route writes
    `{"type":"agent.input_echo"}`, plus `parent_call_id` when set, and no body.
    `LiveChunks` returns none for it and `UnmodeledLine` stays false, so the
    chat runner publishes nothing, which keeps `UnmodeledLine` exactly the
    complement of what the transcript route calls raw. The TUI treats the
    record like `agent.subagent_started`/`_progress`: it never draws a line and
    does not break a run of unrecognized lines. It carries no `text`, because
    the prompt is already on screen as the chat's bubble or the step's prompt.
25. **2026-09-19 — How a model-loaded skill is recognized.** A `user` line
    qualifies as a `Skill` result when it holds exactly one `tool_result`, that
    result is not `is_error`, and the line's `tool_use_result.commandName` is
    non-empty — task 110's attribution rule for a line-level
    `tool_use_result`. The line still normalizes as the `agent.tool_result` it
    was. Qualifying arms a pending load keyed by the line's
    `parent_tool_use_id` (`""` is the main loop), holding the call's id,
    `commandName` and the remembered args. The next line with the same parent
    consumes it, whatever that line is: a synthetic `user` line with no
    `tool_result` block becomes `EventSkill{By: "agent"}`, and any other
    synthetic line stays `EventUnknown`. Keying by parent means a subagent's
    line interleaved between the two does not clear a main-loop load. `Name`
    is `commandName` — what the CLI resolved, possibly namespaced — not the
    model's `input.skill`. The body is never carried (T4.16). The control lines
    never consume a load, because the live read loop answers them before the
    parser sees them and a refetch must agree with the live tail (task 071
    decision 1).
26. **2026-09-19 — 2.1.277 joins claude's `testedVersions`.** The committed
    `stream_skill_permission_2.1.277.jsonl` pins the §7.4 control protocol at
    that build, which is the reason the list gives for 2.1.226.
27. **2026-09-19 — `docs/guides/agents.md` is not touched.** Decision 14 writes
    nothing there before both 124.2 and 124.10 make "the transcript shows a
    skill ran" true for claude; 124.10 adds that row. The tested-build row is
    not a skill fact but `testedVersions` restated, so it gains `2.1.277`
    with decision 26 in the same PR (documentation audit, 2026-09-19).
28. **2026-09-19 — Fixture provenance goes where the repo already keeps it.**
    `internal/agent/*/testdata/` has no README. The argv and the probe skill
    are recorded in the doc comment of the test that loads each fixture, and
    in §9.2's and §9.7's dated notes; the file names carry the CLI version.

## Open questions

Each of the parent's remaining open questions belongs to the item that has to
answer it, so none is lost:

| #496 open question | Owner |
|---|---|
| 1 — Restricted posture: accept that a human-invoked skill's `allowed-tools` widens a restricted claude turn, or pass `--disable-slash-commands` in restricted mode | 124.6 (#502) |
| 2 — Built-in rows: omit every `builtin: true` row, or list them flagged | 124.7 (#503) |
| 3 — claude listing floor: 2.1.277 only, or the whole `[2.1.0, 3.0.0)` input family | 124.7 (#503) |
| 4 — Hooks in the probe: suppress SessionStart hooks and MCP servers, at the cost of missing hook-installed skills | 124.7 (#503) |
| 5 — The skills key: `tab` over `f2` | 124.13 (#509) |
| 6 — The `quiet` level: does a human-invoked skill show there | 124.12 (#508) |
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
  echo stops counting as unrecognized. `EventSkill`, `EventInputEcho` and
  `SkillInvocation`; claude's skill memory, the `skill` summary key and the
  `Skill` permission summary; cursor's echo; both records on the transcript
  route, the live chunk and `apiclient`; the TUI skipping the echo; the
  fakeagent `skill-model` scenario; 2.1.277 tested; spec §9.1, §9.2, §9.3,
  §9.7, §13.2 and §13.3. ✓ 2026-09-19
- [ ] 124.3 (#499) Send claude's linked turn 1 as two text blocks, so a
  leading `/name` expands.
- [~] 124.4 (#500) Fix the chat composer's newline keys, a pre-existing bug.
  In review as #516.
- [ ] 124.5 (#501) `--message-file` on `chat send` and `chat start`, fixing
  Git Bash's `/name` rewrite, and the quoting rules documented.
- [ ] 124.6 (#502) Amend §5.5, §9.4 and §16 on pass-through and restricted
  skills, and record the owner's posture decision.
- [ ] 124.7 (#503) claude lists through `initialize`. Depends: 124.1.
- [ ] 124.8 (#504) codex lists through `skills/list`. Depends: 124.1.
- [ ] 124.9 (#505) The skill cache, `chatrun.Workspace`,
  `GET /v1/chats/{id}/skills`, the `apiclient` types and the MCP exclusion.
  Depends: 124.1.
- [ ] 124.10 (#506) `--replay-user-messages` on claude chat turns, so a
  human-invoked skill shows as `agent.skill{by:"human"}`. Depends: 124.2.
- [ ] 124.11 (#507) `vincent chat skills <chat-id>`. Depends: 124.9.
- [ ] 124.12 (#508) Draw `agent.skill` at each verbosity level, and in
  `vincent task|chat transcript` (decision 22). Depends: 124.2.
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
- 124.2: `TestModelLoadedSkill` is decision 25, table-driven over
  `stream_skill_model_2.1.277.jsonl`: the capture yields
  `EventSkill{By: "agent", Name: "echo-probe", Args: "zebra", CallID}`; a
  body with no `Skill` result, one separated from it by a line of the same
  parent, one after an `is_error` result and one after a two-result line stay
  unknown; a subagent's line in between does not clear the load.
  `TestSkillLoadedOnceAndStillAToolCall` pins the call's `Skill echo-probe`
  summary and one load per load, `TestSkillBodyIsNeverNormalized` T4.16, and
  `TestSkillPermissionFixture` the §7.4 summary over
  `stream_skill_permission_2.1.277.jsonl`, live and refetched alike. Each of
  claude's and cursor's `TestOldFixturesParseAsBefore` compares every earlier
  capture line by line against the classification at 95783734, allowing only
  cursor's `user` line to move from unknown to input_echo; cursor's
  `TestUserLinesAreInputEchoes` and `TestNoSkill` and codex's
  `TestNoSkillAndNoInputEcho` state the rest positively.
  `TestSkillChunkShape` and `TestSkillAndInputEchoAreModeled` pin the live
  side, `TestSkillRecordsMatchTheirLiveChunks`, `TestNormalizeSkillRecord` and
  `TestNormalizeInputEchoRecord` the transcript side, and
  `TestSkillChunkMatchesItsRefetchedRecord` and
  `TestInputEchoPublishesNothingLive` both over a real chat turn behind the
  real handlers. `TestInputEchoIsNeverALine` holds decision 24 in the pane.
