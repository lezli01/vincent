package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
)

// TestGCAgainstLiveDaemon drives `vincent gc` through the real binary against
// a real daemon — the whole command is a thin API client, so the only thing
// worth proving here is that the round trip and the rendering work end to end.
func TestGCAgainstLiveDaemon(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(os.Environ(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})
	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}

	// An orphan of the exact shape a failed project delete leaves: a
	// directory under the worktree root that no row can ever name again.
	orphan := filepath.Join(dataDir, "worktrees", "999999")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatalf("plant orphan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "payload"), []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	out, code := runVincent(t, dataDir, cfgDir, "gc", "--dry-run", "--force")
	if code != 0 {
		t.Fatalf("gc --dry-run: code %d, out %q", code, out)
	}
	if !strings.Contains(out, orphan) || !strings.Contains(out, "dry run") {
		t.Errorf("gc --dry-run does not report the orphan:\n%s", out)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("gc --dry-run removed the orphan: %v", err)
	}

	// --force, because git cannot judge a worktree whose repo never existed
	// (dirty_unknown) — the common shape for a real orphan.
	out, code = runVincent(t, dataDir, cfgDir, "gc", "--force", "--json")
	if code != 0 {
		t.Fatalf("gc --force --json: code %d, out %q", code, out)
	}
	var rep struct {
		Reclaimed      int   `json:"reclaimed"`
		ReclaimedBytes int64 `json:"reclaimed_bytes"`
		Orphans        []struct {
			Path    string `json:"path"`
			Kind    string `json:"kind"`
			Removed bool   `json:"removed"`
		} `json:"orphans"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("gc --json is not JSON: %v (%q)", err, out)
	}
	if rep.Reclaimed != 1 || rep.ReclaimedBytes <= 0 {
		t.Errorf("reclaimed = %d / %d bytes, want 1 and a positive size",
			rep.Reclaimed, rep.ReclaimedBytes)
	}
	if len(rep.Orphans) != 1 || !rep.Orphans[0].Removed ||
		rep.Orphans[0].Kind != "worktree" || rep.Orphans[0].Path != orphan {
		t.Errorf("orphans = %+v, want the planted worktree removed", rep.Orphans)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Error("the orphan survived gc")
	}

	out, code = runVincent(t, dataDir, cfgDir, "gc")
	if code != 0 || !strings.Contains(out, "nothing to reclaim") {
		t.Errorf("gc on a clean daemon: code %d, out %q", code, out)
	}
}

// TestGCWithoutADaemon: gc behaves like every other thin client when nothing
// is listening — exit 2 and the same message, so a script can tell "start the
// daemon" from "fix your request" without parsing stderr (PR U decision).
func TestGCWithoutADaemon(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	out, code := runVincent(t, dataDir, cfgDir, "gc")
	if code != 2 {
		t.Fatalf("gc without a daemon: code %d, want 2 (out %q)", code, out)
	}
	if !strings.Contains(out, "no running daemon found") {
		t.Errorf("gc without a daemon: out %q, want the standard message", out)
	}
}
