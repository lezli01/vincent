// Package container runs a task's step processes inside a container instead of
// on the daemon's own host (spec §16, §20 — task 061).
//
// The shape mirrors internal/agent: one Runtime interface, one implementation
// per CLI, and a step engine that never learns which one it holds. Today the
// only implementation shells out to a docker-CLI-compatible binary named by
// `container.runtime`; only `docker` is verified in CI, and the documentation
// says so rather than implying podman and nerdctl are tested.
//
// Two properties of the design live here rather than in the engine:
//
//   - Paths are identical inside and out (task 061 decision 2). The project
//     repository and the task's worktree are bind-mounted at their own absolute
//     host paths, so a worktree's `.git` file — which holds an absolute
//     `gitdir:` into the parent repository — resolves inside the container with
//     no translation, and §8.4's `.Worktree` and §8.5's `VINCENT_WORKTREE`
//     render values that are true on both sides. `C:\...` cannot exist in a
//     Linux container, which is why a Windows daemon refuses a containerized
//     task at creation rather than translating.
//
//   - The container outlives a step (decision 9). Killing the host-side
//     `docker exec` client leaves the process running inside, so each step
//     writes its in-container pid to a private scratch mount and a stop is an
//     exec that signals it. Removal is reserved for whole-task teardown and
//     §12.4 recovery, so a retry finds what an earlier step installed.
//
// The container confines the filesystem outside the two mounts, the shell and
// the installed tooling. It is not a network boundary — outbound traffic is on
// by default — and it is not a credential boundary once `mount_agent_config`
// puts the host's agent configuration inside it. §16 states both.
package container
