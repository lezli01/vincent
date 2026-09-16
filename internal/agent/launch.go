package agent

import (
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/lezli01/vincent/internal/procx"
)

// Command is one agent run's process, described rather than started (spec
// §9.1, task 062.1). An adapter builds it — the resolved binary, its argv, the
// working directory, the environment — and hands it to a Launcher, which is
// the only thing that turns it into a process.
//
// It exists so where a run executes is the caller's choice and not the
// adapter's. Today every run executes on the host; task 062.2 adds a launcher
// that runs the same Command inside the task's container, and a field that
// launcher needs goes here rather than into three adapters' call shapes.
type Command struct {
	// Path is the resolved binary: the configured path, or the one PATH
	// resolution found on the host.
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
type Launcher interface {
	Launch(cmd Command) (Process, error)
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

// HostLauncher runs a Command as a process tree on this machine: a process
// group on POSIX, a Job object with CREATE_NO_WINDOW on Windows (procx). It
// is exactly the spawn the three adapters each performed before the seam
// existed, and it is the only launcher that ships until task 062.2.
type HostLauncher struct{}

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
