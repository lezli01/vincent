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
| 1 | A `agent: copilot` workflow runs to done; `/v1/agents` lists copilot fourth, with no models, no default model and its effort catalog; the step records `agent=copilot` and tokens and cost unreported (§9.9, until task 138.7); the transcript carries copilot-shaped lines; the branch carries the edit | yes |
| 2 | A model the CLI refuses fails the step with the **stderr text** in the step record — copilot writes no `result` line on that path (§9.9, §18) | yes |
| 3 | `logged_in` is `null` on both `/v1/agents` and `/v1/info`: copilot has no login probe (task 138 decision 10) | no — asserts vincent's own reporting |
| 4 | *Not yet:* restricted mode is task 138.10 (#862), which adds this scenario | — |
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

Scenario 1 names no model: copilot ships no model catalog (task 138
decision 9), so the account's default is the only model known to exist.
Scenario 2 asks for `fake-nonexistent`, which the real CLI refuses the way
the fake does.

## Recorded runs

Each row is one hand-run of the real-CLI legs. CI covers the fakeagent legs on
every push, so this table only tracks what CI cannot. No real-CLI run has been
recorded yet; task 138.9 stays open until one is.

| Date | OS | Launched from | CLI version | Token variables | Scenarios | Result |
|---|---|---|---|---|---|---|
