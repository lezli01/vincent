package procx

import (
	"runtime"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/testutil/wait"
)

func TestExited(t *testing.T) {
	cmd, proc := startSleeper(t)
	waited := false
	defer func() {
		_ = proc.Kill()
		if !waited {
			_ = cmd.Wait()
		}
		proc.Release()
	}()
	pid := cmd.Process.Pid
	ident, err := Identity(pid)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}

	if gone, err := Exited(pid, ident); err != nil || gone {
		t.Fatalf("Exited(live) = %v, %v; want false, nil", gone, err)
	}
	// A token that is not this process's is the reused-PID case: the process
	// it named is gone, whatever now holds the PID.
	if gone, err := Exited(pid, ident+"-other"); err != nil || !gone {
		t.Fatalf("Exited(mismatched identity) = %v, %v; want true, nil", gone, err)
	}

	if err := proc.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// cmd still holds an unwaited handle (Windows) or the child is an
	// unreaped zombie (POSIX). Windows must already read it as exited — the
	// handle keeping its identity alive is exactly what Exited sees past.
	if runtime.GOOS == "windows" {
		if !wait.Poll(10*time.Second, 10*time.Millisecond, func() bool {
			gone, err := Exited(pid, ident)
			return err == nil && gone
		}) {
			t.Fatal("a killed process with an open handle never read as exited")
		}
	}
	_ = cmd.Wait()
	waited = true
	if gone, err := Exited(pid, ident); err != nil || !gone {
		t.Fatalf("Exited(reaped) = %v, %v; want true, nil", gone, err)
	}
}
