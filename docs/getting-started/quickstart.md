# Quickstart

Ten minutes, one real task: register a repository, run a workflow against it,
read the diff, approve the gate, and end up with a pushed branch.

If you have not installed vincent yet, start with
[Installation](installation.md).

> [!WARNING]
> **Agents run full-auto by default: they can execute arbitrary commands as
> you.** This is intentional — unattended orchestration is the point. The git worktree isolates
> tasks from *each other*, not from your machine: an agent can still reach your
> home directory, your credentials and the network. Nothing is pushed or merged
> unless a workflow step does it, everything is transcripted, and any step can
> be set to `permission_mode: restricted`. Run it on repositories you would hand
> to a new contributor, and read the [Security model](../security-model.md)
> before pointing it at anything else.

---

## 0. Start the daemon

```sh
vincent daemon start
vincent daemon status
```

The daemon owns everything: the database, the git worktrees, and the agent
processes. Clients come and go; work does not stop when they do.

You can skip this step — launching the TUI with a bare `vincent` starts a
daemon if none is running. The subcommands deliberately do not, so a script
that finds no daemon gets exit code 2 instead of silently spawning one.

## 1. Register a repository

Any git repository with a clean working tree.

```sh
vincent project add /path/to/your/repo
vincent project ls
```

`project add` detects the default branch (`origin/HEAD`, then a local `main` or
`master`, then the current branch) and names the project after the directory.
The detection happens **once, here** — the name is stored and never looked up
again. What is refreshed is the branch's *content*: each task fetches its base
from that branch's remote before its worktree is cut, unless you turn
[`fetch_base_branch`](../reference/configuration.md#fetch_base_branch) off. Both
are overridable:

```sh
vincent project add /path/to/repo --name api --default-branch develop
```

Note the project **id** it prints — the task commands take `--project <id>`.

## 2. Add a workflow

A workflow is a YAML file listing the steps a task runs. There are two places
to put one:

| Scope | Location | Notes |
|---|---|---|
| **Global** | `{config_dir}/workflows/*.yaml` | Available to every project |
| **Project** | `.vincent/workflows/*.yaml` in the repo | Travels with the repo; shadows a global file of the same name |

`{config_dir}` is `%APPDATA%\vincent` on Windows,
`~/Library/Application Support/vincent` on macOS, `~/.config/vincent` on Linux
(see [Files and directories](../reference/files.md)).

Global is the easier start, and the binary will write the file for you — the
same command on all three platforms, with no checkout of this repository and no
daemon needed:

```sh
vincent workflow init feature-pr --from feature-pr
```

That creates the directory if it is missing, writes the shipped
[example](../../examples/feature-pr.yaml) with its comments intact, and prints
the path. Drop `--from` for a commented one-agent-step skeleton instead, or add
`--project 1` to write into that repository's `.vincent/workflows/` (the one
part that needs a running daemon). It refuses rather than overwriting anything
already there.

The daemon watches both directories and picks the file up **on save**. There is
no restart and no apply step. A file that fails to parse is reported as invalid
and the previously loaded version keeps running.

Check what the registry now holds:

```sh
vincent workflow validate ~/.config/vincent/workflows/feature-pr.yaml
vincent workflow ls                # built-in + global
vincent workflow ls --project 1    # …plus that project's .vincent/workflows
```

`vincent workflow validate` runs entirely locally — no daemon, no network, no
agent CLI installed — which is what makes it usable from a pre-commit hook or
CI.

> There is a second way to get a workflow: the built-in `create-workflow`, an
> agent that *designs* one from a description and installs it. It needs a
> daemon, an agent CLI, tokens and time, and may stop to ask you a design
> question. `workflow init` hands you a file instead — reach for it when you
> know roughly what you want to write. See
> [Writing workflows](../guides/workflows.md#12-where-workflow-files-live).

### What `feature-pr` does

```
implement (agent)  →  commit (command)  →  review (manual gate)  →  publish (command)
        │
        └─ check: go build ./... && go test ./...
```

The agent writes the change; the **check** decides whether the attempt actually
succeeded, because an agent reporting success is a claim and a build is a fact;
a failed check retries the step with the failure appended to the prompt. Then a
human gate, and only after approval does anything get pushed.

The other four examples are
[`fix-and-test`](../../examples/fix-and-test.yaml) (write a failing test, then
fix it), [`docs-update`](../../examples/docs-update.yaml) (runs `restricted`),
[`converge`](../../examples/converge.yaml) (loops a repair step until the test
suite is green), and [`cursor-review`](../../examples/cursor-review.yaml).

`feature-pr` is written for a Go repository — its check is
`go build ./... && go test ./...`. Open the file and change that line to
whatever proves your repository still works (`npm test`, `cargo test`,
`pytest`) before running it anywhere else.

## 3. Create a task

```sh
vincent task add --project 1 --workflow feature-pr \
  --title "Add a --version flag to the CLI" \
  --description "Print version, commit and build date, then exit 0."
vincent task ls
```

The task is `queued` immediately. When a scheduler slot frees up it becomes
`running` in its own git worktree, on a branch named `vincent/{id}-{slug}` unless
you configured a different convention. Nothing touches your checkout.

Useful additions at creation time:

```sh
--branch feat/OPS-123  # name this task's branch outright
--priority 10          # higher runs first
--agent codex          # override the workflow's agent for this task only
--model sonnet         # …and its model
--base-branch develop  # branch from something other than the project default
```

## 4. Watch it

```sh
vincent
```

That opens the TUI. The board lists every task with its state and step
progress; the detail view below shows the step timeline on the left and the
live agent output on the right, with a **Diff** tab beside it.

| Key | Does |
|---|---|
| `↑`/`↓` | Move the selection |
| `enter` | Open the selected task |
| `]` | Switch the output pane between Output and Diff |
| `v` | Show more or less detail (compact → normal → verbose) |
| `:` | Command palette — everything reachable by name |
| `?` | Every key, in context |
| `q` | Quit (the daemon and any running task keep going) |

The full tour is in [Using the TUI](../guides/tui.md). The data commands are
subcommands too:

```sh
vincent task show 1
vincent task ls --state running
```

Everything the TUI does is a subcommand, including the human actions on a live
task — approve, reject, retry, repair, skip, pause, resume, answer, archive —
so the gate you are about to approve by hand can also be approved from a script:

```sh
vincent task approve 1
vincent task ls --state blocked --json | jq -r '.[].id' |
  while read -r id; do vincent task retry "$id"; done
```

The [CLI reference](../reference/cli.md) is the full tree, and
[Scripting vincent](../guides/scripting.md) is the guide.

## 5. Approve the gate

When the agent finishes and the check passes, the task stops at the `review`
step and enters `awaiting_gate`. Tasks waiting on a human are pinned to the top
of the board with a badge; `!` jumps to the next one.

Read the diff in the **Diff** tab, then:

- `a` — approve. The workflow advances and `publish` pushes the branch.
- `x` — reject. The task goes to `blocked`, where you can retry, edit and
  retry, skip the step, or cancel.

If the agent's attempt failed instead, the task is `blocked` and the action bar
offers what the [state machine](../reference/task-lifecycle.md) allows there:
`r` to retry, `E` to edit the step's prompt in `$EDITOR` and retry, `s` to skip
it, `c` to cancel.

## 6. Ship it

The branch is a normal git branch in your repository:

```sh
git log --oneline origin/main..vincent/1-add-a-version-flag-to-the-cli
gh pr create --head vincent/1-add-a-version-flag-to-the-cli
```

When you are done with the task, archive it (`A` in the TUI). That removes the
**worktree** and keeps the record. The branch you just made a PR from carries
commits, so it is kept as well; a branch that never received a commit is deleted
instead of accumulating as an empty ref, and the TUI says which happened. See
[`delete_empty_branch_on_archive`](../reference/configuration.md#delete_empty_branch_on_archive).

---

## What to read next

- [Concepts](concepts.md) — the model behind what you just did.
- [Writing workflows](../guides/workflows.md) — your own, instead of an
  example.
- [Running at login](../guides/running-at-login.md) — `vincent service install`
  so the daemon survives reboots.
- [Agent CLIs](../guides/agents.md) — what claude, codex and cursor each bring.
