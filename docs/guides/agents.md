# Agent CLIs

vincent orchestrates agent CLIs you install and authenticate yourself. It
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
- [Choosing models and effort](#choosing-models-and-effort)
- [Choosing between them](#choosing-between-them)

---

## At a glance

| | claude | codex | cursor |
|---|---|---|---|
| Binary | `claude` | `codex` | **`cursor-agent`** |
| `agent:` value | `claude` | `codex` | `cursor` |
| Mid-run questions (`awaiting_input`) | ✅ | — | — |
| Reports cost | ✅ | — | — |
| `model:` | ✅ | ✅ (free text) | ✅ (~180 enumerated) |
| `effort:` | ✅ | ✅ | **—** (it lives in the model id) |
| `restricted` mode | ✅ | ✅ | ✅ on macOS/Linux, **fails on Windows** |
| Reports whether you are logged in | — | — | ✅ |

`vincent daemon status`, the TUI's daemon view, and `GET /v1/agents` all report
what vincent actually resolved on your machine — path, version, and the model
and effort options it discovered.

## Claude Code

**Binary:** `claude`. **Workflow value:** `agent: claude`.

The most capable adapter, and the only one that can be interrupted mid-step.

- Runs `claude -p --output-format stream-json --verbose`, cwd set to the
  worktree, **prompt on stdin** — never as an argv element, because Windows caps
  arguments at 8 KB and prompts embed task descriptions.
- `full-auto` adds `--dangerously-skip-permissions`; `restricted` maps to
  Claude's allowlist flags with an edit/read/git/test tool set.
- `model:` and `effort:` pass straight through as `--model` / `--effort`.
  Options are discovered by parsing `claude --help` and merged with a curated
  catalog, so a CLI upgrade that adds an effort level makes it selectable without
  a vincent release.
- **Reports token usage and cost.** The board's cost column sums every attempt,
  retries included. The other two adapters report no cost at all.

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
[Writing workflows → mid-run questions](workflows.md#mid-run-questions).

Input support is **version-gated** to the CLI family vincent has verified
against real captured runs. Outside that range the adapter reports
`supports_input: false` and runs exactly as it otherwise would — no input flags,
plain-text prompt. Nothing degrades silently.

## Codex

**Binary:** `codex`. **Workflow value:** `agent: codex`.

- Runs `codex exec --json`, cwd set to the worktree, prompt piped on stdin.
- `full-auto` maps to `--dangerously-bypass-approvals-and-sandbox` — the
  documented automation switch. `restricted` maps to `--sandbox workspace-write`,
  confining writes to the worktree.
- `model:` passes as `-m`; `effort:` as `-c model_reasoning_effort=…`. Efforts
  are `minimal, low, medium, high, xhigh`.
- **No model catalog.** The CLI enumerates nothing, and codex model availability
  is account-dependent — the same id is accepted on one plan and rejected on
  another — so pickers offer free text and the CLI's own default. A model you
  type is passed through with a warning, not rejected.
- **No cost reporting**, and `supports_input: false`: a codex step never enters
  `awaiting_input`, and `on_input` has no effect on it.
- **Reasoning is surfaced.** Codex emits whole reasoning blocks, which the TUI
  shows at the `normal` and `verbose` output levels (`v` cycles them) and the
  transcript records as `agent.thinking`. Whether any are emitted depends on
  the effort you asked for — a low-effort turn can spend reasoning tokens and
  produce no blocks at all.

> **Caveat on `restricted` + git.** In a linked worktree the real git directory
> lives under the main repository, outside the sandbox, so a `git commit` from a
> restricted codex step may be denied. vincent itself never needs a commit — the
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
- vincent's own worktree flags are never passed to it. Cursor has a worktree
  feature; worktrees belong to vincent, and two owners of one concept is a defect.
- Reports token usage but **no cost**, and `supports_input: false`.
- Errors do not arrive in the stream — an invalid model id exits 1 with a message
  on stderr and no result line — so the adapter reports "stream ended without a
  result event" plus the stderr tail, which is what makes an everyday typo
  diagnosable.

Three things about cursor are genuinely different, and all three are visible in
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
Linux"* before doing any work. A restricted cursor step therefore **fails to
start on Windows**, with block reason `restricted_unsupported`, under the normal
retry policy.

Falling back to `--force` was rejected outright: it would run full-auto a step
that explicitly asked not to be, turning a safety choice into its opposite on
exactly one OS — the failure mode nobody would think to check for. See
[Windows](../platforms/windows.md#restricted-mode-and-cursor).

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
re-probes. `GET /v1/agents?refresh=true` (or `R` in the TUI's new-task view)
forces one.

A failed probe **expires**; a clean one does not. Nothing about a binary changes
when a probe times out, so caching that failure forever would serve one bad
moment for the daemon's whole lifetime.

Probe failure degrades rather than blocks: if a CLI is missing or its help
output cannot be parsed, vincent serves the curated catalog with `probe_error`
set, and free-text entry is unaffected.

### "Found" is not "usable"

An installed but unauthenticated CLI probes as healthy and then fails every
single run. Where a CLI can answer cheaply, vincent asks: `cursor-agent status`
populates `logged_in`, so cursor reports a definite true/false. claude and codex
have no cheap probe, so `logged_in` is `null` for them — which the TUI renders as
unknown rather than as fine.

If a service-installed daemon reports agents as missing while the same daemon
started by hand finds them, that is a `PATH` capture problem — see
[Running at login](running-at-login.md#path-too-on-macos-and-linux).

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
  the only adapter that can be asked and resumed.
- **Cost tracking matters** — claude. The other two report none, so the board's
  cost column stays empty for them.
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
- Spec [§9](../versions/v0/spec.md) — the normative adapter contract.
