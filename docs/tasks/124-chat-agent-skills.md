# 124 — Let a chat's human see the agent's skills and invoke one from a message

**Status:** 🔄 in progress (15/20)
**Opened:** 2026-09-19
**Issue:** #496 (parent), #497–#515 (one per item)
**Spec:** §9.1 (`SkillLister`, `SkillInvoker`), §9.6 (`supports_skill_listing`,
`skill_sigil`, `skill_position`), §9.7 (cursor invokes, never lists), §9.8
(where codex and cursor read skills from); later items amend §5.5, §9.4, §12.1,
§13.2, §15 and §16

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
124.8 was, 37–39 when 124.12 was, 40–44 when 124.7 was, 45–53 when
124.5 was, 54–59 when 124.9 was, 60–63 when 124.11 was,
64–68 when 124.10 was, 69–76 when 124.13 was, 77–78 when 124.12's
documentation landed, 79–82 when 124.15 was, and 83–88 when 124.14 was.

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
   *Amended 2026-09-20 by 124.16:* that restoration landed. The adapter now
   keeps every `builtin` row and flags it (decision 86), and the skill cache
   serves one only when a turn on that binary named it (decision 84), so
   `/clear` is still never listed and the route says which of the two
   situations a caller is in. The beaten alternative is not revived: the TUI
   is unchanged (decision 88).
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
    *Amended 2026-09-20 by 124.16:* the adapter no longer omits them. It
    returns every row with `Skill.Builtin` set from the reply, and the cache
    drops the ones no turn has named (decisions 84 and 86) — so what reaches
    a client is unchanged until a turn has run, and gains the bundled rows
    after one.
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
45. **2026-09-19 — `--message-file` on both `chat send` and `chat start`,
    byte for byte.** `-` means stdin. Nothing is trimmed, so a trailing
    newline from `echo`, a heredoc or an editor is part of the message; the
    docs show `printf '%s' '...' | vincent chat send N --message-file -` as
    the form that adds none.
46. **2026-09-19 — Exactly one source of the message.** `chat send` is
    `send <chat-id> [<message>]` (`cobra.RangeArgs(1, 2)`), and `RunE`
    refuses both a positional message and the flag, and neither. `chat
    start` marks `--message` and `--message-file` mutually exclusive; with
    neither it creates the chat and sends nothing, as before.
47. **2026-09-19 — The bound is 4 MiB, `--fields-file`'s.** Chosen by the
    author knowing the send route decodes under §13.1's ordinary 64 KiB tier,
    so the CLI bound guards against an unbounded pipe and is **not** the
    message limit: a longer body is still the daemon's `413
    payload_too_large`, whose message `runChatTurn` prints, and the docs say
    so. The read is `readFieldsFile`'s, one byte past the bound. *Beaten:*
    bounding at the route's own 64 KiB, which applies task 045 decision 6's
    reasoning rather than its number. *Beaten:* moving the send route to the
    4 MiB tier with a per-field bound, an API and §13.1 change out of scope
    here.
48. **2026-09-19 — Input that is not valid UTF-8 is refused locally**, before
    any request, exit 1, with an error naming the flag and never quoting the
    content. `encoding/json` replaces an invalid byte with U+FFFD, so refusing
    is the only way to keep decision 45. A leading BOM is valid UTF-8 and
    passes unchanged; the docs note that it defeats a `/name` behind it and
    that Windows PowerShell 5.1's `Out-File -Encoding utf8` writes one.
    *Beaten:* also refusing a BOM, and sending whatever arrives.
49. **2026-09-19 — Empty input is refused locally.** The daemon's `400
    message is required` says the same later, but on `chat start` a chat
    would already exist. Whitespace alone (`"\n"`) is not empty and is sent.
50. **2026-09-19 — The file is read before any request, in both commands.** A
    missing file, input over the bound, invalid UTF-8 or empty input creates
    no chat. A message the daemon refuses after creation — a 413 between
    64 KiB and 4 MiB — leaves the chat created and `idle`, exactly what
    `--message` does.
51. **2026-09-19 — A local input error exits 1 as a plain `error` from
    `RunE`**, after task 045 decision 5. Exit 2 keeps its one meaning: no
    daemon answered.
52. **2026-09-19 — §12.1 gets a new row, not a note on an existing one.** The
    issue's citation of "the `vincent chat` row" was stale: the table's only
    chat row was `vincent chat transcript`, which defers the rest of the
    family to `docs/reference/cli.md`. The new row is dated in task 103's
    style.
53. **2026-09-19 — One bounded-read implementation.** `readInputFile`
    (`internal/cli/inputfile.go`) is the "path or `-`, `LimitReader` plus one
    byte, refuse over the bound" half of `readFieldsFile`, which now calls it
    as `readMessageFile` does. The constant is shared as `maxInputFileBytes`,
    its comment saying what it bounds for each flag; `readFieldsFile`'s
    messages are unchanged.
54. **2026-09-19 — The skill cache's TTLs are 5 min for a clean answer and
    1 min for a failed probe, both fixed** (open question 7). They are
    unexported constants beside `failureTTL`, `authTTL` and `quotaTTL`, on
    the same argument: a burst of requests costs one probe, and a human who
    changes something and looks again is told the truth. No config key,
    because no other §9.6 TTL has one. `?refresh=true`, and a finished turn in
    the same directory, bypass them. Settled with the author. *Beaten:* a
    config key; one TTL for both outcomes.
55. **2026-09-19 — The cache holds 64 keys, least recently used evicted
    first, fixed.** Far above realistic open chats × adapters, and it stops
    churned worktrees piling up. Not coupled to `max_parallel_chats`: a probe
    holds no slot. Settled with the author. *Beaten:* an unbounded map; a
    bound derived from the chat cap.
56. **2026-09-19 — `chatrun.Runner.Workspace` reads container placement from
    settings and never asks the runtime.** The route must spawn nothing for a
    container-run chat, and `ChatLauncher` runs `rt.Lookup` (a docker
    subprocess) keyed by a turn id the route does not have. So
    `chatrun.Deps.InContainer` is an injected function the daemon wires to
    `taskrun.Runner.ChatInContainer`, which reads the task's workflow snapshot
    through the one helper `chatContainer` also uses, so the two cannot
    disagree. `turnPlace` is `Workspace` then `Launchers`; a configured
    container that is gone still answers `unknown` on the route and still
    fails the turn with `ErrTaskContainerMissing`. The runners still reach
    each other only through injected closures (task 119). *Beaten:* reusing
    `ChatLauncher`.
57. **2026-09-19 — The route writes the "cannot list" reason itself** for an
    adapter without `SkillLister`: `"<agent> does not report the skills it
    loads"`. An adapter that cannot answer has no method to state a reason
    through, and `skills.go` records why none is added. For
    `ErrSkillsUnsupported` the wrapped error text is the reason. *Beaten:* a
    reason method on every adapter.
58. **2026-09-19 — A chat whose adapter is not registered answers
    `list_verdict: unknown`** with a `probe_error`, and `invoke_verdict:
    unknown`: nobody can say, as `RestrictedUnknown` answers for an
    unregistered adapter. Never `unsupported`. *Beaten:* `unsupported`, a
    positive no nobody gave.
59. **2026-09-19 — The route and the cache spawn nothing of their own.** "Probes
    run through the `CREATE_NO_WINDOW` runner" is carried by the listers,
    which spawn through `procx` and `agent.Launch` already; the cache only
    calls `ListSkills`, on the host with the daemon's environment. Here that
    criterion is Windows CI green on the new tests.
60. **2026-09-20 — `vincent chat skills`' table is `SKILL`, `INVOKE`, `ARGS`,
    `DESCRIPTION`.** `INVOKE` carries each row's `invocation` verbatim,
    because it is the only string that survives codex's `[$name](path)`
    disambiguation for a duplicated name: a `SKILL` column alone would print
    two identical rows for two different skills. `SCOPE` earns no column —
    decision 8 records that claude never reports one, so the cell would be
    blank for every claude chat — and `scope`, `plugin`, `path` and `aliases`
    stay in `--json`. Cells are blank, never `-`, where the CLI said nothing.
    Settled with the author. *Beaten:* the issue's `SKILL`, `ARGS`, `SCOPE`,
    `DESCRIPTION`.
61. **2026-09-20 — One generic, sigil-keyed invocation line, always
    single-quoted.** It is built from `invoke_sigil` and `invoke_position`,
    not from a row, so it prints whenever `invoke_verdict` is `supported` —
    including with an empty list and under an `unsupported` or `unknown` list
    verdict, which is the case decision 2 exists for (cursor invokes but
    cannot list). The quoting note keys off the **sigil, not the agent name**:
    `$` gets the expansion warning, `/` the Git Bash `MSYS_NO_PATHCONV=1` /
    `--message-file` note. Settled with the author. *Beaten:* the issue's
    codex-name-keyed `$` note, which hard-codes an adapter name the response
    already describes structurally and leaves the `/` adapters' Git Bash
    hazard unsaid.
62. **2026-09-20 — No truncation.** The `DESCRIPTION` cell goes through
    `table()` like every other subcommand's. Settled with the author.
    *Beaten:* the issue's "truncated to the terminal width": nothing in
    `internal/cli` measures a terminal today, `vincent agents` lets an equally
    long `NOTES` cell run, and a width-dependent table is non-deterministic
    under a pipe and in tests.
63. **2026-09-20 — stdout is the table and nothing else.** The `unsupported` /
    `unknown` verdict with its `unavailable_reason` or `probe_error`, the
    invocation line, and the `problems[]` rows all go to stderr, at exit 0 —
    following `vincent chat handoff`'s `warning: …` lines and `vincent chat
    transcript`'s "this turn has no transcript" notice, both already stderr at
    exit 0. Under a non-`supported` list verdict no table is printed at all: a
    lone header would read as "none". Settled with the author. *Beaten:* the
    verdict on stdout in the table's place.
64. **2026-09-20 — An unresolved `/name` is an `agent.input_echo`, not a
    failed skill.** claude's string-vs-array content shape does say it tried
    to expand a command, but nothing on the line names a failure, and filling
    `SkillInvocation.Error` with a phrase vincent wrote would break that
    field's contract — it holds the CLI's own refusal, reported on a line of
    its own (§9.1, decision 23). Recognizing a leading `/` on an echoed string
    is also the guess T4.17 refuses. *Beaten:* `agent.skill{by:"human", name,
    error:"not expanded"}`, which would give the human a visible row; the cost
    accepted is that the human sees only the assistant's prose reply.
65. **2026-09-20 — A refusal line is paired with the turn's last replayed
    name.** The `<local-command-stderr>` replay names no skill, so the parser
    remembers the last `<command-name>` it replayed in the stream and puts it
    on the refusal's `EventSkill{By:"human", Name, Error}`, falling back to a
    nameless one when nothing preceded it. *Beaten:* the issue's
    always-nameless record, which renders two invocations' failures
    identically; and `agent.input_echo`, which would show the human nothing
    where an injected command was refused. The memory is per stream and per
    turn, in the style of the armed-skill memory, and it is not scoped by
    `parent_tool_use_id`: a replay belongs to no scope. The capture this
    landed with exercises the fallback rather than the pairing — 2.1.277
    replays the refusal *instead of* the invocation when the injected command
    is refused, not after it — so both halves are tested, the fallback over
    the fixture and the pairing over the two captures composed.
66. **2026-09-20 — The flag rides on every chat turn**, not only on turns
    whose message starts with the sigil. One rule, and vincent never inspects
    the human's message to decide an argv flag. The accepted cost is that
    every chat turn's transcript also holds its own prompt echo, a linked
    first turn's preamble among it, and its §7.4 answers, all as
    `agent.input_echo` records no client draws; chat transcripts are capped by
    `transcript_max_bytes`, which `runTurn` already applies per turn.
67. **2026-09-20 — `<command-args>` is taken verbatim; only
    `<local-command-stderr>` is unescaped.** Captured: a typed `<there> & co`
    comes back byte for byte, while the stderr body carries `&amp;&amp;`. So
    the issue's "XML-unescaped" is right for the stderr body and wrong for the
    arguments — unescaping those would corrupt a message that typed a literal
    `&amp;`. Both are flattened to one line and capped with
    `agent.OneLine(…, agent.ToolSummaryMax)`, as decision 21 caps an agent
    invocation's args.
68. **2026-09-20 — A replayed line and an echoed `control_response` belong to
    no scope.** `streamParser.claim` already refuses to let `control_request`
    and `control_cancel_request` claim or disarm an armed model-skill scope
    (decision 22); `isReplay` `user` lines and `control_response` lines join
    that guard. An `isReplay` line is a `user` line that is not `isSynthetic`,
    so without it one interleaving between a `Skill` result and its body would
    silently disarm the scope and turn a model's skill load back into
    `agent.raw`.
69. **2026-09-20 — The skills key is `tab`, with `f2` as its alias.** This
    closes open question 5 rather than answering it with `tab` alone. `tab`
    is the taught key — the registry row's `key`, the hint line's word and
    what the guide's table names — and it is safe on this surface: bubbles
    v2.2.1's textarea binds no tab, `chatView.updateKey` matched none, and
    `root.updateKey`'s capture gate hands every key but `ctrl+c` to a
    capturing view, so `global tab` and the new-chat form's `tab` are
    untouched. `f2` is the alias for a terminal that swallows `tab`,
    recorded the way `ctrl+j`'s row already records `shift+enter` and
    `alt+enter` — named inside the row's label, and carried as its own
    `keymap.fixed` row so `TestEveryMatchedKeyIsRegistered` sees both
    literals. Decision 12 stands unchanged in substance. The test keymaps
    that used `f2` as a free function key moved: `internal/tui`'s
    `reboundKeys` to `f13`, and `internal/config`'s and `internal/cli`'s
    `help` examples to `f3`.
70. **2026-09-20 — The list never touches the draft until a row is
    accepted.** #509's open-by-inserting-the-sigil design is dropped: with
    `invoke_position: leading` and a draft of `fix the bug`, inserting `/`
    at the start makes the token `/fix`, so browse mode is filtered rather
    than full, accepting `/tdd` eats the word `fix`, and `enter` with no
    highlight would send a message beginning with a `/` nobody typed.
    Instead the list owns its own filter buffer, drawn on its title line;
    `esc` has nothing to undo. While it is up it owns the keyboard, which is
    how every other vincent popup behaves and what makes `esc` a real layer.
    124.14 (#510) is not blocked by this: it filters from the draft's own
    sigil token because the human typed that token, and the two openers
    share the ranking function and the row renderer, not the buffer.
71. **2026-09-20 — The hostile-row guard is controls and escapes, not
    whitespace.** A row is drawn disabled, and cannot be accepted, when its
    `invocation` carries a C0/C1 control — a newline among them — or an
    ANSI escape. Whitespace is allowed: #509's whitespace ban would have
    disabled codex's linked form for a duplicated name, which is exactly
    what decision 30 requires, and on macOS a chat worktree's path always
    has a space in it. `sanitizeText` alone is not enough for the drawn
    text — it deliberately keeps `\n` and `\t` — so every name,
    description, argument hint, scope and plugin is flattened with
    `agent.OneLine` and then sanitized before it is measured or drawn,
    because a multi-line string would trip the #299 hazard where `render`'s
    per-line `ansi.Truncate` measures a joined string as the sum of its
    rows.
72. **2026-09-20 — `invoke_verdict: unknown` behaves as `unsupported` for
    accepting.** The list opens read-only and the accept is refused with the
    reason, the way the cannot-invoke state does. Decision 58 gives an
    unregistered adapter `unknown` on both verdicts, and a client must not
    insert an invocation nobody said would work.
73. **2026-09-20 — No refresh key.** The open list has none, and `ctrl+r`
    keeps its one meaning. The daemon invalidates a chat's list when a turn
    ends (124.9) and the clean-list TTL is five minutes (decision 54); a
    human who adds a skill by hand mid-chat waits for one of those or runs
    `vincent chat skills --refresh`. This closes #509's second open
    question.
74. **2026-09-20 — Accepting closes the list.** #509 did not say. Reopening
    as the sigil is typed is 124.14's.
75. **2026-09-20 — The highlighted row's wrapped description keeps a fixed
    reserve.** The three lines are reserved whenever the list is open, not
    added when a row is highlighted, so arrowing through the rows does not
    change the body's budget and scroll the conversation under the reader.
76. **2026-09-20 — A late response is dropped.** The fetch is a `tea.Cmd`; a
    result arriving after the list was hidden, or for a different chat,
    updates nothing. `ChatSkills` goes through `probeClient`, so this is the
    ordinary case rather than an edge.
77. **2026-09-20 — Posture (a): a human-invoked skill may widen a restricted
    claude turn, and vincent documents that rather than disabling skills.**
    Claude's `allowed-tools` frontmatter grants the listed tools for the turn
    that invokes the skill, under vincent's restricted argv as much as anywhere
    and with no control request (#496's capture against 2.1.277, and the vendor
    documentation at <https://code.claude.com/docs/en/skills>). No argv change:
    `restrictedTools` is untouched and `Skill` stays out of it, because skills
    without `allowed-tools` already load without it and pre-approving it would
    let the *model* widen a turn with no human act at all. The widening always
    costs a human act — the human types `/name`, which the CLI expands before
    the model is asked anything, or approves the §7.4 request a model's load
    raises (decision 26) — and the skill is repository or user content the
    human chose to chat inside. What a skill *injects* is still checked against
    the allow-list, and a refusal aborts the invocation before the model runs
    (`stream_skill_inject_denied_2.1.277.jsonl`). **Beaten:**
    `--disable-slash-commands` in restricted mode, which is not the one-line
    argv change the issue estimated. The flag is absent from the pinned
    `help_2.1.224.txt` capture while chat turns run under the whole
    `[2.1.0, 3.0.0)` input family (`internal/agent/claude/input.go`) and
    `buildArgs` adds its input-mode flags unconditionally, so it would need a
    version floor of its own or it would break restricted chats on tested
    builds; and it would contradict 124.9 and 124.11 by leaving
    `GET /v1/chats/{id}/skills` listing skills a restricted chat cannot invoke,
    unless that route grew an `invoke_verdict: unsupported` leg. Written into
    §9.4, §16, `docs/security-model.md`, `docs/guides/agents.md` and
    `docs/guides/workflows.md`; closes the parent's open question 1.
78. **2026-09-20 — A conversation reset is documented here and recorded
    later.** `/clear` passes through like any other message: claude resets its
    own conversation and stamps a new session id, which vincent stores
    last-wins, so the next turn resumes an empty conversation while the chat's
    transcript still shows every turn before it. 124.2 put
    `conversation_reset` explicitly out of scope and nothing maps the line
    today, so the transcript carries no mark at the point the agent's memory
    restarted. This item states that consequence in §5.5 and in
    `docs/guides/agents.md` and stops there: making the reset a *visible
    record* needs a record shape, a mapping and a drawing, so it is appended as
    124.20 rather than smuggled into a documentation item. Closes the parent's
    open question 9.
79. **2026-09-20 — The fake claude derives its `initialize` list from the
    working directory**, the way its app-server dialect already derives
    codex's from `.agents/skills` (`claudeRepoSkills`, closing decision 44's
    open call: the m14 leg does need it). `m14`'s freshness assertion and its
    linked-chat assertion are both about a *name* appearing because a file is
    in a particular directory, which neither a fixed `FAKEAGENT_CLAUDE_COMMANDS`
    nor `FAKEAGENT_SKILLS_ECHO_CWD` can express. The derived rows are
    **appended** to whatever those supply, and a missing or unreadable
    directory — or an entry whose front matter names nothing — is a no-op, so
    every existing test keeps the exact rows it pins. The front matter read
    takes `argument-hint` beside `name` and `description`, because the leg
    asserts the hint reaches the wire; `frontMatter` became a key/value read
    shared with `repoSkills` rather than a second parser. *Beaten:* a second
    `FAKEAGENT_*` variable listing the names, which would have made the
    directory incidental to the assertion.
80. **2026-09-20 — The leg does not pin MSYS's argv rewriting.** It asserts
    only that `--message-file -` delivers `/gate-skill hi` byte for byte,
    which is the guarantee #501 exists to give and the one that must hold on
    all three platforms. Asserting that the *positional* form is mangled
    under Git Bash would pin the behaviour of a runtime this repository does
    not own, and would have to assert opposite outcomes per platform.
81. **2026-09-20 — One daemon for the whole leg**, under
    `FAKEAGENT_SCENARIO=skill-human` and `FAKEAGENT_VERSION=2.1.277`. The
    fake's codex dialect falls through to `codexSuccess` for a scenario name
    it does not know, and the claude listing probe answers `initialize` ahead
    of any scenario dispatch, so the claude invocation legs, the codex
    verbatim leg and every listing share one start. The version is pinned
    because the fake reports 2.1.224 by default, below decision 40's floor:
    without it every claude assertion in the leg would be about the
    adapter's positive no rather than about the daemon. No shipped adapter
    has a version floor of its own, so one value serves all three dialects.
82. **2026-09-20 — Leg 12 is standalone and selectable**, a function beside
    `chat_on_a_task` with a `12` arm in the selector, run last when nothing
    is selected — leg 11's shape, for leg 11's reason. `CLAUDE.md`'s gate
    list is not touched: its one-line description of `m14` ("chats end to
    end (task 067)") still describes the script, and the acceptance
    criterion only asks for a mention if that line changes.
83. **2026-09-20 — The init line is a classifier, never a change detector**
    (124.16). #512's step 3 — compare the turn's init `skills` against the
    cache and keep the entry when they match — is dropped, and 124.9's
    unconditional invalidation stands untouched, so #512's second acceptance
    criterion is withdrawn with it. The line describes the state at the turn's
    *start*, so a turn that writes `.claude/skills/foo/SKILL.md` reports a set
    identical to the cache's; keeping the entry on that basis would hide `foo`
    until the next turn or the five-minute `skillTTL`, which is the exact case
    the invalidation exists for. One `claude` subprocess per turn-then-look
    does not buy that back. *Beaten:* relaxing invalidation for claude only.
84. **2026-09-20 — The bundled set is keyed by the binary, not the
    directory.** A *listing* is per directory, which is why `skillKey` carries
    one, but what a CLI *bundles* is a property of the installed build. Storing
    the classification against the binary identity alone means the first turn
    on that claude — in any directory, in any chat — restores the bundled rows
    everywhere, so a freshly created chat is not penalised for being new. This
    largely answers #512's own open question: bundled skills still need one
    turn to appear, but not one turn *per directory*. *Beaten:* a per-directory
    set, keyed like the listing it filters.
85. **2026-09-20 — The names stay off the wire.** `RunHeader` gains
    `Skills []string` and nothing else; `Commands` is not added, because after
    decision 83 nothing would read it. `headerChunk` and the normalized
    transcript record keep emitting `work_dir` and `available_tools` alone.
    Putting 84 skill and 118 command names on every `agent.run_header` chunk
    and every normalized transcript line would add kilobytes per turn for no
    reader, and the verbatim line is already in the transcript, which is the
    lossless copy. *Beaten:* publishing the arrays "for clients to group".
86. **2026-09-20 — The filter moves from the adapter to the cache.**
    `skillsFrom` keeps every `builtin` row and carries the CLI's flag onto
    `Skill.Builtin` (decision 17's rule for every field); `SkillCache.Lookup`
    is what withholds one. Filtering at probe time instead would freeze a
    directory probed before the first turn into its unclassified answer for a
    whole `skillTTL`, with nothing left to reclassify. *Beaten:* a second probe
    once a turn has reported.
87. **2026-09-20 — `system/commands_changed` is not normalized.** It is seen
    and deliberately left as `EventUnknown` under §9.2's tolerant-parsing rule:
    with invalidation unconditional there is nothing for it to signal, and a
    bundled skill ships with the binary and is never discovered mid-session.
    §9.2 records this rather than leaving it to be rediscovered. *Beaten:*
    normalizing it as a second invalidation signal.
88. **2026-09-20 — Clients are not changed.** The wire gains `builtin` on a row
    and `builtin_skills` on the body, which is all #512 asked for — a client
    *can* group. The TUI skill panel (124.13) and `vincent chat skills`
    (124.11) keep their current shape. This does not revive decision 7's
    beaten alternative: only the bundled rows are restored, and only after a
    turn has proven them bundled. *Beaten:* a `BUILTIN` column and a grouped
    panel in the same piece of work.
89. **2026-09-20 — Task runs are not wired to the cache.** The classification
    is per binary, so a chat turn supplies it for every directory; wiring
    `internal/taskrun` to the cache would be a new dependency for no new
    coverage. The chat runner reports through a nil-tolerated dep beside
    `InvalidateSkills`, so `internal/chatrun` still never imports the cache.
    *Beaten:* reporting from the task engine too.

90. **2026-09-20 — Inline takes both `↑` and `↓`.** While an inline list is
    up both arrows walk the matches and neither moves the draft's cursor;
    `esc` gives them back. This is Claude Code's own gesture and the keys
    124.13's browse list already uses, so one pair of arrows means one thing
    wherever the list is drawn. It amends **task 071 decision 4** ("The
    composer keeps `↑`/`↓` for editing a multi-line message") for exactly
    that state and for no other: with no list up, and after `esc`, the arrows
    edit. *Beaten:* #510's `↓`-only proposal, which leaves the highlight
    walkable in one direction only — past a match there is no way back
    without `esc` and retyping — and which buys almost nothing under
    `leading`, where the list is visible only on row 0 inside the first token
    and `↑` does nothing in a textarea anyway, while being confusing under
    `anywhere`. #500 landing made the cost real rather than paste-only, which
    is why both costs are written into §15 view 9 and the 071 amendment is
    spelled out there rather than left implicit.
91. **2026-09-20 — A typed sigil may ask the daemon, but never speaks up.**
    The first inline open in a view fetches through `apiclient.ChatSkills`
    exactly as the `tab` path does, on the same `chatSkillsTimeout`, but
    draws no loading row; a fetch that fails, or that answers a
    `list_verdict` other than `supported`, writes **nothing** to the note
    line and latches inline off for the chat until `forget()` drops the
    cached answer or the human presses `tab`. The human did not ask for that
    probe — a cold cache spawns the agent CLI, and under codex's
    `$`/`anywhere` a `$HOME` in prose would otherwise turn the note line red.
    The latch is also what stops a failing probe being re-fired on every
    keystroke. `tab` keeps 124.13's explaining note verbatim.

    The probe's *trigger* falls out of the same rule: the sigil comes off the
    wire and is hard-coded nowhere, so before an answer is in hand there is
    nothing to test a token against but its shape — one non-alphanumeric rune
    and then a name (`chatSkillSigilShaped`). A lone sigil is deliberately not
    enough, because one punctuation rune says nothing about whose sigil it
    is. Every keystroke after the answer arrives is filtered by the answer's
    own sigil, and a shape this accepts that no adapter honours costs exactly
    one silent probe.
92. **2026-09-20 — The unmatched-leading hint is only for name-shaped
    tokens.** The dim note — `/foo is not a skill claude reported for this
    chat — it is sent as typed` — appears only where the text after the sigil
    carries no path separator. `/tmp/notes.md` and `/Users/x` are silent,
    `/tdds` is hinted. As #510 wrote it, vincent would nag about exactly the
    case it cites Claude Code for keeping quiet about. It stays a hint and
    never a block (decision 9, task 025 decision 5), and it is still not
    shown under `anywhere`, where a sigil is ordinary text more often. Both
    `/` and `\` count as separators on **every** platform rather than `\` on
    Windows alone: a draft is prose a human may write about any machine, and
    a per-host rule would make the same message say two things — and a test
    of it pass on one CI leg and fail on another.
93. **2026-09-20 — A bare sigil opens the full list under `leading` and not
    under `anywhere`.** `/` on an otherwise empty first token opens every
    row, which is Claude Code's behaviour and almost always what it means. A
    bare `$` mid-prose does not: it is a shell variable far more often than
    the start of an invocation, and an empty filter matches every row, so the
    hide-on-no-match rule could not quiet it. Under `anywhere` the list opens
    once one character follows the sigil.
94. **2026-09-20 — Inline is a mode of the same list, with its own binding
    context.** `chatSkillList` gains a mode; the rows, the ranking, the
    hostile-row guard, the window arithmetic and the renderer are shared
    unchanged, and only the title line's tail differs. `bindingContext`
    returns `ctxChatSkills` for browse and a new `ctxChatSkillsInline` while
    inline is up, because `backspace` means "shorten the filter, and close
    the list when it is already empty" in the one and "edit the draft" in the
    other, and a `?` pane that says the first while the second is true is a
    lie. That is the reason `ctxChatSkills` is its own context in the first
    place. The structural difference underneath it is that **the composer
    keeps the keyboard**: `updateInlineSkillsKey` reports whether it took the
    press, and everything it did not take reaches the draft, after which
    `syncInlineSkills` recomputes the list. `/` and `$` are therefore never
    `case` labels, which is also what keeps a rebound `filter` operation
    irrelevant here and `TestEveryMatchedKeyIsRegistered` green.
95. **2026-09-20 — Accepting inline replaces the token under the cursor**,
    rather than inserting at it: the invocation plus one space, cursor after
    the space. Without it, `/co` + accept would yield `/co/code-review`.
    Browse's insert-at-the-cursor accept (§15 view 9) is unchanged — it has
    no token to stand in for. The replacement goes through the composer's own
    key handling, as synthetic `backspace` presses after a `SetCursorColumn`:
    the textarea exports neither a delete-word nor a way to put the cursor on
    a row, `SetValue` resets the widget and parks the cursor at the very end
    of the new value, and `CursorUp` walks *wrapped* lines, so counting rows
    back to the token's would land elsewhere entirely on a draft that wraps.
    The trailing space is unconditional, so a token that already had a space
    after it ends up with two; that is predictable, and the alternative is a
    rule about the draft's punctuation that the accept would have to guess.

## Open questions

Each of the parent's remaining open questions belongs to the item that has to
answer it, so none is lost:

| #496 open question | Owner |
|---|---|
| 1 — Restricted posture: accept that a human-invoked skill's `allowed-tools` widens a restricted claude turn, or pass `--disable-slash-commands` in restricted mode | closed by 124.6 (#502): decision 77 |
| 2 — Built-in rows: omit every `builtin: true` row, or list them flagged | closed by 124.7 (#503): decision 42 |
| 3 — claude listing floor: 2.1.277 only, or the whole `[2.1.0, 3.0.0)` input family | closed by 124.7 (#503): decision 40 |
| 4 — Hooks in the probe: suppress SessionStart hooks and MCP servers, at the cost of missing hook-installed skills | closed by 124.7 (#503): decision 41 |
| 5 — The skills key: `tab` over `f2` | closed by 124.13 (#509): decision 69 — `tab`, with `f2` as its alias |
| 6 — The `quiet` level: does a human-invoked skill show there | 124.12 (#508); settled by decision 37 |
| 7 — Cache TTLs: 5 min for a clean list and 1 min for a failed one | closed by 124.9 (#505): decision 54 |
| 9 — `/clear` under pass-through: should a conversation reset become a visible record | closed by 124.6 (#502): decision 78; the record itself is 124.20 |
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
- [x] 124.4 (#500) Fix the chat composer's newline keys, a pre-existing bug.
  Merged as #519. ✓ 2026-09-19
- [x] 124.5 (#501) `--message-file` on `chat send` and `chat start`, fixing
  Git Bash's `/name` rewrite, and the quoting rules documented. The shared
  `readInputFile` bounded read (decision 37); `chat send`'s message argument
  made optional; the CLI reference's `--message-file` and quoting passages, a
  troubleshooting entry for the Git Bash symptom, and a §12.1 row. No wire
  change. ✓ 2026-09-19
- [x] 124.6 (#502) Amend §5.5, §9.4 and §16 on pass-through and restricted
  skills, and record the owner's posture decision. §5.5's
  "Skills in a chat's message" block — verbatim pass-through, unknown names,
  `user-invocable: false`, leading whitespace, position and stacking, a skill
  that asks, and `/clear`'s consequence; §9.4's and §16's dated notes on what
  `allowed-tools` does to `restricted` and why `Skill` stays out of the
  allow-list; the same property in prose in `docs/security-model.md`,
  `docs/guides/agents.md` and `docs/guides/workflows.md`. Decisions 69 and 70,
  closing the parent's open questions 1 and 9. Documentation only: no argv
  change, no new capture. ✓ 2026-09-20
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
- [x] 124.9 (#505) The skill cache, `chatrun.Workspace`,
  `GET /v1/chats/{id}/skills`, the `apiclient` types and the MCP exclusion.
  Depends: 124.1. `agent.SkillCache` (key, TTLs, single flight, 64-key LRU,
  `Invalidate`); `chatrun.Runner.Workspace`, `Deps.InContainer` and
  `Deps.InvalidateSkills`, invalidated at every turn ending past placement
  and before the ending is recorded; `taskrun.Runner.ChatInContainer`; the
  route, `apiclient.ChatSkills`, the `mcp.Excluded` row and the daemon
  wiring. No migration and no event were needed. Spec §5.5, §9.1, §9.6, §11,
  §13.2, §13.3, §13.4 and §16 amended. ✓ 2026-09-19
- [x] 124.10 (#506) `--replay-user-messages` on claude chat turns, so a
  human-invoked skill shows as `agent.skill{by:"human"}`. Depends: 124.2.
  `RunSpec.ReportInvocations`, the flag in `claude.buildArgs` under input
  mode, the four replay mappings and the widened `claim` guard in
  `claude/stream.go`, `chatrun.runTurn` setting the field, and the
  `skill-human` and `skill-inject-denied` fakeagent scenarios. Four scrubbed
  2.1.277 captures committed. No migration, no new event type, no API, DTO or
  MCP change, and no change to any task step's behaviour. Spec §9.2 amended.
  ✓ 2026-09-20
- [x] 124.11 (#507) `vincent chat skills <chat-id>`. Depends: 124.9.
  `newChatSkillsCmd` in `internal/cli/chatskills.go`, registered on the chat
  tree, with `--refresh` and `--json`: the `SKILL`/`INVOKE`/`ARGS`/`DESCRIPTION`
  table on stdout and every verdict, problem and invocation line on stderr at
  exit 0. Decisions 60–63; the reverse pointer on `vincent skills`; spec §12.1,
  `docs/reference/cli.md`, `docs/features.md` and `docs/guides/agents.md`
  amended. No wire change. ✓ 2026-09-20
- [x] 124.12 (#508) Draw `agent.skill` at each verbosity level, and in
  `vincent task|chat transcript` (decision 28). The pane and the chat body
  draw `▸ skill <name> <args>`, `(forked)` for a forked skill and
  `▸ skill <name> failed: <error>` for a refusal; a model's load is drawn in
  its `Skill` call's place, paired by `call_id`, and no longer splits an
  unrecognized-line count. The printer prints `> skill …` and
  `! skill … failed: …` on the same pairing within each fetch. Decisions
  37–39; spec §15, `docs/guides/tui.md` and `docs/reference/cli.md`
  amended. ✓ 2026-09-19
- [x] 124.13 (#509) The skill list above the composer, opened with `tab`,
  inserting the picked invocation. Depends: 124.11, 124.7.
  `internal/tui/chatskills.go` (the rows, the filter buffer, the ranking, the
  window, the accept and the rendering, reusing `pickerWindow`, `window()`
  and `readerPicker`'s `› ` marker); `chatSkillList` on `chatView` with the
  `tab`/`f2` cases, the load command, the terminal-chat pre-check, the wheel
  no-op and the ordering that keeps it shut under the §7.4 popup and the
  close confirmation; the list's lines in `footerLines` and `tab skills` in
  the in-view hint; a `ctxChat` row and the new `ctxChatSkills` context;
  `keymap.fixed` rows for the surface and the `f2` alias. No handler contains
  `case "/"`. No daemon, store, API, MCP, migration or wire change. Decisions
  69–76; spec §15 view 9 and §15 Keys, `docs/guides/tui.md` and
  `docs/features.md` amended. ✓ 2026-09-20
- [x] 124.14 (#510) The same list, opened inline as the human types the
  adapter's sigil. `internal/tui` and documentation only: the mode flag, the
  token reader off the composer's `Word()`/`Line()`/`Column()`, the position
  and bare-sigil rules, the name-shaped test behind the hint, the `esc` and
  failed-probe suppressions, the mode-aware title line; `syncInlineSkills`
  after every composer update and inside `paste`, the inline key arm, the
  replace-the-token accept and the after-accept note, and the note line's
  third arm in `footerLines`; a `ctxChatSkillsInline` context with four rows
  and its six `keymap.fixed` rows. No new key literal, no `case "/"` and no
  `case "$"`. No daemon, store, API, MCP, migration, wire or CLI change.
  Decisions 90–95; spec §15 view 9 (amending task 071 decision 4 for the
  list-up state) and §15 Keys, `docs/guides/tui.md` and `docs/features.md`
  amended. ✓ 2026-09-20
- [x] 124.15 (#511) `m14` leg 12, end to end on all three operating systems.
  Each adapter's own listing mechanism against the fake; the directory the
  list is about, a linked chat's being its task's worktree; the per-directory
  cache and `?refresh=`; a `/name` reported back as one `agent.skill` by the
  human, on a linked chat's first turn too; a `$name` that reaches codex
  verbatim and produces none; `409 invalid_state` on a closed and on an
  archived chat; and `vincent chat send --message-file -` delivering a
  leading `/` byte for byte. The fake claude gained the cwd-derived listing
  decision 44 parked here. Decisions 79–82; no wire, CLI or spec change.
  ✓ 2026-09-20
- [x] 124.16 (#512) Read the claude init line's `skills` into the run header
  and restore the bundled skills from it. `RunHeader.Skills` and
  `Skill.Builtin` carry the two CLI facts; `skillsFrom` stops dropping
  `builtin` rows; the skill cache gains a per-binary bundled registry, a
  `ReportBundled` the chat runner calls with a turn's init names, and
  serve-time filtering; the route gains `builtin` on a row and
  `builtin_skills` on the body. The invalidation of 124.9 is untouched — the
  init line classifies, it does not detect change — and `commands_changed`
  stays `EventUnknown`. Decisions 83–89, with 7 and 42 amended; spec §5.5,
  §9.1, §9.2, §9.6 and §13.2 and `docs/reference/api.md` amended. `m14` is not
  extended: leg 12 already drives listing and invocation end to end, and this
  is a function of the cache and the route, provable over the real handlers.
  ✓ 2026-09-20
- [ ] 124.17 (#513) Probe through the task's container instead of answering
  `unknown`. Depends: 124.9, 124.7.
- [ ] 124.18 (#514) Investigate whether ACP should become cursor's listing,
  and record a decision. Depends: 124.1.
- [ ] 124.19 (#515) The `tui-chat-skills.png` tape — the list now has two
  ways in, so the tape shows both: `tab`'s browse list and the inline one a
  typed sigil opens (124.14). Depends: 124.14.
- [ ] 124.20 (no issue yet) Surface claude's `conversation_reset` as a visible
  chat record, so a `/clear` in a chat is marked where it happened rather than
  leaving a transcript the agent no longer shares (decision 78). Depends: none.

The requirement's two done criteria are met once 124.14, 124.11, 124.10, 124.3
and 124.15 have landed — all five of which have, 124.14 last, on 2026-09-20.
124.16, 124.17 and 124.18 widen coverage after that, and 124.19 and 124.20
remain.

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
- 124.5: `TestChatMessageFileReachesTheStoredPrompt` runs the real binary, a
  real daemon and fakeagent turns: `/x hi` on stdin and a file holding a
  leading `/`, a `$name`, a double quote, an embedded newline and a trailing
  `\n` are each the turn's stored `prompt` exactly. Against a recording stub,
  `TestChatSendMessageFileIsSentByteForByte` and
  `TestChatStartMessageFileSendsTheFirstMessage` decode the body the CLI
  POSTed — a BOM and a lone `\n` included; `TestChatMessageSourceIsExactlyOne`
  holds decision 46, `TestChatMessageFileRefusals` decisions 48–50 on both
  commands with no request and no chat, and `TestChatMessageFileBound`
  decision 47 at exactly the bound and one byte over. `TestReadFieldsFileBound`
  is unchanged against the shared helper.
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
- 124.9: the cache is proven in `internal/agent` against `agenttest.StubSkills`
  with an injected clock. `TestSkillCacheTrustsACleanListForItsTTL` and
  `TestSkillCacheFailureWithNoListIsUnknown` hold decision 54's two TTLs;
  `TestSkillCacheRefreshIsSingleFlight` N concurrent refreshes to one probe
  under `-race`, and `TestSkillCacheReaderNeverWaitsBehindAProbe` the
  lock split; `TestSkillCacheFailureKeepsThePreviousList` T4.22;
  `TestSkillCacheUnsupportedIsACleanNo` decision 16 through wrapping;
  `TestSkillCacheCallerCancellationIsNotStored` a hung-up caller;
  `TestSkillCacheEvictsTheLeastRecentlyUsed` decision 55;
  `TestSkillCacheInvalidateDropsOneDirectory` and
  `TestSkillCacheInvalidateOutlivesAProbeInFlight` invalidation;
  `TestSkillCacheNewBinaryIsAMiss` the binary-identity key. In `chatrun`,
  `TestWorkspace*` pin the free chat, the linked task read fresh,
  `ErrLinkedTaskNoWorktree` and the `InContainer` bit;
  `TestTurnEndingInvalidatesItsDirectory`,
  `TestLinkedTurnInvalidatesTheTasksWorktree` and
  `TestStartFailureInvalidatesToo` run real fakeagent turns and see the
  invalidation land before the ending is stored;
  `TestPlacementFailureInvalidatesNothing` and
  `TestNilInvalidateSkillsIsTolerated` the edges. In `taskrun`,
  `TestChatInContainerReadsSettingsOnly` holds decision 56 with a fake
  runtime that records zero lookups. The route's `TestChatSkills*` walk §5.5's
  table row by row — the stub's recorded `WorkDir` for a free and a linked
  chat before any turn, the three terminal states with zero probes even
  under `refresh`, `task_has_no_worktree`, decisions 57 and 58, every probe
  outcome, a container-run chat with zero probes, a non-default sigil's
  `invocation`, order, duplicates and `[]` never `null`, the TTL and
  `refresh`, and a real turn's ending forcing the next probe.
  `TestChatSkillsLive*` decode every field over the real handlers, and
  `TestMCPExcludesDestructiveAdminByName` carries the new row.
- 124.10: `TestBuildArgs` carries the three argv cases — set plus input mode,
  set without it, and the case that matters, an input-mode run that sets
  nothing and keeps a task step's argv — and `TestLaunchSeamCarriesTheRun` a
  fourth launching one. `TestHumanInvokedSkill`,
  `TestHumanInvocationArgumentsAreVerbatim`, `TestRefusedInjectedCommand` and
  `TestRefusalTakesTheLastReplayedName` hold decisions 65 and 67 over the
  committed captures; `TestEchoesAreNotSkills` decision 64 and the
  `control_response` mapping, asserting no replayed line stays
  `EventUnknown`; `TestReplaysAreInNoSkillScope` decision 68 against an
  interleaved model load; `TestMalformedReplayIsAnEcho` T4.17;
  `TestReplaysCarryNoBody` T4.16. `TestFixtureEventTypesArePinned` pins all
  four new captures. `TestSkillHumanScenario` and
  `TestSkillInjectDeniedScenario` tie the fake to the flag rather than to the
  scenario. In `chatrun`, `TestHumanSkillReachesTheChat`,
  `TestPlainMessageEchoIsNotRaw` and `TestAnsweredQuestionEchoIsNotRaw` run
  real turns and read the published chunks and the transcript;
  `TestHumanSkillChunkMatchesItsRecord` compares the SSE chunk with the
  refetched record over the real handlers.
- 124.11: `internal/cli/chatskills_live_test.go` drives the command against
  the real handlers over `httptest`, with a real `agent.SkillCache` over the
  `agenttest` stubs. `TestChatSkillsCommandPrintsTheList` holds decisions 60
  and 63 — the API's order, a duplicated name whose two rows differ only by
  `INVOKE`, a blank cell rather than a `-`, and stdout carrying neither the
  invocation line nor a warning; `TestChatSkillsCommandUnsupportedExitsZero`
  and `TestChatSkillsCommandUnknownPrintsTheProbeError` the two non-`supported`
  verdicts on stderr at exit 0 with no table;
  `TestChatSkillsCommandInvokesWhatItCannotList` decision 2's shape;
  `TestChatSkillsCommandInvokeLineKeysOffTheSigil` decision 61 over `$`/anywhere
  and `/`/leading, asserting the adapter is never named;
  `TestChatSkillsCommandJSON` every field and `[]` never `null`;
  `TestChatSkillsCommandRefreshReachesTheDaemon` counts `?refresh=true` on the
  wire; `TestChatSkillsCommandRefusalsExitOne` the 404, the terminal chat and
  the linked task with no worktree, and
  `TestChatSkillsCommandWithNoDaemonExitsTwo` the shared `withClient` path.
  `TestDocsClaimsEveryCommandIsOnTheCLIPage` and `…EveryFlagIsOnTheCLIPage`
  force the command and both flags onto `docs/reference/cli.md`.
- 124.16: `TestParseInitReadsSkills` fills `Skills` from the `2.1.263` and
  `2.1.268` captures — their length, their opening name and the whole array in
  the CLI's order — proves each holds `simplify`, `loop` and `run` and neither
  `clear` nor `compact` while `slash_commands` holds both, and leaves it empty
  for the `2.1.226` captures; `TestInitLineDecodesNothingElse` proves no
  command name can reach the header from any of the four arrays that stay
  undecoded. `TestHeaderChunkShape` carries a header with skills and still
  marshals to `work_dir` plus `available_tools`, and
  `TestNormalizeRunHeaderAndResultMetadata` asserts the normalized line holds
  neither name nor key — decision 85 on both wires.
  `TestInitializeFixtures`'s "builtin was listed" assertion is inverted: every
  `builtin` row of both 2.1.277 captures comes back, flagged, in the CLI's
  order beside the non-builtin rows it already pinned, and
  `TestListSkillsAgainstTheFake` pins the fake's `compact` row with its flag.
  The cache is `internal/agent/skillbundled_test.go`:
  `TestBundledRowsAreWithheldUntilATurnNamesThem` walks both states over one
  stored listing with no second probe;
  `TestBundledSetIsKeyedByTheBinaryNotTheDirectory` serves a second directory
  the restored rows with no turn of its own and lets another binary identity
  inherit nothing (decision 84);
  `TestBundledStateIsEmptyWhenTheQuestionDoesNotArise` covers a listing with no
  `builtin` row and an unsupported build;
  `TestReportBundledIsToleratedWhereItCannotAct` the nil receiver, the nil
  adapter and the empty report; `TestBundledRegistryIsBounded` the bound and
  its eviction order; and `TestBundledFilteringNeverMutatesTheCachedList` that
  serve never shortens the shared slice. In `internal/chatrun`,
  `TestClaudeTurnReportsItsInitSkills` is the one report per turn with its
  adapter and order, `TestATurnWithNoInitSkillsReportsNothing` the claude build
  that sends no array and the codex and cursor turns that never could,
  `TestNilReportBundledSkillsIsTolerated` the missing dep, and
  `TestReportingDoesNotRelaxInvalidation` is decision 83's guard — a turn
  reporting exactly the cached names still invalidates its directory.
  `TestChatSkillsLiveDecodesBundledSkills` and
  `…LeavesBundledEmptyWhereItCannotArise` decode both fields over the real
  handlers, and codex's `TestListSkillsAgainstFakeAgent` asserts no row of its
  listing ever comes back `Builtin`.
- 124.14: `internal/tui/chatskills_test.go`'s inline block drives the
  composer with real key presses rather than poking the struct, because the
  draft *is* the trigger. `TestChatSkillsInlineOpensOnTheTypedSigil` holds
  the open, the filter, the tier-1 plugin bare-name match, the untouched
  draft, the empty highlight and the binding context;
  `TestChatSkillsInlinePosition` each adapter's own rule, including a `$rev`
  on the third line of a multi-line draft;
  `TestChatSkillsInlineBareSigil` decision 93 both ways;
  `TestChatSkillsInlinePathIsSilent` decision 92's three cases and that the
  hint never blocks `enter`;
  `TestChatSkillsInlineExactMatchThenSpaceHides` the second hide rule;
  `TestChatSkillsInlineEscSuppressesAndReturnsTheArrows` decision 90 in both
  directions and the suppression's lifetime;
  `TestChatSkillsInlineTabReplacesTheToken` decision 95, the cursor's column
  and the rest of the draft byte for byte; `TestChatSkillsInlineEnter` both
  `enter`s; `TestChatSkillsInlineAcceptNote` the after-accept line and its
  expiry; `TestChatSkillsInlinePasteOpensTheList` the paste path;
  `TestChatSkillsInlineNeverOpensUnder` the five states it must stay out of;
  `TestChatSkillsInlineFetchIsSilentAndLatched` decision 91, counting
  requests on a server that only 500s; and
  `TestChatSkillsInlineHelpIsItsOwnSurface` decision 94 from the `?` pane.
  `TestEveryPanelKeyIsHandled` gains the four `ctxChatSkillsInline` probes,
  `TestEveryMatchedKeyIsRegistered` and the 124.13 and task 071 decision 4
  tests pass unchanged. No gate change: `m14` leg 12 is the daemon-side
  proof and the TUI is not gated.
- 124.15: `scripts/m14-gate.sh`'s leg 12 is the proof, and
  `VINCENT_GATE_SCENARIO=12` runs it alone. Against one daemon it asserts
  claude's verdicts, sigil, position and `work_dir` before any turn with the
  committed skill's description, hint and `/gate-skill` invocation; that a
  plain `GET` serves the cached list while `?refresh=true` picks up a skill
  written into the worktree and reports a later `probed_at`; that
  `/gate-skill hello` finishes `done` and normalizes to exactly one
  `agent.skill{name, args, by:"human"}` with no `agent.raw`; codex's
  `$gate-skill` listed through the app-server, reaching the fake verbatim and
  producing no `agent.skill`; cursor `unsupported` with a reason, `[]` and
  `/`/anywhere; a linked chat listing `task-skill` from its *task's* worktree
  while the other project's free chat does not, and invoking it on its first
  turn, which is 124.3's block ordering end to end; `409 invalid_state` after
  `close` and after `archive`; and `vincent chat skills --json` agreeing with
  the API while `printf '/gate-skill hi' | vincent chat send --message-file -`
  stores the prompt byte for byte — the Windows leg's own assertion.
  `TestClaudeListsTheWorkingDirectorysSkills` is the fake's half of decision
  69, through the real claude lister: the seeded rows appended in directory
  order with their hints, an entry with no front matter skipped, and a
  directory without `.claude/skills` listing exactly what it always did.
