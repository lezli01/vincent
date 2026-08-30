# 061 — Run a task's steps inside a container: the exec seam

*Issue #256. Settled with the author on 2026-08-30.*

Status: **done (1/1)** — 061.1 landed 2026-08-30.

Promotes the **container** half of §20's "Container/VM-sandboxed step
execution" and amends §2's "sandboxing agents beyond worktree isolation"
non-goal. Agent steps inside the container are [062](062-agent-steps-in-containers.md);
this task is the seam, proven with `command`, `check` and `manual` steps.

## 061.1 — the container exec seam ✅

One container per task, created with the worktree and removed with it, in which
every step process of that task runs. `container.image: ""` is the default, so
an existing installation is byte-for-byte unchanged.

Landed: `internal/container` (new), the `container:` config block and its
workflow-level override, migration `0021` adding `step_runs.container_id`, the
creation gate and `GET /v1/info` reporting, the taskrun create/exec/stop/remove
paths, container-aware recovery, the two block reasons, a `vincent doctor` row,
and the §2/§8.3/§8.5/§12.3/§12.4/§16/§20 amendments.

## Decisions

These were settled before any code was written and are binding.

1. **MCP reachability (§13.4).** The daemon rewrites the per-step endpoint's
   host to `host.docker.internal` for a containerized agent step and passes
   `--add-host=host.docker.internal:host-gateway` — native on Docker Desktop,
   Docker ≥ 20.10 on Linux. `mcp.wire_steps` keeps its `true` default so a
   containerized step is not quietly less capable than a host step and task
   057's default is not inverted. *Consequence:* `container.network: false`
   with `mcp.wire_steps: true` is a contradiction and is refused at task
   creation with `400 validation_failed`. **The alternative it beat:**
   `--network=host`, which does not exist on Docker Desktop (so macOS would
   need the rewrite anyway — two paths) and silently defeats the `network`
   knob.

2. **Paths are identical inside and out; POSIX hosts only.** The project path
   and `{data_dir}/worktrees/{task_id}` are bind-mounted at their own absolute
   host paths. A worktree's `.git` is a file holding an absolute
   `gitdir: {project.path}/.git/worktrees/{task_id}`, and the parent's own
   `gitdir` file points back — both absolute — so mounting both where they live
   makes the repository resolve with **zero translation code**, and `.Worktree`,
   `.ProjectPath`, `VINCENT_WORKTREE` and `VINCENT_PROJECT_PATH` (§8.4, §8.5)
   keep rendering values true on both sides. *Consequence:* `C:\...` cannot
   exist in a Linux container, so a **Windows daemon refuses a containerized
   task at creation**; `platforms:` keeps gating on the host. **The alternative
   it beat:** mounting at `/vincent/repo` and `/vincent/worktree` with
   translation, which creates two path vocabularies a workflow author has to
   hold apart. Canonical-path mounting for Windows is a named follow-up (§20);
   its trigger is a Windows user asking.

3. **The creation gate is split; the image check moves to admission.** Task
   creation refuses only what is cheap and local — the runtime binary missing
   or unusable, a containerized task on a Windows host, and decision 1's
   contradiction. A missing, unpullable image **blocks at admission** with
   `container_image_unavailable`, before a worktree, a branch or a retry is
   spent; `container_unavailable` is the §12.4-shaped backstop for a task whose
   daemon changed underneath it. This is task 041's actual shape rather than
   "an exact mirror" of it: 041 decision 3 rests on the `restricted` verdict
   being judgeable **with no binary installed**, because it depends on adapter
   identity and `GOOS` — no I/O at all — and 041 decision 4 re-affirms task 003
   decision 4, that there is no pre-flight refusal on an unhealthy environment.
   Verifying an image needs a registry pull. **The alternatives they beat:**
   pulling inside `POST /v1/tasks`, which runs a multi-gigabyte download
   against §13.1's request timeouts; and inspecting local-only, which `400`s
   every first run on a fresh machine.

4. **Recovery journals the container id, and the kill is `rm -f`.** Migration
   0021 adds `step_runs.container_id`; the container carries a
   `com.vincent.task` label. Recovery reads the id, confirms the label, and
   removes the container — which kills every process inside it, so no second
   exec-identity path is needed. `procx.Identity` and the §12.4 PID-reuse guard
   are untouched for host steps, and a container id is never reused, so the
   reuse question does not arise. **The alternative it beat:** journaling both
   the host `docker exec` client PID and the container id; the client PID
   proves nothing the container id does not.

5. **Linux runs as the invoking user.** Every exec passes `--user {uid}:{gid}`
   on a Linux host, so files land owned correctly and git inside the container
   sees the same owner as the worktree on disk. macOS Docker Desktop maps
   ownership itself, so the flag is Linux-only. An image with a baked non-root
   user is overridden, and one whose `HOME` is writable only by its own user
   needs `HOME` pointed at a mounted directory. **The alternative it beat:**
   running as the image's user and `chown -R`-ing the worktree after each step,
   which is slow on a large repository and races a still-running `parallel`
   sub-step.

6. **Two override levels, not three.** `container.image` and its siblings
   resolve from workflow `defaults:`, else `config.yaml`. No task column, no
   `POST /v1/tasks` field, no CLI flag, no New-task TUI control. **The
   follow-up and its trigger:** a per-task override, when somebody needs one
   task run against a different image. This keeps the migration count at one.

7. **A containerized step's environment base is the image's, not the
   daemon's.** `environment.inherit: all` — §12.3's default — is read as `none`
   for a containerized step and logged once per task, because a macOS or Linux
   host's `PATH`, `HOME`, `TMPDIR` and `SHELL` inside a Linux image is a broken
   container, not an inherited one. An explicit name list is honoured verbatim,
   and `environment.unset`, `environment.set` and §8.5's `VINCENT_*` block
   apply exactly as specified on top of the image's own environment. **The
   alternative it beat:** a separate `container.env` block, which is more
   honest about one key meaning two things but duplicates the whole vocabulary
   so every future environment feature lands twice.

8. **`shell: pwsh | cmd` is refused twice, at the two places that can know.**
   At **load**, when the workflow's own `defaults:` pins a `container.image` —
   the only case load-time validation can judge. At **task creation**
   otherwise, with `400 validation_failed` naming the step, because that is the
   first moment the config-level and workflow-level images resolve together.
   Containerization resolves from the hot-reloadable `config.yaml` as well as
   from the workflow, so a workflow validated at load does not know whether the
   task that will run it is containerized. A containerized `run:` body executes
   under the container's `/bin/sh` even on a POSIX host whose daemon shell
   would have been the same; §8.3's inverse is documented, never translated.

9. **The step kill is a PID file in a scratch mount.** Killing the host-side
   `docker exec` client leaves the process running inside. Each step runs as
   `sh -c 'echo $$ > /vincent-run/{run_id}.pid; exec <cmd>'` against a
   container-private scratch mount, under `setsid` where the image has it so
   the tree is one process group. A §7.2 timeout, a §6 cancel and a graceful
   shutdown exec `kill -TERM`, wait the 15 s §12.4 already specifies, then
   `KILL`. The task's container **survives**, so a retry finds what an earlier
   step installed; `rm -f` is reserved for whole-task teardown and recovery
   (decision 4). **The alternatives they beat:** a container per step, which
   reverses decision 1; and `rm -f` on any step stop, which makes a retry a
   different run from the one that timed out.

10. **`docker exec` is attached without a TTY.** `-i`, never `-t`: a TTY merges
    stdout and stderr and translates newlines, which would corrupt the JSONL an
    adapter's `LineParser` reads and therefore §17's token and cost records.

## What the tests prove

Hermetic per the repository's conventions — no real docker in `go test`. The
`Runtime` interface is faked and the docker implementation's argv construction
is table-driven the way adapter parsing is.

- `container.image: ""` consults no runtime and changes no behaviour. This is
  the regression that matters most.
- Each creation refusal names what the human has to change, and the `pwsh` one
  names the step.
- `inherit: all` yields no host variable inside a containerized step; an
  explicit list yields exactly the named ones.
- A `running` step run whose container still carries this task's label is
  removed on recovery; one whose label names another task is **not** touched —
  §12.4's rule that what cannot be proved is not killed.
- A `pwsh` step in an image-pinning workflow fails validation naming the step;
  the same workflow with no image loads.
- The two mounts are the repository and the worktree, at their own paths.

One thing the gate found that no unit test could: decision 2's "identical
inside and out" is about the **physical** path. A project or data directory
reached through a symlink — macOS's `/tmp` and `/var` both are — is mounted as
written while git resolves the worktree's `gitdir:` pointer to the physical
path, and the step reports `not a git repository`. The configuration reference
says so and the gate resolves its own temporary directory with `pwd -P`.

`scripts/m12-gate.sh` covers what unit tests cannot: a real runtime, exactly
one container per containerized task, a step timeout that stops the process
while the container survives, and a daemon killed mid-step leaving no container
behind. It **skips cleanly** on a host that cannot run the feature, which is
what keeps the macOS and Windows CI legs green — but the two skips are not the
same skip. The macOS runner has no docker daemon, so `docker info` fails. The
Windows runner ships docker and its daemon answers, in Windows-container mode,
where the gate's `FROM alpine:3` fails with "no matching manifest for
windows/amd64" — so the gate refuses on the host platform before it probes
anything, which is the honest reason anyway: a Windows daemon refuses a
containerized task at creation (decision 2), leaving nothing there to assert.
That is a real coverage gap and it is stated rather than implied: a gate that
has never run on a platform is not known to pass there.

## Out of scope

Windows container images, a no-network-by-default profile, `devcontainer.json`,
vincent-published images, VM-level sandboxing, and running the daemon itself in
a container. Added by the decisions above, each with its trigger: a per-task
`container.image` override (decision 6) and canonical-path mounting for Windows
hosts (decision 2). Both are recorded in §20 so they are not rediscovered.
