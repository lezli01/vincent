//go:build !windows

package procx

import (
	"errors"
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

func (g groupKill) kill() error {
	err := syscall.Kill(-g.pgid, syscall.SIGKILL)
	// The group being gone already is success, not failure.
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (groupKill) release() {}
