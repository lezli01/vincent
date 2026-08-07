// Package procx starts subprocesses so their whole process tree can be
// killed reliably: a process group on POSIX, a Job object with
// kill-on-close on Windows (T1.7 decision). Agent CLIs spawn children
// (shells, tools); killing only the direct child would leak orphans that
// keep running — and, for agents, keep spending money.
package procx

import (
	"os/exec"
	"sync"
)

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

// Release frees platform resources (Job object handles) once the process has
// been waited for. Safe to call multiple times.
func (p *Proc) Release() {
	p.platform.release()
}

// platformProc is the per-OS tree-kill mechanism.
type platformProc interface {
	kill() error
	release()
}

// directKill is the fallback when tree containment could not be set up.
type directKill struct{ cmd *exec.Cmd }

func (d directKill) kill() error {
	if d.cmd.Process == nil {
		return nil
	}
	return d.cmd.Process.Kill()
}

func (directKill) release() {}
