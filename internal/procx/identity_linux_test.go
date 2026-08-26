//go:build linux

package procx

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// The Linux token is `linux1:<boot id>:<start ticks>:<pid>`, and the boot id is
// what makes a reboot a guaranteed mismatch rather than an arithmetic
// coincidence (issue #149). Rebooting inside a test is not on offer, so the
// property is proved from the other side: within one boot every token carries
// the same, non-empty boot id, and it is the running kernel's — which is
// exactly the component that would differ after a restart.
func TestIdentityCarriesTheBootID(t *testing.T) {
	cmd, proc := startSleeper(t)
	defer func() {
		_ = proc.Kill()
		_ = cmd.Wait()
		proc.Release()
	}()

	sleeper, err := Identity(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Identity(sleeper): %v", err)
	}
	self, err := Identity(os.Getpid())
	if err != nil {
		t.Fatalf("Identity(self): %v", err)
	}

	sleeperParts := strings.Split(sleeper, ":")
	selfParts := strings.Split(self, ":")
	if len(sleeperParts) != 4 || len(selfParts) != 4 {
		t.Fatalf("tokens are not scheme:boot:ticks:pid — %q, %q", sleeper, self)
	}
	if sleeperParts[1] == "" {
		t.Fatal("boot id component is empty")
	}
	if sleeperParts[1] != selfParts[1] {
		t.Errorf("two processes of the same boot disagree on the boot id: %q vs %q",
			sleeperParts[1], selfParts[1])
	}
	want, err := bootID()
	if err != nil {
		t.Fatalf("bootID: %v", err)
	}
	if sleeperParts[1] != want {
		t.Errorf("boot id component = %q, want the running kernel's %q", sleeperParts[1], want)
	}
	// Ticks are the raw kernel field, so a later-started process has counted
	// more of them: the token is not a constant per boot.
	if sleeperParts[2] == "" {
		t.Error("start-ticks component is empty")
	}
}

// USER_HZ is 100, so the tick count is a 10 ms bucket rather than an instant:
// two processes started inside one bucket share it. The PID is what makes the
// token name a single process anyway, and only the pair of them does — which
// is why it is asserted here rather than left implied by
// TestIdentityDiffersBetweenLiveProcesses.
func TestIdentityEndsInThePID(t *testing.T) {
	cmd, proc := startSleeper(t)
	defer func() {
		_ = proc.Kill()
		_ = cmd.Wait()
		proc.Release()
	}()

	got, err := Identity(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	parts := strings.Split(got, ":")
	if len(parts) != 4 {
		t.Fatalf("token %q is not scheme:boot:ticks:pid", got)
	}
	if parts[3] != strconv.Itoa(cmd.Process.Pid) {
		t.Errorf("pid component = %q, want %d (token %q)", parts[3], cmd.Process.Pid, got)
	}
}
