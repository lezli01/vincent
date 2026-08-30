package taskrun

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/container"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// ReasonContainerImageUnavailable blocks a task whose configured image is
// missing and cannot be pulled (§16, task 061 decision 3). It is an admission
// block rather than a creation refusal because verifying an image means a
// registry pull: doing that inside `POST /v1/tasks` runs a multi-gigabyte
// download against §13.1's request timeouts, and inspecting local-only would
// 400 every first run on a fresh machine. Blocking here still spends no
// worktree, no branch and no retry — which is what the acceptance criterion
// was protecting.
const ReasonContainerImageUnavailable = "container_image_unavailable"

// ReasonContainerUnavailable blocks a task whose runtime is gone or cannot
// create a container. Task creation already refuses a missing runtime, so this
// is the §12.4-shaped backstop for a task whose daemon changed underneath it —
// docker uninstalled, the desktop app quit, the socket revoked.
//
// A containerized step is never quietly run on the host instead: a step that
// is not contained after asking to be contained inverts the choice it made,
// which is §9.4's reasoning verbatim.
const ReasonContainerUnavailable = "container_unavailable"

// containerSettings resolves the §8.6 precedence chain for containerization:
// the workflow's `defaults.container:` over the daemon's `container:` block.
// There is no task level in this delivery (decision 6).
//
// It is read per admission rather than cached, so a hot reload reaches the
// next task admitted — the same rule ensureWorktree applies to
// `fetch_base_branch`.
func (r *Runner) containerSettings(wf *workflow.Workflow) config.Container {
	c := r.deps.Config().Container
	if wf != nil {
		c = c.Merge(wf.Defaults.Container)
	}
	return c
}

// runtimeFor returns the Runtime for a resolved settings block, through the
// injectable factory so tests never need docker.
func (r *Runner) runtimeFor(c config.Container) container.Runtime {
	if r.deps.Containers != nil {
		return r.deps.Containers(c.RuntimeBinary())
	}
	return container.New(c.RuntimeBinary())
}

// taskContainer is what a step needs to run inside one: the handle to exec
// into and the settings that produced it. A zero value means "run on the
// host", which is every task of an installation that never sets
// `container.image`.
type taskContainer struct {
	id       string
	settings config.Container
	rt       container.Runtime
}

func (t taskContainer) active() bool { return t.id != "" }

// ensureContainer creates the task's container on first admission and finds it
// again on every later one (§16, task 061). It runs after ensureWorktree
// because the worktree is one of the two bind mounts.
//
// A failure blocks the task, never falls back to the host.
func (r *Runner) ensureContainer(
	ctx context.Context, task *store.Task, project *store.Project, wf *workflow.Workflow, log *slog.Logger,
) (taskContainer, error) {
	c := r.containerSettings(wf)
	if !c.Enabled() {
		return taskContainer{}, nil
	}
	rt := r.runtimeFor(c)
	name := container.Name(task.ID)
	if id, err := rt.Lookup(ctx, name); err == nil && id != "" {
		// The container this task left behind on an earlier admission. Reusing
		// it is the point of decision 9: a retry finds what an earlier step
		// installed.
		return taskContainer{id: id, settings: c, rt: rt}, nil
	}
	if err := rt.Available(ctx); err != nil {
		r.fail(task, ReasonContainerUnavailable, log, "container runtime", err)
		return taskContainer{}, err
	}
	if err := rt.EnsureImage(ctx, c.Image); err != nil {
		if ctx.Err() != nil {
			r.interrupt(task, log)
			return taskContainer{}, err
		}
		reason := ReasonContainerImageUnavailable
		if !errors.Is(err, container.ErrImageUnavailable) {
			reason = ReasonContainerUnavailable
		}
		r.fail(task, reason, log, "container image", err)
		return taskContainer{}, err
	}
	id, err := rt.Create(ctx, container.CreateSpec{
		Image:  c.Image,
		Name:   name,
		Labels: map[string]string{container.LabelTask: strconv.FormatInt(task.ID, 10)},
		Mounts: containerMounts(project.Path, task.WorktreePath, c),
		// The gateway host is mapped whenever the container has a network at
		// all: it costs nothing when unused and is what a containerized agent
		// step needs to reach the daemon's per-step MCP endpoint (decision 1).
		Network:        c.Network,
		AddHostGateway: c.Network,
		User:           container.HostUser(),
	})
	if err != nil {
		if ctx.Err() != nil {
			r.interrupt(task, log)
			return taskContainer{}, err
		}
		r.fail(task, ReasonContainerUnavailable, log, "create container", err)
		return taskContainer{}, err
	}
	log.Info("container created", "task", task.ID, "image", c.Image,
		"runtime", rt.Name(), "container", name)
	return taskContainer{id: id, settings: c, rt: rt}, nil
}

// containerMounts is decision 2's mount set: the project repository and the
// task's worktree, each at its own absolute host path.
//
// Mounting both at their own paths is what makes the repository resolve with
// zero translation code: a worktree's `.git` is a file holding an absolute
// `gitdir:` into `{project}/.git/worktrees/{id}`, and that directory's own
// `gitdir` file points back at the worktree. Mount either alone and the pair
// is broken; mount both anywhere else and both pointers are wrong.
func containerMounts(projectPath, worktreePath string, c config.Container) []container.Mount {
	mounts := []container.Mount{{Source: projectPath, Target: projectPath}}
	if worktreePath != "" && worktreePath != projectPath {
		mounts = append(mounts, container.Mount{Source: worktreePath, Target: worktreePath})
	}
	mounts = append(mounts, agentConfigMounts(c)...)
	for _, spec := range c.ExtraMounts {
		if m, ok := parseMount(spec); ok {
			mounts = append(mounts, m)
		}
	}
	return mounts
}

// parseMount splits a validated `host:container[:ro]` entry. Validation
// already refused a malformed one at load, so an unparseable entry here is
// dropped rather than failing an admission over a config the daemon accepted.
func parseMount(spec string) (container.Mount, bool) {
	parts := splitMount(spec)
	if len(parts) < 2 {
		return container.Mount{}, false
	}
	m := container.Mount{Source: parts[0], Target: parts[1]}
	if len(parts) == 3 && parts[2] == "ro" {
		m.ReadOnly = true
	}
	return m, true
}

func splitMount(spec string) []string {
	var out []string
	start := 0
	for i := range len(spec) {
		if spec[i] == ':' {
			out = append(out, spec[start:i])
			start = i + 1
		}
	}
	return append(out, spec[start:])
}

// removeTaskContainer tears a task's container down. It is called where the
// worktree is removed, because the container holds that worktree as a bind
// mount and a live mount is a reason a removal fails.
//
// Errors are logged, never returned: a container that will not die must not be
// able to stop a task from being archived, and `docker rm -f` on a container
// that is already gone is the ordinary case after a crash.
func (r *Runner) removeTaskContainer(ctx context.Context, task *store.Task, log *slog.Logger) {
	c := r.deps.Config().Container
	// The workflow's own override may have named a different runtime binary;
	// the snapshot is the only place that survives here, and reading it for
	// one string is not worth a parse. Runtime binaries are docker-compatible
	// by definition, so the configured one can address any of them.
	rt := r.runtimeFor(c)
	name := container.Name(task.ID)
	id, err := rt.Lookup(ctx, name)
	if err != nil || id == "" {
		return
	}
	if err := rt.Remove(ctx, id); err != nil {
		log.Warn("remove container", "task", task.ID, "container", name, "error", err)
		return
	}
	log.Info("container removed", "task", task.ID, "container", name)
}

// containerEnv is a containerized step's environment (decision 7): the §12.3
// policy with `inherit: all` read as `none`, layered on the image's own
// environment rather than the daemon's.
//
// The reinterpretation is logged once per task by the caller, not here: a
// macOS or Linux host's PATH, HOME, TMPDIR and SHELL inside a Linux image is a
// broken container, not an inherited one, and a user who wrote nothing should
// still learn that the default read differently.
func (r *Runner) containerEnv() []string {
	return config.ContainerEnvironment(r.deps.Config().Environment).Resolve(nil)
}

// containerGraceTimeout is how long a stopped step gets between TERM and KILL
// inside the container. It is §12.4's own 15 s, applied at the only place that
// can deliver a signal to a process the host cannot see.
const containerGraceTimeout = 15 * time.Second

// stopInContainer implements decision 9's kill: TERM the step's process from
// inside the container, wait, then KILL. The task's container **survives** —
// `rm -f` is reserved for whole-task teardown and recovery, so a retry finds
// what an earlier step installed rather than starting from the image again.
func stopInContainer(tc taskContainer, key string, log *slog.Logger) {
	// A context of its own: the run context is already canceled by the time
	// this runs — that cancellation is what called it — and a signal that
	// inherits it would be dead on arrival.
	ctx, cancel := context.WithTimeout(context.Background(), containerGraceTimeout*2)
	defer cancel()
	if err := tc.rt.Signal(ctx, tc.id, key, "TERM"); err != nil {
		log.Warn("signal step in container", "container", tc.id, "signal", "TERM", "error", err)
	}
	select {
	case <-time.After(containerGraceTimeout):
	case <-ctx.Done():
		return
	}
	if err := tc.rt.Signal(ctx, tc.id, key, "KILL"); err != nil {
		log.Warn("signal step in container", "container", tc.id, "signal", "KILL", "error", err)
	}
}

// execKey names a step run's pid file inside the scratch mount. It is the run
// id, so a parallel group's sub-steps exec'ing into the one container do not
// share a file (§7.5).
func execKey(runID int64) string { return "step-" + strconv.FormatInt(runID, 10) }

// envNoticeOnce keeps decision 7's reinterpretation to one line per daemon.
var envNoticeOnce sync.Once

// LogContainerEnvironmentOnce reports decision 7's reinterpretation the first
// time a containerized step runs in this daemon. Once per process rather than
// once per task: the message is about the policy, not about the task, and one
// line per task on a busy board is noise.
func LogContainerEnvironmentOnce(log *slog.Logger, e config.Environment) {
	if e.Inherit.Mode != config.InheritAllMode {
		return
	}
	envNoticeOnce.Do(func() {
		log.Info("containerized steps read environment.inherit: all as none; " +
			"the base environment is the image's, not the daemon's (§12.3)")
	})
}

// agentConfigMounts are the host's agent configuration directories, mounted at
// their own paths so subscription-based auth survives into the container
// (`mount_agent_config`). Nothing less works: the CLIs that authenticate by
// subscription take no key from the environment, and cursor persists `--model`
// to its own config (§9.7).
//
// A directory that does not exist is skipped rather than created: an empty
// mount would make a CLI believe it had been configured and never logged in.
func agentConfigMounts(c config.Container) []container.Mount {
	if !c.MountAgentConfig {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []container.Mount
	for _, dir := range []string{".claude", ".codex", ".cursor"} {
		p := filepath.Join(home, dir)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		out = append(out, container.Mount{Source: p, Target: p})
	}
	return out
}

// setTaskContainer records the container an admission is running in and
// returns the release that forgets it. It lives on the Runner rather than on
// stepEnv because every step of a task shares one container (decision 1) and
// stepEnv is constructed at a dozen call sites — threading a value every one
// of them would have to pass through unchanged is how a case gets missed.
func (r *Runner) setTaskContainer(taskID int64, tc taskContainer) func() {
	if !tc.active() {
		return func() {}
	}
	r.mu.Lock()
	if r.containers == nil {
		r.containers = make(map[int64]taskContainer)
	}
	r.containers[taskID] = tc
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.containers, taskID)
		r.mu.Unlock()
	}
}

// taskContainerOf returns the container this task's steps run in, or a zero
// value meaning the host.
func (r *Runner) taskContainerOf(taskID int64) taskContainer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.containers[taskID]
}
