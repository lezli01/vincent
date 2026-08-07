//go:build !windows

package daemon

import "syscall"

// detachSysProcAttr puts the child in its own session so it survives the CLI
// process exiting and is not signaled with the parent's terminal group.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
