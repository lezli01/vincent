package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
)

// vincentBin is the real binary under test, built once in TestMain. The
// detached self-exec in `daemon start` (spec §12.1) can only be exercised
// through the actual executable, not in-process.
var vincentBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "vincent-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		os.Exit(1)
	}
	vincentBin = filepath.Join(tmp, "vincent")
	if runtime.GOOS == "windows" {
		vincentBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", vincentBin, "github.com/lezli01/vincent/cmd/vincent")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: build vincent: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// hermeticEnv is the test process's environment with every VINCENT_* variable
// removed.
//
// This suite is itself run from inside a vincent step — that is what the
// repository's own workflows do, and what CI's gates do — and a step's
// environment carries §8.5's VINCENT_* block. Inheriting it makes a child
// `vincent` believe it belongs to the *outer* task: `TestStatusOutsideAStep`
// saw a real VINCENT_TASK_ID and reported a daemon problem instead of the
// missing-variable error it asserts. The two dirs the callers pin are appended
// after this, so nothing a test relies on is lost.
func hermeticEnv() []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "VINCENT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// runVincent runs the built binary with isolated config/data dirs and
// returns its combined output and exit code.
func runVincent(t *testing.T, dataDir, cfgDir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(vincentBin, args...)
	cmd.Env = append(hermeticEnv(),
		config.EnvDataDir+"="+dataDir,
		config.EnvConfigDir+"="+cfgDir,
	)
	out, err := cmd.CombinedOutput()
	code := cmd.ProcessState.ExitCode()
	if err != nil && code < 0 {
		t.Fatalf("vincent %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), code
}

// TestDaemonStartStatusStopCycle is the T1.3 acceptance: the full lifecycle
// through the real binary, including detached start, idempotent start/stop,
// status exit codes, and token/daemon.json creation.
func TestDaemonStartStatusStopCycle(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		// Never leak a detached daemon, even when an assertion fails.
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(hermeticEnv(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})

	out, code := runVincent(t, dataDir, cfgDir, "daemon", "status")
	if code != 1 || !strings.Contains(out, "not running") {
		t.Fatalf("status before start: code %d, out %q; want 1, 'not running'", code, out)
	}

	out, code = runVincent(t, dataDir, cfgDir, "daemon", "stop")
	if code != 0 || !strings.Contains(out, "not running") {
		t.Fatalf("stop before start: code %d, out %q; want 0, 'not running'", code, out)
	}

	out, code = runVincent(t, dataDir, cfgDir, "daemon", "start")
	if code != 0 || !strings.Contains(out, "daemon started") {
		t.Fatalf("start: code %d, out %q; want 0, 'daemon started'", code, out)
	}

	out, code = runVincent(t, dataDir, cfgDir, "daemon", "status")
	if code != 0 || !strings.Contains(out, "daemon is running") {
		t.Fatalf("status while running: code %d, out %q; want 0, 'daemon is running'", code, out)
	}

	out, code = runVincent(t, dataDir, cfgDir, "daemon", "start")
	if code != 0 || !strings.Contains(out, "already running") {
		t.Fatalf("second start: code %d, out %q; want 0, 'already running'", code, out)
	}

	// Token and daemon.json exist with the promised properties (§12.2, §13.1).
	fi, err := os.Stat(daemon.TokenPath(dataDir))
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("token permissions = %o, want 600", perm)
		}
	}
	raw, err := os.ReadFile(daemon.RuntimePath(dataDir))
	if err != nil {
		t.Fatalf("daemon.json: %v", err)
	}
	var ri daemon.RuntimeInfo
	if err := json.Unmarshal(raw, &ri); err != nil {
		t.Fatalf("daemon.json parse: %v (%s)", err, raw)
	}
	if ri.Port <= 0 || ri.PID <= 0 || ri.StartedAt.IsZero() {
		t.Errorf("daemon.json = %+v; want positive port/pid and a start time", ri)
	}
	// A generated default config.yaml appears on first start (§12.3).
	if _, err := os.Stat(filepath.Join(cfgDir, config.FileName)); err != nil {
		t.Errorf("default config.yaml not generated: %v", err)
	}

	out, code = runVincent(t, dataDir, cfgDir, "daemon", "stop")
	if code != 0 || !strings.Contains(out, "daemon stopped") {
		t.Fatalf("stop: code %d, out %q; want 0, 'daemon stopped'", code, out)
	}

	out, code = runVincent(t, dataDir, cfgDir, "daemon", "status")
	if code != 1 || !strings.Contains(out, "not running") {
		t.Fatalf("status after stop: code %d, out %q; want 1, 'not running'", code, out)
	}
	if strings.Contains(out, "stale") {
		t.Errorf("graceful stop left a stale daemon.json: %q", out)
	}

	out, code = runVincent(t, dataDir, cfgDir, "daemon", "stop")
	if code != 0 || !strings.Contains(out, "not running") {
		t.Fatalf("second stop: code %d, out %q; want 0, 'not running'", code, out)
	}
}
