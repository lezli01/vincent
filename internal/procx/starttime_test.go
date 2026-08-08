package procx

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// startSleeper spawns a long-sleeping child through Start, the same way the
// engine spawns step processes.
func startSleeper(t *testing.T) (*exec.Cmd, *Proc) {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 300")
	} else {
		cmd = exec.Command("sleep", "300")
	}
	proc, err := Start(cmd)
	if err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	return cmd, proc
}

func TestStartTimeOfLiveProcess(t *testing.T) {
	cmd, proc := startSleeper(t)
	defer func() {
		_ = proc.Kill()
		_ = cmd.Wait()
		proc.Release()
	}()

	got, err := StartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("StartTime: %v", err)
	}
	diff := time.Since(got)
	if diff < 0 {
		diff = -diff
	}
	// The crash-recovery guard compares within ±5 s; the reader itself must
	// land well inside that for a process spawned moments ago.
	if diff > 5*time.Second {
		t.Errorf("start time %v is %v from spawn; want within the guard tolerance", got, diff)
	}
}

func TestStartTimeSelf(t *testing.T) {
	got, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime(self): %v", err)
	}
	if got.IsZero() || got.After(time.Now().Add(time.Minute)) {
		t.Errorf("start time of the test process = %v; want a sane past instant", got)
	}
}

func TestStartTimeGoneAfterExit(t *testing.T) {
	cmd, proc := startSleeper(t)
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_ = cmd.Wait()
	proc.Release()

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := StartTime(cmd.Process.Pid)
		if errors.Is(err, ErrProcessGone) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("StartTime after exit = %v, want ErrProcessGone", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestKillPIDReapsAndToleratesDead(t *testing.T) {
	cmd, proc := startSleeper(t)
	defer proc.Release()

	if err := KillPID(cmd.Process.Pid); err != nil {
		t.Fatalf("KillPID: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = proc.Kill()
		t.Fatal("process survived KillPID")
	}
	// A PID that is already gone is success, not failure (§12.4: the good
	// case for crash recovery).
	if err := KillPID(cmd.Process.Pid); err != nil {
		t.Errorf("KillPID on a dead pid: %v", err)
	}
}
