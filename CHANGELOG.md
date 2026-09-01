# Changelog

All notable changes to vincent are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with the pre-1.0 caveat spelled out
in [Versioning and stability](#versioning-and-stability) below.

Release Please creates release entries from Conventional Commit history. Its
release pull request is the review point for replacing the mechanical commit
list with the user-facing context a commit subject cannot carry.

## [Unreleased]

### Added

- **An assistant message split across records renders as one message.** When an
  agent delivers one answer as several output records, consecutive records are
  now read as a single Markdown document rather than one document each. A
  table, a list or a fenced code block spread across two of them renders as the
  one thing it was written as instead of two broken fragments, and a link
  destination named twice in one message gets one number and one reference
  line. Anything that is not assistant prose — reasoning, a tool call, command
  output — ends the message, and prose after it starts the next.
- **A pane you have scrolled away from keeps its place.** Resizing the
  terminal, changing the verbosity level, toggling the raw view, and output old
  enough to be dropped from the window used to move a paused reader somewhere
  else. The output pane and the chat body now hold the block at the top of the
  view across all four, in both workspaces. Following the tail is unchanged.
- **The copy picker's rows name a message rather than a snapshot of it.**
  Picking a message that is still being written copies the whole message as it
  stands at that moment; a finished one is unaffected, and a row whose records
  have since been dropped from the window still copies what it offered, so a
  pick can never fail.

- **The mouse wheel scrolls a chat's conversation.** The chat workspace was the
  one scrollable pane the wheel did not reach — `pgup`/`pgdown` and `ctrl+g`
  were the only way to move it, which matters most here because the composer
  holds the keyboard almost always. A tick now moves the conversation one line,
  wheeling back pauses following the live end and `ctrl+g` re-arms it, and turns
  whose transcripts had not been fetched yet are fetched as you wheel to them
  rather than leaving blank space. It scrolls from anywhere in the view: the
  conversation is the only thing on that screen that scrolls, which is what the
  TUI's rule — the wheel moves the focused panel, not the hovered one — already
  says to do. The chats board stays keyboard-only.

- **A chat can now hand its worktree and branch to a task.** An idle chat gains
  one action — `hand off` — that creates a task adopting the chat's worktree
  path, branch, base branch and base SHA *exactly as they are*. Nothing is
  copied, renamed, merged or committed, so committed **and uncommitted** work
  are both there when the task's first step runs. The chat becomes terminal
  (`handed_off`) and keeps a permanent link to the task; the task links back
  with `source_chat_id`. From then on the task is the sole owner of that
  worktree and branch, which is structural rather than a guard: archiving is
  not legal on a handed-off chat, so chat cleanup can never remove task-owned
  state.

  It is one transaction — task row, branch claim, link, transition, claim
  release and both durable events (`task.created` and the new
  `chat.handed_off`) — with the scheduler notified only after the commit, so
  the scheduler cannot admit a task before it owns a complete workspace and
  `vincent gc` never sees the directory claimed twice or not at all. Everything
  is validated first, so a refused handoff leaves the chat exactly as it was.

  Reachable everywhere, not just in the TUI: `ctrl+t` in the chat workspace
  opens the new-task form in handoff mode (the project, base branch and branch
  shown read-only, because they name a worktree that already exists),
  `vincent chat handoff CHAT_ID --title ...` carries `vincent task add`'s
  flags, and `POST /v1/chats/{id}/handoff` takes `POST /v1/tasks`' body and is
  validated by the same code. Like the rest of the chat family, it is
  deliberately **not** an MCP tool.

  Two refusals, both `409` and both typed: a chat that is not idle or has no
  worktree to give, and a worktree partway through a merge, rebase,
  cherry-pick, revert or bisect — named in `details.operation`, rather than
  inherited silently and surfacing later as an unexplained `git_error` inside a
  step. Ordinary uncommitted changes are never refused.

- **Tables, links and highlighted code blocks in assistant prose.** A Markdown
  table is laid out to the pane rather than left as pipes: aligned with a rule
  under its header when it fits, and — when the pane is narrower than its
  columns' widest unbreakable words — stacked as one `column: value` record per
  row, never clipped and never behind a sideways scroll. Surplus width is
  shared between columns in proportion to what each still wants, so the same
  source at the same width always draws the same table. A link renders its
  label as prose with a dim `[1]`, and the message ends with the `[1] https://…`
  lines that resolve them, one per distinct destination; an image is its alt
  text plus its source. Nothing is fetched, nothing is opened, and vincent
  emits no OSC 8 hyperlink, so a destination — whatever its scheme — is text
  you can read and copy. A fenced block now shows the language it declared and
  tints its body from vincent's own palette over a written-down language list,
  with a plain fallback for the rest; the tint is styling only, so stripping it
  gives back the agent's bytes exactly. Reference links, autolinks, bare URLs
  and titled links stay literal. Spec §15, §16.
- **Rendered/raw toggle and copy actions for assistant Markdown.** `ctrl+o`
  swaps the rendered view for the stored Markdown in both the task output pane
  and the chat workspace — one session-wide choice, shared by the two, nothing
  persisted — and `ctrl+y` opens a copy picker offering each assistant message
  as its Markdown, as plain text with the punctuation gone, and each fenced code
  block on its own without its fence or language label. Payloads are built from
  the source rather than from what is drawn, so the pane's width is never baked
  into what you paste, and they are stripped of escape sequences and control
  characters on the way out. The system clipboard is tried first and the
  terminal's own OSC 52 second, which is the one that works over SSH; the notice
  says which ran and never claims an unverified copy. A plain-text copy keeps
  each link's `[1]` marker and the `[1] https://…` block that resolves it, so a
  destination survives the punctuation being stripped; copying one on its own is
  still a follow-up.
- **`ctrl+p` opens the command palette from a text field.** `:` is a printable
  key, so in a chat it typed a colon into the draft and opened nothing — the
  palette had been unreachable from the chat workspace since it landed. `ctrl+p`
  is hoisted above the input-capture gate the way `ctrl+v` already is, and works
  everywhere `:` does.
- **Assistant prose renders as Markdown in the task output pane and in chats.**
  Headings, emphasis, strong text, ordered and unordered lists, nested lists,
  blockquotes, inline code, fenced code and horizontal rules become terminal
  structure instead of literal syntax, through the one renderer both workspaces
  already share. Everything else stays literal — reasoning, tool calls, tool
  results, command output, errors and unmodeled lines keep their own gutter
  marks, because Markdown punctuation in a grep hit was never meant as
  formatting. Structure lives inside the assistant content column, so the
  two-column activity gutter is unchanged, and every marker is a glyph rather
  than a colour: a monochrome terminal or an SSH session keeps every
  distinction. Constructs outside the supported list — reference links,
  autolinks, bare URLs, titled links, HTML, footnotes — render as safe literal
  text, and raw HTML is never parsed, fetched or executed. The transcript on
  disk and every API payload keep the agent's exact bytes; a resize re-renders
  from the Markdown. Spec §15.

- **A codex step's output pane now shows what codex actually reported.** The
  adapter read the subset of `codex exec --json` real captures existed for, and
  the upstream schema had grown well past it — so a codex step's pane was
  markedly thinner than a claude step's. It now surfaces the agent's **running
  to-do list** (`☰`, ticked over live, at `normal` and `verbose`), **what a
  command printed** (`verbose` only, capped and with the cut stated rather than
  silent), **file changes named by their paths and kinds** instead of the bare
  word `file_change`, and **MCP calls named by server and tool**. Two new
  transcript and live-output records carry them — `agent.plan` and
  `agent.command_output` — in the shared vocabulary rather than a codex-only
  one, so a second adapter that reports a plan fills the same record. Because
  transcripts are re-normalized on every read, this improves codex transcripts
  **already on disk**. Full detail in spec §9.3, §13.2, §13.3 and §15.
- **Chats work on codex and cursor.** Both adapters now resume their own
  session, so a chat can be created on either and turn 2 sees turn 1 — codex
  through `codex exec --json resume <thread_id>`, cursor through
  `--resume <session_id>`. Each half is pinned to a fixture captured from a
  named CLI build (codex-cli 0.150.1, cursor-agent 2026.08.11-e8db854), which
  was the condition task 063 attached to this. Nothing replays a log as prompt
  context; the `agent_cannot_resume` refusal is kept as the contract for the
  next adapter, and now has no shipped adapter to refuse. Two limitations are
  stated rather than worked around: a resumed codex run is always full-auto
  (`codex exec resume` has no `--sandbox`, and a chat has no permission mode to
  ask for), and cursor never reports `session_lost` — handed an id it does not
  know it starts a fresh chat under that id and answers, so a cursor chat whose
  session has aged out replies without remembering (spec §9.3, §9.7).
- **`agent.result` reports codex's cache and reasoning token spend.** All five
  of `turn.completed.usage`'s counters are read now, not two:
  `cached_input_tokens` and `cache_write_input_tokens` land in the existing
  `cache_read_tokens`/`cache_write_tokens` keys, and `reasoning_output_tokens`
  in a new `reasoning_tokens`. A codex turn's cache behaviour and reasoning
  spend were previously unobservable.

- **A Pull Request tab on the task workspace, with live CI checks.** A sixth
  full-screen tab, appended after Workflow and selected by `6`, showing the
  linked pull request's facts plus one row per check on its head commit — its
  name, its state and its own GitHub page. It is the first conditional tab: it
  is on the strip only when the task has a live link and `github.enabled` is
  on, and `tab`/`shift+tab` skip it when it is not there. `↑`/`↓` select a
  check, `c` opens it, `o` opens the pull request, `r` re-reads both and `u`
  unlinks — unlink now has a second home here, alongside the pull-requests
  screen it has lived on since task 052. Checks are fetched live and never
  stored: a stored check result reads exactly like a current one while being
  wrong. Both credential legs answer into the same rows, so GitHub's check
  runs and its older commit statuses render identically whether `gh` or a
  token answered. New route `GET /v1/tasks/{id}/github/pull/checks` and MCP
  tool `task_github_pull_checks`. vincent still writes nothing to GitHub
  (task 068.1–068.3).
- **Open a pull request from vincent itself.** A task with a branch and no pull
  request can get one without leaving the TUI: `P` in the task workspace opens a
  form with the title, the body and a **draft / ready** toggle, all editable
  first, and `ctrl+s` pushes the branch to `origin` and creates the pull
  request. The link appears immediately rather than waiting for the next
  reconciler tick. The Pull Requests screen reaches the same action through
  `P`, which offers a picker of every task that has a branch and no pull
  request. `vincent github pr create --task ID --title TITLE [--draft]` does it
  without the TUI, over the new
  `POST /v1/tasks/{id}/github/pull/create`.

  This also fixes a page that could not work. The compare URL vincent offered
  before was built for branches nobody had pushed, so it led to a dead GitHub
  page and the fix was a manual `git push` in the worktree. The branch is now
  pushed first — and when there is no credential with write scope, or GitHub
  refuses the create, vincent still falls back to opening that compare page,
  which now works.

  **This is the only thing vincent writes to GitHub.** It never updates,
  comments on, closes or merges anything; it happens only when a human asks for
  it; `github.enabled` turns it off along with every read; and it is
  deliberately not an MCP tool, so an agent cannot reach it. The push **never
  forces** — a diverged, protected or rejected push creates no pull request and
  changes nothing on the remote — and it sends committed work only, which the
  form says before you confirm.

- **Free chat: conversational agent sessions beside tasks.** A `chat` is a
  titled conversation with an agent, scoped to a project, running in its own git
  worktree and `vincent/{id}-{slug}` branch. Each turn resumes the agent CLI's
  *own* session, so turn N has turns 1..N-1 in context — nothing is replayed as
  prompt context. Chats are a first-class entity beside tasks, not a task with a
  flag: they have their own `chats`/`chat_turns` tables, their own four-state
  lifecycle (`idle` / `running` / `awaiting_input` / `archived`), their own
  route family under `/v1/chats`, and they never appear on the task board or in
  `GET /v1/tasks`. Turns carry the same accounting a step run does — tokens,
  cost, duration, exit code — and each gets its own transcript, which closes the
  gap the feature was asked for: a conversation held outside vincent leaves no
  record at all. Drive one with `vincent chat start|send|list|show|archive`, or
  over the API. Mid-run questions use the existing §7.4 flow verbatim; answering
  one is `vincent chat answer` or `POST /v1/chats/{id}/answer`.

  Two limits worth knowing before you reach for it. **codex and cursor cannot
  hold a chat**: creating one on either is refused with `400
  agent_cannot_resume`. Both CLIs have a resume of some shape, but vincent does
  not read it yet and no fixture captured against a named CLI version pins it,
  and replaying the conversation into the prompt would be an emulation of a
  capability the adapter does not have — so vincent refuses instead of faking
  it. A stored session the CLI no longer knows fails that turn with
  `session_lost` and leaves the chat usable, for the same reason: a silently
  fresh session answers as if it had context it does not have, and you could
  not tell that apart from a working conversation.
- **`max_parallel_chats` (default 3).** Chats are bounded by their own cap,
  counted independently of `max_parallel_tasks`: a running chat consumes no task
  slot and never delays an admissible task. A `send` over the cap is refused
  with `409 chat_cap_reached` **immediately** rather than queued — a chat is a
  foreground reply, and waiting behind batch work is the thing chats exist to
  avoid.
- **`--resume` for the claude adapter.** `RunSpec.ResumeSessionID` and
  `RunResult.SessionID`, plus the `agent.Resumer` capability. Only chat turns
  set it; every workflow step still gets a fresh session, unchanged.
- **Chats in the TUI: a chats board and a chat workspace.** Reached from the
  command palette. The board lists every conversation grouped by project, with
  its state, agent, last activity and title; `enter` opens the workspace, `n`
  starts a chat, `a` archives one, `/` filters and `←`/`→` fold a project group.
  It is a *second* board rather than rows on the first, because a chat has no
  workflow, no step `k/n` and none of the task actions — and a chat has never
  appeared on the task board by design.

  A chat waiting on you is pinned to the top of the chats board and counted in
  *its* header. `!` and the task board's needs-attention count stay task-only,
  so nothing about the task board moves.

  The workspace is the conversation: finished turns above, the running turn's
  live output below, a composer at the bottom. `enter` sends, `ctrl+x` stops the
  live turn. When the agent asks something mid-turn you get **the same popup a
  task's question opens** — same options, same multi-select, same allow/deny —
  and answering it from `vincent chat answer` or the API closes it here too. A
  send refused because the cap is full says so and creates nothing: it is a
  refusal, not a queue.
- **`GET /v1/chats/{id}/events` and `GET /v1/chats/{id}/turns/{seq}/transcript`.**
  A per-chat SSE stream — that chat's events plus its live output, filtered so a
  task's never leak onto it and vice versa — and the turn's durable record, with
  the step transcript's `?offset=`/`?tail=` contract and `X-Next-Offset`. A turn
  is named by its `seq` because a chat turn is its own run. Neither is an MCP
  tool: the whole chat family stays off the tool surface.
- **`vincent chat answer` and `vincent chat cancel`, and `--json` on every
  `vincent chat` subcommand.** Interrupting `vincent chat send` stops the CLI,
  not the turn — the turn belongs to the daemon — so `cancel` is what ends one,
  and `answer` is what moves a parked chat on without curl. `chat show` now
  prints a parked chat's questions, numbered, which are the numbers
  `chat answer --answer N=VALUE` reads.
- **Chat turns are bounded by `agent_timeout` and `input_timeout`.** The same
  two clocks a workflow step gets, applied verbatim: a turn that runs past
  `defaults.agent_timeout` fails `timeout`, and a chat left in `awaiting_input`
  past `defaults.input_timeout` fails `input_timeout`. Either kills the process
  tree, returns the chat to `idle` and **releases its `max_parallel_chats`
  slot** — which is what a chat you walked away from used to hold forever, with
  nothing that would ever make a `409 chat_cap_reached` succeed. There is no
  chat-specific setting and no per-turn override; the numbers are the ones you
  already tune. `transcript_max_bytes` applies to a turn's transcript the same
  way, and `transcript_retention_days` now reclaims an **archived chat's**
  transcripts, which it previously walked straight past.

- **Run a task's steps inside a container.** A new `container:` block in
  `config.yaml` names an image, and a task's step processes run inside **one**
  container, created with the task's worktree and removed with it. That is every
  command step and every `check:` today; the **agent process itself still runs
  on the host**, with your whole filesystem in reach, until the launch seam that
  moves it lands — so a containerized task with agent steps is a mixed run, and
  it is neither refused nor warned about. `container.image: ""` is the
  default, so an installation that sets nothing consults no runtime and behaves
  exactly as it did. The image is yours and must already carry your agent CLI
  and `git`; vincent builds, publishes and bundles nothing. The project
  repository and the task's worktree are bind-mounted **at their own absolute
  host paths**, so a path means one thing on both sides and `.Worktree`,
  `VINCENT_WORKTREE` and a worktree's absolute `gitdir:` all resolve with no
  translation — which is also why a **Windows daemon refuses a containerized
  task**, since `C:\...` cannot exist in a Linux container. A workflow can pin
  its own image in `defaults.container:`, which merges over the daemon's block
  per field. What the container confines is the filesystem outside those two
  mounts, the shell and the image's tooling; outbound network is open by
  default and your agent's configuration is mounted inside it so the CLI can
  authenticate, both stated in [the security model](docs/security-model.md)
  rather than implied. A missing runtime, a Windows host, a
  `network: false`/`mcp.wire_steps: true` contradiction and a `shell: pwsh` step
  are refused when the task is created; a missing or unpullable image blocks the
  task at admission with `container_image_unavailable`, before a worktree, a
  branch or a retry is spent. A step timeout signals the process **inside** the
  container and leaves the container running, so a retry finds what an earlier
  step installed, and crash recovery removes a container only after confirming
  it still carries the label naming that task. `vincent doctor` gained a
  container row, which probes the runtime even when the feature is off.
- **Create a task from a GitHub pull request, checked out on the pull request's
  head branch.** `POST /v1/tasks` grows `github_pull`, `vincent task add` grows
  `--github-pull N`, and the pull-requests screen grows `c` — all three resolve
  the same daemon-side prefill, the way `github_issue` already does, and the
  title, description and declared fields stay editable before you confirm. The
  task's branch **is** the pull request's head branch: its worktree is that
  branch checked out with an upstream, so when a workflow pushes, the commits
  land on the pull request. A workflow field declared `pull` receives the
  number, which is how a `run:` step acts on it. Fork pull requests run — their
  head is fetched from `refs/pull/N/head` into a branch with no upstream, and
  vincent says up front that nothing can be pushed back. `s` on the same screen
  cycles the listing between open, closed and all (`?state=` on the API,
  `--state` on `vincent github prs`), so a merged pull request is reachable; the
  default stays open-only. Reading only, as before: no write method, no `POST`,
  no mutating `gh` subcommand.
- Three new block reasons for that path — `pull_fetch_failed`,
  `pull_branch_diverged` and `pull_branch_checked_out`. A local branch of the
  head's name is fast-forwarded; one that has diverged blocks with your unpushed
  commits intact, and one checked out in another worktree blocks rather than
  letting git's own message surface.

- **A workflow editor in the TUI: create, edit and fork through structured
  forms.** The workflows screen authored nothing — `e` handed the terminal to
  `$EDITOR` on raw YAML, and that was the whole story, which left the workflow
  as the one first-class thing you could not author from the product. It now
  takes three more keys: `i` opens a structured form on the entry under the
  cursor, `a` creates a workflow in a scope you pick, and `f` forks a built-in
  or global entry into a project, where it shadows the original. `e` is
  unchanged and still means `$EDITOR` — it is the escape hatch for a file too
  broken for the forms to load.

  The forms are rendered from a schema the daemon serves
  (`GET /v1/workflows/schema`), so a field that is not legal on the step you
  are editing is one you are never offered: no `run:` row on an `agent` step,
  no `manual` in the `type` row inside a `parallel` group, no `break` outside a
  loop. **A save preserves everything you did not edit** — comments, key order,
  blank lines, block scalars and CRLF endings all survive, because the client
  sends edit operations and the daemon applies them to the file's own bytes
  rather than re-emitting the document from a struct.

  New routes: `POST /v1/workflows` (create or fork), `PATCH /v1/workflows`
  (apply edit operations) and `GET /v1/workflows/schema`. A workflow file has
  writers other than you — the `create-workflow` built-in, your own `$EDITOR`
  — so a read hands back a version token and a write that carries a stale one
  is refused with 409 instead of silently overwriting; the form offers to
  re-read. Both write routes are excluded from MCP: an agent must not edit the
  workflows the daemon supervising it runs. No delete, and no CLI counterpart —
  `vincent workflow init|validate|render` already cover that surface.

- **Enum task fields, with a value picker in New task, and a `default:` for
  every field type.** A workflow's `fields:` gains `type: enum`, whose members
  are declared in `values:` and published through `GET /v1/workflows` — which a
  `pattern: '^(dev|staging|prod)$'` never could, so no client could build a
  control from one. New task opens a scrollable, filterable list on `enter` and
  steps through the members in place with `←`/`→`, the way a boolean cycles.
  `multiple: true` accepts more than one member; the value is stored as the
  members joined with `,` in **declared** order, which the daemon normalizes to
  before it checks membership, so `--field reviewers="cy, ana"` and a TUI
  selection produce the same task row and the same branch name. Any field may
  now carry a `default:`, written as its own YAML scalar (`default: true`,
  `default: 3`, `default: staging`): the daemon substitutes a **required**
  field's default when a caller omits the key, so a scripted `vincent task add`
  no longer 400s for a value the workflow already knows, while an **optional**
  field's default is seeded by the TUI and never invented server-side — an
  optional key you omit is still absent from `.Task.Fields`. A bad declaration
  is a visibly invalid workflow with a source path, not a surprise at task
  creation.

- **A task-details tab inside the answer, repair and follow-up popups.** All
  three form popups now carry a two-tab strip of their own — the form and
  **Task details** — switched with `ctrl+t` while the popup stays open. The
  second tab is the same read-only inspector the task workspace's Task Details
  tab shows, with its own sidebar and scroll, so the prompt, the workflow and
  step that is asking, the agent and model, the timings and the linked GitHub
  issue are all readable while you decide. Nothing about the draft changes
  across the switch: picked options, typed answers and half-written repair or
  follow-up prompts are exactly where you left them. `ctrl+t` works while a text
  field or a picker has the keyboard and types nothing into it; on the details
  tab `esc` returns to the form rather than closing the popup. Previously the
  only way to the task's context was `esc`, which discards the repair and
  follow-up drafts outright.

- **The daemon configuration is readable and editable from every client.**
  `GET /v1/config` now serves every key in `config.yaml` — `branch_template`,
  `debug`, `environment`, `parallel`, `fan_out`, `loop`, `include`, `github`,
  `update`, `mcp` and `notify` were absent from it, which meant no client could
  see them at all — and a new `PATCH /v1/config` changes them. The daemon
  validates the whole candidate file before writing anything, edits the key in
  place so your comments and key order survive, writes atomically at `0600`, and
  puts the result into force before it answers: a read issued straight after a
  successful patch sees the new value, and nothing needs restarting. An invalid
  value leaves the file byte-identical. `listen` is the one key that is written
  and does not take effect until the daemon restarts, and every surface says so.
- **`vincent config get|set`.** `vincent config get` prints the configuration in
  effect, one `path = value` per line, or one key; `vincent config set` changes
  one. Lists and argv are whitespace-separated in a single argument
  (`vincent config set notify.on "blocked awaiting_gate"`).
- **The TUI's daemon view edits the configuration.** `tab` moves the arrows onto
  the config block, which then lists every key rather than the digest, and
  `enter` opens a typed editor on the selected one — a chooser for the keys with
  a fixed vocabulary, a field for the rest, with the daemon's own validation
  error rendered against it. Each row shows the value in force and the built-in
  default where they differ. `notify.command`, `environment`, `agents.*.path`
  and `listen` ask for an explicit confirmation before they apply, because they
  decide what the daemon executes or exposes.

- **The agent-facing surface is narrower than the human one.** `PATCH
  /v1/config` is not an MCP tool — an agent must not reconfigure the daemon
  supervising it — and the MCP `config_get` masks `environment.set`'s values and
  `notify.command`'s argv, keeping the names, because a tool result lands in the
  model's context and in the step's transcript. `GET /v1/config` over HTTP
  serves them, behind the loopback listener and the owner-only token.

- **A pull-requests screen in the TUI, and a browser to open them in.** A new
  takeover lists every open pull request across every registered project whose
  `origin` is a github.com repository vincent can authenticate to, grouped by
  project, with the task that claims each row and whether the daemon matched it
  by head branch or a human linked it. `o` opens the selected one in a browser,
  `enter` jumps to the claiming task's workspace, and `l`/`u` link and unlink —
  `u` being the sticky refusal the reconciler will not undo, which the
  confirmation says out loud. A project whose listing fails shows its reason
  without hiding the others, and a reconciler tick re-renders the screen with no
  keypress. The entry appears in the command palette only when at least one
  project qualifies. The task workspace's **Task Details** tab gains a matching
  **GitHub pull request** section: the linked pull request with its live state,
  the reason the integration is unusable, or — with nothing linked — `P` to open
  GitHub's own new-pull-request page with an editable prefill. vincent builds
  that URL and never fetches it; nothing is created on GitHub from here.
  Opening a URL uses `open`, `xdg-open` or the Windows shell handler, and says
  so on screen when there is no browser to open — unlike the clipboard
  fallback, which is silent by design.

- **Vincent tells you when a newer release exists, and can install it.** The
  daemon asks GitHub once a day for the latest **stable** release, caches the
  answer and serves it on `GET /v1/update`; `vincent doctor` grows an `UPDATE`
  group and `vincent daemon status` says when the running daemon is older than
  the binary on disk. A prerelease is never offered, and a failed check
  (offline, rate-limited) degrades quietly and blocks nothing.

  `vincent update --check` asks on demand — it queries the feed itself, so it
  works before the first poll and with no daemon running. `vincent update`
  applies one, and does only what is honest for the way you installed:
  a binary a package manager owns is never modified and that channel's upgrade
  command is printed instead; a binary vincent owns — the direct-download
  archive, or one you placed by hand — is downloaded, **verified, then**
  swapped. On any verification failure nothing is replaced and the old binary
  is left byte-identical. Exit codes are documented and distinct, and `--json`
  carries `swapped`.

  Verification is the chain the release notes already tell you to run by hand:
  the cosign signature over `checksums.txt`, then the archive's SHA-256 against
  that verified file. `cosign` is used when it is on your `PATH` and is never
  bundled; without it the checksum check runs alone and the command says so,
  and `--require-signature` makes its absence fatal.

  The check is an opt-out: a new `update:` block in `config.yaml` with
  `check: true` and `poll_interval: 24h`. With `update.check: false` the daemon
  makes **no** outbound request for it, and only running `vincent update` does.
  It sends nothing identifying — no token, no telemetry, no machine or install
  identifier. Applying an update is never automatic.

- **The daemon speaks MCP, so an AI agent can drive vincent directly.** `POST
  /mcp` serves the Model Context Protocol on the same loopback listener as the
  REST API and behind the same bearer token — no second listener, no second auth
  story. Point any MCP client at `http://127.0.0.1:{port}/mcp` and it gets the
  whole API as tools, with discovery, argument schemas and typed errors instead
  of hand-rolled curl. The tool surface *is* the route table, because a call is
  dispatched by replaying it against the same handler `/v1` uses: a `409` still
  carries `details.state`, field bounds still name the field and the limit, and
  `Idempotency-Key` still works. **Five routes are deliberately not tools** —
  `daemon/stop`, `daemon/backup`, `DELETE /v1/projects/{id}`, `maintenance/gc`
  and `doctor/fix` — because an agent has no business stopping or reconfiguring
  the daemon supervising it.
- **`task_wait`, so following a run does not mean polling.** One blocking call
  returns when a task reaches `done`, `aborted`, `archived`, `awaiting_input`,
  `blocked` or `awaiting_gate`, or when its timeout elapses (5 minutes by
  default, hard ceiling 30). Step transitions stream as MCP progress
  notifications, and the result is complete for a client that drops every one of
  them. A step that waits on a task which cannot start while that step holds its
  concurrency slot gets an immediate `would_deadlock` error rather than hanging.
- **Vincent's own agent steps get the vincent tools, with nothing to
  configure.** The daemon registers a per-step MCP endpoint with the agent CLI
  it spawns, so a step can file follow-up work, read a sibling lane's
  transcript, or answer a gate. On by default; `mcp.wire_steps: false` turns it
  off. Each adapter carries it its own way — claude on `--mcp-config` with
  `--strict-mcp-config`, codex on `-c mcp_servers.…` overrides with the token in
  its environment, cursor via a `.cursor/mcp.json` written into the task
  worktree for the duration of the run and removed after (your global
  `~/.cursor/mcp.json` is never touched). An adapter that cannot carry one fails
  the step with `mcp_unsupported` rather than running an agent that silently has
  no tools.
- **`mcp:` in `config.yaml`** — `wire_steps` (default `true`), and `max_depth`
  (3) / `max_tasks` (32), which bound a chain of tasks created over MCP. A
  step's agent can create a task whose step runs an agent that creates a task,
  and that shape is discovered as it happens rather than declared, so neither
  `fan_out`'s bounds nor `include`'s covered it.

- **A live workflow graph on the task workspace.** A fifth tab, **Workflow**
  (`5`, or `tab` round to it), draws the workflow a task is running as a
  control-flow graph with its run state on it: which node is running, what
  succeeded or failed, what a false `if:` guard skipped as against what you
  skipped by hand, which loop pass it is on, and — for a parked task — where it
  is stuck and why. A fan-out lane's caption carries its child task and that
  child's state. Attempts that ran outside the workflow, such as a follow-up
  round, appear in a frame below `END` rather than being hidden. The graph is
  the task's own snapshot, so it keeps showing what actually ran even if the
  workflow file is edited underneath it. It reads with colour turned off.
- **`GET /v1/tasks/{id}/workflow`** serves a task's own workflow snapshot as a
  full definition — includes already spliced, `edit + retry` rewrites
  reflected. A snapshot that does not parse is a `200` with findings and a null
  `definition`, matching `GET /v1/workflows/definition`.

- **GitHub pull requests: list a project's open ones, and link them to board
  tasks.** `GET /v1/projects/{id}/github/pulls` lists a project's open pull
  requests, and `vincent github prs --project ID` prints the same listing. A
  task is linked to the pull request opened from its branch automatically: the
  daemon matches an open pull request's head branch against the task's own
  branch every `github.poll_interval` (5m by default; `0` switches it off,
  which is worth knowing because it is vincent's only background network
  traffic). `GET /v1/tasks/{id}/github/pull` serves that task's pull request
  **live**, which is what lets a task still name one that has since merged and
  dropped off the open listing; `POST` and `DELETE` on the same path let you
  link or unlink by hand, and an unlink is remembered so the daemon does not
  re-apply it. A task with no pull request gets a prefilled `compare_url` —
  GitHub's own "open a pull request" page, with the task's title and
  description and `Closes #N` where it applies.

  All of it is read-only: **vincent still writes nothing to GitHub**. The
  compare URL is built, never fetched, and no `gh` invocation or network call
  is made at all when the integration is off or a project's `origin` is not a
  github.com remote. Only the *link* is stored — a pull request's title, state,
  draft and merged status are re-read every time, because a snapshot of them
  would go stale and lie. See
  [task 052](docs/tasks/052-github-pull-requests.md); the TUI pull-request
  screen and the browser opener are still to come.
- **`enter` in the workflow graph opens the selected node in full.** The
  inspector strip packs two lines and drops the rest; the popup `enter` opens
  shows everything the definition says about that node, wrapped and never
  truncated — the `prompt`, the `run:` body, `env`, `instructions`,
  `permission_mode`, the input and check timeouts, a group's `max_parallel`, a
  loop's `count`/`for_each` and `max_iterations` — above a header naming the
  workflow it sits in, with its description, declared fields, platforms and
  file. A value the step leaves empty that the workflow's `defaults` block
  supplies is shown as the effective value and marked `(inherited from
  defaults)`, so the graph can answer "what will actually run here" without
  hiding which of the two said it. A field neither sets is simply absent — the
  popup shows the file, never the daemon's own run-time fallback. Every node
  opens something: a merge shows its conflict policy and its resolver agent, a
  collapsed workflow reference says whether it becomes a child task or spliced
  steps, a group header shows its bounds, and `END` says the workflow ends
  there. `esc` closes it back to the graph with the same node still selected,
  and `e` and `R` keep working from inside it. Reading a prompt no longer means
  handing the whole file to `$EDITOR` and losing the picture you were reading.
  The graph itself is unchanged: node boxes and the inspector strip render
  exactly as they did. See [Using the TUI](docs/guides/tui.md).

- **Board groups fold.** `←` collapses the group the cursor is in and `←` again
  the group around that; `→` opens one level; `C` and `O` do the whole table.
  A collapsed header shows `▸`, keeps its task count, its `!` needs-attention
  badge and its selected count, and the cursor rests on it. Folds are remembered
  across restarts in `{data_dir}/tui.json`, survive `g`, a filter and a
  reconnect, and are forgotten when the project or workflow leaves the board.
  A fold can never hide work waiting on you: `!` opens whatever group it lands
  in, and a collapsed group opens by itself the moment a task inside it enters
  `awaiting_input`. `V` still selects tasks inside a fold. With
  `tui.board.group_by: []` there are no groups and the four keys do nothing;
  a fresh install has nothing folded. See
  [Using the TUI](docs/guides/tui.md#folding-groups).

- **The output pane shows much more of what a Claude Code run reported about
  itself.** Claude's `stream-json` carries far more than vincent read, and the
  discarded part was exactly what a reader watching a run wants: a run now opens
  with a `#` header naming the working directory the CLI reported and the tools
  it was given — the only place *"what could this agent actually reach"* is
  written down — and closes with a line carrying its duration, turn count,
  refused tool calls and, when it is not the ordinary one, why it stopped.
  `stop: max_tokens` on that line is the difference between a model that
  finished and a model that ran out; both read as a bare success before. At
  `verbose` the line also splits API time out of wall clock, splits cache reads
  from cache writes, and breaks the spend down per model. A tool call a
  permission rule **refused** is now marked `⊘` rather than `✗`, because that is
  the step's permission mode rather than the agent's problem, and an outcome the
  dialect described structurally leads with its verb (`✓ created · …`).

  `compact` is unchanged — that level means "what the agent said and did,
  nothing else", and the run's own account of itself is neither. The same
  material is on the API: `GET …/transcript?format=normalized` gains an
  `agent.run_header` record and a richer `agent.result`, `agent.tool_result`
  entries gain `verb` and `blocked`, and any record may carry `parent_call_id`.
  Every new key is omitted when unreported, so "absent" stays distinguishable
  from "zero". Because transcripts are normalized **on read**, this improves
  runs already on disk — a claude run recorded last week renders with all of it
  today. Codex and cursor report none of it and nothing is synthesised for them.
  See [Using the TUI](docs/guides/tui.md#what-v-adds).

### Changed

- **The new-chat form uses the same list every other create form uses.**
  Project, agent, model and effort are now `picker` rows: `enter` opens the
  list, `/` filters it as you type, and the model and effort lists are the
  selected agent's own catalog — tagged `cli` or `curated`, led by an
  `(agent default)` row naming what that agent would use, and ending in a row
  for typing a value the catalog has never heard of. Changing the agent
  re-scopes both lists and clears anything chosen under the previous one. The
  base-branch row's placeholder now leads with the selected project's actual
  default branch — `main (the project's default)` — rather than the phrase
  alone, and still submits empty so the daemon resolves it. `←`/`→` still step
  the project and agent rows in place; `enter` no longer creates from the title
  row, leaving `ctrl+s` the sole create key, which is what the key table always
  said. `esc` with a list open closes the list and keeps the draft.
- **Archiving never deletes a branch vincent did not cut.**
  `delete_empty_branch_on_archive` and `delete_remote_branch_on_archive` both
  skip a task whose branch came from a pull request, and the archive reports
  `not_ours`. Without this, archiving a task made from a *merged* pull request —
  which by definition has no commits past its base — would have deleted a
  contributor's head branch, on the forge with the remote key opted in.
- `POST /v1/tasks/{id}/retry` refuses `branch_override` with a `409` on a task
  created from a pull request: renaming its branch would detach it from the pull
  request it was created for.

- **Worktree directories are named by owner.** A task's is still
  `{data_dir}/worktrees/{task_id}`, unchanged and unmoved; a chat's is
  `{data_dir}/worktrees/chat-{chat_id}`. Both live under one root, so without
  this, task 7 and chat 7 would claim the same directory. `vincent gc` builds
  its claim sets from both tables, so a chat's worktree and transcripts are not
  strays and do not inflate the orphan count on `GET /v1/info`.
- **Tasks now start from the *current* base branch, not from whatever your last
  `git pull` left behind.** Before a task's worktree is created, vincent fetches
  its base branch from that branch's own remote and cuts the task branch from the
  fetched commit. Nothing in your checkout moves: the local branch keeps its SHA
  and its working tree, so `git log <base>` there no longer matches what tasks
  build on. The remote is read from the base branch's upstream configuration and
  `origin` is never assumed, so a repository with no remote, a branch that never
  left your machine, and a `fan_out` lane based on its parent's branch all carry
  on exactly as before. An unreachable remote or an auth failure is a warning and
  the task is created from the local base — a fetch never blocks a task. Turn it
  off with `fetch_base_branch: false`. See
  [Configuration](docs/reference/configuration.md#fetch_base_branch).

- **The board stops spending a wide terminal on the title column, and a cell
  too long for its column now wraps instead of vanishing.** `TITLE` used to
  take every cell the fixed columns left — 100 of them on a 200-column
  terminal, mostly trailing blanks — while `STEP` was still cutting
  `3/7 green · loop 4/10` and `STATUS` was still cutting a clause. The title
  now stops at a comfortable width and the surplus goes to those two columns,
  with only what neither can use coming back to it. Separately, `TITLE`,
  `STATE`, `STEP` and `STATUS` wrap across up to three lines of the same row,
  so `awaiting_children (2 blocked)` and a step's own status message are
  readable without opening the task. Every row on a board is the same height —
  as tall as the tallest row in the list — so a board where nothing overflows
  renders exactly as it did before. Column widths are unchanged on a narrow
  board; wrapping applies at every width, because 80–120 columns is where cells
  were being cut worst. See [Using the TUI](docs/guides/tui.md#the-board).

- **`permission_mode: restricted` does not restrict what a step does to
  vincent.** Claude's restricted allow-list now carries `mcp__vincent__*` in
  full, so a restricted step wired to the MCP server can create and cancel
  tasks. Without it the step would have seen the whole tool list and been denied
  every call, which is worse than not offering it. `restricted` bounds the
  filesystem and the shell, and that is all it claims to bound.

### Fixed

- **The chat workspace's footer hint was drawn off the bottom of the screen.**
  The composer counted as one line while it rendered three, so the chat's body
  overran its frame by two rows: the hint line naming `enter send`,
  `pgup/pgdown scroll` and `ctrl+g live` was cut off entirely and the composer
  showed two of its three rows. Both are on screen now.
- **The mouse wheel reached the tab behind an open task popup.** Clicks were
  already ignored while a question, repair or follow-up popup owns the screen;
  the wheel was not, so a tick scrolled the tab underneath it — and on the Steps
  and Pull Request tabs, where the wheel moves a cursor rather than a viewport,
  it changed which step or pull request was selected. A popup now takes the
  wheel as well.
- **The chat composer hid what you typed whenever its pane was narrower than
  the terminal.** The composer was sized from the whole terminal rather than
  from the pane it is drawn in, so each of its wrapped rows was cut off with an
  ellipsis at the pane's right edge and everything past that was typed blind.
  It is sized from the pane now. The answer form a chat can put up is counted
  in the same height budget as the rest of the frame too, instead of being
  appended past it where it pushed the chat's bottom rows off the screen.
- **Every other text field ran off the right edge of the pane.** A title, a
  branch, a filter, a palette query or a config value longer than its pane was
  drawn on one row past the edge, taking the cursor with it, and the host either
  cut the tail off with an ellipsis or let the terminal reflow it and push the
  rest of the form down. Text fields now wrap onto further rows of their pane
  and the form reflows around them, which is what the boards and rendered
  Markdown have always done.
- **Archived chats now leave the chats board.** Archiving a chat took it off
  nothing: the row stayed, and then behaved as though the conversation were
  still alive. Three things are fixed together. `GET /v1/chats`,
  `vincent chat list` and the chats board had no way to exclude a terminal
  chat — nothing between the store and the TUI could express the idea — so
  every chat that had ever existed piled up forever. There is now an
  `archived=false|true|all` filter, spelled and defaulted exactly as
  `GET /v1/tasks`' is, covering **both** terminal states (`archived` and
  `handed_off` alike, since both are equally done with); `vincent chat list
  --archived` brings them back, and `s` on the chats board cycles the listing
  the way `s` cycles a pull-request listing. A terminal chat's last-activity
  cell no longer counts up second by second like a live conversation: it shows
  *when* the chat ended. And archiving a chat that is already archived, or one
  that was handed off, is no longer refused with "a chat with a live turn
  cannot be archived" — a sentence about a process neither state can hold, and
  which contradicted the state the error's own details reported. The refusal
  now names the real reason on both sides: the API says which state blocked it,
  and `a` on a terminal row declines with a note instead of asking you to
  confirm removing a worktree that is already gone, or that a handoff gave to a
  task.
- **Palette entries for `ctrl+`-modified keys did nothing.** Running one
  replays its direct key, and the replay had a hand-written case per ctrl key
  that had fallen a registry behind — the chat's `ctrl+r` (detail level) and
  `ctrl+g` (follow the live end) both replayed as a bare `c`. Any
  `ctrl+<letter>` is now synthesized by rule.
- **The output pane measures terminal cells rather than runes.** A line
  containing an emoji, a CJK glyph, a combining mark or a ZWJ sequence was
  measured by counting runes, so the pane over- or under-filled it and an
  over-long word was hard-split at a rune index — which could cut a ZWJ emoji
  into its parts. Every measurement on that path is now `ansi.StringWidth`.
  This also corrects the answer form, which wraps through the same helper.
- **Agent-supplied text can no longer drive the terminal.** Escape sequences
  and C0/C1 control characters in any record — a tool result summary, a
  command's output body, an error message — reached the pane unfiltered, so an
  agent that emitted `ESC[2J` or an OSC title sequence could clear the screen
  or rewrite the window title. They are now stripped before the pane measures
  or draws anything, and only vincent's own styles are emitted. Spec §16.
- **A chat turn whose transcript has gone to retention shows its whole
  answer.** The fallback that renders `result_text` capped it at 40 lines with
  an ellipsis and narrowed it by two columns; it now goes through the shared
  renderer at the pane's full width. That answer is all the turn has left, and
  the cap hid its tail.
- **A chat's live output was raw agent JSON, and nothing turned it down.** The
  chat workspace rendered every line of a running turn as the agent CLI's own
  dimmed stream-json, clipped at 200 characters — because the daemon published a
  chat's live output as one untyped chunk carrying only that raw line, while the
  client's renderer assumed it was already normalized. Nothing lined up, so
  every line fell to the fallback. Two more defects lived in the same renderer:
  a line delivered over the stream and the same line refetched from the turn's
  transcript disagreed, so a reconnect silently changed what was on screen, and
  tool results, run headers and the result line were dropped outright at every
  level.

  A chat's live-output chunks now carry the same normalized types and fields a
  task's do — mapped by shared code, with the verbatim line kept beside them as
  `raw` — and the conversation body is the task workspace's output pane: same
  records, same gutter marks, same three levels. **`ctrl+r`** cycles compact →
  normal → verbose (not `v`: the composer owns every printable key), and it is
  the *same* level the task pane is on, so setting it in either place sets it in
  both. **`pgup`/`pgdown`** scroll the conversation and **`ctrl+g`** jumps back
  to the live end. Finished turns are now drawn from their transcripts — the
  five newest when the chat opens, the rest as you scroll to them — so raising
  the level reveals what already happened and not only what happens next; a turn
  whose transcript has aged out still shows its answer.
  See [Using the TUI](docs/guides/tui.md#chat-workspace) (#282).

- **A chat turn that hit its transcript cap, either clock or a cancel could
  hang forever.** When a turn ended early the runner stopped reading the
  adapter's event stream and waited for the run to finish — but an adapter's
  reader goroutine blocks handing an event over, and its wait does not return
  until that reader is done. With a talkative agent the two waited on each
  other: the turn never reached a terminal state, the chat never came back to
  idle, its `max_parallel_chats` slot was never released, and stopping the
  daemon blocked with it. The runner now drains the stream it abandons, the
  way the task engine already did. Only chats were affected; a workflow step
  hitting the same cap always drained.

- **The new-chat form's pickers never filled, and its picker rows leaked
  keys.** The form fetched the projects and the adapters and then dropped the
  answer on the floor: the message it comes back in had no case in the chats
  board's update, which is the form's only message entry point. Both pickers
  stayed empty for the life of the form, so `←`/`→` did nothing on either row,
  the project row showed a numeric id instead of the project's name, the agent
  row read "the daemon's default" and could not be moved off it, and a failed
  `GET /v1/projects` was swallowed — a broken form looked exactly like a
  working one. On a fresh installation with nothing on the board the form
  opened with no project at all and answered `ctrl+s` with `pick a project`,
  with no field that would accept one; `n` there now says to register a project
  first rather than opening a dead end. The agent picker offers only adapters
  that can hold a chat, using a new `supports_resume` field on
  `GET /v1/agents` — the daemon's `agent_cannot_resume` refusal is unchanged
  and stays the authority, it is just no longer something the form walks you
  into. An adapter whose daemon is too old to send the field is not judged and
  not filtered out. Finally, an open draft now owns the keyboard on every row:
  `q` on the project or agent row used to quit the TUI with the draft, and
  `n`, `!`, `:`, `?` and `M` fired straight through the open form. `esc` is
  still the way out.

- **`n` on the chats board opened the new-task form.** The chats board's own
  `n` — "start a chat in the project you are looking at", the one key whose
  meaning depends on where you are — never reached the board: the root's global
  `n` consumed it first and swapped the screen for the new-task form. The
  palette row went the same way, since a keyed palette entry replays its
  keypress through that same handler, which left the new-chat form with no
  route into it from the TUI at all (chats were still creatable with `vincent
  chat start` and over the API). A global single-key binding now stands down
  when the surface underneath it declares the same key in the binding registry,
  so the collision is answered by the registry rather than by a list of views;
  the palette's "new task" row still opens the new-task form from the chats
  board, by navigating instead of replaying the key.

- **Writing a chat's session id crashed every open event stream.** The store
  publishes an event after each write, and the three chat columns no client is
  told about — session id, worktree path, pending input — handed it a `nil`
  instead of skipping the publish. Every SSE subscriber dereferences the event
  it is handed, so finishing the first turn of any chat panicked
  `GET /v1/events`, and any per-task stream, for everyone connected; the
  connection dropped with a `500` mid-stream and reconnected into the next
  crash. A `nil` is now dropped where it is produced rather than guarded in each
  handler, since "there is no event to announce" is the publisher's business.
- **New task fell back to `adhoc` for a project whose `default_workflow` lives in
  its own `.vincent/workflows/`.** The form loads its workflow catalog before a
  project is selected, so that first, unscoped listing cannot contain a
  project-scoped workflow (§5.2); when the form then selected the project itself
  — from the board's cursor, or because exactly one is registered — the Workflow
  row settled on `adhoc` and the project-scoped listing that arrived a moment
  later was ignored, because `adhoc` still resolved. Since the form submits what
  it displays, a task created without noticing ran `adhoc`. The row now tracks
  whether it was chosen by hand and re-derives the default when the
  project-scoped catalog lands; a workflow picked in the picker still survives
  it. Picking the project by hand, and a global or built-in `default_workflow`,
  worked before and are unchanged.

- **The footer and `?` described the wrong surface while an answer, repair or
  follow-up popup was open.** The task workspace fell through to the current
  tab's context, so a popup that owned the keyboard was documented as the Steps
  tab and the keys registered for the three forms were unreachable from the
  footer. The `?` sheet also never printed a follow-up form section at all. All
  three popups now name themselves, and the sheet prints all three.

- **`GET /v1/tasks/{id}/diff` measured the diff from the wrong commit once a task
  started from a fetched base.** The merge-base was computed against the base
  branch *by name*, so every upstream commit the fetch brought in was presented as
  the task's own work. A task now records the commit it was actually cut from and
  the diff is measured from that; tasks with nothing recorded — anything created
  before this, or with `fetch_base_branch: false` — are read exactly as before.

- **`delete_empty_branch_on_archive` stopped deleting anything for projects whose
  local base branch was behind its remote.** A task that wrote nothing is still
  *ahead* of a stale local base, so the "no commits past its base" check answered
  "has commits" and quietly kept every branch. The check now runs against the
  commit the task was cut from when one was recorded.

- **Two nodes in a workflow graph could answer to one id.** A `fan_out` lane's
  inline steps have their own step-id namespace, so a lane's `build` and a
  top-level `build` are different steps — but the graph gave both boxes the same
  node id, which made selecting one ambiguous. Lane-inner nodes are now
  namespaced by their lane.

### Not included

- **codex and cursor cannot hold a chat yet, and say so.** Creating one on
  either is refused with `400 agent_cannot_resume`. Both CLIs have a resume of
  some shape, but vincent does not read it yet and no fixture captured against a
  named CLI version pins it. Replaying the conversation into the prompt would be
  an emulation of a capability the adapter does not have, so vincent refuses
  instead of faking it. A stored session the CLI no longer knows fails that turn
  with `session_lost` and leaves the chat usable, for the same reason: a silently
  fresh session answers as if it had context it does not have, and you could not
  tell that apart from a working conversation.
- **No chats view in the TUI yet.** Chats are driven from `vincent chat` and the
  API in this release.

## [0.7.0](https://github.com/lezli01/vincent/compare/v0.6.0...v0.7.0) (2026-08-29)

### Added

- **The TUI now opens tasks in a dedicated full-screen workspace.** The home
  view is the task board alone; pressing `enter` opens four full-view tabs for
  Steps & Attempts, Task Details, Output, and Diff. Attempt selection follows
  across tabs, Output can switch between attempts, and entering an attempt from
  the timeline jumps directly to its output. Task Details exposes the complete
  task record through a section sidebar, while long titles, branches, status
  messages, and failed-attempt summaries wrap within the terminal instead of
  overflowing. `esc` returns to the board. See [Using the TUI](docs/guides/tui.md).

- **A step can now say what it is doing, in its own words.** A `run:` body — or
  an agent whose prompt asks for it — calls `vincent status "<message>"`, and
  that line becomes the step's live status: on the board on a wide terminal, on
  the attempt line in the task view as a cyan `» …`, and as `status_message` on
  every step run the API serves. The last line written before an attempt ends
  stays on the finished row as the step's own account of how it went. That is
  **not** a failure reason: `failure_reason` is a closed set of daemon-authored
  constants and vincent's own verdict, while this is free text the step chose,
  possibly long before it died — a step killed on a timeout may still be
  carrying a line it wrote half an hour earlier, so it is rendered as the last
  status and never as the cause of anything. The producer is the API rather than
  a sentinel line on stdout, deliberately (task 033 decision 1): a marker forces
  a strip-or-keep choice over the transcript and `result_summary` that has no
  good answer, it cannot see the obvious agent spelling at all — an agent that
  runs `echo` through its Bash tool produces a tool-use event, never step output
  — and it would make every step's stdout a control channel, so any program that
  happened to print the marker would change daemon state. `vincent status`
  addresses its own step from §8.5's `VINCENT_TASK_ID` and `VINCENT_STEP_ID`,
  and the route is keyed by step id rather than by task because a `parallel`
  group's sub-steps share one task id and run concurrently (§7.5). That carried
  one prerequisite, which is the second user-visible change here: **agent steps
  now get the §8.5 `VINCENT_*` block**, which command and check steps already
  had and agent steps did not — useful on its own, and what makes `vincent
  status` reachable from an agent's shell tool. The message is bounded like
  state and not like output: flattened to one line, stripped of control
  characters and invalid UTF-8, and truncated on a rune boundary at 256 bytes
  rather than refused, since failing a status write would turn a display nicety
  into a step failure. Writes are paced rather than rejected, a value identical
  to the stored one appends no event, and a write against a step that is not
  running is a `409` rather than a silent drop. The task view also now shows an
  unsuccessful attempt's **result summary** — the agent's final message, or the
  tail of a command's output — on a dim line beneath it: the sentence that
  decides whether to open the transcript.

- **vincent can now tell you it needs you, with nothing open.** Point
  `notify.command` in `config.yaml` at a program and the daemon runs it whenever
  a task enters one of the states you list in `notify.on` — `blocked`,
  `awaiting_gate`, `awaiting_input`, `done`, or any other state from the task
  lifecycle. It writes a JSON envelope to the command's standard input: task id
  and title, the transition, `block_reason`, the project, the workflow, the step
  cursor, the branch and worktree path, and on `awaiting_input` the agent's
  question — enough for a one-line script to write a message without calling
  back into the API. Until now the only alert in the whole system was the TUI's
  terminal bell, which rings on `awaiting_input` and only while a board is open,
  so a task could wait a full day for an answer, fail on the timeout, and the
  first you knew was the next time you looked. `command` is argv, not a shell
  line, so it composes with `terminal-notifier`, `notify-send`, `msg`, a Slack
  `curl` or a file drop without vincent picking a notification stack. It is
  **off unless you configure it**, it hot-reloads like everything else in the
  file, and an unknown state name fails the load naming the value rather than
  silently never firing. Delivery is deliberately modest: root tasks only (a
  fan-out lane does not send its own message), at most four notifiers at once
  drained from a bounded queue, a fixed 10-second budget after which the
  command's process tree is killed, failures logged and never retried, and
  nothing replayed for events that happened while the daemon was down. A
  notifier that hangs or fails changes nothing about the task it was about.
  Documented in the
  [configuration reference](docs/reference/configuration.md#notify) and the
  [security model](docs/security-model.md) — it is code the daemon runs as you,
  and its argv can hold a webhook secret, which is why it is not readable back
  through the API.

- **The daemon log and step transcripts now have command lines.** Both were
  reachable only from the TUI, or by knowing where the files live and reaching
  for `tail` — which is no help over SSH, and none at all when the daemon is what
  is broken. `vincent daemon logs [-n N] [-f]` prints the tail of
  `{data_dir}/logs/daemon.log`, 500 lines by default, `-f` following it. It reads
  the file **from disk and never contacts the daemon**, so it answers when no
  daemon does; that also means it never exits 2, a missing file is an error
  naming the path, and an empty log prints nothing and succeeds. Following is
  safe for the daemon's own rotation: every poll opens, reads and closes the
  file. `vincent task transcript <id> [--step RUN] [-f]` prints one attempt's
  transcript — the complete record `vincent task show` only names the file of.
  `--step` takes the `RUN` column's step_run id, which is unambiguous across
  retries; omitted, it picks the running attempt, else the newest. Output is
  rendered as text for a human, `--json` gives the normalized records as NDJSON
  for `jq`, and `--raw` gives the agent's own JSONL byte for byte. `-f` opens on
  the tail and resumes from the record boundary, ending when that attempt stops
  running. A manual gate, which records nothing, says so and exits 0; a
  transcript whose file was pruned exits 1. Documented in the
  [CLI reference](docs/reference/cli.md), the
  [scripting guide](docs/guides/scripting.md) and
  [troubleshooting](docs/guides/troubleshooting.md).

- **Every human action on a task now has a command line.** `vincent task` grew
  `pause`, `resume`, `skip`, `approve`, `reject`, `retry`, `repair`, `archive`
  and `answer`, and `vincent project` grew `rm` — so a blocked task can be
  rescued from a shell loop, a cron job or an SSH session with no usable
  terminal, instead of only from the TUI or hand-assembled `curl`. This matters
  most when an agent credential expires: every task that reaches an agent step
  blocks on `agent_unauthenticated` once its retry budget is spent, waiting
  fixes nothing, and until now that board could not be cleared from a script.
  `vincent task ls --state blocked --json | jq -r '.[].id'` piped into
  `vincent task retry` now does it. Each action takes one id, carries `--json`,
  and prints the daemon's own post-action view of the task rather than a guess.
  `retry` takes `--branch` (the `branch_exists` recovery: the task keeps its id
  and its transcripts) and `--prompt`/`--run` for edit+retry, each with a
  `-file` twin that reads stdin from `-`; `repair` takes a required `--prompt`
  plus `--agent`/`--model`/`--effort`; `archive` takes `--force` and tells you
  so when it refuses a dirty worktree; `answer` takes `--answer <n>=<value>`
  against the questions `vincent task show` numbers, `--allow`/`--deny` for a
  permission request, or `--body` to post a payload verbatim. `vincent task
  show` now prints the pending input request and the actions the daemon will
  accept right now, so a script can read what is legal instead of probing for
  errors. Documented in the [CLI reference](docs/reference/cli.md#vincent-task)
  and [Scripting vincent](docs/guides/scripting.md).

- **A task can now be created from a GitHub issue.** On a project whose `origin`
  remote points at github.com, with `github.enabled` on, the daemon lists that
  repository's issues and prefills a task from the one you pick: the title from
  the issue title; the description from its body followed by a `GitHub issue #N:
  <url>` link line as its own trailing block, so a task read on its own still
  points back; and workflow-declared `fields:` (§8.1.2) matched by **exact name
  only** — no aliases, no case folding — against `labels`, `assignee`,
  `milestone` and `issue`. A name is offered only when the declaration's `type`
  and `pattern` would accept the value, so a declared `integer` milestone gets
  the number and a `string` gets the title, and anything that would fail
  validation is left empty rather than pre-filling a value the create call would
  then reject. Every value lands in an ordinary editable row: nothing is locked,
  so a guess is reviewed before the task exists rather than applied silently at
  run time. The list endpoint *previews* the prefill and `POST /v1/tasks`
  applies it, from the same code — which is what makes "`vincent task add
  --github-issue N` produces the same stored task as the TUI path" a tested
  claim rather than a coincidence, and why the CLI flag is resolved daemon-side.
  The issue is fetched **once**, at creation, and stored on the task row, so
  every later step renders from that snapshot: a run stays reproducible, no
  network call can enter the render path, and an issue edited on GitHub
  afterwards is deliberately not reflected. It reaches §8.4 templates as a new
  top-level `.Issue`, zero-valued when nothing is linked the way `.Loop`'s
  `Index: 0` is, so `{{ if .Issue.Number }}` tells the two apart and one
  template serves both linked and unlinked tasks; fan-out lanes inherit the
  parent's snapshot verbatim, as they already inherit its `Fields`. Because a
  step body receives §8.5's environment and not §8.4's context, `issue` is a
  prefilled field name of its own, so a `run:` can act on the number without
  parsing it back out of the task title.

- **One task's spend can now be capped.** Cost was measured and never acted on:
  `cost_usd` is written from the adapter's terminal `result` line, rolled up
  across every attempt, and rendered on the board and the detail view — and
  nothing in the engine, the scheduler or the store ever read it to decide
  anything. That left a gap between the two guards that do exist.
  `agent_timeout` bounds one attempt's wall clock and `transcript_max_bytes`
  bounds its bytes on disk; an agent that loops *productively* — quick turns,
  modest output, no hang — trips neither, and can spend its full timeout per
  attempt across the whole retry budget with nothing but a human noticing to
  stop it. The new top-level `max_task_cost_usd` is one ceiling on one task's
  rolled-up spend, checked at **every attempt boundary** rather than between
  top-level steps: a `loop` is one such position and a `parallel` group is
  reduced to one, so a check there would let a fifty-iteration loop, or a step's
  whole `max_retries` budget, run before the cap was consulted once. Crossing it
  **blocks** the task with a new `cost_limit` reason rather than failing the
  step: the finished attempt keeps its own state and its own reason, no retry is
  consumed, and a retry that was already due does not run — spending more money
  to arrive at the same wall is not a repair. Zero is the default and means no
  cap, so nothing changes for anyone who does not ask; a negative value fails
  the load; and it hot-reloads like everything else in the file.
  ([#97](https://github.com/lezli01/vincent/issues/97))

- **`vincent workflow init <name>` writes a valid workflow file into the right
  directory.** Nothing in the binary helped you write your first workflow. The
  CLI's workflow surface was `ls` and `validate`, neither of which creates
  anything, and the TUI deliberately does not either, so anything past a one-step
  ad-hoc run meant reading the schema reference, finding `examples/`, copying
  one, working out which of the two scope directories it belongs in, and
  validating in hope. `init` writes the file, prints the path, and refuses to
  damage anything already there. `--from <example>` hands over one of the
  shipped examples **with its comments intact**: only the top-level `name:` line
  is rewritten, and as text, because a round trip through the YAML library would
  drop exactly the comments that make an example worth handing over. Both the
  accepted values and the list in the error for an unknown one are read from the
  embedded examples at run time, so a sixth example is offered the day it lands.
  The default (global) scope resolves `{config_dir}/workflows/` with **no daemon
  at all**; only `--project N` contacts one, to resolve the id to a repository
  root, and it exits `2` without writing when none answers. See
  [the CLI reference](docs/reference/cli.md#vincent-workflow-init).

- **`vincent workflow render <file>` dry-runs a workflow's templates.**
  `workflow validate` only checks that a template *parses*, so
  `{{.Task.Titel}}`, `{{.Task.Fields.ticket}}` on a task that sets no `ticket`,
  and `{{.Steps.plan.Reslt}}` all passed validation and then failed the moment
  the step rendered — findable only by creating a task and watching it fail.
  `render` executes every template a file declares — `prompt`, `run`, `check`,
  `instructions`, `if` and `for_each` — and prints what each step would send,
  with the resolved agent/model/effort triple and the level that supplied each
  field. It needs no daemon, so it belongs in the same pre-commit hook as
  `validate`. Values a run discovers appear as visible placeholders
  (`<worktree>`, `<steps.plan.result>`), so the output reads as a preview rather
  than as the literal prompt an agent will receive; a field the workflow
  declares `required` binds, an optional one stays absent so a non-defensive
  read is reported. `--title`, `--description`, `--field k=v` and
  `--agent`/`--model`/`--effort` describe a hypothetical task; `--task ID` and
  `--project ID` bind a real one and resolve `include` steps and named fan-out
  lanes through the registry. Exit `0` clean, `1` a template that does not
  execute, `2` no daemon answered a `--task`/`--project`. See
  [the CLI reference](docs/reference/cli.md#vincent-workflow-render).

- **`vincent task add` can take its task fields from a JSON file or from
  stdin.** `--fields-file ./inputs.json` — or `--fields-file -` to read the
  document a `jq` pipeline just produced — fills the same
  [task field map](docs/guides/workflows.md#54-task-fields) that repeatable
  `--field name=value` fills, without every newline, quote and space having to
  survive the shell first. The two combine and `--field` wins the names it
  names, so one generated document can be reused across runs that vary a single
  input. The values must all be JSON strings; a number, boolean, `null`, array
  or object is refused with a message naming the **key** and never the value, as
  are an empty name, anything after the first JSON object, and a read over the
  API's own 4 MiB body bound. Everything else stays the daemon's call —
  required fields, `type`, `pattern` and the per-field limits — and names the
  workflow never declared are still accepted, exactly as before. Creating a task
  without `--json` now also confirms what it carries by **name and count and
  never by value** (`fields: notes, ticket (2)`), which catches a mistyped name
  while staying safe to leave in a CI log. See
  [Scripting vincent](docs/guides/scripting.md#supplying-task-fields).

- **Creating a task can now be retried safely.** If the daemon commits your task
  but you never see the response — a timeout, a dropped connection, a script
  that dies mid-`curl` — re-sending the request used to create a second task, a
  second worktree and a second agent run against the same repository. Send an
  `Idempotency-Key` header on `POST /v1/tasks` and the retry returns the task the
  first request created instead. Same key with a *different* body is refused with
  a `409` carrying `details.reason: "idempotency_key_reused"`, so a key
  accidentally reused for a second operation cannot silently answer with the
  wrong task. Two requests racing with the same key commit exactly one task. The
  key and the task are written in one transaction, so neither can exist without
  the other. Keys are kept for 24 hours and then pruned, they are deleted along
  with the task they name, and `vincent doctor` counts them under
  `database.table_rows`. Nothing changes without the header: the CLI and the TUI
  do not send one, they do not retry a create, and two identical sends still make
  two tasks — which is what pressing enter twice means. Documented in the
  [API reference](docs/reference/api.md#replaying-a-create).

- **Vincent now tells you whether it has ever been tested against the agent CLI
  you have installed.** Each adapter carries the list of builds its parsers were
  captured against, and `GET /v1/agents`, `GET /v1/info`, `vincent doctor` and
  the TUI's daemon view report a `version_verdict` for the build you are running:
  `tested`, `untested`, or `incompatible` for a build vincent knows breaks. It is
  **advisory and blocks nothing** — `untested` is the normal, expected answer a
  few weeks after any vincent release, and it changes nothing about how a step
  runs. The `incompatible` list ships empty for all three adapters, because no
  such build has been observed. Adapter health is now five separately reported
  facets: installed, authenticated, protocol-compatible (`supports_input` plus
  the version verdict), permission-compatible (`restricted_verdict`), and
  model-catalog health, which is the existing `probe_error` and is not
  duplicated. ([#148](https://github.com/lezli01/vincent/issues/148))

- **Every task now records which workflow definition it actually ran.** A
  project or global workflow file shadows a built-in of the same name — that is
  by design, and it includes the `adhoc` a task falls back to when you create it
  without naming a workflow. Until now the task recorded only the *name*, so a
  repository's own `.vincent/workflows/adhoc.yaml` standing in for the built-in
  was invisible on the task forever afterwards. Tasks created from here on carry
  a `workflow_origin`: the scope that won (`builtin`, `global`, `project`, or
  `derived` for a fan-out lane), the source file relative to that scope's root,
  and a SHA-256 of the file's bytes as the registry loaded them. It appears as
  the `origin` row in `vincent task show`, beside the workflow name in the TUI's
  task-detail header, as `workflow_origin` on every task the API serves, and in
  the `task.created` event. It is captured once, at creation, and never
  recomputed, so editing a workflow file does not rewrite the history of tasks
  that already ran it. Tasks created before this change report `unknown` — the
  honest answer; vincent does not look the name up again to invent one.

- **The `update-workflows` built-in brings a project's own workflows up to date
  with the features a release added.** A workflow written against 0.3 is still
  valid against 0.7 — unknown keys are errors, unused features are not — so a
  registry ages silently. Fan-out, conditions, loops, includes, fields,
  `.Issue`, `retry_backoff` and `vincent status` all shipped after the first
  workflows anyone wrote, and nothing in the product ever suggested reaching for
  them. Task 024's `create-workflow` closed the "I have no workflows" gap; this
  closes the other end. Six steps: a `git ls-files --error-unmatch` probe that is
  both the file list and the "this project versions none" signal, a `condition`
  that ends the run `done` when there are none, one agent step carrying the same
  embedded `vincent-workflows` skill `create-workflow` carries plus a checklist
  of what a workflow can be behind on, then a relist and a `for_each` loop
  running `vincent workflow validate` over every file — including one the pass
  added. Its deliverable is the task's **own worktree and branch**, not the live
  registry: these files already exist and are already versioned, so the change
  becomes real through review rather than by being written underneath you. This
  repository's own workflows were brought to the same bar in the same change and
  now report status between the phases of a long `run:`, and `prepare-release`,
  the workflow that walks a Release Please pull request to mergeable, replaces
  the release workflow it supersedes.

- **"Why vincent is awesome" — ten linked articles on the ideas behind the
  product.** They cover the repeated workflow that inspired vincent,
  command-first cost effectiveness, deterministic verification, human control,
  durable execution, recovery, worktree isolation, executable team knowledge,
  agent portability and workload visibility. The documentation site gained
  unique per-page titles and descriptions, canonical URLs, Open Graph and
  Twitter metadata, structured data, a sitemap, `robots.txt`, a custom 404 page,
  deterministic 1200×630 social cards and previous/next navigation across the
  series. The product-name convention is now recorded and applied throughout:
  lowercase `vincent` everywhere except where it begins a complete sentence,
  with case-sensitive identifiers preserved exactly.

### Changed

- **vincent is MIT-licensed again.** The separate commercial license is gone —
  from the repository, the documentation, the release archives and the
  package-manager metadata alike — and release validation was updated to match.
  The historical licensing records are preserved rather than deleted, with the
  old packaging decision marked superseded.

- **Releases ship unsigned by design, and a missing certificate can no longer
  destroy one.** macOS code signing, notarization and a stapled
  `vincent_*_darwin_universal.pkg` are implemented and wired into the release:
  darwin binaries are `codesign`ed in a GoReleaser build hook, because the
  signature lives *inside* the Mach-O and so must land before the archive is
  assembled and before `checksums.txt` is computed; the archives are notarized
  with `notarytool submit --wait`, which modifies nothing and only gates
  publication; and the universal `.pkg` is the one artifact that can carry a
  stapled ticket, and therefore the only one whose first launch works offline.
  None of it runs today. The Apple Developer Program membership was never
  bought, and the first `v*` tag to try it died at its first signing step,
  because `MACOS_SIGN_REQUIRED` was keyed on the tag rather than on the
  certificates. It is keyed on the certificates now: with them installed a tag
  still cannot publish an unsigned macOS artifact, and without them every
  signing step warns and the release ships unsigned. Half-configured secrets,
  and certificates without a notary key, keep their hard error — an unnotarized
  Developer ID signature makes Gatekeeper refuse the file anyway, which is worse
  than shipping plainly unsigned. Consequently the Homebrew cask's
  quarantine-stripping `postflight` hook is back, the smoke job's Gatekeeper
  assessment is skipped on an unsigned release rather than softened into an
  assertion that would prove nothing on a signed one, and nine user-facing pages
  that claimed the macOS artifacts were signed — one of which told you *not* to
  run `xattr -d com.apple.quarantine`, which against a real release leaves a
  binary that will not start — now document the unsigned path. deb and rpm ship
  unsigned by decision rather than by omission: vincent publishes no APT or YUM
  repository, `apt` does not verify a per-package signature on a `.deb`
  downloaded from a release page at all, and signing only the rpm half would
  trade this project's keyless supply chain for a long-lived secret with
  publication and rotation duty. Windows Authenticode is reopened on one new
  fact — free-for-OSS programs exist and vincent is unambiguously OSI-licensed —
  and stops at an application; nothing else lands until a certificate exists.
  cosign signatures and build provenance are unchanged, always present, and are
  not a substitute for either.
  ([#150](https://github.com/lezli01/vincent/issues/150),
  [#207](https://github.com/lezli01/vincent/issues/207))

- **A `restricted` step bound for an agent that cannot restrict on this machine
  is now refused when you create the task, not when it runs.** Cursor's
  restricted mode needs its CLI sandbox, which exists on macOS and Linux only;
  on Windows such a step used to reach the engine, start nothing, and fail
  `restricted_unsupported` after spending a worktree, an admission and a retry.
  `POST /v1/tasks` now answers `400 validation_failed` naming the step and the
  agent, and the daemon publishes the `restricted_verdict` its gate uses. The
  answer needs no installed binary — it is a fact about the adapter and the
  operating system — so it is correct even on a machine where the CLI is
  missing. Nothing is downgraded to full-auto, then or now: a restricted mode
  that silently is not restricted is worse than none. The engine's
  `restricted_unsupported` failure stays exactly as it was, for a task whose
  daemon changed underneath it — a data directory carried to Windows, or a
  workflow edited after the task was queued. Retries are not gated; that
  backstop is what catches them.
  ([#148](https://github.com/lezli01/vincent/issues/148))

- **gosec now runs on every build, as part of the existing lint gate.** The
  security linter is enabled inside the golangci-lint the `go.mod` tool directive
  already pins, so `go run mage.go lint` is still one command and still the
  byte-identical one CI runs on Linux, macOS and Windows — a new unsuppressed
  finding now fails the build. Every one of the 39 current findings in production
  code was read individually and either fixed or suppressed with a reason at the
  site; the reasoning is recorded in
  [task 042](docs/tasks/042-gosec-static-analysis.md), and
  [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md) explain how
  gosec and govulncheck divide the work. No runtime behaviour changed and no file
  or directory permission moved: tightening one is a user-visible change that
  needs its own issue, not a side effect of turning a linter on.
  ([#147](https://github.com/lezli01/vincent/issues/147))

### Fixed

- **Crash recovery can no longer kill a process that merely inherited a PID.**
  The guard compared two different clocks: a spawn journaled the daemon's own
  wall clock into `step_runs.proc_started_at`, recovery compared that against
  kernel bookkeeping, and anything inside a five-second tolerance counted as the
  same process — so in a narrow crash-and-reuse window an unrelated process
  could be killed as an orphan. A spawn now journals a **platform-native process
  identity** beside the PID, in the same write, and recovery compares it
  exactly: an opaque versioned token whose contract is *compare, never parse* —
  the boot id joined with the start ticks from `/proc/<pid>/stat` on Linux, the
  `kinfo_proc` fork stamp on macOS, the creation `FILETIME` on Windows. Keeping
  the Linux value a count *since boot* rather than an absolute instant is what
  makes it immune to an NTP step or a suspend/resume, and the boot id makes a
  reboot a guaranteed mismatch rather than an arithmetic coincidence. A row with
  **no** identity — written before this migration, or by a spawn whose identity
  read failed, which is real rather than hypothetical — keeps the ±5 s
  comparison unchanged, so no installation is worse off than it was. "Cannot
  prove, do not kill" holds in both branches: an identity that cannot be read
  during recovery, when one was journaled, is never a kill, and a mismatch is a
  log line and nothing else — the task re-queues normally, with no new block
  reason and no doctor problem.

- **A question longer than 256 bytes can now be answered.** A task parked in
  `awaiting_input` on a long question refused every answer with `400
  validation_failed: answers key must be at most 256 bytes`, then sat holding
  its concurrency slot until it was cancelled or `input_timeout` — 24 hours by
  default — expired and failed the step. The daemon parked on, persisted and
  rendered a question it would then refuse every answer to. One constant was
  serving two unrelated kinds of key. A `fields` key is a caller-chosen
  identifier a human or a workflow author types (§8.1.2), and 256 bytes is
  generous for one; an `answers` key is not chosen by the caller at all — it is
  the agent's verbatim question text, which §7.4 makes the lookup key and §9.2
  writes back to the CLI under the same text, so no layer between the agent and
  the answer is permitted to shorten it. The asymmetry was the tell: the answer
  *value* bound is 64 KiB, so a 438-byte answer was fine while the 438-byte
  question it belonged to was not. `answers` keys now take their own bound,
  sized like the value they arrive beside rather than like an identifier, and
  `fields` keys keep theirs. Nothing became unbounded: the route still reads at
  4 MiB and still applies the field and value counts. Mid-run input exists only
  on the claude adapter, which is exactly where questions over 256 bytes are
  routine. ([#197](https://github.com/lezli01/vincent/issues/197))

- **`vincent doctor` no longer times out instead of answering.** The report asks
  the daemon to probe every agent CLI, and each probe carries that adapter's own
  deadline — up to 145 seconds in total, with cursor's model catalog being an
  authenticated network call. The client gave up after ten, so on a machine
  where a probe was merely slow the command printed `context deadline exceeded`
  rather than the report that would have named the adapter holding it up. The
  two calls whose cost is that probe walk — `vincent doctor` and a forced
  refresh of the agent picker — now wait longer than the adapters can, so the
  diagnosis reaches you. Every other call still gives up after ten seconds: a
  daemon that cannot answer a cached read that fast is wedged.

- **The first `v0.7.0` tag was withdrawn, and this is the re-cut release.** That
  tag build died at its first signing step, before GoReleaser ran, and produced
  no archives, no deb or rpm, no attestations and no Homebrew, Scoop or WinGet
  metadata. The tag and its empty GitHub release were removed; nothing was ever
  published under it, and the cause is fixed above.

## [0.6.0](https://github.com/lezli01/vincent/compare/v0.5.0...v0.6.0) (2026-08-25)

### Added

- **The database now reports its own size, row counts and history span.**
  Vincent keeps database rows forever — one `events` row per state change, plus
  the whole workflow YAML on every task — and that is a deliberate decision, but
  nothing measured what it cost, so "rows are small" was untestable on your own
  machine after six months of use. `vincent doctor` now prints the footprint
  *including* the WAL and SHM sidecars (the file size alone understates it
  between checkpoints), the row count for every table biggest-first so the one
  that is growing names itself, the total bytes of stored workflow snapshots,
  and how far back the event history reaches. The TUI's daemon view shows the
  same block, and `GET /v1/info` carries the byte figures for anything else that
  wants them. The counts are read from the schema itself, so a table a later
  version adds is counted without a client change. Purely informational: nothing
  prunes, nothing warns, no threshold exists and no exit code moved — the
  measurement comes first, and any argument about retention now has evidence
  behind it. `GET /v1/doctor` also accepts `?probe=false` to skip the forced
  agent re-probe; the default is unchanged and `vincent doctor` still forces it.
  ([#98](https://github.com/lezli01/vincent/issues/98))

- **`vincent daemon backup` and `vincent daemon restore` — a supported way to
  copy daemon state.** `vincent.db` holds every project, task, step run, cost
  record and transcript pointer, and until now nothing documented how to copy
  it and no command did. The obvious workaround was actively unsafe: under WAL
  a committed row lives in `vincent.db-wal` until a checkpoint, so `cp
  vincent.db` while the daemon runs produces a file missing recent work, and
  copying the three files separately produces a set that can restore into a
  torn database — a backup that looks fine until the day you need it.
  `vincent daemon backup <path.tar.gz>` writes one archive holding a
  `VACUUM INTO` copy of the database, every transcript, `config.yaml`, your
  global workflows and a manifest. The daemon takes it, so it is consistent
  **while tasks are running**, and it prints the bytes it wrote broken down by
  database and transcripts rather than pretending the artifact is small.
  `vincent daemon restore <path.tar.gz>` is the reverse and runs against a
  **stopped** daemon; it refuses a running one, an archive whose schema is
  newer than your binary, and a destination that already holds state unless
  `--force` — which moves the old state aside as `<name>.bak-<timestamp>` and
  deletes nothing. Worktrees are not in the archive (the branches they held are
  in your repositories) and neither is the API token, so a restored
  installation mints a fresh one at next start.
  ([#99](https://github.com/lezli01/vincent/issues/99))

- **Follow-up runs on a finished task, before it is archived.** A `done` or
  `aborted` task still owns its worktree, its branch and its commits until
  `archive` tears them down, and until now vincent could do nothing in that
  window — a branch that needed rebasing onto a `main` that had moved, one more
  commit a review asked for, or a stray file to drop all meant leaving vincent,
  finding the worktree by hand, and coming back only to press archive. The new
  `follow_up` action runs that work inside the daemon's own ledger: give the
  task an agent prompt, a shell command, or the name of a workflow from the
  registry, and it runs on that task's own branch in that task's own worktree,
  with a step run, a transcript, events and token and cost accounting like any
  other step. It is repeatable — each run is a *round* — and it never changes
  the task's verdict: a done task returns to `done` and an aborted one to
  `aborted`, whatever the run did. A follow-up round's rows are recorded past
  the workflow's last step index, so the workflow snapshot does not grow and
  `step k of n` still describes the workflow somebody wrote. A follow-up step
  that fails blocks the task at its own index, where `retry` re-runs the
  follow-up, `repair` sends an ad-hoc agent at *that* failure, `skip` abandons
  it and restores the task's original state, and `cancel` aborts. It is
  available as `POST /v1/tasks/{id}/follow_up`, as `F` in the TUI, and — alone
  among the human actions, because batches want a command line — as
  `vincent task follow-up <id> --prompt/--run/--workflow`.
  ([#181](https://github.com/lezli01/vincent/issues/181))

- **`retry_backoff`: workflow steps can pace their retries.** Retries were
  immediate and nothing could make them wait, so the default `max_retries: 1`
  meant a step hitting a transient problem — a flaky network call, a
  `git index.lock` held by another process — burned both of its attempts inside
  a few seconds and blocked, needing a human even though nothing was wrong with
  the work. A step (or a workflow's `defaults:`) may now carry
  `retry_backoff: 30s`, and the wait costs nothing: the task returns to `queued`
  showing `queued · retry backoff → 14:20`, **gives up its concurrency slot** so
  other work keeps running, and re-runs the step by itself when the wait is
  over. Nothing sleeps and no slot is held. The default is `0s` — an immediate
  retry — so every existing workflow behaves exactly as it did. The wait only
  decides *when* an attempt happens, never *whether*: the attempt still counts
  against `max_retries`, and a step out of budget blocks at once however long
  its backoff.

- **Each agent's usage window is reported in the daemon and TUI.** When an agent
  CLI stops because the account's quota for the window is spent, vincent now
  remembers *which adapter* it was and when it resets, instead of losing that
  the moment the held task moves on. The board header badges the agent
  (`claude ⏳14:20` in place of `claude ✓`), the daemon view names the reset
  beside path, version and login state, and the new-task form warns before you
  queue more work against a spent window. `→` marks a reset the CLI stated and
  `≈` one vincent estimated from `usage_limit_recheck_interval`, so an estimate
  is never presented as a fact. The warning is advisory only: nothing is
  refused, admission and both concurrency caps are unchanged, and the next
  successful step on that adapter clears the badge. `GET /v1/agents` and
  `GET /v1/info` carry it as a nullable `quota` block, and `agent.quota_changed`
  announces every change on the event stream. There is no probe and no
  percentage: no CLI vincent supports can report remaining quota without
  actually running, and vincent reports what it has watched rather than a
  number it would have to invent.
  ([#179](https://github.com/lezli01/vincent/issues/179))

- **Ad-hoc repair agents for a blocked task.** A blocked task now offers a
  `repair` action (`R` in the TUI, `POST /v1/tasks/{id}/repair`) that runs one
  throwaway agent in the task's existing worktree and branch, with the prompt
  you write and the blocked step's failure context around it. It is the escape
  hatch for a block that `retry`, `edit + retry` and `skip` cannot clear because
  the worktree itself is what is wrong. The repair decides nothing: whatever the
  agent exits with, the task returns to `blocked` at the same step with the same
  reason, so you read the diff and then choose. It is recorded as its own step
  run with its own transcript, tokens and cost, shown as its own timeline entry,
  and it does not consume the blocked step's retry budget.

- **The `create-workflow` built-in — a workflow whose deliverable is another
  workflow.** A first workflow no longer has to be written by hand. Its one
  agent step carries the published `vincent-workflows` authoring skill, embedded
  from `skills/vincent-workflows/SKILL.md` at build time, so the skill stays the
  single copy of that guidance and editing it changes the built-in at the next
  build. Two task fields shape the result: `workflow_name` is required and held
  to `^[a-z0-9][a-z0-9._-]*$` — stricter than the schema's rule for a workflow
  name, because the value is also a file name — and `global` picks the registry,
  true writing `{config_dir}/workflows` and false or unset writing the project's
  `.vincent/workflows`. Both are live registry directories rather than the task's
  worktree, since a file left in a worktree would not be a workflow until the
  branch merged. The step runs under `on_input: wait` and the prompt says so: it
  may stop and ask a design question the repository cannot answer, bounded by
  what asking costs under §7.4 — a parked task holds its concurrency slot, and an
  unanswered question fails the step on `input_timeout`.

### Fixed

- **Crash recovery no longer re-queues a task whose previous attempt it could
  not close.** Startup recovery ran as two independent sweeps — finalize every
  open step run, then re-queue every live task — with nothing carrying a failed
  finalize forward into the re-queue decision. A storage failure at that one
  write was logged and walked past, and the owning task went back to `queued`
  anyway: a `queued` task with a step run the database still called `running`,
  which the scheduler then admitted, starting a second attempt against a first
  one that was, durably, still open. Recovery is now atomic per task — the step
  runs, the task transition and its durable event commit together — and
  fail-closed: a task that cannot be reconciled is left exactly as found, and
  the daemon refuses to start rather than running the scheduler over rows it
  knows are contradictory. Restarting the daemon retries recovery, and nothing
  is duplicated if it runs twice. Two guards back it up: admission refuses a
  `queued` task that still has a `running` step run, and `vincent doctor`
  reports the combination under a new `tasks` problem and a
  `tasks.unreconciled[]` list.
  ([#142](https://github.com/lezli01/vincent/issues/142))

- **Reconnecting to the event stream no longer stalls the daemon.** A client
  resuming `GET /v1/events` or `GET /v1/tasks/{id}/events` with a
  `Last-Event-ID` had its whole backlog read into memory in one query, and
  written in full, before the stream went live. Event rows are kept
  indefinitely, so that backlog only grows: on a long-lived installation a
  single reconnect could allocate tens of mebibytes and hold the daemon's one
  SQLite connection for the length of the scan, delaying task transitions and
  every other write behind it. The catch-up now reads in fixed pages up to the
  newest event id at the moment the stream opened, so the memory and the
  connection are held for one page no matter how far behind the cursor is.
  Nothing about what a client receives changed: every event after the cursor
  is still delivered, in id order, exactly once across the hand-off to live
  events. ([#138](https://github.com/lezli01/vincent/issues/138))

- **A request body is now exactly one JSON document, and it is bounded.** The
  daemon read the *first* JSON value in a request body and discarded everything
  after it unread, so a client that framed two documents into one request — a
  retry that re-wrote the body, a `jq -c` loop piped into a single `curl -d @-`,
  a buggy generator — got a `201` for work vincent never saw, with nothing in the
  response to distinguish it from the single-document call. Trailing content is
  now `400 invalid_json`; trailing whitespace stays valid. Bodies are also read
  under a fixed bound instead of into memory whole (64 KiB, or 4 MiB on the
  routes that carry a prompt or a workflow source), over which is a new
  `413 payload_too_large` naming the limit and never echoing the body, and long
  strings, big maps and prompt or run overrides are bounded field by field with a
  `400` naming the field. `POST /v1/workflows/validate` now honours the same
  1 MiB workflow-source limit the registry applies to a file, so a source too
  large to be catalogued no longer validates cleanly. A body labelled a clearly
  non-JSON `Content-Type` — what a plain `curl -d` sends — is `415`; an absent
  header and any `*/json` are still accepted. The daemon additionally sets read
  and idle timeouts, so a client that sends headers and then dribbles a body no
  longer holds a connection indefinitely; SSE streams are unaffected and keep
  their unbounded response lifetime.
  ([#140](https://github.com/lezli01/vincent/issues/140))

- **`config.yaml` is no longer created world-readable.** On Linux and macOS the
  daemon created `{config_dir}/` `0755` and `config.yaml` `0644`, so any other
  local account could read the one file vincent creates that can hold your
  secrets — `environment.set` values are literal, which is where an API token or
  a license key ends up. Both are now created `0700`/`0600`, subject only to a
  stricter umask, and **every daemon start re-tightens an existing installation**
  the way it already re-tightens `{data_dir}/token`: group and other access is
  dropped, owner bits are kept and the file's contents are never rewritten. The
  change is announced rather than silent — the daemon logs the path and the mode
  it found, and `vincent doctor` grows a `permissions` warning row carrying the
  path, the observed mode, the expected mode and the exact `chmod`. That warning
  is not part of the closed set that makes `vincent doctor` exit `1`. Windows is
  unaffected: modes carry no access control there and access comes from the
  per-user ACL `%APPDATA%` inherits. The docs now also say plainly that
  "vincent stores no credentials" is about *vendor* credentials, and that
  `environment.set` is not a secret store.
  ([#141](https://github.com/lezli01/vincent/issues/141))

- **A step whose output vincent could not capture is no longer reported as a
  success.** Command output was read a line at a time with a one-mebibyte
  ceiling; a longer line — minified JSON, a base64 blob, a `git diff` of a
  generated file — stopped capture dead, sent the rest of the stream to
  `/dev/null`, and left the step `succeeded` on its exit code alone, with a
  megabyte of evidence gone and nothing a client could query saying so.
  Over-long lines are now captured in bounded `partial` pieces that rejoin in
  order, so an ordinary big-output command stays a success *with* its output.
  Genuine evidence loss now fails the attempt instead: transcript write, encode
  and close errors (a full disk, a revoked permission, a short write — `Close`
  is checked, because that is where a buffered filesystem reports ENOSPC) fail
  it with the new `transcript_io_error` reason, and a stream an agent adapter
  could not read to the end fails it with `agent_protocol_error` rather than
  blaming the CLI. Both retry normally and neither can be swallowed by
  `allow_failure:`. `transcript_max_bytes` is unchanged and remains the only
  size-based failure. ([#139](https://github.com/lezli01/vincent/issues/139))

- **The repair prompt's transcript excerpt keeps its own bound.** The 256 KiB
  ceiling #139 gave the shared output-tail helper silently halved the 512 KiB
  transcript window the repair prompt had already narrowed its own read to, so a
  repair agent saw half the failure context it was designed to get. The bound is
  now the caller's: the shared helper keeps §8.4's, and a caller that has already
  bounded its own read says so. A silent narrowing is the shape of the bug #139
  is about, so it should not arrive as a side effect of fixing it.

- **Workflow loading is bounded and refuses non-regular files.** A `*.yaml`
  entry in a workflow directory that is a symlink, named pipe, socket or device
  is no longer opened or followed, and a source is capped at 1 MiB. Previously a
  named pipe in a registered repository's `.vincent/workflows/` parked the loader
  in `open()` forever — enough to stop the daemon from starting, hang
  `POST /v1/projects`, or kill hot reload for every scope — and a symlink was
  followed out of the repository into whatever it pointed at. Rejected files are
  listed as invalid entries naming the reason, so the valid workflows in the same
  directory keep working. ([#136](https://github.com/lezli01/vincent/issues/136))

- **Logging in to an agent CLI is noticed within five minutes.** Adapter probe
  results are cached by binary identity, which is exact for the model and effort
  catalogs but only a floor for authentication — nothing about the binary
  changes when you log in, so `logged_in: false` survived on the board, the
  detail view and the new-task form until the CLI was upgraded or
  `?refresh=true` was passed. `vincent doctor` was the only surface that told
  the truth. That one field now has a five-minute freshness window of its own;
  the option catalog is untouched, and a refresh that fails keeps the previous
  answer rather than downgrading a logged-in account to "not authenticated".

- **A human action racing scheduler admission no longer fails with a state
  conflict.** Cancelling or pausing a queued task at the moment the scheduler
  started it returned `409` (`task 3 is running, not queued`) — in the TUI, a
  keypress that appeared to do nothing until pressed again — even though both
  actions are valid from `running` too. An action that loses its compare-and-swap
  is now re-applied once from the state it lost to, when the lifecycle allows it
  from there. Fan-out lanes, which are cancelled the moment their rows appear,
  hit this most. ([#127](https://github.com/lezli01/vincent/issues/127))

- **Simultaneous worktree creation in one project.** Tasks admitted at the same
  moment in the same project no longer fail each other's `git worktree add`
  with a `git_error` block — the daemon now serializes worktree creation and
  cleanup per project, since git leaves its own `.git/worktrees` bookkeeping
  unprotected while an entry is half-built. A `fan_out` step, whose lanes all
  live in the parent's repository and all start together, was the reliable way
  to hit this; a blocked lane left the parent waiting in `awaiting_children`.
  Creation in *different* projects stays parallel. ([#126](https://github.com/lezli01/vincent/issues/126))

- **`vincent task ls` reports each task's branch.** The list row carries
  `BRANCH` in the table and `branch_name` in `--json`, so the documented
  cleanup path — `vincent task ls --archived` — actually names the branches
  vincent made. Branch names are configurable, so a `vincent/*` glob is not
  guaranteed to find them. ([#137](https://github.com/lezli01/vincent/issues/137))

- **Corrected documentation that promised branches are never deleted.** The FAQ
  said archiving always keeps the branch; archiving deletes a branch that has no
  commits past its base, and has since `delete_empty_branch_on_archive` shipped
  defaulting to true. A branch carrying any commit is still never deleted. The
  README, quickstart and scripting guide also claimed full TUI/CLI parity, which
  does not hold for the human actions on a running task.
  ([#137](https://github.com/lezli01/vincent/issues/137))

- **The documentation site renders again.** GitHub Pages builds this repository
  with Jekyll, whose Liquid parser tried to evaluate the Go-template
  expressions that the workflow schema, guide, concepts, troubleshooting and
  bundled reference pages exist to document — breaking those builds. Every
  template-carrying page is now wrapped in `raw` blocks, internal engineering
  records (the spec, tasks, gates and history) are excluded from the site build
  rather than half-rendered, two pages whose names collided on case-insensitive
  filesystems were renamed, and the rendered changelog and contributing pages are
  published and linked from the site navigation.

## [0.5.0](https://github.com/lezli01/vincent/compare/v0.4.2...v0.5.0) (2026-08-22)

### Added

- **The vincent Workflows authoring skill.** A portable agent skill for
  designing workflows: it prefers deterministic commands and the language's
  native control flow over asking an agent, keeps the cost of each step in
  view, and asks about human gates, mid-run interaction, acceptance checks,
  side effects and failure policy before it generates anything. The skill is
  published at `skills/vincent-workflows/`.

- **Workflow-declared task fields.** Workflows can publish ordered task inputs
  with labels, descriptions, required flags, string/integer/number/boolean
  types and Go RE2 patterns. The TUI pre-renders those inputs, the daemon
  validates declared values for every client, and additional undeclared fields
  remain accepted and recorded on the task.

## [0.4.2](https://github.com/lezli01/vincent/compare/v0.4.1...v0.4.2) (2026-08-21)

### Fixed

- **RPM release verification.** Release validation now converts RPM payloads to normalized tar paths before extraction, avoiding GNU cpio's warning exit while still running the packaged binary and keeping extraction inside a temporary directory. ([#161](https://github.com/lezli01/vincent/pull/161))

## [0.4.1](https://github.com/lezli01/vincent/compare/v0.4.0...v0.4.1) (2026-08-21)

### Fixed

- **Release package verification.** RPM payloads are now extracted safely inside a temporary directory during validation, allowing provenance generation and Linux, macOS and Windows smoke tests to complete for published releases. ([#159](https://github.com/lezli01/vincent/pull/159))

## [0.4.0](https://github.com/lezli01/vincent/compare/v0.3.0...v0.4.0) (2026-08-21)

### Added

- **Roomier, responsive TUI workflows.** At terminals 128×24 and larger, task
  creation now uses a six-stage guided takeover layout, while Projects and
  Workflows gain persistent navigation rails and contextual main panes. The
  workflow graph stays alongside its registry, interaction state survives
  resizes, and smaller terminals retain the compact layouts.
  ([#153](https://github.com/lezli01/vincent/pull/153))

- **WinGet, Scoop, mise, deb and rpm distribution.** Stable releases now publish
  vincent's Windows archives through its Scoop bucket and submit them to
  Microsoft's WinGet catalog, attach native deb/rpm packages for x86-64 and
  ARM64, and support mise through its standard GitHub backend. All formats use
  the same checksummed release binaries and preserve the noncommercial license;
  deb/rpm packages install no root-owned service for vincent's per-user daemon.
  ([#158](https://github.com/lezli01/vincent/pull/158))

## [0.3.0](https://github.com/lezli01/vincent/compare/8efa4c8c7bb8b034831c04447f17122f9d8aaf0a...v0.3.0) (2026-08-19)

### Added

- **Reusable workflow composition with `type: include`.** An include splices a
  registry workflow's steps into the caller when the task is created, so shared
  fragments run in the same task and worktree and their results remain available
  through `.Steps`. Nested includes carry provenance, honour the included
  workflow's defaults, and are checked for cycles, depth, duplicate step ids and
  platform compatibility before execution. Includes work at top level and inside
  loops, parallel groups and inline fan-out lanes.

- **Workflow platform restrictions with `platforms:`.** A workflow can declare
  `linux`, `darwin`, `windows` or `posix`. Unsupported workflows remain visible
  and explain why they cannot run, but task creation refuses them and migrated
  snapshots block with `platform_unsupported` before creating a worktree.

- **Input-capability gating with `on_input: require`.** Workflows that depend on
  mid-run questions can now reject an agent that cannot pause for an answer,
  instead of silently continuing with a guess. Static incompatibilities fail
  validation; installed-agent capability is checked again at creation and just
  before the step starts.

- **A configurable, grouped task board.** `tui.board.group_by` defaults to
  project then workflow, with `g` cycling project/workflow/flat layouts for the
  current session. Groups preserve the existing attention-first ordering and
  never hide tasks behind collapsible headers.

- **Bulk task actions in the TUI.** `space` marks one task and `V` marks all
  visible tasks; the normal action keys then operate on the eligible selection.
  Successful tasks are unmarked, refusals remain selected for retry, and dirty
  worktrees receive the same explicit confirmation as single-task archive.

- **File-grouped diffs.** The task detail diff tab groups hunks by file and
  starts them folded, so large agent changes are navigable without losing the
  full patch.

- **A control-flow graph for workflows in the TUI — `g`.** The workflows screen
  explained a workflow as a numbered list of its top-level steps, which was
  enough while workflows were linear. The language now has structure —
  `parallel` groups, `fan_out` lanes and their merge, guards, `condition`,
  `loop` and `break` — and a list can name those constructs without showing
  where control goes. `g` on an entry draws it.

  The graph opens *over* the registry list rather than replacing it: `enter`'s
  step list still carries the findings, platform notes and agent resolution the
  picture does not show, and `esc` closes one layer at a time. Arrows move the
  selection and the view follows it; `shift`+arrows pan; a graph larger than the
  terminal is cropped and panned, never reflowed into a different shape. `e`
  works from inside the layer, so saving the file in your editor redraws the
  graph in place with the same node still selected.

  Everything the picture says survives having colour stripped: frame weights
  separate a `parallel` group from a `fan_out` from a `loop`, boxes carry the
  step's own type word, and a `condition`'s two ways out are labelled `true` and
  `false`. A `fan_out` shows a merge node because its join is a git merge that
  runs and can block; a `parallel` group shows none, because its join is only
  its members finishing. A guard on an ordinary step draws no second branch —
  false there means skip and carry on.

  A new endpoint backs it: `GET /v1/workflows/definition?name=&project_id=`
  serves one workflow's whole recursive structure, as authored, with workflow
  defaults kept in their own block. The registry list keeps its compact shape.

- **Loops in workflows — `type: loop` and `type: break`.** A workflow can now
  repeat a body of steps: `count:` a fixed number of times, or `for_each:` once
  per item in a list, including a list a step discovered at run time. That
  makes three shapes writable that were not: **converge** ("run the tests, fix
  what broke, run them again" — a probe under `allow_failure:`, a `break`
  reading it, a repair), **repeat** (ten passes of the race detector without
  ten copy-pasted steps), and **iterate a set** (once per changed file). A loop
  is one step, one index, one concurrency slot and the task's one worktree — no
  branch and nothing to merge, which is what separates it from `fan_out`.

  `type: break` ends the loop successfully when its `if:` is true. There is no
  `continue` type: a `condition` inside a loop body ends *that iteration*,
  which is what continue means, using the meaning that word already had. There
  is no `while:` either — a guard can only read what a run has produced, so a
  `while:` about its own body is either loud on iteration 1 or silently false,
  and `count:` plus `break` is the same loop written correctly.

  `.Loop` (`Index`, `Item`, `IsFirst`, `IsLast`) joins the template context,
  with `Index: 0` outside any loop. `loop.max_iterations` (default **10**) is
  the ceiling: `count:` is checked against it when the file loads, and a
  `for_each` list longer than it blocks with `loop_limit` before the first
  iteration rather than quietly doing the first ten — ten iterations of a
  three-step body is already thirty agent runs. A loop's position is derived
  from its step rows on every admission and never persisted, so `retry` and a
  daemon restart both resume **mid-iteration**; `skip` skips the whole loop,
  and `edit + retry` on a body step applies to every remaining iteration. The
  board shows `loop 4/10` and the detail view groups rows by iteration, folded
  with the latest open. See
  [`type: loop`](docs/reference/workflow-schema.md#type-loop).
- **Conditions between steps.** A workflow can decide at run time what to do
  next. `if:` on any step is a guard: false skips that step and the workflow
  carries on, recording a `skipped` row whose reason says a condition did it
  rather than you. On a fan-out lane or a `parallel` sub-step the same `if:`
  subsets the set instead — the others still run and the join still happens.
  `type: condition` is a step whose whole body is the guard: false ends the run
  and the task is `done`, which is how a workflow finishes early. And
  `allow_failure:` on agent and command steps turns the failures a step itself
  produced into an advance, so a guard has something a run *discovered* to
  read — without it, a guard could only see what you typed when you created the
  task. Guards are ordinary templates that must render exactly `true` or
  `false`, are re-evaluated every time rather than cached, and can now read
  `.Host.OS`. See [Conditions](docs/reference/workflow-schema.md#conditions).
- **`type: parallel` — sub-steps that run at once.** A group runs its
  sub-steps concurrently in the task's one worktree: one step, one index, one
  concurrency slot, no branch and no merge. It succeeds when every sub-step
  does, a failure does not cancel its siblings, and a retry re-runs only what
  failed. `parallel.max_parallel` (default 4) bounds it, and is a **second
  concurrency dimension your task caps do not govern** — a board reading "1
  running" can be a machine running four compilers. `manual`, nested groups
  and `on_input: require` are refused inside a group.
- **`type: fan_out` — lanes as real child tasks.** Each lane becomes an
  ordinary task with its own worktree, branch, retries, gates and blocks, and
  their branches are merged back (`--no-ff`, in declared order) into the
  branch the task already owns, so one branch is still delivered. A lane is a
  named workflow or inline steps, resolved into the task's snapshot at
  creation; lanes may nest to any depth, bounded by `fan_out.max_depth` (3)
  and `fan_out.max_tasks` (64), both checked at creation with a `400` naming
  what is wrong.

  A merge conflict blocks the task with `merge_conflict` and leaves the
  worktree conflicted so you resolve it in place, stage, and retry;
  `merge: {on_conflict: agent}` opts into an agent attempt first. A lane that
  is cancelled or ends without finishing blocks with `lane_failed` and merges
  **nothing**.

  Two things worth knowing before you use it: a fan-out **fills** your
  concurrency caps rather than exceeding them, and N lanes leave N worktrees
  on disk until the tree is archived.

  New `awaiting_children` task state (holds no slot), `?parent_id=` and
  `?include_children=` on `GET /v1/tasks`, a `children` rollup on the task
  detail, the `task.children_changed` event, `vincent task ls
  --include-children/--parent`, and `L` in the TUI to drill into a fan-out's
  lanes.

### Fixed

- **Long structured-input prompts no longer truncate in the TUI.** The answer
  popup is wider and wraps both questions and free-text answers, keeping the
  full prompt usable in ordinary terminal sizes.

- **A fan-out whose spawn failed part-way could never be retried.** Lanes were
  created one at a time, each committing before the next, so a failure on lane
  two left lane one committed; the cleanup cancelled it, and a cancelled lane
  stays attached to its step. The parent's `retry` therefore found a lane, took
  the *join* path instead of re-spawning, read the lane as aborted and blocked
  `lane_failed` — again on every retry, with nothing in the API or the TUI able
  to clear it. Lanes are now inserted in one transaction, so a failure leaves no
  lane behind and `retry` re-spawns from a clean slate.

- **A fan-out could join lanes that had not started.** The parent decided
  *spawn or join* on whether the step had lanes, which only answers "have the
  lanes finished" if the park after spawning always commits — and it is a
  compare-and-swap. A parent left `running` with `queued` lanes joined on its
  next admission and blocked `lane_failed` against work about to run perfectly
  well. It now parks again instead.

- **A `parallel` sub-step's guard could read a sibling after a retry.** §7.5
  says a group is a set whose members cannot see each other, and that held on a
  group's first admission only: a re-admitted group skips the sub-steps that
  already succeeded, and their rows were still visible in `.Steps`. The same
  guard against the same context answered one way on the first run and another
  after a human pressed `retry`. Set-invisibility now holds in every admission.

- **A `loop` whose `for_each` list re-derived shorter than its own rows.** The
  extent came from the fresh list alone, so a shorter one would have left the
  loop reporting success over iterations it started and never revisited. The
  extent is now the longer of the list and the recorded iterations, with the
  `max_iterations` ceiling re-checked against it. Every `for_each` source §8.4
  offers is stable between admissions, so this bounds the derivation rather than
  a reachable failure.

- **A `loop` with an empty `for_each` list left its step index with no row.**
  The two structure steps each have a case where they are reached and run
  nothing — every `fan_out` lane guarded off, an empty `for_each` list — and only
  the fan-out recorded a row saying so, leaving a detail view unable to tell
  "ran nothing" from "never reached" for the loop. The empty case now records
  one row under the loop's own id. That row is deliberately not a `.Steps`
  entry: a loop's id is never one, or it would be a key present exactly when the
  loop did nothing and absent when it did something.

- **A leaked context in `parallel` and `loop` steps.** Both created a
  cancellable context and then overwrote it — `cancel` included — when the step
  carried a `timeout:`, leaving the first context attached to the parent for the
  rest of the task's run.

### Changed

- **Vincent is now source-available and dual-licensed, not MIT.** Personal and
  non-commercial use is free under the
  [PolyForm Noncommercial License 1.0.0](LICENSE); commercial or business use —
  including running vincent inside a for-profit company's own development
  workflow, without selling it — requires a separate commercial license, see
  [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md). This is deliberately *not* an
  OSI-approved open-source license: restricting commercial use is incompatible
  with the Open Source Definition, so "source-available" is the accurate word.

  **The change is not retroactive.** `v0.2.0` and every release before it were
  published under the MIT License and stay usable under it forever, on the terms
  they shipped with; no tag or published artifact is modified. The first release
  published after this change is the first release under the new licensing, and
  every release after it follows.

- **Archiving removes a task's empty branch by default.** A branch is deleted
  only when it contains no commit beyond the task's recorded base; dirty,
  diverged or unverifiable branches are retained and archiving still succeeds.
  `delete_empty_branch_on_archive: false` restores the old behaviour, while the
  opt-in `delete_remote_branch_on_archive` also removes a configured upstream
  after the safe local deletion. Project deletion never deletes remote branches.
- **Release dependencies were refreshed without major-version changes.** This
  release uses Bubble Tea 2.0.9, Lip Gloss 2.0.6, `x/ansi` 0.11.8,
  modernc.org/sqlite 1.57.0 and govulncheck 1.7.0.

## [0.2.0](https://github.com/lezli01/vincent/compare/v0.1.1...v0.2.0) (2026-08-16)


### Features

* **codex:** report logged_in from `codex login status` ([7c1a506](https://github.com/lezli01/vincent/commit/7c1a506ef826d2508c1164eb484d2d07067e9783))
* **doctor:** one command that answers "why is nothing running?" ([e0d63c7](https://github.com/lezli01/vincent/commit/e0d63c7f99b8ad04e72c72cb6e64cff49cfac0b5))
* reclaim orphaned worktrees with `vincent gc` ([5a9037d](https://github.com/lezli01/vincent/commit/5a9037d8392fb32b9e539f072d5fd75c73429743)), closes [#95](https://github.com/lezli01/vincent/issues/95)

## [0.1.1] — 2026-08-15

### Added

- **Repository workflows for GitHub issues and releases.** The checked-in
  project registry now includes `github-issue`, `github-enhancement`,
  `github-bug`, `prepare-release` and `release`. `github-issue` turns a rough
  task into a `bug` or `enhancement` issue, parks in `awaiting_input` while
  Claude asks for missing detail, and puts a manual gate before the
  non-retrying `gh issue create`; it is POSIX-only and deliberately leaves its
  empty worktree branch behind.

  `github-enhancement` takes an open enhancement's id as the first token of the
  task title — `42`, `#42` and a full issue URL still work, and optional prose
  after that token now produces a useful branch and PR name — then separates
  clarification from implementation, runs the expensive cross-platform check
  without an agent retry, and gates the diff before the non-retrying push and PR
  creation. `github-bug` likewise proves a regression test red before fixing it.
  Both are Claude-only and POSIX-only; codex and cursor do not support the
  `on_input` clarification they rely on.

  `prepare-release` audits all six build targets, dependencies, archive
  contents, smoke assertions and pinned actions, lets an agent clear FAIL
  findings, and verifies a real `dry_run` artifact without publishing anything;
  its task-title version is optional. `release` follows `RELEASING.md` through
  preflight and the changelog PR, then stops at a manual gate before the tag.
  Its PR and tag steps do not retry, and everything after the tag only verifies
  the published result. `release` is Claude-only and POSIX-only.

- **Explicit child-process environments.** The new
  [`environment`](docs/reference/configuration.md#environment) config block
  applies to agent steps, command steps and checks: `inherit` accepts `all`
  (the unchanged default), `none` or a list of names; `unset` removes names
  next; and literal `set` values win last. An empty inherit list means nothing,
  `$` is not expanded in `set`, and a step's own `env` still wins. This makes
  hermetic runs possible and, on Windows, lets `unset: [MSYSTEM]` stop Cursor
  importing Claude Code hooks through the MSYS environment. The daemon logs
  resolved variable names, never their credential-bearing values or the values
  in a transcript, and warns rather than rewriting an environment with no
  `PATH` or Windows `SystemRoot`.

- **Richer output and complete transcripts in the TUI.** Tool calls now show a
  useful subject rather than only `Bash` or another bare tool name, followed by
  their success or failure; reasoning is marked with `·`, calls with `▸`, and
  outcomes are indented beneath them. Claude, codex and cursor all surface tool
  outcomes. Claude and cursor surface complete reasoning blocks, and codex now
  does too when its CLI emits the effort-dependent `reasoning` item. Long
  assistant text wraps with a hanging indent instead of disappearing past the
  output pane's right edge; tool output bodies remain in the raw transcript.

  Pressing `e` from either detail pane opens the selected attempt's complete raw
  JSONL transcript in `$EDITOR`, including the beginning omitted by the TUI's
  256 KB tail. The truncation notice advertises the binding, and a pruned
  transcript, a gate with no transcript, or an editor failure is reported
  instead of opening a misleading empty file.

- **Agent usage limits are now a wait, not a failure**
  ([task 003](docs/tasks/003-usage-limit-classification.md)). When a step stops
  because the agent's usage quota for the window is spent, vincent records the
  attempt as `usage_limit`, **consumes no retry**, releases the task's
  concurrency slot, and re-queues it until the window reopens — the step then
  re-runs with **no human action**. Previously that run was indistinguishable
  from a genuine failure: it burned the whole retry budget in seconds (there is
  no delay between attempts) and blocked the task with `agent_error`, which sent
  you to read a transcript about a task that was fine. With several tasks running
  on the same agent, the whole board went down at once.

  A held task shows when it resumes — `queued → 14:20` on the board,
  `queued · usage limit → 14:20` in the detail header — and gives up its slot, so
  other work carries on. The resume time is the reset the CLI reported; when it
  reports none, vincent waits
  [`usage_limit_recheck_interval`](docs/reference/configuration.md#usage_limit_recheck_interval)
  (new, default 15m) and tries again. Cancelling, pausing or resuming a held task
  drops the wait immediately.

  Only the **claude** adapter recognizes usage-limit wording today. Capturing a
  real quota exhaustion means burning a real five-hour window, so codex and
  cursor deliberately ship no pattern and behave exactly as before — a wrong
  guess would park a genuinely failed task in a wait it never leaves.

- **`agent_unauthenticated` block reason**
  ([task 003](docs/tasks/003-usage-limit-classification.md)). A claude step that
  fails because the CLI is not logged in now says so, instead of surfacing as
  `nonzero_exit` or `agent_error`. Nothing else changes: the step still runs, the
  retry budget still applies, and the task still blocks — the reason just names
  the fix.

- `queued_reason` and `admit_not_before` on every task in the API and on
  `GET /v1/config`'s `usage_limit_recheck_interval`. Both task fields are `null`
  for an ordinarily queued task, so the addition is invisible to existing
  clients, and they are separate from `block_reason`, which still means only
  "stopped, needs a human".

- **Homebrew install on macOS** ([task 002](docs/tasks/002-homebrew-tap.md)).
  `brew install lezli01/tap/vincent`. The cask clears the quarantine attribute
  during install, so macOS users no longer meet the Gatekeeper "unidentified
  developer" prompt or have to run `xattr -d com.apple.quarantine` by hand — the
  release binaries are still cosign-signed rather than Apple-notarized, so the
  archive path is unchanged. `brew uninstall --zap vincent` also unloads the
  LaunchAgent and removes the config and data directory. Linux and Windows keep
  the release archives and `go install`.

- **Configurable branch names**
  ([task 001](docs/tasks/001-configurable-branch-names.md)). A task's branch no
  longer has to be `vincent/{id}-{slug}`. Names resolve through a chain, most
  specific first: a per-task literal (`vincent task add --branch feat/OPS-123`, or
  `branch_name` on `POST /v1/tasks`), a project template
  (`branch_template` on `PATCH /v1/projects/{id}`), the global
  [`branch_template`](docs/reference/configuration.md#branch_template) in
  `config.yaml`, and finally the built-in name. **The default is unchanged**, so
  nothing moves unless you configure it.

  Templates get `{{.ID}}`, `{{.Title}}`, `{{.Slug}}`, `{{.BaseBranch}}`,
  `{{.Fields.NAME}}`, `{{.Project.*}}` and a `slug` function. The new-task form
  previews the resolved name and the level it came from as you type, via
  `POST /v1/resolve`.

  Two things to know. Because vincent never deletes branches, a template with no
  discriminator in it collides on the *second* task for the same input — put
  `{{.ID}}` in it or expect to name repeats by hand. And `{{.Fields.x}}` fails
  loudly on a missing field while `{{ index .Fields "x" }}` renders nothing, which
  yields a legal-but-wrong name like `feat/-fix-login`; prefer the first.

- `branch_override` on `POST /v1/tasks/{id}/retry`, which makes a `branch_exists`
  block recoverable: the branch is renamed and the task re-admitted, keeping its
  id and history. Previously nothing in the API could change a branch name.

- `branch_name_invalid` block reason. Branch names are validated by
  `git check-ref-format` rather than a hand-rolled matcher, and a rejected name is
  reported rather than silently rewritten.

- `go run mage.go vuln` and a weekly `Vulnerabilities` workflow: govulncheck
  over the module's reachable code, swept across `linux`, `darwin` and `windows`
  because 15 packages (`x/sys/windows/svc`, `modernc.org/libc/*`) reach the
  binary on Windows only.
- `CHANGELOG.md`, [`RELEASING.md`](RELEASING.md) and `.github/CODEOWNERS`.
- `go install github.com/lezli01/vincent/cmd/vincent@latest` as a documented
  install path, and a versioning-and-stability policy in the README and
  `SECURITY.md`.

### Changed

- **Normalized transcript and live-output clients must read structured tool
  records.** On
  `GET /v1/tasks/{id}/steps/{run_id}/transcript?format=normalized` and the
  per-task SSE stream, `agent.tool_use.tools` changed from `[]string` to
  `[{name, summary, call_id}]`; `agent.tool_result` adds
  `results: [{call_id, name, summary, is_error}]`, and `agent.thinking` adds
  whole-block reasoning text. Clients that rendered each tool as a string must
  read its `name` field and should tolerate the two new record types.
  Normalization happens on read, so stored raw transcripts need no migration
  and gain the richer rendering retroactively.

- A task's branch name is now resolved and written inside the same transaction as
  the task row, so no committed task can carry an empty one. This removes a window
  in which a crash between two writes left the name unset — harmless while names
  were derived from `(id, title)`, but it would have silently discarded a
  configured name and run the task on a default branch.
- `docs/` is no longer versioned: `docs/versions/v0/spec.md` is now
  [`docs/spec.md`](docs/spec.md), the single platform spec, amended in place.
  Planned work lives in [`docs/tasks/`](docs/tasks/README.md), the closed v0 ledger
  in `docs/history/`, and the gate walkthroughs in `docs/gates/`.
- A `go install`ed binary now reports the module version from `vincent version`
  instead of `dev`.
- CI runs the three acceptance gates as steps of one per-OS job rather than
  three separate jobs, sharing a checkout and a toolchain setup.
- Release notes are grouped into Features / Bug fixes / Other changes instead of
  one flat list of commit SHAs.
- Third-party actions in the release workflow are pinned to commit SHAs; every
  job carries a `timeout-minutes`.

### Fixed

- **Release binaries no longer depend on a stale vulnerable Go patch.** The
  module and release workflow now pin Go 1.26.6, which clears five reachable
  standard-library advisories across linux, darwin and windows instead of
  accepting whichever 1.26 patch happens to be on a runner. A weekly workflow
  proposes patch-only toolchain bumps within the declared minor series, and
  admits them only after build, test and the vulnerability sweep pass.

- **Installed daemons now start as the right user with a usable `PATH`.** macOS
  launchd and Linux systemd units capture the installing shell's `PATH`, so
  Homebrew, npm, nvm and `~/.local/bin` agent CLIs are visible after login;
  reinstall the service after changing that path. Windows now uses an
  unelevated, per-user Scheduled Task at login instead of a LocalSystem service,
  so the daemon shares the user's data, token, git config and agent credentials.
  The task pins the new internal `vincent daemon --config-dir` and `--data-dir`
  flags; only removing a legacy LocalSystem service still needs Administrator.

- **Windows login no longer leaves a daemon console open or a slow agent
  permanently unavailable.** The scheduled daemon's `--hide-console` path now
  releases the Windows Terminal console safely and starts agent probes without
  replacement windows, while a manually entered flag leaves the user's terminal
  alone. Failed probes expire after one minute instead of being cached for the
  daemon's lifetime, cold-login probes get 20/25-second bounds, and a timed-out
  Cursor status probe is no longer reported as definitely unauthenticated.
  `vincent service status` points users to `Task Scheduler Library\\vincent`, not
  `services.msc`, and diagnoses elevated task ownership that would prevent a
  later unelevated reinstall or uninstall.

- **The advertised output-tab keys now work.** `[` and `]` switch between the
  detail view's output tabs from either pane, as the help, palette and footer
  already promised; the existing `d` alias continues to make the same toggle.

- **The TUI answer form no longer truncates what it asks you**
  ([#83](https://github.com/lezli01/vincent/issues/83)). A question, an option
  label or a permission summary longer than the popup's inner width was cut with
  an `…` and there was no way to see the rest from inside the form — no wrap, no
  scroll, no expand — and because the popup is capped at 76 columns, a wider
  terminal did not help. That hid the end of an agent's question, the
  `(Recommended)` suffix agents put at the *end* of an option label, and, in
  `restricted` mode, the tail of the command you were being asked to approve.
  Rows now wrap inside the popup, with continuation lines indented under the
  marker so a wrapped option still reads as one option; `up`/`down` still move a
  whole option at a time, and the focused row is kept fully on screen.

## [0.1.0] — 2026-08-12

First release. All 70 tasks of the
[v0 breakdown](docs/history/v0-tasks.md) are complete, and the M1, M2, M4 and
M5 acceptance gates are met.

### Added

- **Daemon** owning all state and execution — SQLite (WAL, single writer),
  git worktrees, and agent CLI subprocesses. Work continues with no client
  attached; crash recovery finalizes interrupted step runs and kills verified
  orphans (PID *and* start time must match).
- **Localhost REST + SSE API** with bearer auth, the single interface every
  client uses.
- **Workflow engine** — YAML registry with builtin < global < project
  shadowing, live reload, and three step types (agent, command, manual) plus
  retries, timeouts and human actions.
- **Bubble Tea TUI** with six views (board, detail, new-task, projects,
  workflows, daemon), holding no state the daemon does not have.
- **CLI subcommands** over the same API.
- **Agent adapters** for Claude Code, Codex and Cursor. Capability differences
  are documented and ignored at run time, never emulated.
- **OS service registration** on Windows (Scheduled Task, as the invoking
  user), macOS (launchd) and Linux (systemd user unit).
- **Signed releases** — cross-compiled archives for linux/darwin/windows on
  amd64/arm64, SHA-256 checksums, a keyless cosign signature over the checksum
  file, and GitHub build provenance attestations.

### Security

- Agents run **full-auto by default** — a documented design decision
  ([spec §16](docs/spec.md)), surfaced once by the TUI on first run.
  Git worktrees isolate collisions between tasks, not privileges.
- The daemon's trust boundary is the OS user: loopback-only listener, bearer
  token stored `0600` in the data directory, and no agent credentials stored.

## Versioning and stability

Vincent is `0.x`. Until `1.0.0`:

- **Breaking changes may land in any minor release** (`0.1` → `0.2`), and are
  called out under a `### Changed` heading here with the migration needed.
- **Patch releases** (`0.1.0` → `0.1.1`) are fixes only, with no breaking
  change to the config file, the workflow YAML schema, the REST API or the CLI
  flags.
- The **config file, workflow schema, REST API and CLI surface are not stable
  yet.** Pin a version if you script against them.
- The **on-disk database migrates forward automatically** and is append-only by
  policy (`internal/store/migrations/`); downgrading a binary across a
  migration is not supported.

[0.1.1]: https://github.com/lezli01/vincent/releases/tag/v0.1.1
[0.1.0]: https://github.com/lezli01/vincent/releases/tag/v0.1.0
