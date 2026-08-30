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
	// Network false drops the container off the network entirely. It is a
	// contradiction with `mcp.wire_steps: true` and refused at task creation
	// (decision 1) rather than producing an agent wired to a dead endpoint.
	Network bool
	// AddHostGateway maps host.docker.internal to the host, which is how a
	// containerized agent step reaches the daemon's per-step MCP endpoint
	// (decision 1).
	AddHostGateway bool
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
	// Env are `K=V` strings layered on top of the image's own environment.
	// A containerized step's base is the image's, never the daemon's
	// (decision 7).
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

// Name builds the container name for a task. It is derived rather than stored
// so a daemon that lost its journal can still find what it left behind.
func Name(taskID int64) string { return fmt.Sprintf("vincent-task-%d", taskID) }
