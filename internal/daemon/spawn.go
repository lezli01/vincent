package daemon

import (
	"fmt"
	"os"
	"os/exec"
)

// StartDetached spawns `vincent daemon` as a detached background process
// (phase 1 decision: self-exec with platform SysProcAttr) and returns its
// pid. Stdio is discarded; the child logs to the rotating daemon log. The
// caller polls daemon.json + /v1/health to confirm startup.
func StartDetached() (pid int, err error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("resolve own executable: %w", err)
	}
	// G204: exe is os.Executable() — this binary re-executing itself with a
	// fixed argument. No shell is involved.
	cmd := exec.Command(exe, "daemon") //nolint:gosec // G204: see above
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("spawn detached daemon: %w", err)
	}
	pid = cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return 0, fmt.Errorf("release detached daemon: %w", err)
	}
	return pid, nil
}
