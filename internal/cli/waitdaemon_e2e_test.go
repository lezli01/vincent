package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
)

// waitHelperEnv gates the child half of the test below. It is deliberately
// not VINCENT_-prefixed: hermeticEnv strips that whole block (§8.5) from
// every child this suite spawns, and the daemon the child starts must stay
// hermetic while the flag itself survives.
const waitHelperEnv = "E2E_WAIT_DAEMON_HELPER"

// TestWaitDaemonAPIReportsWhyTheDaemonNeverCameUp is issue #340's regression
// guard. waitDaemonAPI is the shared startup wait behind TestCrashRecoveryE2E
// and TestBackupRestoreE2E; when its budget expires it reports only "daemon
// did not become healthy within 30s" — no daemon log, no exit status — so a
// timeout on CI is indistinguishable between "too slow under load" and "the
// daemon died at startup". `daemon start` already solves this the right way
// (internal/cli/daemon.go: the error carries daemon.LogTail(dirs.Data, 20));
// the test helper diverged from it.
//
// The failure path calls t.Fatal, so it can only be observed from outside the
// test that runs it: this re-execs the test binary against the child half and
// reads its output. The child points the daemon at an unparseable config, so
// the daemon logs "startup failed: invalid config" and exits within
// milliseconds — a cause that is sitting in {data_dir}/logs/daemon.log the
// whole time waitDaemonAPI is polling, and that the timeout throws away.
func TestWaitDaemonAPIReportsWhyTheDaemonNeverCameUp(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestWaitDaemonAPIOnADoomedDaemon$", "-test.v")
	cmd.Env = append(hermeticEnv(), waitHelperEnv+"=1")

	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("child test passed; it must fail, the daemon never serves\n%s", out)
	}
	if got := string(out); !strings.Contains(got, "startup failed: invalid config") {
		t.Errorf("waitDaemonAPI gave up after %s naming no cause; the daemon log said why "+
			"and the failure did not carry it:\n%s", elapsed, got)
	}
}

// TestWaitDaemonAPIOnADoomedDaemon is the child half: it starts a daemon that
// cannot come up and lets waitDaemonAPI fail. It always fails, which is why it
// skips unless its parent asked for it.
func TestWaitDaemonAPIOnADoomedDaemon(t *testing.T) {
	if os.Getenv(waitHelperEnv) != "1" {
		t.Skip("child half of TestWaitDaemonAPIReportsWhyTheDaemonNeverCameUp")
	}
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	// Unparseable YAML. The daemon builds its logger before it loads config
	// (internal/daemon/daemon.go), so the reason reaches daemon.log and the
	// process exits non-zero straight away.
	bad := "listen: \"127.0.0.1:0\"\nagents: [unclosed\n"
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(bad), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	p := startDaemonProcess(t, dataDir, cfgDir, "success")
	waitDaemonAPI(t, dataDir, p)
}
