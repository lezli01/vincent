# M12 phase gate — steps in a container (tasks 061 and 062.2)

**Acceptance (spec §16, §12.3):** a task's steps — agent steps included, since
task 062.2 — run inside one container on an image the user supplies, and the
container is created, kept and removed at the moments the spec says.

Almost all of this gate is scripted. [`scripts/m12-gate.sh`](../../scripts/m12-gate.sh)
drives a real daemon against a real docker runtime; its header lists the ten
scenarios, and CI runs them on the Linux leg (the macOS and Windows skips are
explained there and in [task 061](../tasks/061-container-step-execution.md)).
Scenarios 6–10 run `cmd/fakeagent`, cross-compiled for linux and bind-mounted
into the image as `claude`, so no real agent CLI is involved. Scenario 10 is
task 115's: a chat opened on a blocked containerized task runs its turn inside
that task's container, and a free chat's turn runs on the host.

```sh
./scripts/m12-gate.sh    # every scenario; there is no single-scenario switch
```

## Manual leg: a real claude in the container

One thing fakeagent cannot prove, left for a manual run by
[062.2](../tasks/062-agent-steps-in-containers.md#0622-decisions): that a
**headless claude starts with an empty `~/.claude.json` under the vincent
home** when its credentials come from a token variable rather than the mounted
`~/.claude`. That is the macOS case, where claude's login is in the Keychain and
the container gets `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` through
`environment` instead.

To walk it: an image carrying `git` and the real `claude` CLI; `container.image`
set to it with `mount_agent_config` on; the token variable given to containerized
steps through `environment`; and a one-step `agent: claude` workflow that makes a
commit. It passes when the task reaches `done`, the step records claude's tokens
and cost, the branch carries the commit, and no step failed
`agent_unauthenticated`.

## Recorded runs

| Date | OS | Runtime | CLI version | What | Result |
|---|---|---|---|---|---|
| — | — | — | — | manual leg, real claude, token variable | **not yet walked** |
