# FAQ

Short answers, with links to the long ones.

---

### What is vincent, in one sentence?

A local-first control plane for AI coding-agent workloads: a background daemon
runs agent CLIs against your git repositories under workflows you write, and
every client is a thin consumer of its localhost API.

### Does it need an API key or an account?

No. Vincent stores **no credentials**. It runs the agent CLI you already
installed and authenticated. There is no vincent account and no telemetry.

Almost every call vincent makes is on your behalf: whatever your agent CLI and
your workflow's own git commands do, plus reading GitHub — an issue or a pull
request when you create a task from one, and a project's pull requests when you
ask for them.
That reading uses your existing `gh` login, or a `GITHUB_TOKEN`/`GH_TOKEN`
already in the daemon's environment; vincent stores neither, and never writes
anything to GitHub.

Two calls the daemon makes **without** being asked, both switchable:

- **The release check.** Once a day the daemon asks GitHub for vincent's latest
  stable release, so `vincent doctor` can tell you a newer one exists. It is one
  unauthenticated GET and it sends nothing identifying — no token, no telemetry,
  no machine or install identifier. It downloads and installs nothing; that is
  [`vincent update`](reference/cli.md#vincent-update), which you run. Set
  [`update.check: false`](reference/configuration.md#update) and the daemon
  makes no such request at all.
- **The pull-request reconciler.** Every
  [`github.poll_interval`](reference/configuration.md#github) (5 minutes by
  default) the daemon lists each GitHub-based project's open pull requests, so a
  task can name the pull request opened from its branch. It fires only for
  projects whose `origin` is a github.com repository, so a daemon with no such
  project never makes it. Set `poll_interval: 0` to stop it and keep the rest,
  or [`github.enabled: false`](reference/configuration.md#github) to switch off
  every GitHub call vincent makes.

### Which agent CLIs work?

Claude Code (`claude`), Codex (`codex`) and Cursor (`cursor-agent`). Pick one
per workflow, per step, or per task. See [Agent CLIs](guides/agents.md).

### Does it work on Windows?

Yes — fully, and CI proves it on every pull request alongside macOS and Linux.
The one genuine gap is that cursor's `restricted` mode requires macOS or Linux;
elsewhere vincent refuses the task at creation rather than silently running the
step full-auto. See
[Windows](platforms/windows.md).

### Is my repository safe? What does "full-auto" actually mean?

An agent in full-auto can run arbitrary commands **as you**. The git worktree
isolates tasks from each other, not from your machine. Nothing is pushed or
merged unless a workflow step does it, everything is transcripted, and any step
can be `restricted`. Read the [Security model](security-model.md) before
pointing it at anything sensitive.

### Does it touch my working copy?

No. Each task gets its own `git worktree` on its own branch — `vincent/{id}-{slug}`
unless you configure a different convention. Your checkout, current branch and
stash are untouched.

### Does it delete my branches?

Only an empty one. Archiving a task removes its **worktree** and keeps the
record. It keeps the branch too, unless that branch has no commits past the base
it was cut from — a workflow that never wrote to the repository leaves nothing
on its branch, and archiving deletes it rather than leaving an empty ref behind.
**A branch carrying any commit is never deleted.** Turn the cleanup off with
[`delete_empty_branch_on_archive: false`](reference/configuration.md#delete_empty_branch_on_archive).

The remote counterpart is left alone unless you also set
[`delete_remote_branch_on_archive: true`](reference/configuration.md#delete_remote_branch_on_archive)
— off by default, and even then it runs only after the local delete succeeded,
only for a branch with a configured upstream, and only when you archive the task
yourself.

To find what to clean up, ask vincent rather than git — with configurable names a
glob no longer finds them all:

```sh
vincent task ls --archived      # the branch column lists every branch vincent made
```

### What happens if I close the TUI?

Nothing. The daemon owns all execution; clients are disposable. Close the TUI,
close the terminal, log out — tasks keep running (on Linux, surviving logout
needs `loginctl enable-linger`, which the service installer handles).

### What happens if the daemon crashes mid-run?

Every transition is persisted before it is acted on. On restart, interrupted step
runs are finalized, verified orphan processes are killed (the PID must still
exist **and** still hold the same process, checked against a platform-native
identity recorded at spawn — a PID the operating system has since handed to
something else is left alone), and the step re-runs as an attempt that **does
not consume a retry**. See [Task lifecycle](reference/task-lifecycle.md#interruption-is-not-failure).

### Do I have to write YAML?

Not to start: the built-in `adhoc` workflow is one agent step, and five
[examples](../examples) ship ready to copy. You will want your own soon after —
[Writing workflows](guides/workflows.md) is short. If you would rather not write
the first one by hand, run the built-in `create-workflow` against the repository
that needs it: describe the workflow in the task, name it in the required
`workflow_name` field, and leave `global` unset to install into that repo's
`.vincent/workflows` — or set it to `true` for `{config_dir}/workflows`.

Later, when vincent has grown features your files predate, the built-in
`update-workflows` rewrites the workflows a project versions against the
current schema and hands you the result as a reviewable diff. It changes how
they are written, not what they do.

### How do I make a workflow only apply to one repository?

Put it in `.vincent/workflows/` inside the repo. Project scope shadows a global
file of the same name, and the file travels with the repository.

### Do I need to restart anything after editing a workflow or the config?

No. The daemon watches both directories and reloads on save. An invalid file is
reported and the last good version keeps running. The one exception is `listen:`,
which takes effect at the next daemon restart.

### My workflow file does not show up in `vincent workflow ls`

Add `--project <id>` — without it you see built-in and global scopes only.

### Why did a step fail when the agent said it succeeded?

Because a `check` disagreed. An agent reporting success is a claim; a build is a
fact. `check_failed` is the healthy, common failure: read the transcript, then
press `E` to edit the prompt and retry. See
[Checks](guides/workflows.md#7-verification-with-check).

### A task is stuck in `queued`

It is waiting for a scheduler slot. Check `max_parallel_tasks` (default 3) and
the per-project cap, and raise the task's priority to move it up the queue.

### A task is stuck in `awaiting_input`

An agent asked you something. It is pinned to the top of the board with a badge —
press `enter` on the row to answer. Note that `awaiting_input` **holds a
concurrency slot**, because the agent process is alive mid-step. Set
`on_input: deny` on workflows that must stay unattended.

### vincent says my agent CLI is missing, but my shell finds it

Almost always `PATH`. A service-installed daemon captures `PATH` at install time
on macOS and Linux, so a CLI installed afterwards is invisible to it — rerun
`vincent service install`. Or skip `PATH` entirely with
`agents.<name>.path` in [config.yaml](reference/configuration.md). If it is
cursor: vincent resolves **`cursor-agent`**, never `cursor`.

### Why does running a cursor step change my cursor model?

Cursor persists whatever `--model` it is given to `~/.cursor/cli-config.json`,
and vincent always passes one (defaulting to `auto`) so runs are reproducible.
It is the one place vincent writes outside its own directories, and it is
documented rather than discovered.

### Can I use it in CI?

`vincent workflow validate` yes — it needs no daemon, no network and no agent
CLI, which makes it ideal for a pre-commit hook or a CI job. Running *tasks* in
CI is possible via the API but was not the design target: vincent is built for a
developer machine.

### Can I drive it from a script?

Yes. Every subcommand takes `--json`, exit codes distinguish "fix your request"
(1) from "no daemon" (2), and the API is a documented localhost REST + SSE
surface. See [Scripting vincent](guides/scripting.md).

### Is there a web UI?

Not today. The TUI and the CLI are the shipped clients; both are thin consumers
of the API, so a web UI is a client someone could write against the same
endpoints.

### Does it cost anything? Does it show me what a run cost?

Vincent is MIT-licensed and free. Your agent CLI's usage is billed by its vendor.
Claude Code reports cost, so the board's cost column sums every attempt for it;
codex and cursor report none, and vincent shows nothing rather than guessing.
That figure can also be a limit:
[`max_task_cost_usd`](reference/configuration.md#max_task_cost_usd) blocks a task
once it has spent past a ceiling you set. It is off by default, and — for the
same reason the column is empty — it cannot see a task that ran on codex or
cursor.

### How do I stop everything right now?

```sh
vincent daemon stop
```

Graceful: admission stops, running processes get 15 seconds, then they are
killed and marked `interrupted` — the same resume path as a crash, so nothing is
lost. `--force` if it will not go.

### How do I remove it completely?

`vincent service uninstall`, `vincent daemon stop`, delete the binary, delete the
[config and data directories](reference/files.md), then clean up `vincent/*`
branches in any repository you used.

### Where do I report a bug?

[GitHub issues](https://github.com/lezli01/vincent/issues/new/choose). Security
issues go through
[private advisories](https://github.com/lezli01/vincent/security/advisories/new)
instead. Include `vincent version`, your OS, and the relevant part of
`{data_dir}/logs/daemon.log`.

### Can I contribute?

Please. The [Contributing guide](https://lezli01.is-a.dev/vincent/contributing.html) has the setup, the commit
convention, architecture pointers, documentation expectations, and the PR
checklist. For substantial changes, open an issue first so the problem and user
impact can be agreed before implementation begins.
