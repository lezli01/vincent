//go:build windows

package daemon

import "syscall"

// detachedProcess starts the child without a console; syscall exports
// CREATE_NEW_PROCESS_GROUP but not this flag.
const detachedProcess = 0x00000008

// detachSysProcAttr detaches the child from the parent's console and control
// signals so it survives the CLI process and terminal closing.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}
