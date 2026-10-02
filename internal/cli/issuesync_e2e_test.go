package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github/githubtest"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestIssueSyncSwitchesOff drives `vincent issue sync` through the real
// binary against a real daemon with each of the two switches off (task
// 130.8): the status names the switch, and with the integration disabled
// nothing — neither the sync request nor the status read — calls GitHub.
func TestIssueSyncSwitchesOff(t *testing.T) {
	ghDir := filepath.Dir(githubtest.BuildFakeGH(t))
	argvLog := filepath.Join(t.TempDir(), "gh-argv.log")
	t.Setenv("FAKEGH_ARGV_FILE", argvLog)
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(os.Environ(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})
	pointAgentsAtNothing(t, cfgDir)
	f, err := os.OpenFile(filepath.Join(cfgDir, config.FileName), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	_, err = f.WriteString("github:\n  enabled: false\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("write config: %v", err)
	}
	run := func(args ...string) (string, int) {
		return runVincentGH(t, dataDir, cfgDir, ghDir, "success", args...)
	}
	if out, code := run("daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}
	repo := testrepo.Init(t, "main")
	gitRemote(t, repo, "https://github.com/octo/repo.git")
	if out, code := run("project", "add", repo, "--json"); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}

	out, code := run("issue", "sync", "--project", "1")
	if code != 0 {
		t.Fatalf("issue sync: code %d, out %q", code, out)
	}
	for _, want := range []string{"enabled", "octo/repo", "last synced", "never", "github_disabled", "import complete", "import is off"} {
		if !strings.Contains(out, want) {
			t.Errorf("issue sync does not show %q:\n%s", want, out)
		}
	}

	status := func() map[string]any {
		t.Helper()
		out, code := run("issue", "sync", "--project", "1", "--status", "--json")
		if code != 0 {
			t.Fatalf("issue sync --status --json: code %d, out %q", code, out)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("issue sync --json is not JSON: %v (%q)", err, out)
		}
		return got
	}
	got := status()
	if got["enabled"] != false || got["ok"] != false || got["reason"] != "github_disabled" || got["provider"] != "github" {
		t.Errorf("disabled status = %v", got)
	}
	if _, err := os.Stat(argvLog); !os.IsNotExist(err) {
		b, _ := os.ReadFile(argvLog)
		t.Errorf("a disabled integration called gh:\n%s", b)
	}

	// The other switch: on, but no background tick. Read only — whether a
	// request wakes an import with the tick off is the importer's call.
	for _, kv := range [][2]string{{"github.poll_interval", "0"}, {"github.enabled", "true"}} {
		if out, code := run("config", "set", kv[0], kv[1]); code != 0 {
			t.Fatalf("config set %s: code %d, out %q", kv[0], code, out)
		}
	}
	got = status()
	if got["enabled"] != false || got["ok"] != false || got["reason"] != "poll_disabled" {
		t.Errorf("poll-disabled status = %v", got)
	}

	if out, code := run("issue", "sync", "--project", "99", "--status"); code == 0 || !strings.Contains(out, "not found") {
		t.Errorf("unknown project: code %d, out %q", code, out)
	}
}
