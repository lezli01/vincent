# Security model

This page describes how vincent is designed to behave. To **report** a
vulnerability, use [private security advisories](https://github.com/lezli01/vincent/security/advisories/new)
— see [SECURITY.md](../SECURITY.md).

> [!WARNING]
> **Agents run full-auto by default, which means an agent can execute arbitrary
> commands as you.** That is a documented design decision, not a bug: unattended
> orchestration is the point of the tool. Everything below explains exactly what
> that buys and what it costs.

- [The trust boundary is the OS user](#the-trust-boundary-is-the-os-user)
- [Full-auto is the headline risk](#full-auto-is-the-headline-risk)
- [What the worktree does and does not isolate](#what-the-worktree-does-and-does-not-isolate)
- [Restricted mode](#restricted-mode)
- [The API surface](#the-api-surface)
- [Credentials](#credentials)
- [What vincent writes outside its own directories](#what-vincent-writes-outside-its-own-directories)
- [Tightening it](#tightening-it)
- [What is not in scope](#what-is-not-in-scope)

---

## The trust boundary is the OS user

Everything vincent does, it does **as you**. There are no accounts, no roles and
no privilege separation inside vincent, because there is nothing meaningful to
separate: an agent it launches has your privileges, your agent-CLI logins and
your git identity.

That is why the service registration is per-user on every platform and needs no
elevation anywhere — a machine-wide service would run agents as a system
account, with *more* privilege and none of your credentials, which is strictly
worse in both directions.

## Full-auto is the headline risk

In `full-auto`, permission prompts are bypassed at the CLI level. The adapter
switches are:

| Adapter | Full-auto switch |
|---|---|
| claude | `--dangerously-skip-permissions` |
| codex | `--dangerously-bypass-approvals-and-sandbox` |
| cursor | `--force` |

**All three are equivalent in blast radius.** Cursor's reads mildest and is not.
An agent in full-auto can read your home directory, use your credentials, and
reach the network — it is not confined to the worktree.

What limits the damage, and what does not:

| Mitigation | Real |
|---|---|
| Nothing is pushed, merged or deployed unless a **workflow step does it** | ✅ — put a `manual` gate in front of any such step |
| Everything is transcripted: every prompt, tool call and command | ✅ — and when it cannot be, the step **fails** rather than passing (see below) |
| Any step can run `permission_mode: restricted` | ✅ — see below |
| The git worktree | ⚠️ collision isolation, **not** security isolation |
| Running under a service | ❌ changes nothing — it is still you |

**"Everything is transcripted" is a promise about completeness, not about
durability.** A transcript vincent could not finish writing does not quietly
become a shorter transcript: a failed write, encode or close fails the attempt
with `transcript_io_error`, and a stream an adapter could not read to the end
fails it with `agent_protocol_error`. Output too long for one record is split
across `partial` records rather than dropped, so length alone never costs you
evidence; only `transcript_max_bytes` deliberately stops a runaway, and it says
so in the file. What is *not* promised: vincent writes and closes the file and
checks both, but does not fsync per line, so a host that loses power can still
lose the tail of a transcript. See [spec §12.2](spec.md#122-directories-platform-native).

The TUI shows this warning **once**, on first run. The acknowledgment persists in
`{data_dir}/tui.json` and is written when the notice is *dismissed*, never when
it is shown, so quitting two seconds in does not bury it. Any failure reading or
writing that file shows the notice again — a security warning that suppresses
itself because a parse failed has failed in the wrong direction.

## What the worktree does and does not isolate

Every task runs in its own `git worktree` on its own branch.

**Does:** keep two tasks in the same repository from colliding; keep your own
checkout, current branch and stash untouched; make every change reviewable as a
real git diff before anything is pushed.

**Does not:** confine the process. The worktree is a directory, not a sandbox. A
full-auto agent's cwd is the worktree; its *reach* is your whole account.

Command steps and checks are the same story: they execute user-authored workflow
content at the same trust level as your own shell. No additional sandboxing is
attempted or implied.

**Workflow files are read at registration and on reload, not when you run one.**
Adding a project puts every `*.yaml` in its `.vincent/workflows/` in front of the
parser right away, and the daemon re-reads them whenever they change — before
anybody picks a workflow. Cataloguing is therefore bounded (spec §5.2): only
**regular files** are sourced, so a symlink, FIFO, socket or device named `*.yaml`
is rejected without being opened or followed, and a source is capped at **1 MiB**.
A rejected file is listed as an invalid entry naming the path and the reason; its
valid siblings stay available. The same 1 MiB bound applies to a source posted to
`POST /v1/workflows/validate`, so the API is not a way around it. What the parsed
content is then allowed to *do* is unchanged — a `command` step is still a shell
command.

## Restricted mode

`permission_mode: restricted` maps each adapter onto its own confinement:

| Adapter | Restricted means | Platforms |
|---|---|---|
| claude | Allowlist flags with an edit/read/git/test tool set | all |
| codex | `--sandbox workspace-write` — writes confined to the worktree | all |
| cursor | `--sandbox enabled` | **macOS and Linux only** |

Two properties matter more than the mechanism:

**An adapter that cannot restrict on your platform fails the step.** It never
downgrades. Cursor on Windows returns a distinct error and vincent blocks the
step with `restricted_unsupported`, under the normal retry policy. Falling back
to full-auto was rejected outright: it would run wide-open a step that explicitly
asked not to be, converting a safety choice into its opposite on exactly one OS —
the failure mode nobody would think to check for.

**Denied actions can become questions.** On an input-capable adapter (claude), a
denied action surfaces as a `permission` request and the task waits for you —
subject to `on_input`. Set `on_input: deny` and vincent denies them
automatically, keeping the run strictly unattended.

Use `restricted` for steps that have no business running commands: a docs pass, a
review, a summarization step. See
[Writing workflows](guides/workflows.md#93-permission-modes).

## The API surface

- **Loopback only.** `listen:` is validated to a loopback host; anything else is
  rejected at config load.
- **Bearer token on every request**, read from `{data_dir}/token`, created
  `0600`. On Windows it relies on the per-user ACL that `%LOCALAPPDATA%`
  inherits. Compared in constant time.
- **CORS is disabled**, which together with the token blocks drive-by requests
  from a browser tab.
- **No TLS**, deliberately: the socket is loopback and the token is the
  authenticator. There is nothing on the wire that does not already require local
  account access to reach.
- `GET /v1/health` is the single unauthenticated endpoint.
- **`POST /v1/daemon/backup` writes a file at a path the caller names**, as the
  daemon's user, anywhere that user can write. That is stated here rather than
  left to be discovered — but it is not an *additional* grant: the same token
  already starts agents that run full-auto as that user (above), which is a
  strictly larger capability. The endpoint refuses a relative path, refuses to
  overwrite an existing file, and refuses a destination inside
  `{data_dir}/transcripts`.

Anyone who can read your token file can drive your daemon — which, on a machine
where they are already you, is not an additional grant. On a shared machine, it
is the boundary that matters.

## Credentials

**vincent has no credential store of its own.** No vendor API keys, no OAuth
flow, no keychain entries. An agent step spawns the CLI you installed, which
authenticates however it already does.

That is a statement about *vincent's* credentials, not about yours. Two files
vincent owns can hold sensitive data because you put it there:

- **`{config_dir}/config.yaml`**, through
  [`environment.set`](reference/configuration.md#environment), whose values are
  literal — an API token, a proxy credential or a license key written there is
  plaintext on disk. The file is created `0600` inside a `0700` directory, and
  every daemon start re-tightens both on POSIX (contents untouched) and says in
  the log what it changed. `vincent doctor` reports a broad mode with the exact
  `chmod`. On Windows the per-user ACL of `%APPDATA%` applies instead.
- **Transcripts**, which record what your agent and commands actually printed.
  They are `0600` for the same reason.

`environment.set` is **not a secret store** — nothing is encrypted, and the file
is one you may sync between machines. Prefer naming a variable under
`environment.inherit` and letting the value come from the environment that
starts the daemon.

The token file gates only vincent's own API. It is not an agent credential and
grants no model access.

One consequence worth stating plainly: **vincent cannot reduce what your agent
CLI is allowed to do at the vendor.** It can only choose which permission switch
to pass.

## What vincent writes outside its own directories

Exactly one thing, and it is recorded here rather than discovered:

**A cursor step passes `--model`, and cursor persists that selection to
`~/.cursor/cli-config.json`.** vincent always passes one (defaulting to `auto`)
because leaving it unset would mean "whatever the last invocation chose",
possibly a previous vincent step — and determinism is worth more to an
orchestrator than preserving an interactive preference.

It is not a secret and not an escalation, but it does mean a cursor step
overwrites the model you last picked in an interactive `cursor-agent` session.

Everything else vincent writes lives in its
[config and data directories](reference/files.md), plus the git branches and
worktrees it creates in your repositories — and the one file you name yourself
when you run [`vincent daemon backup`](reference/files.md#backup-and-restore).

**A backup archive is sensitive.** It carries every transcript, which means
every rendered prompt and everything the agents did, plus your `config.yaml`.
It does *not* carry the API token. Treat the file the way you would treat the
data directory it came from.

## Tightening it

In rough order of value:

1. **Gate anything that leaves the machine.** A `manual` step in front of every
   push, publish, deploy or delete. This is the single highest-leverage control.
2. **Use `restricted` where the step does not need commands.** Docs, reviews,
   summaries.
3. **Point vincent at repositories you would hand to a new contributor.** That is
   the right calibration for full-auto.
4. **Read the diff before approving.** The Diff tab exists for this; the gate
   exists so you have a moment to use it.
5. **Cap concurrency.** `max_parallel_tasks` bounds how much can be happening
   without you.
6. **Turn on `debug: true` when a run surprises you.** It records the resolved
   settings and full argv of every step in its transcript.
7. **Prefer per-project scoped workflows** (`.vincent/workflows/`) so a
   repository's automation is reviewed with the repository.

## What is not in scope

Stated so you do not assume otherwise:

- **Sandboxing the agent beyond what its own CLI offers.** vincent passes the
  switch; it does not build a jail.
- **Multi-user or multi-tenant operation.** One OS user, one daemon.
- **Protecting you from your own workflow files.** A `command` step is a shell
  command.
- **Secret redaction in transcripts.** A transcript records the rendered prompt
  and everything the agent did. Read one before pasting it into an issue.
- **Remote access.** There is no remote binding, and adding one would need a
  different auth story than a loopback token.

---

## See also

- [SECURITY.md](../SECURITY.md) — reporting a vulnerability.
- [Agent CLIs](guides/agents.md) — per-adapter behavior.
- [Writing workflows](guides/workflows.md#93-permission-modes).
- Spec [§16](spec.md) — the normative version.
