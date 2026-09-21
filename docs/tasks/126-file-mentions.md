# 126 — File mentions in the chat composer

Issues [#544](https://github.com/lezli01/vincent/issues/544) (the set) and
[#545](https://github.com/lezli01/vincent/issues/545) (this document and the
records in the spec). Spec §5.5 (file mentions in a chat's message), §9.1,
§9.2, §9.3 and §9.7 (the adapters), §13.2 (the route, 126.6), §15 (the
composer, 126.13), §16 (containers, full-auto). Related: task 124 (chat
skills), whose pass-through rule and inline picker this work reuses and does
not reopen; task 061 (containers, identical paths inside and out).

## What this is

An `@` file picker in the chat composer, and the records that make offering it
honest. claude expands an `@path` mention into the model's context before the
model sees it, without a tool call and without leaving a trace in the
normalized stream; codex and cursor do not expand, though with tools allowed
each reads the mentioned path itself in one tool call. The picker therefore
opens and inserts on all three adapters and a note says which of the two the
human is getting (decision 6). Underneath it sit a daemon-built mention
string, a chat-file enumeration route, and a data-neutral inline list core
extracted from task 124's skill picker.

126.1 — this document and the §5.5 and §9.x records — is the only part that
writes no code. It exists because the adapter code the rest of the set writes
carries `spec §9.x` comments that have to point at something real, and because
the per-CLI behaviour is worth recording even if the picker is never built.

## Decisions

Settled with the author on 2026-09-21, carried over from #544's "Decisions
taken here" with their reasoning and the alternative each beat. Two of #544's
own citations are wrong; they are corrected below rather than copied.

1. **A file row carries a daemon-built `mention` string, not just a path.**
   The research findings contradicted each other — one proposed
   `[{"path": …}]`, two required a ready-to-insert string — and the daemon
   wins: the quoting rule (`@"…"` when the path contains a space, and **claude
   only**) must have exactly one definition, and it belongs in the adapter, the
   way `internal/api/chatskills.go` already builds a skill row's `invocation`
   from `SkillInvoker.Invocation` rather than letting each client spell it.
   The extra bytes are bounded by the response cap. *Beaten:* a bare path per
   row, with every client reimplementing the quoting rule.

2. **Mentions are workspace-relative.** That removes every path-translation
   question at once: the worktree is bind-mounted into a container at its own
   absolute host path (`internal/container/doc.go`,
   `internal/taskrun/container.go`), and on macOS an absolute worktree path
   always contains a space and so would need quoting on every single mention.
   §5.5 records the underlying CLI fact that makes this safe — claude resolves
   a relative mention against the process cwd, and `Command.Dir` is always a
   repository toplevel. *Beaten:* absolute paths.

3. **Enumerate on the host, even for a containerized task.** A scoped
   departure from **task 124 decision 11** ("A container-run linked chat is
   answered `unknown` in v1", *amended 2026-09-21 by 124.17* — the host is
   never a fallback), with **task 124 decision 96** (the skills route asks the
   container runtime on every call) as its companion. The rule is unchanged
   **for skills**: decision 11 exists because an image's skill directories are
   a different machine's, so a host probe would list skills the chat's agent
   will never load. Files are not that case. Paths are identical inside and
   out (`internal/container/doc.go`, task 061 decision 2;
   `internal/taskrun/container.go`) because the worktree is bind-mounted at its
   own absolute host path, so a host listing names the same bytes at the same
   path the agent will resolve. The case is narrow to begin with: §16 says
   chats are not tasks and keep running on the host, so a container enters the
   question only for a *linked* chat whose task is containerized — the same
   case 124.17 probes skills through. *Beaten:* enumerating inside the
   container, which buys nothing here and costs a runtime call per request.
   #544's second argument for this decision, that an image is "only ever
   promised to carry the agent CLI", is **not** part of the reasoning: it is
   false (see the corrections below). Task 124 is not edited by this work.

4. **Fetch once, filter locally**, with a `limit` and a `truncated` flag. The
   picker filters the fetched set as the human types instead of re-querying.
   *Beaten:* a query parameter per keystroke.

5. **No server cache, therefore no `?refresh=`.** The two were recommended
   together and are inconsistent with each other; a 10–30 ms git call does not
   earn a second invalidation surface. The client's cache is the only cache: it
   re-fetches when the picker opens and drops on any `chat.*` event. That is
   also the best answer available to the linked-chat staleness gap —
   `matchesChat` (`internal/api/chatstream.go`) carries only `chat.*` events,
   never `task.*` or `step.*`, so a task step writing a file in the shared
   worktree emits nothing the chat stream sees. *Beaten:* a server cache plus
   an explicit refresh parameter.

6. **The verdict gates the note, never the picker.** The picker opens and
   inserts on all three adapters, because a mentioned path still gets read by
   codex and cursor as a tool call (§5.5). What differs is guaranteed context
   against a hint, and that is what the note says. *Beaten:* hiding the picker
   on adapters that do not expand, which would deny a useful hint on two of
   three.

7. **A shared `chatInlineList` core plus a typed `chatFileList`.** Not a third
   mode on `chatSkillList`, which would widen `canInvoke`, `probeFailed` and
   `chatSkillsNoInvokeReason` into a list that has no verdicts and no probe;
   and not a fork, which would duplicate four comments carrying recorded
   decisions. Keeping the core data-neutral also leaves the other two
   `textarea` composers — the new-task objective and the follow-up prompt — a
   wiring job rather than a rewrite, though neither is in this set: the
   new-task form has no worktree and no task at the moment the objective is
   typed, so "the files of the next turn's directory" has no referent there.
   *Beaten:* a third mode, and a fork.

8. **Bare `@` opens nothing, and nothing is said on no match.** Task 124
   decision 93's `anywhere` branch applies, since `@` is positionally anywhere
   (§5.5), and `@lezli01` is a GitHub handle far more often than a missing
   file — decision 92's rule with more force. *Beaten:* opening the picker on
   the bare sigil, and an empty-state message.

9. **If an adapter ever reports `invoke_sigil: "@"`, the wire's sigil wins**
   and the file picker stands down for that chat. §5.5 makes the adapter's
   word authoritative about its own syntax, and honouring it is one `if`.
   *Beaten:* assuming `@` is free for files on every adapter forever.

10. **No `keymap.Op`.** `@` is never matched as a key, so there is nothing to
    rebind; the binding context and its `keymap.fixed` rows land inside the
    picker's own pull request. This follows task 118 decision 1, the way task
    124 decision 12 did for `tab`. *Beaten:* a rebindable operation.

11. **The server drops control-character and non-UTF-8 rows.** Go's
    `encoding/json` replaces invalid UTF-8 with U+FFFD and returns no error, so
    such a path cannot cross the wire honestly — a row that arrives corrupted
    is a mention that will not resolve. No size field in v1. *Beaten:*
    forwarding them and letting the client decide.

12. **No migration.** A mention is text in `chat_turns.prompt`, which is
    already `TEXT NOT NULL` (`internal/store/migrations/0022_chats.sql`).
    *Beaten:* a column recording what a turn mentioned, which would duplicate
    the prompt and could not be kept true — §5.5 records that claude's
    expansion leaves no trace vincent can read.

13. **`docs/features.md` gains its sentence once**, in 126.13 (#556). Two of
    the research findings both claimed the line. *Beaten:* a sentence per
    issue, which would land the same claim twice.

14. **`tracked` on the wire is deferred.** Rows stay objects rather than
    strings so the field can be added later without a breaking change.
    *Beaten:* shipping it in v1 before anything filters on it.

Decisions 15–21 were settled with the author on 2026-09-21 in 126.5 (#549),
which is where the enumeration underneath decisions 3 and 11 is actually
written. They are implementation choices inside that subtask; nothing above is
reopened.

15. **The enumeration lives in `internal/worktree`, not `internal/gitx`.** It
    is `ListBranches`' sibling in every respect that matters: `Manager` already
    holds the `*gitx.Git` runner, the API already has `s.deps.Worktrees` wired,
    and — decisively — `worktree` owns the `*worktree.Error` / `ReasonOf`
    reason vocabulary, so 126.6's "a missing workspace is 400
    `validation_failed`" is the mapping `handleProjectBranches` already
    performs, not a new error taxonomy. `gitx` stays the thin single door phase
    1 made it and gains only the runner. *Beaten:* a second enumerator in
    `gitx`, with its own reasons.

16. **A raw-output runner, rather than changing `Run`.** `gitx.Run` returns
    `strings.TrimSpace(stdout.String())` and every existing caller depends on
    that — a SHA with a newline after it is not a SHA. A `-z` listing does not
    survive it: NUL is not whitespace, so the trailing separator is left alone
    and the split still works, but a file named ` leading.txt` comes back as
    `leading.txt`, a path that does not exist. `RunRaw` is therefore the opt-in
    and the trim stays the default, with `run` refactored to trim `runRaw`'s
    output so both keep one code path and one `*Error` construction.
    *Beaten:* relaxing `Run`'s trim.

17. **No `--deduplicate` flag; dedupe in Go.** #549 proposed sending the flag
    *and* deduping in Go, on the reasoning that the Go pass makes the coupling
    to git 2.31 disappear. It does not. git rejects an unknown long option
    outright — exit 129, usage on stderr, no listing at all — so on git 2.30
    the flag turns a degraded result (a conflicted path printed once per index
    stage) into a dead picker. vincent's floor is a startup *warning*, not a
    refusal (phase 1 decision), so 2.30 daemons are a supported configuration.
    The Go pass alone is sufficient and complete: the multi-stage rows are the
    identical path repeated, and the enumerator emits paths only, never stages.
    The three-way-conflict test still gets written — it now proves the Go path.
    *Beaten:* sending the flag as well.

18. **The dropped-row count is returned, and logged by the caller.** The
    enumerator returns `(paths, dropped, err)` and logs nothing itself;
    `worktree`'s methods take no logger and its siblings do not log. Whether
    the count reaches the wire and where the daemon-log line goes is 126.6's
    call, as is the shape of any `details` it might carry. One aggregate count,
    not a per-reason breakdown. *Beaten:* logging inside `worktree`.

19. **A missing directory is a typed error from an `os.Stat` pre-check, not
    from git's exit code.** #549's stated mechanism — "`git ls-files` exits 128
    with `fatal: cannot change to '…'`" — is what happens under
    `git -C <missing>`, not under what vincent does. `gitx` sets `cmd.Dir`, and
    Go's own `fork/exec` fails the chdir before git is ever reached: verified,
    the error is `chdir …: no such file or directory`, **not** an
    `*exec.ExitError`, so it arrives as `*gitx.Error` with `ExitCode: -1`,
    empty stderr and no exit 128 to match on. The enumerator therefore guards
    with `os.Stat` the way `requireProjectPath` does and returns
    `ReasonWorkspacePathMissing`. A new reason constant rather than
    `ReasonProjectPathMissing`: the directory here is usually a worktree and
    only sometimes the project checkout, and a reason that lies about which is
    worse than one more entry in a vocabulary that already has eleven. Any
    other git failure — a directory that exists but is not a repository, say —
    stays `ReasonGitError`, which 126.6 maps to 500.

20. **Hostile rows are dropped where they are read.** This is decision 11's
    mechanism: the predicate is the server-side twin of `chatSkillHostile`
    (`internal/tui/chatinlinelist.go` since 126.9; `chatskills.go` when this
    was written), duplicated rather than shared because `worktree` cannot
    import `internal/tui` and the client guard stays as defence in depth. Whitespace is *not* hostile — task 124 decision 71's
    reasoning applies unchanged, and here a leading space is part of the file's
    own name. *Beaten:* filtering at the route, which would leave the
    enumerator's own callers unguarded.

21. **Paths are returned exactly as git printed them.** Forward slashes on
    every platform, workspace-relative, in git's own order, with no sort, no
    case folding and no `filepath.FromSlash`. Converting would produce a
    backslash path the listing never observed, and folding case would serve a
    path that differs from the file's name. Every flag of
    `git ls-files -z --cached --others --exclude-standard` is load-bearing:
    `--others --exclude-standard` is what "the files of the project" means to a
    human typing `@`, and `-z` is the only form in which git does not C-quote
    an unusual byte (`core.quotePath=false` unquotes the high-bit path and
    still quotes the ones carrying control characters) and the only form in
    which a newline inside a filename cannot corrupt the split. No
    `--full-name` (a worktree target and a project path are both toplevels) and
    no `--recurse-submodules` (a submodule is one gitlink row). *Beaten:*
    normalizing for the client.

Decisions 22–25 were settled with the author on 2026-09-21 in 126.3 (#547),
which writes the adapter capability decision 1 depends on. They are
implementation choices inside that subtask; nothing above is reopened.

22. **`MentionPosition` is its own type**, not a reuse of `SkillPosition`.
    §5.5 records that `@` and `/` do not share a positional rule on claude —
    the mention is `anywhere`, the skill invocation is `leading` — so a shared
    type would invite a reader to assume the two move together, and 126.4
    (#548) gets its own `mention_position` wire vocabulary. *Beaten:* reusing
    `SkillPosition` with a comment, and dropping `Position` altogether on the
    grounds that all three adapters answer the same value. The field is kept
    because decision 8 keys the picker's open rule on `@` being positionally
    anywhere, and the adapter is where that fact belongs.

23. **`Expands bool` keeps its name.** It matches §5.5's own verb ("claude
    expands a mention; codex and cursor do not"), and a boolean is the honest
    shape: this is a static per-adapter fact, not a verdict with an `unknown`
    leg. *Beaten:* `ExpandsMention`, and a string enum
    (`"expanded" | "prose"`), which would add a second wire vocabulary and
    invite an `unknown` value the fact never has.

24. **A double quote inside a filename is left unquoted unless the path also
    contains a space**, and the gap is recorded in the test table rather than
    papered over. claude was never probed with one; §5.5 records that
    backslash escaping is exactly what does *not* work after claude's `@`, so
    `\"` would be a rule invented ahead of the observation. A path carrying
    both a space and a quote yields `@"a "b".txt"`, which claude will very
    likely mis-parse — the test pins that as the observed behaviour of the
    rule, not as a guarantee, and 126.2 (#557) does not settle it either: it
    is a claude parsing question, not a Windows one. *Beaten:* escaping
    embedded quotes, and returning `""` so the row is omitted, which is
    daemon-side validation of a path and contradicts task 124 decision 9.

25. **The false leg of `CanMentionFiles` gets its own stub**,
    `agenttest.StubNoMentions`, rather than a third refusal hung on
    `StubNoSkills`. That is task 124 decision 15's recorded reasoning applied
    unchanged: a stub carrying refusals from two unrelated capability families
    blurs which refusal a failing test was proving, and `StubNoSkills`' own
    comment ("implements neither ... and does nothing else at all") stays
    true. *Beaten:* a third refusal on `StubNoSkills`.

    Two smaller calls follow recorded reasoning rather than a new decision.
    `FileMention("")` returns `"@"`, because pass-through with no validation
    is task 124 decision 9 and a guard returning `""` would be the daemon
    judging a path — the empty row exists in the table to pin that, not to
    specify a refusal. And the *true* leg's stub lives in
    `internal/agent/mentions_test.go` rather than in `agenttest`: the
    capability is static and spawns nothing, so a consumer testing against a
    shared stub would be testing a constant.

Decisions 26–32 were settled with the author on 2026-09-21 in 126.9 (#554),
which draws the seam decision 7 named. They are numbered 1–7 in that issue and
the code cites them in that form — `issue #554 decision 5` — the way 124.21's
code cites `issue #553 decision 1`. Nothing above is reopened: decision 7 is
implemented here, not reconsidered, and task 124 decision 94's "inline is a
mode of the same list" is untouched, because `mode` stays on `chatSkillList`
and the core has none.

26. **2026-09-21 — The core carries data, not callbacks** (#554 decision 1).
    `chatInlineList` holds plain fields the owner fills before drawing —
    `title`, `tail`, `loadingText`, `emptyText`, `noMatchText`, `reserve` — and
    never a `rebuild func()`, a `titleTail func(…)` or an owner back-pointer.
    `chatview.go` assigns a bare `chatSkillList{}` and `hideSkills` re-zeroes
    the filter, so anything a constructor wired would be silently dropped at
    run time with nothing failing at compile time; it is the discipline task
    124 decision 103's nil `rowLine` default already follows. The consequence
    is that `typeText` and `backspace` no longer rebuild — they report that the
    filter moved and the owner re-ranks, which is two explicit
    `v.skills.build()` calls in `chatview.go`. *Beaten:* a constructor, and a
    callback the core could reach the owner through.

27. **2026-09-21 — The core renders the title line's shape, the owner fills
    it** (#554 decision 2). The core owns `styleTitle.Render(title)` plus the
    `styleDim` dot-joined tail; `chatSkillList` supplies `"skills"` and the
    cells. Decision 102's `%d of %d` counter stays in the core as a `capNote()`
    helper, because `cap` and `matched` do, but the owner splices it into the
    tail **in its current position** — after the filter, before the probe age.
    Appending it in the core would reorder the drawn line, which a pure
    refactor may not do. `now time.Time` therefore never enters the core and
    `chatSkillList.render(width, paneHeight, now)` keeps its signature, so
    `chatrender.go` is unchanged. *Beaten:* a whole `titleLine` in the owner
    (it would duplicate the styling) and a `now` on the core (it would drag a
    clock into a type with no data to age).

28. **2026-09-21 — `chatDraftToken` and `chatDraftTokenAt` move into the
    core's file; `chatView.replaceDraftToken` stays in `chatview.go`** (#554
    decision 3), which closes #554's own open question. The token reader is a
    pure `textarea` reader with no skills in it; the replacer is a `chatView`
    method because the composer is the view's, and task 124 decision 95's
    comment sits on it. It gains one line naming it shared by every inline
    picker and is otherwise untouched. *Beaten:* moving both, and leaving
    both.

29. **2026-09-21 — Renames are the type and the field only** (#554
    decision 4). `chatSkillRow` → `chatInlineRow`, `chatSkillRow.invocation` →
    `chatInlineRow.insert`, and the new `chatInlineList`. The free functions
    keep their names even after changing files — `chatSkillFlatten`,
    `chatSkillHostile`, `chatSkillRowLine`, `chatDraftTokenAt`,
    `nonEmptyStrings` — so the diff stays a move plus two renames and "nothing
    changed but names" is reviewable. *Beaten:* a wider rename to
    `chatInline*`, on review cost.

30. **2026-09-21 — The reserve is a core field the skills wrapper sets on
    every call, not at construction** (#554 decision 5). `chatSkillsDescLines
    = 3` stops being a package constant baked into `height`/`window` and
    becomes `chatInlineList.reserve`; decision 75's comment moves onto the
    field. Because nothing constructs a `chatSkillList`, a reserve wired once
    would be silently zero, so `chatSkillList`'s `render`, `height`, `window`
    and `titleLine` set it — through one `core()` helper — before delegating.
    `TestChatSkillsZeroValueStillReservesItsLines` is the one test added, and
    it exists for exactly that hazard. *Beaten:* a constructor, and leaving
    the reserve a constant the core would have had to know.

31. **2026-09-21 — Embedding shadows, it does not dispatch** (#554
    decision 6). Go has no virtual dispatch through an embedded struct: if the
    core's `render` called `l.titleLine(…)` it would call the *core's*, never
    `chatSkillList`'s override. That is the trap that kills a naive embed, and
    it is the structural reason decisions 26 and 27 land where they do — the
    core reads fields, and the outer methods shadow the promoted ones rather
    than being called back into.

    Its one cost is the test literals. Go forbids a promoted field in a
    composite literal, so the eight `&chatSkillList{open: true, …}` literals in
    `chatskills_test.go` nest their `open`, `cap` and `filter` into a
    `chatInlineList{…}`. #554 predicted "renames and nothing else"; that is a
    language rule rather than a wrong seam, no assertion moved, and no literal
    gained a `reserve:`. *Beaten:* a named field instead of an embed, which
    would have rewritten every `v.skills.open`, `.cursor`, `.filter`, `.rows`
    and `.suppressed` in `chatview.go` and the tests.

32. **2026-09-21 — No spec amendment, and this ledger is not optional**
    (#554 decision 7). Nothing observable changes, so decision 104's precedent
    applies to `docs/spec.md`, `docs/reference/`, `docs/features.md` and the
    screenshots, and none of them moves. It does not extend to the maintainer
    record, which CLAUDE.md requires: 126.9 is flipped here with its decisions,
    and the two neighbouring rows that landed elsewhere are corrected rather
    than left to rot. *Beaten:* writing a §15 note about a type a reader of the
    TUI cannot see.

Decisions 33–35 were settled with the author on 2026-09-21 in 126.4 (#548),
which publishes the capability decision 1 defined. They are numbered 1–3 in
that issue's brief and the code cites them in this document's numbering.
Nothing above is reopened.

33. **2026-09-21 — Four wire fields, not the issue's three** (#548 decision
    1). `supports_file_mentions`, `file_mention_sigil` and
    `file_mention_position` cannot carry the capability split the issue
    itself cites: 126.3 landed with all three shipped adapters implementing
    `FileMentioner`, so `agent.CanMentionFiles` is `true` for every one of
    them and §9.1 states outright that `Expands` is the honest capability
    statement, not the interface. `file_mention_expands` therefore joins the
    trio — it is the field that differs per adapter, and task 124 decision
    19's "invocation gets no note: every shipped adapter can invoke" is the
    same reasoning one capability family over. `supports_file_mentions` is
    kept rather than derived from a `""` sigil, even though one interface
    supplies both: it carries no note today, exactly as `supports_resume`
    does, and exists so a client can tell "cannot mention" from "nobody can
    say" the day a fourth adapter lacks the interface. *Beaten:* three fields
    keyed on `file_mention_sigil != ""`, and the issue's trio with no
    `expands` at all.

34. **2026-09-21 — The `vincent agents` note reads `no @ file expansion`**
    (#548 decision 2). Pure negative, matching the cell's existing shape
    (`no mid-run input`, `no restricted mode on <os>`, `no skill listing`) and
    task 124 decision 19's bad-news-only rule to the letter. It is keyed on
    `file_mention_expands == false` — nothing for claude, a note for codex and
    cursor — and a `null` from an older daemon or an absent registry adds
    nothing, as `CannotListSkills` already behaves. `supports_file_mentions`
    gets no note, by decision 33. *Beaten:* naming the consequence in the cell
    ("`@` paths are prose, not context"), which would be the first note there
    to say what *does* happen; and deferring the note to 126.11 (#555).

35. **2026-09-21 — The spec is amended in 126.4, not only
    `docs/reference/api.md`** (#548 decision 3). The issue said "nowhere
    else", with the spec out of scope as 126.1's job; but 126.1's records are
    §5.5 and §9.1 — the CLI *behaviour* — and this work makes a **wire** fact
    true. Task 124's precedent for these exact siblings amended §9.6's
    row-field list and §13.2's `vincent agents` row, and CLAUDE.md requires
    the amendment in the same pull request as the code. §9.1's "nothing
    consumes this yet" is amended in the same breath, because it names this
    subtask as the thing that had not happened. The CLI reference moves with
    §13.2 for the same reason: the `NOTES` vocabulary is what changed.
    *Beaten:* `api.md` alone, which would leave §9.6's field list stale
    against the route it describes.

Decisions 36–40 were settled with the author on 2026-09-21 in 126.6 (#550),
the subtask that puts the listing on the wire. Nothing above is reopened:
decision 3's "enumerate on the host, even for a containerized task" is
implemented here, not reconsidered, and decisions 1, 2 and 21 supply the
mention string, its input contract and the path form unchanged.

36. **2026-09-21 — The cap is 50,000, and `limit` may only lower it.**
    `?limit=N` is validated as a positive integer and then clamped to
    `min(N, 50000)`; a client can ask for less and never for more, and
    `truncated` is true whenever rows were cut by either bound. This is
    `MaxSourceBytes`' reasoning (`internal/workflow/registry.go`) applied to a
    listing: a bound the daemon chose beats an allocation it did not, and a
    `limit` that could raise the ceiling would hand that choice back to the
    caller. The number is a judgement — "larger than any repository worth
    calling normal", about 3 MB of JSON by #550's own measurement — and
    `truncated` is what makes a wrong guess visible rather than silent.
    *Beaten:* a 10,000 bound, and a `limit` that overrides freely.

37. **2026-09-21 — The mention facts are flat siblings, never a nested
    object.** The body carries `mention_sigil`, `mention_position` and
    `mention_expands` at the top level, the way `invoke_sigil` and
    `invoke_position` already sit on `chatSkillsBody`. §9.6's rule is flat
    siblings — recorded on that type and quoted verbatim in §13.2's skills row
    — and task 041's reasoning against nesting one facet while its siblings
    stay flat applies unchanged. It also leaves the key `mention` meaning
    exactly one thing on this route: a row's ready-to-insert text. It is the
    same vocabulary 126.4 (#548) carries on `GET /v1/agents`, which landed
    first, under decision 33 and with the `file_mention_` prefix a row of
    adapters needs; neither subtask waited on the other and the two agree.
    *Beaten:* #550's own nested `{"sigil", "position", "expands"}`, which would
    need its own departure decision and would give `mention` two meanings in
    one body; and renaming the row field to `insert`.

38. **2026-09-21 — An adapter that cannot mention files is an empty sigil, not
    a refusal.** When the chat's adapter is unregistered, or is registered and
    does not implement `FileMentioner`, the route still answers `200` with the
    paths: they are true regardless of who reads them. `mention_sigil` and
    `mention_position` are `""` — the skills route's own spelling for "this
    adapter cannot" — `mention_expands` is `false`, and every row's `mention`
    is `""`, which is the rule `chatSkillBody.Invocation` already follows. An
    empty sigil is the client's signal not to offer the picker. No verdict
    vocabulary is imported: a file listing has no axis on which nobody can
    say, because git either answered or errored. *Beaten:* three nullable
    fields distinguishing "no" from "nobody can say", and refusing the route
    outright, which would make an adapter capability into a refused read
    against task 124 decision 4.

39. **2026-09-21 — The dropped-row count reaches the daemon log and not the
    wire.** This closes decision 18's explicit deferral. `ListFiles` returns
    `(paths, dropped, err)`; the handler logs one line when `dropped > 0` and
    the body gains no field. The rows are undrawable by definition, so a
    client can do nothing with the number, and the operator still has the
    record when a path is missing from a picker. The route's test asserts the
    body's whole key set, so a `dropped` field cannot appear without that
    failing. *Beaten:* a `dropped` integer beside `truncated`, and discarding
    the count.

40. **2026-09-21 — `work_dir` on this route is a host path, and that is a
    stated consequence.** It happens to equal the container path for a linked
    chat on a containerized task, because `containerMounts`
    (`internal/taskrun/container.go`) bind-mounts the worktree at its own
    absolute host path so the repository resolves with zero translation (task
    061 decision 2). `chatSkillsBody.WorkDir` is a *container* path in that
    same case. The two agree by construction, not by contract: if the
    bind-mount invariant ever changes — a remote container runtime, say, since
    `container.Runtime` shells out and holds no daemon connection — this
    route's paths break silently, with nothing failing at compile time. Said
    out loud in §5.5, in §13.2's row and in the handler's doc comment.

    Two smaller calls follow recorded reasoning rather than a new decision.
    The placement is `chatrun.Runner.Workspace` and not `SkillPlace`, which is
    the whole of decision 3 in code: `Workspace` answers the directory alone
    and never asks the container runtime, so there is no
    `taskrun.ErrTaskContainerMissing` leg and no `missingContainerSkillsReason`
    twin. And `(*Client).ChatFiles` uses the plain `rest` client rather than
    `probeClient(true)`: the probe deadline is three minutes because a cold
    skills cache spawns an agent CLI, which a 30 ms git call is not. That
    leaves the pre-existing `requestTimeout`/`gitx.QueryTimeout` mismatch
    standing, which binds only for a worktree on a cold network filesystem;
    closing it by borrowing the probe deadline would mean a picker that hangs
    for three minutes. It is stated in the client method's doc comment so it
    is a known gap rather than a surprise.

## Citations corrected

#544 and #545 both carry citations that do not resolve at HEAD. The decisions
stand; the reasoning is recorded here in the form that is true.

- **"A departure from task 124.17 decision 1" resolves only through the
  code.** `124-chat-agent-skills.md` numbers its decisions globally and carries
  no "124.17 decision 1" entry, so the citation cannot be looked up in the
  document. It is not invented, though: `124.17 decision N` is the form the
  code itself uses for that subtask's four decisions
  (`internal/api/chatskills.go`, `internal/chatrun/runner.go`,
  `internal/chatrun/recover.go`, `internal/taskrun/linkedchat.go`,
  `internal/taskrun/container.go` and their tests), and those four are the
  document's **96–99** — a mapping written into decision 56's own amendment,
  "*Amended 2026-09-21 by 124.17 (decision 96)*". So 124.17 decision 1 **is**
  decision 96, and #544 named the right rule. Decision 3 above cites the
  document's numbers — **decision 11**, as amended on 2026-09-21 by 124.17,
  with **decision 96** as its companion — because those are the ones a reader
  can find where the decisions are written.
- **"Task 124 decision 9's lesson applies again: observation wins" mis-names
  the decision.** Decision 9 is *Pass-through only* — no validation, no
  rewrite, no translation — which is what §5.5's new subsection cites it for,
  and nothing else. No decision in task 124 states an
  observation-over-vendor-documentation rule. The precedent that does state it
  is §5.5's own stacking bullet ("expanded only `/a` on 2.1.277, whatever the
  vendor documentation says", task 124.6, #502), with §9.7's ACP listing note
  as a second instance; the new subsection cites those two.
- **"An image only ever promised to carry the agent CLI" is false**, and is
  not used as an argument anywhere in this document. The spec line #544 cites
  for it is `vincent status`'s CLI-table row, which says the image carries no
  **vincent** binary (task 062.2 decision 4) and says nothing about git. §16
  promises git on the image twice: "it must already carry the agent CLI a
  workflow's agent steps resolve to, and `git`", and again in the §16 summary.
- **"Roughly 63 KB" contradicts the observation.** 63 KB **expanded**. §5.5
  records the bracket instead — 16 KB and 63 KB expanded, 247 KB and 441 KB did
  not, threshold not bisected — which is both true and the honest shape for a
  number a picker will make easy to hit.

## Risks

- **claude's silent size cap.** A mention of a large file does nothing and says
  nothing, and a picker makes that trivial to hit: a lockfile, a generated
  file, a `testdata` capture. Recorded in §5.5 as a bracket rather than a
  number, because the threshold was not bisected.
- **The expansion is unattributable in a transcript.** A reader sees a turn
  whose input-token count jumped and no record of why. Task 124.20's unmarked
  `conversation_reset` is the adjacent precedent.
- **Linked-chat staleness.** A task step writing a file in the shared worktree
  emits no event the chat stream carries, so a cached list can go stale.
  Mitigated by re-fetching on picker open (decision 5), not solved.
- **The renderer.** Handing a file-sized row set to the inline picker's current
  `render` would make the TUI unusable on a large repository. 126.10 (#553) is
  the single highest-value fix in the set and it is small.
- **Windows is unobserved.** Every adapter probe ran on darwin. 126.2 (#557)
  settles it before any separator handling is written; §5.5 states the gap
  rather than assuming Windows matches.
- **The cap number is a guess.** The built-set cap is "larger than any
  repository worth calling normal", not a measurement; `truncated` is what
  makes a wrong guess visible rather than silent.
- **Version drift.** The claims are pinned to claude 2.1.278, codex-cli
  0.154.0 and cursor-agent 2026.09.18, while §9.1's adapter table and §5.5's
  skills subsection name claude 2.1.277. The new record names its own build and
  widens neither older pin.

**Methodology note.** A first probe pass that put six path spellings into one
message reported a false positive through contamination — one expansion put
enough of the workspace in front of the model that it could answer about a
spelling that had not expanded. Every row of §5.5's path-form table was re-run
in isolation, one spelling per message, and the table records those runs.

## Tasks

The probe results recorded by 126.1 were observed by the author against the
real CLIs; this workflow cannot run an agent CLI session, and the record
attributes them to that observation run, the way §5.5's skills subsection
attributes task 124.6's.

- [x] 126.1 (#545) Record what each agent CLI does with an `@path` mention:
      §5.5's "File mentions in a chat's message" subsection with the path-form
      table, the per-adapter paragraphs in §9.2, §9.3 and §9.7, and this
      document with its index row. No code. ✓ 2026-09-21
- [ ] 126.2 (#557) Observe `@path` on a Windows claude and record the answer.
      Needs a Windows host and a real claude session. Depends: none.
- [x] 126.3 (#547) `agent.FileMentioner` in claude, codex and cursor — the
      capability and the mention string, quoting per decision 1.
      `internal/agent/mentions.go` with `FileMentionSyntax`,
      `MentionPosition` and `CanMentionFiles`; a `mentions.go` beside each
      adapter's `skills.go`, each naming the build it was observed on;
      `agenttest.StubNoMentions` for the false leg; one shared seven-row input
      table run against all three adapters, the unprobed quote-and-space row
      commented as the gap it is. §9.1 gains its `FileMentioner` declarations
      and its **File mentions** record. Nothing is served or consumed:
      `GET /v1/agents` is 126.4, the `mention` field 126.6, the picker
      126.11. Decisions 22–25. ✓ 2026-09-21
- [x] 126.4 (#548) Report the capability on `GET /v1/agents` and in
      `internal/apiclient`. Four fields, not the issue's three:
      `supports_file_mentions` from `agent.CanMentionFiles`, plus
      `file_mention_sigil`, `file_mention_position` and — the one that
      separates the shipped adapters, since all three implement
      `FileMentioner` — `file_mention_expands`. Filled from the same single
      registry lookup as `supports_resume` and the skill trio, `null` for an
      adapter the registry does not know and for a daemon with none.
      `apiclient.Agent` mirrors them with `CannotExpandMentions()`, and
      `vincent agents` notes `no @ file expansion` — the negative only,
      decision 19's rule. §9.6, §13.2 and §9.1's "nothing consumes this yet"
      are amended, with `docs/reference/api.md` and the CLI page. Nothing in
      the TUI reads a mention field until 126.11. ✓ 2026-09-21
- [x] 126.5 (#549) A raw-output git runner, and enumerating a chat's workspace
      on the host (decision 3). `gitx.RunRaw` and the `run`/`runRaw` refactor;
      `worktree.Manager.ListFiles` and `ReasonWorkspacePathMissing`; their
      tests — tracked, untracked and ignored; a three-way conflict listed once;
      ` leading.txt` and `trailing .txt` byte for byte; three symlinks never
      followed; a newline path and an invalid-UTF-8 path dropped and counted; a
      missing directory carrying the reason. Package-internal: nothing is
      served, cached, persisted or shown, so no spec amendment lands here —
      §5.5 and §13.2 are amended by 126.6. Decisions 15–21. ✓ 2026-09-21
- [x] 126.6 (#550) `GET /v1/chats/{id}/files`, with `limit` and `truncated`
      (decisions 4, 5, 11), and §13.2 plus `docs/reference/api.md`.
      `internal/api/chatfiles.go` — the route over `chatrun.Runner.Workspace`
      and `worktree.Manager.ListFiles`, its two 409s, the missing workspace's
      400 and the clamped `limit`; the exclusion in `internal/mcp/tools.go`
      and its parity row; `ChatFiles`/`ChatFile` and `(*Client).ChatFiles` in
      `internal/apiclient`; `chatfiles_test.go` and
      `chatfiles_live_test.go`. §5.5, §11, §13.2 and §13.4 amended,
      `docs/reference/api.md` and `docs/security-model.md` with them.
      Decisions 36–40. It did not wait for 126.4, which landed first: the
      two settled the same flat-sibling vocabulary independently, on their
      own routes (decision 37). ✓ 2026-09-21
- [ ] 126.7 (#551) `vincent chat files`, and a files leg in the chat gate.
      Depends: 126.6.
- [x] 126.8 (#552) Stop an `@` token spending the chat's one silent skills
      probe. It landed as an amendment to task 124 decision 91 rather than
      under this document; the record is `124-chat-agent-skills.md`'s dated
      note on that decision. ✓ 2026-09-21
- [x] 126.9 (#554) Extract a data-neutral inline picker core from
      `chatSkillList` (decision 7). `chatInlineList` and `chatInlineRow` in
      `internal/tui/chatinlinelist.go` with the rows, the filter buffer, the
      window arithmetic, the renderer and the draft-token reader; a
      `chatSkillList` that embeds it and keeps the verdicts, the probe latch,
      the ranking and the words; `chatSkillRow.invocation` renamed to
      `chatInlineRow.insert`. No behaviour change and no wire change, so no
      spec amendment (decision 32). Decisions 26–32. Depends: none.
      ✓ 2026-09-21
- [x] 126.10 (#553) Window the inline picker's rows, and cap the built set. It
      landed first, as task 124.21 (`124-chat-agent-skills.md`, decisions
      101–104), so the dependency ran the other way: 126.9 rebased on the
      fixed renderer and moved it into the core unchanged. ✓ 2026-09-21
- [ ] 126.11 (#555) The `@` file picker in the chat composer (decisions 6, 8,
      9, 10). Depends: 126.6, 126.9.
- [ ] 126.12 (#546) Assert `@`-mention pass-through end to end in the chat
      gate. Depends: none.
- [ ] 126.13 (#556) The TUI guide, §15, `docs/features.md`'s one sentence
      (decision 13) and a new screenshot from `scripts/screenshots.sh`.
      Depends: 126.11.
