//go:build !windows

package procx

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setSysProcAttr places the child in its own process group so the whole
// group can be signaled at once.
func setSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func attach(cmd *exec.Cmd) (platformProc, error) {
	return groupKill{pgid: cmd.Process.Pid}, nil
}

// groupKill signals the process group created by Setpgid (pgid == child pid).
type groupKill struct{ pgid int }

func (g groupKill) terminate() error { return g.signal(syscall.SIGTERM) }

func (g groupKill) kill() error { return g.signal(syscall.SIGKILL) }

func (g groupKill) signal(sig syscall.Signal) error {
	err := syscall.Kill(-g.pgid, sig)
	// The group being gone already is success, not failure.
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (groupKill) release() {}

// signalProcess asks a single process to exit, for the fallback path where
// no process group was established.
func signalProcess(cmd *exec.Cmd) error {
	err := cmd.Process.Signal(syscall.SIGTERM)
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
