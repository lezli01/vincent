# 095 — Report whether the published skills are installed, and install them

**Status:** ✅ done (10/10)
**Issue:** #357
**Amends:** new §9.8 "Published skills"; §9.5 (one sentence closing the facet
set at five); §12.1 (the `vincent skills` row); §15 (the `S` offer beside the
status-line one); §17 (the doctor group). All dated 2026-09-10, in place.
**Keeps, without relitigating:**
[041](041-agent-health-facets.md) — the §9.5 facet vocabulary stays at five.
[006](006-doctor.md) decision 7 — doctor's unhealthy set stays closed.
[082](082-reported-agent-quota.md) decision 2 — vincent may write another
tool's configuration file when a human asks it to, on a screen that shows
exactly what it will write.
The v0 **T1.7 decision** ("no state-file parsing") — not reopened; see below.

## The problem

vincent published a skill — `skills/vincent-workflows/` — and nothing in the
product ever looked at whether anybody had it. The only install path was a
command a human had to find in prose (`README.md`, `docs/guides/workflows.md`)
and run by hand. `grep -ri skill internal/` found one consumer: `builtin.go`
splicing the embedded `SKILL.md` into two built-in prompts.

So the gap bit exactly where it was hardest to notice. A run *inside* vincent
was fine — `create-workflow` and `update-workflows` carry the skill's text in
their own prompts — and a user talking to their own agent directly, which is
where a workflow actually gets written, got no help and no way to learn that
help existed. An installed copy then went stale invisibly, because the skill
carried no version marker of any kind.

## Decisions

1. **Install shells out to `npx skills add`, with `-a` and `-y` added**
   (2026-09-10). vincent runs `npx skills add lezli01/vincent --skill <name>
   --agent <slugs> --yes --global`, not the published command verbatim, because
   the published command opens an interactive agent multi-select and there is
   nothing to pick with from a TUI takeover or a non-TTY CLI. The agent list
   defaults to the vincent adapters detected on the box, mapped through the slug
   table in §9.8.

   Beat: **writing the skill files ourselves from the embedded copy.** The
   issue rejected it partly on "vincent does not write into another tool's
   config directory itself", which is *not true of this codebase* — task 082
   decision 2 writes `~/.claude/settings.json` and says so in as many words. The
   rejection stands on its other reason: `skills/embed.go` embeds only
   `SKILL.md`, so doing it means embedding `references/`, `LICENSE.txt` and
   `agents/openai.yaml` too and reimplementing another tool's install layout,
   symlink/copy split included.

   Consequence accepted: the command in `README.md` /
   `docs/guides/workflows.md` and the command vincent runs are two spellings of
   one thing and must be kept in sync. `TestInstallArgs` and
   `TestInstallArgvReachesTheProcess` are what catch them drifting; `npx` is a
   runtime dependency of the **install action only**.

2. **Detection never uses `npx`** (2026-09-10). It is a filesystem read: the
   store copy at `~/.agents/skills/<name>/SKILL.md` for the version, each agent
   directory for whether it is linked. That is what makes it work in `doctor`,
   in the TUI, and on a machine with no node at all — proved by
   `TestDetectNeedsNoNpx` and by the CLI test that lists state with an empty
   `PATH` right after an install failed for want of `npx`.

3. **One row per skill, carrying the agent list** (2026-09-10) — not a row per
   adapter per skill. `skills add … -g` keeps one copy in a global store and
   links it into each agent's directory, so a per-adapter version column would
   be the same string three times by construction. The row reads: name, shipped
   version, store state, and the agents it is linked into.

   The agent list is discovered by **scanning the home directory** rather than
   from a table of paths. A table would have to assert install locations for
   agents this repository cannot verify; a scan reports what is there. It names
   agents vincent does not drive — one store serves Cline, Copilot, Zed and the
   rest — which is honest rather than noisy.

4. **The row is not a sixth §9.5 facet** (2026-09-10). Task 041 closed that
   vocabulary at five, and a skill is a property of the machine's agent
   configuration rather than of an adapter binary: one copy serves every agent,
   and a machine with no adapter installed can still hold it. The skills group
   sits beside the agents group, and §9.5 gained one sentence saying so, so the
   closed set stays closed.

5. **Nothing here moves an exit code** (2026-09-10). Task 006 decision 7's
   unhealthy set stays closed: a missing, stale or unreadable skill is a row, on
   the GitHub (035), release-check (055) and container (061) precedents.
   `vincent doctor` still exits 0. `TestSkillsAreNeverAProblem` pins it with all
   five bad states at once.

6. **`metadata.version`, enforced by a test** (2026-09-10). The marker goes
   under the `metadata:` key the front matter already uses for `author`, because
   that is the format's extension point and a bare top-level `version:` is not a
   key the skills format defines — a validator rejecting unknown top-level keys
   would break the published skill. `skills/version_test.go` hashes each
   published tree and fails with "bump `metadata.version`" when the content
   moved and the version did not, because `references/*.md` goes stale on its
   own and a hand bump is forgotten. The version lives inside `SKILL.md`, so
   bumping it moves the hash too: the test cannot be satisfied by editing one of
   the two.

7. **Comparison is semver, and never a guess** (2026-09-10).
   `golang.org/x/mod/semver` was already a direct dependency
   (`internal/release`). Where either side does not parse, the row says the
   versions **differ** and prints both; it claims no direction. This mirrors
   §9.5's tri-state discipline rather than its exact-string-equality rule, which
   cannot answer "older". `newer` is its own state, not folded into `current`: a
   downgraded binary must not report an up-to-date skill.

   *Settled in the diff:* a copy that reads perfectly and carries **no**
   `metadata.version` is `older`, not `unreadable`. It is every copy installed
   before this change shipped, and the marker's absence is itself the
   direction. It was `unreadable` until the command was run against a real
   machine, where it made the one state every existing user would see the one
   state that sounds like a fault. `unreadable` now means only what it says: a
   `SKILL.md` that could not be read or parsed.

8. **The TUI surface is a daemon-view offer, on task 082's pattern**
   (2026-09-10) — not a firstrun-style blocking notice. A line under the
   adapters, a takeover showing the exact command, and a decline persisted in
   `tui.json` beside `StatusLineDeclined`; nothing re-asks while it is set. The
   offer is actionable only because decision 1 made the install
   non-interactive.

   **The key is `S`, not `i`** (2026-09-10). The brief left this to the diff.
   Both keys lead to a write outside vincent's own directories, but they are two
   different operations on two different targets, and clause 1 of §15's key
   vocabulary lets a key be shared only where it means the same operation. `s`
   was unavailable — it is the vocabulary's "cycle a listing's scope" — and `S`
   carries no term. The flow is its own binding context (`ctxSkills`) for the
   reason the config editor's keys are their own: while it is open it owns the
   keyboard, and `n` means "not now" in there and nothing at all outside it.

   One thing differs from the status-line flow, and it comes from what is being
   run: that flow rewrites a small local file synchronously, this one spawns a
   subprocess that downloads a package. So the install runs off the event loop
   in a `tea.Cmd`, with the screen saying what is running. A TUI frozen on a
   network install is a TUI that cannot be quit.

9. **Detection is composed by `internal/doctor`, like every other local probe**
   (2026-09-10). Task 006 decision 6 gives that package every probe needing no
   database, so the group is composed server-side when a daemon answers and
   client-side when none does — identical results, because vincent's daemon is
   localhost and runs as the invoking user. The TUI reads the group off the
   `GET /v1/doctor` report its daemon view already fetches; only the decline
   flag is local, exactly as the status-line flow already splits it.

10. **Enumeration is generic** (2026-09-10). `skills/embed.go` grows an
    `embed.FS` over `*/SKILL.md`, and nothing enumerates names in Go. A second
    directory under `skills/` appears in doctor, the command and the TUI with no
    code change. `TestPublishEnumeratesGenerically` proves it against an
    injected `fs.FS` rather than by adding a second real skill.

## The v0 T1.7 decision is not reopened

T1.7 forbids inferring another tool's **authentication** from its private state
files. `~/.agents/skills/` and `~/.<agent>/skills/` are a public CLI's
documented install locations, and the file read out of them is one this
repository published. §9.8 says this out loud rather than leaving the adjacency
for somebody to notice later and file.

## Two things a reader will trip over

- **vincent disagrees with `npx skills list -g`.** On the machine this was
  probed on, that command reported vincent-workflows installed for "Claude Code,
  Cline, Codex, Cursor, GitHub Copilot +4" while only three directories held a
  link. That column is the CLI's *selection memory*
  (`lastSelectedAgents` in `~/.agents/.skill-lock.json`), not an on-disk fact.
  vincent reports the links. This is written down so the first person to notice
  does not file it as a bug. The lock file also carries no version at all, which
  is why decision 6's marker is needed rather than reusable from it.

- **Whether codex and cursor actually read those directories is unconfirmed.**
  The path table is the `skills` CLI's and says where that CLI *writes*. This
  work could not confirm what each agent then does with it, so §9.8 records the
  limitation instead of asserting the capability — the standing §9 rule that a
  capability an adapter lacks is documented and ignored, never emulated. The
  skill ships `agents/openai.yaml` for codex-side packaging; beyond that vincent
  reports what is on disk and claims nothing about what reads it.

## No gate script

An acceptance gate drives a real daemon over curl against the fake agent. A
skills assertion would depend on the host's home directory and on network
access to npm, and a gate that installs into the CI runner's home is a gate that
fails differently on three platforms. Go tests with an injected home root cover
it, and `docs/gates/` records no manual leg — the same reason, and the same
answer, as task 082.

## Where it lives

| Path | What |
|---|---|
| `skills/embed.go` | `//go:embed */SKILL.md` → `FS`. `VincentWorkflows` stays for `builtin.go` |
| `skills/version_test.go` | Decision 6's drift guard |
| `skills/vincent-workflows/SKILL.md` | `metadata.version`. Front matter is stripped by both built-in consumers, so no prompt changed |
| `internal/skill` | The leaf: front-matter parse, the adapter→slug→directory table, `Detect` (pure filesystem), `Install` (the `npx` argv, `exec.LookPath` for the missing-dependency outcome) |
| `internal/doctor/skills.go` | The `Skills` group on `Report`; `Evaluate` untouched |
| `internal/apiclient/doctor.go` | `DoctorSkill` alias and the state constants |
| `internal/cli/skills.go` | `vincent skills ls` / `install`, both `--json`, no exit 2 |
| `internal/cli/doctor.go` | The `SKILLS` group |
| `internal/tui/skillsflow.go`, `skills.go`, `daemon.go`, `daemonrender.go`, `bindings.go` | The `S` offer and its decline flag |
