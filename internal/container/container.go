package container

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ScratchDir is the container-private mount a step writes its pid file into
// (task 061 decision 9). It is deliberately not under either bind mount: the
// worktree is the user's working tree and a stray `.pid` in a `git status` is
// noise a workflow author would have to explain.
const ScratchDir = "/vincent-run"

// HomeDir is the vincent-provided HOME every containerized step runs with
// while `mount_agent_config` is on (task 062.2 decision 3). An image's HOME is
// usually `/` under `--user uid` on Linux, where `~/.claude` finds nothing;
// this is a writable tmpfs with the host's agent configuration directories
// bind-mounted beneath it.
const HomeDir = "/vincent-home"

// LabelTask names the task a container belongs to. Recovery matches on it
// before removing anything: a container id journaled by a step run is only
// killed when the container still claims the task that journaled it (§12.4 —
// what cannot be proved is not killed).
const LabelTask = "com.vincent.task"

// ErrUnavailable is returned by Available when the runtime binary is missing
// or cannot talk to a daemon. It is the creation-time gate's error and the
// `container_unavailable` block's cause.
var ErrUnavailable = errors.New("container runtime unavailable")

// ErrImageUnavailable is returned by EnsureImage when the image is missing
// locally and cannot be pulled. It becomes the `container_image_unavailable`
// admission block — before a worktree, a branch or a retry is spent.
var ErrImageUnavailable = errors.New("container image unavailable")

// Mount is one bind mount. Source and Target are absolute paths; under task
// 061 decision 2 they are equal for the repository and the worktree.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// CreateSpec describes the container a task runs in.
type CreateSpec struct {
	Image string
	Name  string
	// Labels are applied at creation; LabelTask is always among them.
	Labels map[string]string
	Mounts []Mount
	// Network false drops the container off the network entirely. Decision 1
	// makes that a contradiction with `mcp.wire_steps: true` for a workflow
	// with an agent step, which task creation refuses (062.2 decision 5).
	Network bool
	// AddHostGateway maps host.docker.internal to the host, which is how a
	// containerized agent step reaches the daemon's per-step MCP endpoint
	// (decision 1, 062.2 decision 1).
	AddHostGateway bool
	// Home mounts a writable tmpfs at HomeDir (062.2 decision 3). Mounts
	// targeting paths beneath it land on top of it.
	Home bool
	// User is passed as `--user`; empty means the image's own user. It is set
	// on a Linux host so files land owned by the invoking user (decision 5),
	// and left empty on macOS where Docker Desktop maps ownership itself.
	User string
}

// ExecSpec is one step process to run inside an existing container.
type ExecSpec struct {
	// Key names the pid file under ScratchDir. It is the step run's id, so a
	// parallel group's sub-steps executing into the one container do not
	// share a file (§7.5).
	Key string
	// Argv is the command exactly as it would have run on the host — the
	// resolved shell and its flags, or an agent CLI and its flags. The
	// runtime wraps it; it never rewrites it.
	Argv []string
	// Env are `--env` values layered on top of the image's own environment.
	// A containerized step's base is the image's, never the daemon's
	// (decision 7). An entry is `K=V`, or a bare `K` whose value the runtime
	// client reads from its own environment — which is how an agent step's
	// secrets stay off the host argv (SplitEnv).
	Env     []string
	WorkDir string
	User    string
}

// Runtime is one container CLI. Implementations shell out; nothing here holds
// a daemon connection, which is what lets a config reload change the runtime
// binary between tasks.
type Runtime interface {
	// Name is the binary the runtime drives, for logs and `vincent doctor`.
	Name() string
	// Available reports whether the runtime can be used at all. It is the
	// cheap, local half of the creation gate (decision 3).
	Available(ctx context.Context) error
	// EnsureImage makes the image present locally, pulling it if needed. It
	// is the expensive half, and runs at admission.
	EnsureImage(ctx context.Context, image string) error
	// Create starts a container that stays up until Remove, and returns its
	// id.
	Create(ctx context.Context, spec CreateSpec) (string, error)
	// Exec returns the **host** argv that runs one step inside the container.
	// The caller spawns it itself, so the streaming, transcript and §17
	// parsing paths are the same code they are for a host step — which is
	// what makes "identical to a host run" testable rather than asserted.
	Exec(id string, spec ExecSpec) []string
	// ExecDirect is Exec without the pid-file wrapper: the argv runs as the
	// exec's own process. It is for short probes — resolving an agent CLI on
	// the image's PATH, its `--version` — that nothing ever signals (task
	// 062.2 decision 2). Key is ignored.
	ExecDirect(id string, spec ExecSpec) []string
	// Gateway returns the gateway IP of the container's network, the address
	// the host answers on from inside it (062.2 decision 1). "" with no error
	// means the container has no network with a gateway.
	Gateway(ctx context.Context, id string) (string, error)
	// Signal delivers a signal to the process ExecSpec.Key names, from
	// inside the container. Killing the host-side client would leave that
	// process running (decision 9).
	Signal(ctx context.Context, id, key, signal string) error
	// Remove force-removes the container, which kills every process in it.
	// Reserved for whole-task teardown and recovery.
	Remove(ctx context.Context, id string) error
	// Lookup returns the id of a **running** container by name, or "" when
	// there is none. It is what makes container creation idempotent across a
	// daemon restart: the name is derived from the task id, so a task that
	// comes back finds the container it left.
	Lookup(ctx context.Context, name string) (string, error)
	// TaskLabel reads LabelTask off an existing container. A container that
	// is gone reports "" with no error: recovery treats "already gone" as
	// success, and "not this task's" as leave-it-alone.
	TaskLabel(ctx context.Context, id string) (string, error)
}

// New returns the Runtime for a configured `container.runtime` value. Every
// supported value today is docker-CLI-compatible, so the name selects the
// binary rather than an implementation.
func New(binary string) Runtime {
	if strings.TrimSpace(binary) == "" {
		binary = "docker"
	}
	return &dockerRuntime{bin: binary}
}

// clientEnvNames are the variables the runtime client resolves its own daemon
// connection and configuration from. They keep the daemon's values on the host
// side, so a step's value for one of them is passed literally instead.
func clientEnvName(name string) bool {
	return name == "HOME" || name == "PATH" || strings.HasPrefix(name, "DOCKER_")
}

// SplitEnv turns a step's `K=V` environment into the two halves a
// containerized agent exec needs (task 062.2): flags for ExecSpec.Env and
// values for the runtime client's own environment. Every variable goes in by
// name, its value only in the client's environment, so a secret — codex's MCP
// token (task 057 decision 8), an API key the policy passes — never appears on
// the host argv. The client's resolution variables (HOME, PATH, DOCKER_*) are
// the exception: their host values are what lets the client find its daemon,
// so the step's values go in literally. Duplicates keep the last value, the
// way exec does.
func SplitEnv(env []string) (flags, client []string) {
	last := make(map[string]string, len(env))
	var order []string
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if _, seen := last[k]; !seen {
			order = append(order, k)
		}
		last[k] = v
	}
	for _, k := range order {
		if clientEnvName(k) {
			flags = append(flags, k+"="+last[k])
			continue
		}
		flags = append(flags, k)
		client = append(client, k+"="+last[k])
	}
	return flags, client
}

// Name builds the container name for a task. It is derived rather than stored
// so a daemon that lost its journal can still find what it left behind.
func Name(taskID int64) string { return fmt.Sprintf("vincent-task-%d", taskID) }
