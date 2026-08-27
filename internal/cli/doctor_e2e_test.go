package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/doctor"
)

// TestDoctorWithoutADaemon is the property that separates doctor from every
// other data subcommand: the daemon being down is one of the answers, so the
// report is still printed in full and only the exit code says the request was
// never made (task 005 decision 7).
func TestDoctorWithoutADaemon(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	pointAgentsAtNothing(t, cfgDir)

	out, code := runVincent(t, dataDir, cfgDir, "doctor")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (no daemon answered)\n%s", code, out)
	}
	for _, want := range []string{"PATHS", "DAEMON", "LOG", "DATABASE", "AGENTS", "STORAGE", "TASKS"} {
		if !strings.Contains(out, want) {
			t.Errorf("the degraded report is missing the %s group:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not_running") {
		t.Errorf("the daemon row does not say it is down:\n%s", out)
	}
	// The rows only the daemon can serve say so rather than being read behind
	// its back: "only the daemon opens SQLite" is an ownership invariant.
	if !strings.Contains(out, "unknown — daemon not running") {
		t.Errorf("database/task rows do not report themselves unknown:\n%s", out)
	}
	if !strings.Contains(out, dataDir) || !strings.Contains(out, cfgDir) {
		t.Errorf("the report does not name the §12.2 directories:\n%s", out)
	}
}

// TestDoctorFixWithoutADaemonIsRefused: every repair is a write, and only the
// daemon writes.
func TestDoctorFixWithoutADaemonIsRefused(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	pointAgentsAtNothing(t, cfgDir)

	out, code := runVincent(t, dataDir, cfgDir, "doctor", "--fix")
	if code != 2 {
		t.Fatalf("exit = %d, want 2\n%s", code, out)
	}
	if !strings.Contains(out, "--fix needs a running daemon") {
		t.Errorf("no explanation of why the repair did not happen:\n%s", out)
	}
}

// TestDoctorUnparseableConfigExitsOne pins the one local finding that is a
// real problem: the daemon refuses to start on a config it cannot read, which
// is the "why is nothing running?" this command exists for.
func TestDoctorUnparseableConfigExitsOne(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName),
		[]byte("max_parallel_tasks: [not a number\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// A daemon is started first, so the exit code under test is 1 (problems
	// found) rather than 2 (no daemon) — exit 2 wins over any finding.
	t.Cleanup(func() { stopDaemon(t, dataDir, cfgDir) })
	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code == 0 {
		t.Fatalf("the daemon started on an unparseable config: %q", out)
	}

	out, code := runVincent(t, dataDir, cfgDir, "doctor")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 with no daemon\n%s", code, out)
	}
	if !strings.Contains(out, "DOES NOT PARSE") || !strings.Contains(out, "PROBLEMS") {
		t.Errorf("an unparseable config is not called out:\n%s", out)
	}
}

// TestDoctorAgainstALiveDaemon walks the healthy path end to end through the
// real binary: exit 0, every group served by the daemon, and --json that
// parses into the shared report type.
func TestDoctorAgainstALiveDaemon(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	pointAgentsAtNothing(t, cfgDir)
	t.Cleanup(func() { stopDaemon(t, dataDir, cfgDir) })
	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}

	out, code := runVincent(t, dataDir, cfgDir, "doctor")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 on a healthy daemon\n%s", code, out)
	}
	if !strings.Contains(out, "no problems found") {
		t.Errorf("healthy report does not say so:\n%s", out)
	}
	// Missing agent CLIs are reported and deliberately do not set the exit
	// code (decision 7) — the config above points every adapter at nothing.
	if !strings.Contains(out, "not found") {
		t.Errorf("the agents group does not report the missing adapters:\n%s", out)
	}

	out, code = runVincent(t, dataDir, cfgDir, "doctor", "--json")
	if code != 0 {
		t.Fatalf("--json exit = %d\n%s", code, out)
	}
	var rep doctor.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor --json is not the report shape: %v\n%s", err, out)
	}
	if rep.Daemon.Status != doctor.StatusRunning || rep.Daemon.PID == 0 {
		t.Errorf("daemon group = %+v", rep.Daemon)
	}
	if !rep.Database.Known || rep.Database.IntegrityCheck != "ok" {
		t.Errorf("database group = %+v", rep.Database)
	}
	if !rep.Tasks.Known {
		t.Error("task counts are unknown against a live daemon")
	}
	if !rep.Healthy() {
		t.Errorf("problems = %v", rep.Problems)
	}

	// --fix on a healthy installation has nothing to remove and compacts.
	out, code = runVincent(t, dataDir, cfgDir, "doctor", "--fix")
	if code != 0 {
		t.Fatalf("--fix exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, doctor.ActionCompactDatabase) {
		t.Errorf("--fix did not report the compaction:\n%s", out)
	}
}

// pointAgentsAtNothing keeps these tests independent of whichever agent CLIs
// happen to be installed on the machine running them.
func pointAgentsAtNothing(t *testing.T, cfgDir string) {
	t.Helper()
	body := "agents:\n" +
		"  claude:\n    path: \"/nonexistent/claude\"\n" +
		"  codex:\n    path: \"/nonexistent/codex\"\n" +
		"  cursor:\n    path: \"/nonexistent/cursor-agent\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func stopDaemon(t *testing.T, dataDir, cfgDir string) {
	t.Helper()
	cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
	cmd.Env = append(hermeticEnv(),
		config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
	_, _ = cmd.CombinedOutput()
}
