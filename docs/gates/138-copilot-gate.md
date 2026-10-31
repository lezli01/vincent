# 138 gate — copilot adapter (task 138.9)

**Acceptance (task 138.9, spec §9.9):** a workflow whose steps name
`agent: copilot` runs unattended to completion against the real `copilot`
CLI, on each OS.

Most of this gate is scripted. `scripts/138-gate.sh` drives a real daemon over
curl and asserts four things; CI runs it against the committed fakeagent on
all three OSes, in `gate-group` group 3, and it is the same script you run by
hand against the real CLI:

```sh
./scripts/138-gate.sh                              # fakeagent — what CI runs
VINCENT_GATE_AGENT=copilot ./scripts/138-gate.sh   # the real copilot CLI
VINCENT_GATE_SCENARIO=2 ./scripts/138-gate.sh      # one scenario, for debugging
```

| # | Asserts | Real CLI? |
|---|---|---|
| 1 | A `agent: copilot` workflow runs to done; `/v1/agents` lists copilot fourth, with no models, no default model and its effort catalog; the step records `agent=copilot` and non-zero tokens (the fake's 100 in, 42 out) and no cost (§9.9, task 138.7); the transcript carries copilot-shaped lines; the branch carries the edit | yes |
| 2 | A model the CLI refuses fails the step with the **stderr text** in the step record — copilot writes no `result` line on that path (§9.9, §18) | yes |
| 3 | `logged_in` is `null` on both `/v1/agents` and `/v1/info`: copilot has no login probe (task 138 decision 10) | no — asserts vincent's own reporting |
| 4 | A `permission_mode: restricted` copilot step runs to done on every OS, and the edit reaches the branch through a command step. The fake refuses — exit 2, `fakeagent: copilot restricted argv widened` on stderr — any argv pairing `--available-tools` with an `--allow-all-*` flag, `--yolo` or `COPILOT_ALLOW_ALL`, or naming `bash`/`powershell` in it; the §12.3 debug record shows `--available-tools=view,grep,glob,edit,create,apply_patch` and no `--allow-all` (task 138.10) | no — the fake checks the argv; the real CLI's restricted runs are the task document's captures |
| 5 | A copilot step calls `step_status` over `/mcp/step/{run_id}` through the inline `--additional-mcp-config` JSON, and the status reaches the API — on Windows through `cmd /c` and the npm shim (task 138.8) | no — the fake makes the call |

## Running the real-CLI leg

Install the standalone CLI (`npm install -g @github/copilot`), not the retired
`gh copilot` extension, and log in once interactively (`copilot`, then
`/login`) or export `COPILOT_GITHUB_TOKEN`, `GH_TOKEN` or `GITHUB_TOKEN`.
Vincent cannot tell you beforehand whether that worked — `logged_in` is
`null` by design — so an unauthenticated run fails scenario 1 at the agent
step with copilot's own "No authentication information found" text.

Under `VINCENT_GATE_AGENT=copilot` the gate runs scenarios 1 and 2 only,
exports `COPILOT_AUTO_UPDATE=false`, and prints at the top of the run the CLI
version (through `cmd //c copilot --version` on Windows, for the npm shim),
`COPILOT_HOME`, and **whether** each token variable is set — never its value.
Copy those into the row you record below.

Check the version with `COPILOT_AUTO_UPDATE=false copilot --version`, not a
bare `--version`. The launcher normally runs the newest build it has
auto-updated into its cache, but the gate turns that off, so the daemon runs
the build that was *installed*. A Homebrew cask (`copilot-cli`) installed
before the cache moved on stays at its install-time build: on 2026-10-31 a
bare `--version` said 1.0.105 while the gate ran 1.0.61, whose output has no
usage checkpoint, and scenario 1 failed with `copilot reported no tokens`.
`brew upgrade --cask copilot-cli` brings the installed build up to date.

On Windows, put PowerShell 7 (`pwsh`) on `PATH`. Without it, vincent's command
steps fall back to Windows PowerShell 5.1, which cannot parse the `&&` in
scenario 1's commit step, so the gate fails after the copilot step has passed.

Scenario 1 names no model: copilot ships no model catalog (task 138
decision 9), so the account's default is the only model known to exist.
Scenario 2 asks for `fake-nonexistent`, which the real CLI refuses the way
the fake does.

## The two-turn chat

The script does not cover chats. Their gate (`m14`) runs only against the
fake, so a real-CLI chat is walked by hand on the same host as the gate run.
Start a daemon with a fresh `VINCENT_CONFIG_DIR` and `VINCENT_DATA_DIR`,
`agents.copilot.path: ""`, and `COPILOT_AUTO_UPDATE=false` exported. Register a
throwaway repo (`POST /v1/projects`), then open a chat with
`"agent": "copilot"` (`POST /v1/chats`) and send two messages
(`POST /v1/chats/{id}/send`), waiting for each turn to leave `running`:

1. `Remember the code word PELICAN for later. Reply with just OK. Do not touch any files.`
2. `What code word did I ask you to remember? Reply with the word only. Do not touch any files.`

It passes when both turns are `done`, both carry the same `session_id`, and
turn 2's `result_text` is `PELICAN`. That proves the second turn resumed the
first turn's session. The TUI's new-chat screen is an equivalent route.

## Recorded runs

Each row is one hand-run of the real-CLI legs. CI covers the fakeagent legs on
every push, so this table only tracks what CI cannot. The first real-CLI run was
recorded on 2026-10-30, on Linux. With the two Windows rows that
followed, task 138.9 was closed on 2026-10-30.

| Date | OS | Launched from | CLI version | Token variables | Scenarios | Result |
|---|---|---|---|---|---|---|
| 2026-10-30 | Linux arm64 (Docker, `node:24-bookworm`) | bash, npm `copilot` bin on `PATH` | 1.0.105 | `GH_TOKEN` (gh OAuth) | 1, 2 | pass (task 138.3) |
| 2026-10-30 | Windows (pwsh 7.6.9 on `PATH`) | Git Bash, npm `copilot.cmd` shim first on `PATH` | 1.0.105 | not reported | 1, 2 | pass (task 138.3) |
| 2026-10-30 | Windows (pwsh 7.6.9 on `PATH`) | Git Bash, npm-bundled native `copilot.exe` first on `PATH` | 1.0.105 | not reported | 1, 2 | pass (task 138.3) |
| 2026-10-31 | macOS arm64 | zsh, Homebrew cask `copilot` on `PATH` | 1.0.61 | none set (stored `/login`) | 1 | fail: `copilot reported no tokens`. The cask build ran because the gate turns auto-update off (see above) |
| 2026-10-31 | macOS arm64 | zsh, Homebrew cask `copilot` on `PATH` | 1.0.105 | none set (stored `/login`) | 1, 2; two-turn chat | pass |
| 2026-10-31 | Linux arm64 (Docker, `node:24-bookworm`, git 2.39.5) | bash, npm `copilot` bin on `PATH` | 1.0.105 | `GH_TOKEN` (gh OAuth) | 1, 2; two-turn chat | pass |
