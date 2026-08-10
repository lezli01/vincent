//go:build windows

package gitx

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// TestHideConsoleSetsWindowFlags pins the T3.8 finding: a git child spawned
// by the console-less daemon must not open a console window of its own.
func TestHideConsoleSetsWindowFlags(t *testing.T) {
	cmd := exec.Command("git", "version")
	hideConsole(cmd)
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
