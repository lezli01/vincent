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
	assertNoWindow(t, cmd)
}

// TestNoWindowIsSetForProbes pins the same guarantee on the exported entry
// point the agent probes use (T4.21). They spawn without a Job object, so
// nothing else in their path would set the flag — and once the daemon has
// released its own console, a probe without it is given a console, i.e. a
// window, of its own.
func TestNoWindowIsSetForProbes(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit 0")
	NoWindow(cmd)
	assertNoWindow(t, cmd)
}

func assertNoWindow(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
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
