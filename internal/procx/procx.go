// Package procx starts subprocesses so their whole process tree can be
// killed reliably: a process group on POSIX, a Job object with
// kill-on-close on Windows (T1.7 decision). Agent CLIs spawn children
// (shells, tools); killing only the direct child would leak orphans that
// keep running — and, for agents, keep spending money.
package procx

import (
	"errors"
	"os/exec"
	"sync"
)

// ErrProcessGone reports that no process with the queried PID exists —
// for crash recovery, the good case: the orphan already exited.
var ErrProcessGone = errors.New("process not found")

// Proc is a started subprocess whose whole tree Kill terminates.
type Proc struct {
	cmd      *exec.Cmd
	killOnce sync.Once
	killErr  error
	platform platformProc
}

// Start configures cmd for tree-kill (SysProcAttr on POSIX, Job object
// assignment on Windows) and starts it. The caller keeps using cmd (Wait,
// pipes) as usual.
func Start(cmd *exec.Cmd) (*Proc, error) {
	setSysProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &Proc{cmd: cmd}
	pp, err := attach(cmd)
	if err != nil {
		// Tree containment failed (e.g. Job object creation); fall back to
		// best-effort direct kill rather than leaving the process unkillable.
		p.platform = directKill{cmd: cmd}
		return p, nil
	}
	p.platform = pp
	return p, nil
}

// Kill terminates the process tree. It is idempotent; later calls return the
// first outcome.
func (p *Proc) Kill() error {
	p.killOnce.Do(func() { p.killErr = p.platform.kill() })
	return p.killErr
}

// Terminate asks the process tree to exit, giving a well-behaved child the
// chance to clean up — the graceful half of spec §6's "graceful term, then
// kill after 10 s". The caller waits and calls Kill if the tree is still
// alive.
//
// On POSIX this signals the process group with SIGTERM. On Windows there is
// no graceful counterpart: a Job object offers only TerminateJobObject, and
// §6's own `taskkill /T /F` is that same abrupt termination, so Terminate
// and Kill do the same thing there. The grace period still elapses on both
// platforms, so behaviour differs only in how much warning a child gets.
func (p *Proc) Terminate() error { return p.platform.terminate() }

// Release frees platform resources (Job object handles) once the process has
// been waited for. Safe to call multiple times.
func (p *Proc) Release() {
	p.platform.release()
}

// platformProc is the per-OS tree-kill mechanism.
type platformProc interface {
	terminate() error
	kill() error
	release()
}

// directKill is the fallback when tree containment could not be set up.
type directKill struct{ cmd *exec.Cmd }

func (d directKill) terminate() error {
	if d.cmd.Process == nil {
		return nil
	}
	// Signal only the child: without tree containment there is no group to
	// signal, and Kill has the same reach.
	return signalProcess(d.cmd)
}

func (d directKill) kill() error {
	if d.cmd.Process == nil {
		return nil
	}
	return d.cmd.Process.Kill()
}

func (directKill) release() {}
