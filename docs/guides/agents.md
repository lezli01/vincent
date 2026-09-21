# Agent CLIs

Vincent orchestrates agent CLIs you install and authenticate yourself. It
stores no credentials, embeds no model access, and speaks to no vendor API of
its own: an agent step spawns the same binary you would have run by hand, in
the task's worktree, with its prompt on stdin.

Three adapters ship. **What an adapter cannot do is documented and ignored at
run time — never emulated.** A field an adapter has no concept of is stated as
ignored here and in the spec, so a workflow never appears to honor something it
silently drops.

- [At a glance](#at-a-glance)
- [Claude Code](#claude-code)
- [Codex](#codex)
- [Cursor](#cursor)
- [How vincent finds a CLI](#how-vincent-finds-a-cli)
- [Agents in a container](#agents-in-a-container)
- [Choosing models and effort](#choosing-models-and-effort)
- [Choosing between them](#choosing-between-them)

---

## At a glance

| | claude | codex | cursor |
|---|---|---|---|
| Binary | `claude` | `codex` | **`cursor-agent`** |
| `agent:` value | `claude` | `codex` | `cursor` |
| Mid-run questions (`awaiting_input`) | ✅ | — | — |
| Resumes its own session ([chats](../reference/cli.md#vincent-chat)) | ✅ `--resume` | ✅ `exec resume` | ✅ `--resume` |
| Reports a session it can no longer resume | ✅ `session_lost` | ✅ `session_lost` | **—** (it adopts the id and answers) |
| Lists its skills in a chat | ✅ from 2.1.277 | ✅ `skills/list` | **—** (its `-p` mode has no listing) |
| Invokes a skill from a chat message | ✅ `/name` at the **start** | ✅ `$name` anywhere | ✅ `/name` anywhere |
| Transcript shows a skill you invoked | ✅ in chats | **—** | **—** |
| Expands an `@path` file mention | ✅ it reads the file for you | **—** (the path stays prose) | **—** (the path stays prose) |
| Reports cost | ✅ | — | — |
| `model:` | ✅ | ✅ (free text) | ✅ (~180 enumerated) |
| `effort:` | ✅ | ✅ | **—** (it lives in the model id) |
| `restricted` mode | ✅ | ✅ | ✅ on macOS/Linux, **refused on Windows** |
| Carries [vincent's MCP server](mcp.md) for one run | ✅ `--mcp-config` | ✅ `-c mcp_servers.…` | ✅ `.cursor/mcp.json` **in the worktree** |
| Tested-build list | `2.1.224`, `2.1.226`, `2.1.268`, `2.1.277` | `0.142.5`, `0.147.0`, `0.150.1`, `0.154.0` | `2026.08.04-aaa8809`, `2026.08.11-e8db854`, `2026.08.25-3e8eec8`, `2026.09.18-9a7762b` |
| Reports whether you are logged in | ✅ from 2.1.41 | ✅ | ✅ |
| Recognizes a usage limit / auth failure in a run | ✅ | — | — |
| Reports **remaining** quota without running | **push only** (its status line) | ✅ `app-server --stdio` | **—** (no usage surface) |

A skill is invoked in the CLI's own syntax, typed into your chat message;
vincent passes the message through unchanged and does not check the name. A
chat turn on claude asks it to report the commands it expanded, so the skill
you named — and a command inside it that a permission rule refused — shows up
in the turn's transcript as its own row. codex and cursor report no skill load
at all, which is what the third row says: there, the `$name` or `/name` in
your own message is the only evidence it ran. A task step asks for none of
this, on any agent. The first two skill rows are also on `GET /v1/agents`, as
`supports_skill_listing`, `skill_sigil` and `skill_position`. claude and codex
can list the skills they would load in a worktree, which is what the listing
row says. A chat's list,
with the exact invocation for each skill, is at
[`GET /v1/chats/{id}/skills`](../reference/api.md#skills), from
[`vincent chat skills`](../reference/cli.md#vincent-chat-skills), and on `tab`
in the TUI's [chat workspace](tui.md#chat-workspace), which writes the row you
take into your message. claude lists without spending a turn, on 2.1.277
or a later 2.x build. Its own built-in commands are never listed; its bundled
skills — `simplify`, `loop` and `run`, which it marks exactly the way it marks
those commands — are listed once one turn has run on that claude, a turn's own
report of what it loaded being the only thing that tells the two apart. That
first turn brings them back in every chat and every directory, not just the one
that ran it, and each arrives marked `builtin`. A chat on a task that runs in
a [container](configuration.md#container) is listed inside that container, by
the image's CLI — so its list, and its bundled skills, are the container's and
never your host's; `~/.agents` is not mounted there, so a containerized codex
or cursor chat lists nothing from it.
When two codex skills share a name, `$name` selects neither; name one
with codex's linked form, `[$name](path)`, which is the invocation that route
gives for it. In a chat
opened on a task, the task's context reaches claude as a separate block ahead
of your **first** message, so a `/name` there still starts the message and
runs. The exception is a claude build outside the verified input family (see
[Claude Code](#claude-code)): it receives the context and your message as one
piece of text, and leaves a first-message `/name` to the model.

In a chat that runs `restricted` — one opened on a task whose agent steps run
restricted, whether the task was created that way or its workflow says so — a
skill you invoke can carry its own tool grant: claude's `allowed-tools`
frontmatter applies for the turn that invokes the skill, so it may use tools
the restricted mode otherwise withholds. It takes an act of yours, either
typing the name or approving the request the agent raises when *it* wants the
skill, and a command the skill injects is still checked, with a refusal ending
the invocation before the agent runs. codex and cursor have no such field. The
[Security model](../security-model.md#restricted-mode) has the whole picture.

An `@path` **file mention** passes through the same way, and all three
adapters recognize one anywhere in a message — but only claude acts on it.
claude reads the mentioned file and puts it in front of the model before the
model sees your message; codex and cursor leave the path as prose, and the
model reads it with a tool if it has one, so a mention there is a hint rather
than guaranteed context. That is the expansion row above, and it is on
`GET /v1/agents` as `supports_file_mentions`, `file_mention_sigil`,
`file_mention_position` and `file_mention_expands`, of which only the last
tells the three apart — which is why
[`vincent agents`](../reference/cli.md#vincent-agents) notes `no @ file
expansion` on codex and cursor and nothing on claude. Vincent writes no
mention for you and checks no path: what you type is what the CLI gets, in a
chat turn and in a workflow agent step alike.

A CLI's **built-in** commands pass through the same way, and `/clear` is the
one worth knowing about. claude clears its own conversation and reports a new
session, which vincent uses for the next turn — so the agent starts empty while
the chat still shows every turn you already had, and nothing in the transcript
marks where that happened. If you want a fresh conversation with a record of
it, close the chat and open another.

[`vincent agents`](../reference/cli.md#vincent-agents), the TUI's daemon view,
and `GET /v1/agents` all report what vincent actually resolved on your machine — path, version, the model and
effort options it discovered, and the health verdicts below: whether the build
you have is one vincent has been tested against, and whether the adapter can
restrict anything on this operating system.

## Claude Code

**Binary:** `claude`. **Workflow value:** `agent: claude`.

The most capable adapter, and the only one that can be interrupted mid-step.

- Runs `claude -p --output-format stream-json --verbose`, cwd set to the
  worktree, **prompt on stdin** — never as an argv element, because Windows caps
  arguments at 8 KB and prompts embed task descriptions.
- `full-auto` adds `--dangerously-skip-permissions`; `restricted` maps to
  Claude's allowlist flags with an edit/read/git/test tool set — plus
  `mcp__vincent__*` in full, so `restricted` bounds the filesystem and the
  shell, **not** what the step does to vincent. See
  [Driving vincent from an agent](mcp.md#what-this-is-not).
- **Resumes its own session**, with `--resume <session_id>`. That is what makes
  a [chat](../reference/cli.md#vincent-chat) work: vincent stores the
  `session_id` claude stamps on its stream and hands it back on the next turn,
  so turn N sees turns 1..N-1 without anything being replayed into the prompt.
  Workflow steps never set it — every step still gets a fresh session. A session
  the CLI no longer knows fails that turn with `session_lost` rather than
  quietly starting a new one.
- **Carries [vincent's own MCP server](mcp.md#your-own-steps-get-this-too)** on
  `--mcp-config` with an inline config, alongside `--strict-mcp-config` so your
  own MCP servers never leak into a step. Per run: nothing global is written.
- `model:` and `effort:` pass straight through as `--model` / `--effort`.
  Options are discovered by parsing `claude --help` and merged with a curated
  catalog, so a CLI upgrade that adds an effort level makes it selectable without
  a vincent release.
- **Reports the most about its own run.** The output pane's `#` header line —
  the working directory and the tool set a run was given — and the closing
  line's duration, turn count, cache-token split, per-model breakdown, refused
  tool calls and stop reason all come from claude's stream and from no other
  adapter's. A tool call a permission rule refused is marked apart from one that
  ran and failed. See [the output pane](tui.md#what-v-adds).
- **Reports its subagents.** When claude hands work to subagents, every line
  names the one that produced it, and claude says how each one ended. The output
  pane and `vincent task transcript` draw that work nested behind a rail rather
  than as the agent's own. Codex and cursor report no subagents, and vincent
  does not infer one from their tool calls. See
  [When the agent runs subagents](tui.md#when-the-agent-runs-subagents).
- **Reports what its edits changed.** An edit's outcome is the lines it added
  and removed, `✓ +13 −9`, and at `verbose` the output pane shows the hunks
  themselves. A new file reads `created`, and a subagent's edits keep claude's
  own sentence, because claude reports no change for either. Cursor reports the
  counts for its edits and no hunks; codex reports neither. See
  [the output pane](tui.md#what-v-adds).
- **Reports token usage and cost.** The board's cost column sums every attempt,
  retries included. The other two adapters report no cost at all — which also
  means [`max_task_cost_usd`](../reference/configuration.md#max_task_cost_usd)
  can only stop a task that ran on claude. The same goes for
  [`max_tree_cost_usd`](../reference/configuration.md#max_tree_cost_usd): a
  fan-out tree counts only its claude spend, so codex and cursor lanes add
  nothing to the total.
- **Recognizes a spent usage quota and a logged-out CLI** in the output of a run
  that failed. A quota stop becomes `usage_limit` — no retry consumed, and by
  default the task waits and re-runs itself, though
  [`usage_limit_auto_continue`](../reference/configuration.md#usage_limit_auto_continue)
  can make it block for you instead — and a logged-out CLI becomes
  `agent_unauthenticated`, which blocks with the fix named. Codex and cursor do
  neither: their wordings have not been captured from a real run (doing so means
  burning a real quota window), and vincent will not guess at one, because a
  wrong guess parks a genuinely failed task in a wait it never leaves. On those
  two, both conditions still read as `agent_error` or `nonzero_exit`. See
  [Troubleshooting](troubleshooting.md#usage_limit--do-nothing-unless-you-asked-to-be-told).
- **Reports its remaining quota, but only by pushing.** There is no usage
  subcommand to poll; what Claude Code has is a status line, which it hands both
  usage windows on every render.
  [`vincent statusline`](../reference/cli.md#vincent-statusline) is what catches
  them, and the daemon view's `i` is what installs it — after showing the exact
  JSON it would write to `~/.claude/settings.json`. Until then claude reports
  nothing, and a claude step [in a container](#agents-in-a-container) reports
  nothing either. See [How much quota is left](#how-much-quota-is-left-and-who-will-say).

### Mid-run questions

Claude Code is the one adapter with a control channel, so a step can pause,
ask, and resume in the same session. When the agent uses its AskUserQuestion
tool, vincent normalizes the request into a `question` (with option labels, and
multi-select honored); in `restricted` mode a denied tool surfaces as a
`permission` request instead.

The task moves to `awaiting_input`, keeps its concurrency slot, and pauses the
step's timeout clock. Answer from the TUI popup or
`POST /v1/tasks/{id}/answer`; the run resumes where it stopped. Set
`on_input: deny` on a workflow that must stay unattended — see
[Writing workflows → mid-run questions](workflows.md#94-mid-run-questions-on_input).

Input support is **version-gated** to the CLI family vincent has verified
against real captured runs. Outside that range the adapter reports
`supports_input: false` and runs exactly as it otherwise would — no input flags,
plain-text prompt. Nothing degrades silently.

A workflow that *needs* the conversation says `on_input: require`, and then a
claude outside the verified family is refused for that step rather than run
unattended — `GET /v1/agents` reports `input_verdict: "unsupported"` for it, and
a step that reaches the engine anyway fails with `input_unsupported`.

## Codex

**Binary:** `codex`. **Workflow value:** `agent: codex`.

- Runs `codex exec --json`, cwd set to the worktree, prompt piped on stdin.
- `full-auto` maps to `--dangerously-bypass-approvals-and-sandbox` — the
  documented automation switch. `restricted` maps to `--sandbox workspace-write`,
  confining writes to the worktree.
- `model:` passes as `-m`; `effort:` as `-c model_reasoning_effort=…`. Efforts
  are `minimal, low, medium, high, xhigh`.
- **Carries [vincent's own MCP server](mcp.md#your-own-steps-get-this-too)** the
  same way: `-c mcp_servers.vincent.…` dotted overrides, with the bearer token
  handed to the child in its environment rather than written down. Per run —
  your `config.toml` is never modified.
- **No model catalog.** The CLI enumerates nothing, and codex model availability
  is account-dependent — the same id is accepted on one plan and rejected on
  another — so pickers offer free text and the CLI's own default. A model you
  type is passed through with a warning, not rejected.
- **No cost reporting**, and `supports_input: false`: a codex step never enters
  `awaiting_input`, and `on_input: wait|deny` has no effect on it. `on_input:
  require` is the one that does: a step declaring it cannot use codex at all,
  and a workflow pinning `agent: codex` on such a step fails validation.
- **Resumes its own session**, so codex holds a
  [chat](../reference/cli.md#vincent-chat) like Claude Code does. Resume is a
  subcommand rather than a flag — `codex exec --json resume <thread_id>` —
  and the prompt stays on stdin, because `exec resume` reads stdin when it is
  given no prompt argument. vincent stores the `thread_id` the stream reports
  on `thread.started` and hands it back on the next turn. The argv is pinned by
  a capture against codex-cli 0.150.1; nothing is emulated, and vincent never
  replays a conversation into the prompt. Workflow steps never set it: every
  step still gets a fresh session. Two consequences, both stated rather than
  worked around:
  - A **resumed run is always full-auto.** `codex exec resume` has no
    `--sandbox`, so `restricted` has no spelling on it. Only a chat turn ever
    resumes, and a chat you start yourself is always full-auto — there is no
    permission mode to ask for on one. A chat
    [opened on a task](../reference/task-lifecycle.md#chatting-with-a-stopped-task)
    is the exception: it takes the task's permission mode, and codex cannot
    keep a resumed turn restricted. So opening a codex chat on a task that runs
    `restricted` is refused, and codex itself refuses a resumed restricted run
    rather than running it full-auto. claude and cursor pass their restriction
    on every turn, so use one of those for a chat on a restricted task.
  - A thread codex no longer knows fails that turn with `session_lost` rather
    than quietly starting a new one.
- **The plan and command output are surfaced.** Codex reports a running to-do
  list, which the pane shows with a `☰` gutter at the `normal` and `verbose`
  output levels, ticking over as the agent works. What a command printed shows
  at `verbose` only — it is the output body, and a step running `go test ./...`
  would otherwise flood the level most readers use; a body long enough to hit
  the cap ends in `… output truncated`. File changes are named by their paths
  and kinds, and MCP calls by server and tool.
- **Token usage is read whole.** All five of the counters codex reports land in
  the run's accounting, so `verbose` shows a codex turn's cache read/write split
  and its reasoning spend rather than leaving them blank. Cost is still absent,
  because the CLI does not report one — see the bullet above.
- **Reports its remaining quota on request**, and is the only adapter that
  needs no setup to do it: `codex app-server --stdio` answers
  `account/rateLimits/read` with its `primary` and `secondary` windows, which
  vincent asks for on the ordinary catalog refresh. A missing binary, a
  logged-out account, a timeout or a malformed answer degrade silently and fail
  no probe. See [How much quota is left](#how-much-quota-is-left-and-who-will-say).
- **Reasoning is surfaced.** Codex emits whole reasoning blocks, which the TUI
  shows at the `normal` and `verbose` output levels (`v` cycles them) and the
  transcript records as `agent.thinking`. Whether any are emitted depends on
  the effort you asked for — a low-effort turn can spend reasoning tokens and
  produce no blocks at all.

> **Caveat on `restricted` + git.** In a linked worktree the real git directory
> lives under the main repository, outside the sandbox, so a `git commit` from a
> restricted codex step may be denied. Vincent itself never needs a commit — the
> diff reads the working tree — but a workflow that commits from a restricted
> codex step should use a `command` step for the commit instead.

## Cursor

**Binary:** `cursor-agent` — **never `cursor`**, which is the editor launcher
and would open a GUI. **Workflow value:** `agent: cursor`.

- Runs `cursor-agent -p --output-format stream-json --trust`, cwd set to the
  worktree, prompt on stdin. `full-auto` adds `--force`; `restricted` adds
  `--sandbox enabled` instead.
- `--trust` is passed in **both** modes: a task runs in a git worktree the CLI
  has never seen, and a workspace-trust prompt in a headless run is a hang, not a
  question.
- Vincent's own worktree flags are never passed to it. Cursor has a worktree
  feature; worktrees belong to vincent, and two owners of one concept is a defect.
- Reports token usage but **no cost**, and `supports_input: false` — so, like
  codex, cursor cannot back a step declaring `on_input: require`, and pinning it
  on one is a validation error.
- **Resumes its own session** too, with `--resume <session_id>` — the same
  `session_id` its stream stamps on every line — so cursor can hold a chat.
  Unlike claude and codex it has **no way to tell you a session is gone**:
  handed a `--resume` id it has never seen it starts a fresh chat under that
  id and answers normally, so a cursor chat whose session has aged out replies
  without remembering the conversation instead of failing `session_lost`.
- Errors do not arrive in the stream — an invalid model id exits 1 with a message
  on stderr and no result line — so the adapter reports "stream ended without a
  result event" plus the stderr tail, which is what makes an everyday typo
  diagnosable.

Four things about cursor are genuinely different, and all four are visible in
a workflow:

### 1. Effort lives in the model id

Cursor has no effort flag. Reasoning depth is encoded in the model:
`claude-sonnet-5-thinking-xhigh`, `gpt-5.4-mini-high`. So `effort:` on a cursor
step is **ignored**, its effort catalog is empty, and
`vincent workflow validate` rejects a claude or codex effort value on a cursor
step — which is exactly the error a workflow author needs to see.

Run `cursor-agent models` to see what your account offers; vincent probes the
same list.

### 2. A cursor step overwrites your saved CLI model

Cursor persists whatever `--model` it is given into `~/.cursor/cli-config.json`,
so leaving the model unset means "whatever the last invocation chose" — possibly
a previous vincent step. To keep runs reproducible the adapter **always passes
`--model`**, defaulting to `auto`.

The accepted cost: running a cursor step overwrites the model you last picked in
an interactive `cursor-agent` session. Determinism is worth more to an
orchestrator than preserving an interactive preference, and `auto` at least
lands on cursor's own default rather than on wherever the last task left it.

### 3. `restricted` needs macOS or Linux

`--sandbox enabled` exits 1 on Windows with *"Sandbox requires macOS or
Linux"* before doing any work. Vincent therefore **refuses to create a task**
whose restricted step resolves to cursor on Windows: `POST /v1/tasks` answers
`400` naming the step and the agent, and the TUI shows that message on the
new-task form. A task that reaches the engine anyway — a data directory carried
to Windows, or a workflow edited after the task was queued — fails to start with
block reason `restricted_unsupported`, under the normal retry policy.

Falling back to `--force` was rejected outright: it would run full-auto a step
that explicitly asked not to be, turning a safety choice into its opposite on
exactly one OS — the failure mode nobody would think to check for. See
[Windows](../platforms/windows.md#restricted-mode-and-cursor).

### 4. A cursor step writes `.cursor/mcp.json` into the worktree

Cursor has no per-run MCP flag at all — `cursor-agent mcp` reads only
`.cursor/mcp.json` in the workspace or `~/.cursor/mcp.json` globally. So to give
a step [vincent's own tools](mcp.md#your-own-steps-get-this-too) the adapter
writes the **workspace** file into the task worktree before the run, passes
`--approve-mcps` so a headless run does not stop on a trust prompt for the
server vincent just configured, and removes the file (and an empty `.cursor/`)
after the step ends. Your global `~/.cursor/mcp.json` is never touched.

The accepted cost, and the one you can see: while a cursor step is running, that
file is an untracked file **inside a git worktree**. It shows up in `git status`,
in the task's diff, and in dirty detection for the duration of the run. It is
gone once the step ends, and if the daemon dies mid-step
[recovery](../reference/task-lifecycle.md) sweeps it on the next start. The
alternative — writing your global config instead — was rejected: a per-task
change to a file you own outside vincent is worse than a transient file inside a
worktree vincent already owns.

`mcp.wire_steps: false` skips all of it: no file is written and `--approve-mcps`
is not passed.

### The model list is advisory in both directions

`cursor-agent models` lists roughly 180 ids, but the list is account-scoped and
still over-broad: an id it lists can be rejected at run time. So membership is a
hint, free text stays accepted, and the CLI is the final authority. The TUI's
model picker is windowed and type-filterable for exactly this catalog.

## How vincent finds a CLI

By default the daemon resolves each adapter's binary from `PATH`. Override it
per adapter in [`config.yaml`](../reference/configuration.md):

```yaml
agents:
  claude: { path: "" }                              # "" = resolve from PATH
  codex:  { path: "/usr/local/bin/codex" }
  cursor: { path: "C:/Users/me/.local/bin/cursor-agent.exe" }
```

An explicit path is absolute and never consults `PATH`, which makes it the
standing fix for "my shell finds it, vincent does not".

**Detection is cached by binary identity** — resolved path + mtime + version.
Help output is a pure function of the installed binary, so the cache cannot go
stale by construction: upgrading a CLI invalidates it and the next request
re-probes. `vincent agents --refresh`, `GET /v1/agents?refresh=true` or `R` in
the TUI's new-task view forces one.

A failed probe **expires**; a clean one does not. Nothing about a binary changes
when a probe times out, so caching that failure forever would serve one bad
moment for the daemon's whole lifetime.

Probe failure degrades rather than blocks: if a CLI is missing or its help
output cannot be parsed, vincent serves the curated catalog with `probe_error`
set, and free-text entry is unaffected.

### "Found" is not "usable"

An installed but unauthenticated CLI probes as healthy and then fails every
single run. Every supported CLI can answer that cheaply, so vincent asks:
`claude auth status`, `codex login status` and `cursor-agent status` populate
`logged_in` with a definite true/false. claude is asked only from 2.1.41, the
build that introduced `auth status`, up to 3.0.0; an older (or not yet verified
newer) claude is never asked, reports `null`, and the TUI renders that as
unknown rather than as fine.

`true` means the CLI found credentials configured, not that they work: a
claude.ai login, an `ANTHROPIC_API_KEY` or a Bedrock/Vertex switch all report
`true` for claude, and codex's `login status` makes the same claim. An expired
or revoked key still shows up as a failed step. vincent reads only the yes/no —
never the account email, organization or plan the CLI prints alongside it.

No probe ever guesses. For codex and cursor, a non-zero exit is `false`, an
explicit negative is `false`, an explicit positive is `true`. For claude, only
the `loggedIn` field of its JSON answer decides, whatever the exit code — a
bare exit `1` is just as often a CLI error. And for all three, **anything else —
including a probe that times out or cannot be spawned — is `null`**. That last rule is why a slow
machine never gets told it is logged out: on Windows a killed probe exits `1`,
and reading that as "not authenticated" would be a false accusation.

[`vincent doctor`](../reference/cli.md#vincent-doctor) prints this row for every
adapter and **re-probes each time**, so logging in and running it again shows the
change immediately — unlike `GET /v1/agents`, which serves the binary-identity
cache.

If a service-installed daemon reports agents as missing while the same daemon
started by hand finds them, that is a `PATH` capture problem — see
[Running at login](running-at-login.md#path-too-on-macos-and-linux).

`logged_in` gets its own five-minute freshness window inside that cache. Binary
identity is exact for `--help` output and only a floor for auth state — nothing
about the binary changes when you log in — so past five minutes vincent re-asks
that one question and leaves the option catalog alone. A re-ask that fails keeps
the previous answer rather than downgrading it, for the same reason the probe
never guesses.

### Was this build ever tested?

Every adapter carries the list of CLI builds vincent's parsers were captured
against, and reports a verdict on the one you have installed:

| Verdict | Meaning | What it changes |
|---|---|---|
| `tested` | your build is one vincent has fixtures for | nothing |
| `untested` | anything else | **nothing** |
| `incompatible` | a build vincent knows breaks | nothing today |

`untested` is the normal, expected answer. Agent CLIs ship far more often than
vincent does, so a few weeks after any release most people are on a build newer
than the fixtures — and it is expected to work. The verdict is **advisory
everywhere**: no run is refused, no task is blocked, and `vincent doctor` does
not exit 1 over it. It exists so that when something *does* look wrong, you can
tell at a glance whether you are on ground vincent has walked.

Comparison is exact: cursor's version is `2026.08.04-aaa8809`, calver plus a
commit sha, which no version range can order — so rather than have one adapter
answer a different question from the other two, all three compare whole strings.
The `incompatible` list ships empty for all three, because no such build has
been observed.

`vincent doctor` and the TUI's daemon view print the verdict beside each
adapter, along with the builds it was judged against;
[`GET /v1/agents`](../reference/api.md) carries it as `version_verdict` and
`tested_versions`.

### Can this agent restrict anything here?

`restricted_verdict` answers whether an adapter can honour
`permission_mode: restricted` **on this machine**. Cursor answers `unsupported`
on Windows and `supported` everywhere else; claude and codex answer `supported`
everywhere.

This is the one health verdict that refuses something. A task whose restricted
step resolves to an adapter that cannot restrict here is rejected at creation
with a `400` naming the step and the agent, rather than being allowed to spend a
worktree and a retry to fail with `restricted_unsupported`. The answer does not
depend on the installed binary — it is a fact about the adapter and the
operating system — so it is correct even where the CLI is missing entirely.

The step failure still exists as a backstop, for a task whose daemon changed
underneath it: a data directory carried to Windows, or a workflow edited after
the task was queued.

### How much quota is left, and who will say

Two of the three adapters can now answer "how much quota do I have?" without
running a step, and they answer in opposite directions.

- **codex answers on request.** `codex app-server --stdio` speaks JSON-RPC over
  stdio and replies to `account/rateLimits/read` with the same `primary` and
  `secondary` windows the CLI prints when it walls a run. Vincent asks on the
  ordinary catalog refresh — at most once every five minutes, and
  unconditionally on `vincent doctor`, `vincent agents --refresh` or
  `GET /v1/agents?refresh=true`. There is
  nothing to install and nothing to configure.
- **claude pushes.** Claude Code has no `usage` or `limits` command to poll, but
  it hands its status line a JSON object carrying both windows on every render.
  [`vincent statusline`](../reference/cli.md#vincent-statusline) reads that
  object, posts it to the daemon and prints the status line you already had; the
  daemon view's `i` is what installs it, after showing the exact JSON it would
  write to `~/.claude/settings.json`. Nothing is reported while it is not
  installed.
- **cursor cannot.** `cursor-agent about --format json` reports a plan tier and
  no numbers, so cursor implements no quota capability at all, spawns nothing,
  and is observation-only exactly as before. Vincent does not emulate what an
  adapter cannot do — there is no "cannot report" stub and no fabricated
  percentage.

A reported reading is held **in memory**, not the database: it is as durable as
the daemon, and a restart drops it until the next probe or push. Nothing here
can fail a probe — a missing CLI, a logged-out account, a handshake that times
out or a malformed answer all fall back to the behaviour below, and
`probe_error` keeps meaning only "the option probe failed".

Underneath both, vincent still **remembers**. When an agent stops on a spent
window, the daemon records that per adapter — when it was seen, when it resets,
and whether the CLI named that reset or vincent estimated it from
[`usage_limit_recheck_interval`](../reference/configuration.md#usage_limit_recheck_interval).
That observation outlives the task's own wait, and is what an adapter with no
reported reading is rendered from. A reading wins where there is one, so:

- the board header badges the adapter — `claude ⏳14:20` instead of `claude ✓`;
- the daemon view spells it out beside path, version and login state. A reading
  is written window by window with the time it was taken —
  `quota codex app-server · 5h 28% → 13:00 · 7d 53% → 11:00 · read 09:14` — and
  an observation as `usage limit → 14:20` with `→` for a reset the CLI stated
  and `≈` for one vincent estimated. An adapter with neither says
  `quota unknown`;
- the new-task form warns under the agent row — `· usage limit until 14:20`;
- [`vincent agents`](../reference/cli.md#vincent-agents) prints the same block
  in its `QUOTA` column for a shell or a script, with full local timestamps
  because a seven-day reset is days away —
  `codex app-server · 5h 28% → 2026-09-16T13:00:00+02:00 · … · read …`,
  `spent → …` or `spent ≈ …` for an observation, `unknown` for neither.

The warning is advisory. The form still submits and admission is unchanged. A
task queued against a window vincent watched close parks on the ordinary
[`usage_limit` wait](troubleshooting.md#usage_limit--do-nothing-unless-you-asked-to-be-told)
when it reaches its agent step, without starting the agent. If
[`usage_limit_auto_continue`](../reference/configuration.md#usage_limit_auto_continue)
says not to wait, it starts and finds the limit itself. A reported reading never
holds a task, even at 100%. Since only claude recognizes a quota stop at all, that key is
inert on the other two, exactly as the table above says. The next
successful step on that adapter retires the **observation** — and only the
observation, since a step completing proves the wall vincent watched has come
down and proves nothing about a percentage a vendor reported — so an estimate is
never left standing over a CLI that is visibly working.

`GET /v1/agents` and `GET /v1/info` carry all of it as
[`quota`](../reference/api.md#usage-quota), whose `source` says which kind of
fact you are holding.

## Agents in a container

Set [`container.image`](../reference/configuration.md#container) and a task's
agent steps run **inside the task's container**, next to its command steps. The
CLI that runs is the image's, not yours: vincent looks up `claude`, `codex` or
`cursor-agent` by name on the image's `PATH`. `agents.*.path` is a host path and
is ignored there, and the host does not need the CLI installed. An image without
it fails the step `agent_unavailable`. The transcript, the token and cost
records and the exit code are the same as a host run's. Chats are not tasks and
run on the host — except a chat
[opened on a containerized task](../reference/task-lifecycle.md#chatting-with-a-stopped-task),
whose turns run in that task's container with the image's CLI, resuming the
session the mounted agent configuration keeps between turns.

Everything on this page that vincent *probes* still describes the host's CLI —
`vincent agents`, `vincent doctor`, `GET /v1/agents`, the model and effort
catalog, login state, codex's quota reading and the usage-limit holds. Two
things are asked of the image instead. claude's
[mid-run questions](#mid-run-questions): whether a containerized claude can take
an answer is decided by running `claude --version` **in the image**, so
`on_input: require` is judged against the claude that will actually run. And
the skill list of a chat opened on such a task, which is read by the image's
CLI inside the container, as above.

**Logging in.** [`mount_agent_config`](../reference/configuration.md#container),
on by default, mounts your `~/.claude`, `~/.codex` and `~/.cursor` read-write
under the container's `HOME`, so a CLI you are logged in to on the host is
logged in inside the container too. There is one gap: **claude on macOS** keeps
its login in the Keychain, not in `~/.claude`, and the container cannot reach
it. On a Mac, pass `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` to the container through
[`environment`](../reference/configuration.md#environment). Codex's
`auth.json`, and claude's own credentials file on Linux, carry over as they are.

**`vincent status` does not work inside a container.** The image has no vincent
binary, and `127.0.0.1` inside the container is not your daemon. A
containerized agent reports what it is doing through the `step_status` tool on
[its MCP endpoint](mcp.md#your-own-steps-get-this-too) instead, which it has as
long as `mcp.wire_steps` is on. If a prompt asks for `vincent status`, ask for
that tool when the workflow runs in a container.

**The `vincent statusline` hook does not report from a container.** If you
installed it, the mounted `~/.claude/settings.json` points claude at a vincent
binary on your host that the container does not have. Claude tolerates a status
line that fails, so the run is unaffected, but the usage windows it would have
pushed during that run never reach the daemon.

**Permission modes are unchanged.** `restricted` in a container is still
restricted, and `full-auto` is still full-auto, with the container's reach
instead of your machine's. Cursor's "`restricted` needs macOS or Linux" rule is
judged against the host, and a Windows daemon refuses containerized tasks
anyway. See [the security model](../security-model.md#what-the-container-does-and-does-not-isolate).

## Choosing models and effort

Set `agent`, `model` and `effort` on a step, in workflow `defaults`, or per task
at creation. Resolution is first-hit-wins:

1. the explicit step field
2. the task-level override chosen at creation (`--agent` / `--model` / `--effort`)
3. workflow `defaults`
4. the adapter's default (usually empty — the CLI decides)

**Model and effort only inherit from a level whose agent matches.** When a step
or a task override switches agent without setting them, they reset to the new
adapter's default rather than leaking across — a claude alias like `sonnet` must
never reach codex. The TUI's new-task form shows which level won for each field,
and `POST /v1/resolve` answers the same question for a script.

`vincent workflow validate` catches a value belonging to another adapter's
catalog. It cannot catch a model your account lacks: the CLI is the final
authority there, and you find out at run time.

## Choosing between them

- **Anything where you may want to answer a question mid-run** — claude. It is
  the only adapter that can be asked something mid-run; all three can be
  resumed, so all three can hold a chat.
- **Cost tracking matters** — claude. The other two report none, so the board's
  cost column stays empty for them and a configured spend cap, per task or per
  tree, never counts their runs. Vincent will not estimate money from token
  counts.
- **Cheap, strictly unattended passes** — codex or cursor are fine; set
  `on_input: deny` and neither will ever try to stop for you anyway.
- **You want a specific model cursor offers** — cursor, remembering that the
  reasoning level is part of the model id and that the step will rewrite your
  saved CLI selection.
- **Mixed workflows are normal.** Set `agent:` per step: implement with one,
  review with another. A second opinion from a different vendor on the same
  diff is one of the better uses of a multi-step workflow.

---

## See also

- [Writing workflows](workflows.md) — where `agent:`, `model:` and `effort:` go.
- [Security model](../security-model.md) — what `full-auto` and `restricted`
  actually mean.
- [Troubleshooting](troubleshooting.md#an-agent-cli-is-not-found).
- Spec [§9](https://github.com/lezli01/vincent/blob/master/docs/spec.md) — the normative adapter contract.
