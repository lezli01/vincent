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
    (`internal/tui/chatskills.go`), duplicated rather than shared because
    `worktree` cannot import `internal/tui` and the client guard stays as
    defence in depth. Whitespace is *not* hostile — task 124 decision 71's
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
- [ ] 126.4 (#548) Report the capability on `GET /v1/agents` and in
      `internal/apiclient`. Depends: 126.3.
- [x] 126.5 (#549) A raw-output git runner, and enumerating a chat's workspace
      on the host (decision 3). `gitx.RunRaw` and the `run`/`runRaw` refactor;
      `worktree.Manager.ListFiles` and `ReasonWorkspacePathMissing`; their
      tests — tracked, untracked and ignored; a three-way conflict listed once;
      ` leading.txt` and `trailing .txt` byte for byte; three symlinks never
      followed; a newline path and an invalid-UTF-8 path dropped and counted; a
      missing directory carrying the reason. Package-internal: nothing is
      served, cached, persisted or shown, so no spec amendment lands here —
      §5.5 and §13.2 are amended by 126.6. Decisions 15–21. ✓ 2026-09-21
- [ ] 126.6 (#550) `GET /v1/chats/{id}/files`, with `limit` and `truncated`
      (decisions 4, 5, 11), and §13.2 plus `docs/reference/api.md`. Depends:
      126.4, 126.5.
- [ ] 126.7 (#551) `vincent chat files`, and a files leg in the chat gate.
      Depends: 126.6.
- [ ] 126.8 (#552) Stop an `@` token spending the chat's one silent skills
      probe. Depends: 126.6.
- [ ] 126.9 (#554) Extract a data-neutral inline picker core from
      `chatSkillList` (decision 7). Depends: none.
- [ ] 126.10 (#553) Window the inline picker's rows, and cap the built set.
      Depends: 126.9.
- [ ] 126.11 (#555) The `@` file picker in the chat composer (decisions 6, 8,
      9, 10). Depends: 126.6, 126.10.
- [ ] 126.12 (#546) Assert `@`-mention pass-through end to end in the chat
      gate. Depends: none.
- [ ] 126.13 (#556) The TUI guide, §15, `docs/features.md`'s one sentence
      (decision 13) and a new screenshot from `scripts/screenshots.sh`.
      Depends: 126.11.
