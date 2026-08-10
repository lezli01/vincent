//go:build windows

package procx

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// TestSetSysProcAttrHidesConsole pins the T3.8 finding: step subprocesses
// spawned by the console-less daemon must not flash console windows.
func TestSetSysProcAttrHidesConsole(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit 0")
	setSysProcAttr(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("no SysProcAttr set")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("HideWindow not set")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Error("CREATE_NO_WINDOW not set")
	}
}
