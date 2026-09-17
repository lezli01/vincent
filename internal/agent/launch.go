package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/lezli01/vincent/internal/procx"
)

// Command is one agent run's process, described rather than started (spec
// §9.1, task 062.1). An adapter builds it — the resolved binary, its argv, the
// working directory, the environment — and hands it to a Launcher, which is
// the only thing that turns it into a process.
//
// It exists so where a run executes is the caller's choice and not the
// adapter's. A host run executes on this machine; task 062.2's container
// launcher runs the same Command inside the task's container, and a field that
// launcher needs goes here rather than into three adapters' call shapes.
type Command struct {
	// Path is the resolved binary, as the same Launcher's Resolve found it:
	// the configured path or a host PATH hit for the host launcher, the
	// image's PATH hit for a container one.
	Path string
	// Args are the arguments after Path, exactly as the adapter built them.
	Args []string
	// Dir is the working directory — the task worktree.
	Dir string
	// Env is the child's environment. nil inherits the daemon's, the same
	// convention RunSpec.Env carries.
	Env []string
	// Stdin is what the child reads on its standard input. It is ignored when
	// StdinPipe is set.
	Stdin io.Reader
	// StdinPipe asks for a stdin the caller keeps writing to after launch, and
	// closes when it is done (claude's input mode, §7.4). Process.Stdin
	// returns it.
	StdinPipe bool
	// Stderr receives the child's standard error — an adapter's tail buffer.
	Stderr io.Writer
}

// Launcher starts a Command (spec §9.1). An adapter never spawns its run
// itself: it calls Launch, and everything it does to the process afterwards —
// reading, answering, stopping, waiting — goes through the returned Process.
//
// It also answers the two questions a run asks about its binary *before* it
// starts — where is it, and what does it say to a short probe — because those
// answers belong to wherever the run will execute (task 062.2 decision 2). A
// containerized run is judged against the image's CLI, never the host's. The
// catalog, Detect and Options keep describing the host (062.1 decision 2), so
// only an adapter's Start goes through these hooks.
type Launcher interface {
	Launch(cmd Command) (Process, error)
	// Resolve returns the binary a run executes. adapter names the adapter
	// for messages ("cursor"), configured is its `agents.*.path` ("" when
	// unset) and binary its bare binary name ("cursor-agent"). A launcher
	// that runs somewhere the configured host path means nothing ignores it.
	Resolve(adapter, configured, binary string) (string, error)
	// Probe runs one short-lived probe of a resolved binary, with Probe's
	// contract: stdout and stderr apart, a timeout that says it was one.
	Probe(ctx context.Context, timeout time.Duration, path string, args ...string) (stdout, stderr []byte, err error)
}

// Process is a launched Command. Its stop and identity primitives are the
// ones RunHandle exposes, which is the point of the seam: a launcher that runs
// the child somewhere other than a host process tree answers Kill, Terminate
// and PID for where it actually is (task 062.1 decision 1).
type Process interface {
	// Stdout is the child's standard output. It must be read to its end
	// before Wait, which may close it.
	Stdout() io.Reader
	// Stdin is the retained input pipe when Command.StdinPipe was set, and
	// nil otherwise.
	Stdin() io.WriteCloser
	// Wait blocks until the child exits and returns its exit code. The error
	// is waiting itself failing, never a non-zero exit: that is the code.
	Wait() (exitCode int, err error)
	// Terminate asks the child's tree to exit, the graceful half of §6's
	// cancel.
	Terminate() error
	// Kill terminates the child's whole tree. It is idempotent.
	Kill() error
	// PID is the OS process id journaled for §12.4 recovery.
	PID() int
	// Argv is the command line actually spawned, argv[0] included.
	Argv() []string
	// Release frees the platform handle holding the tree (a Job object on
	// Windows) once the child has been waited for. Safe to call twice.
	Release()
}

// Launch starts cmd through l, or through the host launcher when l is nil —
// the nil-means-host rule RunSpec.Launcher documents, stated once so the three
// adapters cannot disagree about it.
func Launch(l Launcher, cmd Command) (Process, error) {
	if l == nil {
		l = HostLauncher{}
	}
	return l.Launch(cmd)
}

// InputProber is an adapter that can judge its own §7.4 input support for a
// run through a given Launcher (task 062.2 decision 2). A containerized step's
// `require` pre-flight asks it, because the catalog's verdict describes the
// host's CLI and the run will execute the image's. Only an adapter whose
// verdict depends on its installed version needs it.
type InputProber interface {
	InputVerdictWith(ctx context.Context, l Launcher) InputVerdict
}

// ResolveWith resolves an adapter's binary through l, or through the host launcher
// when l is nil — Launch's nil-means-host rule, for the pre-start half.
func ResolveWith(l Launcher, adapter, configured, binary string) (string, error) {
	if l == nil {
		l = HostLauncher{}
	}
	return l.Resolve(adapter, configured, binary)
}

// ProbeWith probes a resolved binary through l, or on the host when l is nil.
func ProbeWith(
	ctx context.Context, l Launcher, timeout time.Duration, path string, args ...string,
) (stdout, stderr []byte, err error) {
	if l == nil {
		l = HostLauncher{}
	}
	return l.Probe(ctx, timeout, path, args...)
}

// HostLauncher runs a Command as a process tree on this machine: a process
// group on POSIX, a Job object with CREATE_NO_WINDOW on Windows (procx). It
// is exactly the spawn the three adapters each performed before the seam
// existed. Every run outside a containerized task, and every chat, uses it.
type HostLauncher struct{}

// Resolve implements Launcher: the configured path when set, otherwise the
// bare binary name from the host's PATH — the resolution each adapter carried
// before task 062.2 moved it behind the seam, messages included.
func (HostLauncher) Resolve(adapter, configured, binary string) (string, error) {
	if configured != "" {
		if _, err := exec.LookPath(configured); err != nil {
			return "", fmt.Errorf("configured %s path %s: %w", adapter, configured, err)
		}
		return configured, nil
	}
	p, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH: %w", binary, err)
	}
	return p, nil
}

// Probe implements Launcher with the package's Probe.
func (HostLauncher) Probe(
	ctx context.Context, timeout time.Duration, path string, args ...string,
) (stdout, stderr []byte, err error) {
	return Probe(ctx, timeout, path, args...)
}

// Launch implements Launcher.
func (HostLauncher) Launch(c Command) (Process, error) {
	//nolint:gosec // Path comes from config or PATH resolution by design.
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	if c.Env != nil {
		cmd.Env = c.Env
	}
	p := &hostProcess{cmd: cmd}
	if c.StdinPipe {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("stdin pipe: %w", err)
		}
		p.stdin = stdin
	} else {
		cmd.Stdin = c.Stdin
	}
	cmd.Stderr = c.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	p.stdout = stdout
	proc, err := procx.Start(cmd)
	if err != nil {
		return nil, err
	}
	p.proc = proc
	return p, nil
}

// hostProcess is a HostLauncher child.
type hostProcess struct {
	cmd    *exec.Cmd
	proc   *procx.Proc
	stdout io.Reader
	stdin  io.WriteCloser
}

func (p *hostProcess) Stdout() io.Reader     { return p.stdout }
func (p *hostProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *hostProcess) Terminate() error      { return p.proc.Terminate() }
func (p *hostProcess) Kill() error           { return p.proc.Kill() }
func (p *hostProcess) PID() int              { return p.cmd.Process.Pid }
func (p *hostProcess) Argv() []string        { return p.cmd.Args }
func (p *hostProcess) Release()              { p.proc.Release() }

func (p *hostProcess) Wait() (int, error) {
	err := p.cmd.Wait()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return p.cmd.ProcessState.ExitCode(), err
	}
	return p.cmd.ProcessState.ExitCode(), nil
}
