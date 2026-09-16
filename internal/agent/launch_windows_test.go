package agent

import (
	"io"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// TestHostLauncherKeepsTheChildOffTheDesktop pins the one Windows-only
// attribute the adapters' own spawns carried (T3.8): a console-less daemon's
// agent run gets CREATE_NO_WINDOW, or every step opens a console window.
func TestHostLauncherKeepsTheChildOffTheDesktop(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	// This test binary with nothing to run: it starts, prints and exits.
	p, err := HostLauncher{}.Launch(Command{Path: exe, Args: []string{"-test.run=^$"}, Stderr: io.Discard})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	_, _ = io.Copy(io.Discard, p.Stdout())
	_, _ = p.Wait()
	p.Release()

	attr := p.(*hostProcess).cmd.SysProcAttr
	if attr == nil || attr.CreationFlags&windows.CREATE_NO_WINDOW == 0 || !attr.HideWindow {
		t.Errorf("SysProcAttr = %+v, want HideWindow and CREATE_NO_WINDOW", attr)
	}
}
