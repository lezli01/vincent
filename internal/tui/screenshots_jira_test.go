//go:build !windows

package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/jira/jiratest"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// shotsFuncs returns the named shell functions of scripts/screenshots.sh,
// verbatim, so a test runs the script's own code rather than a copy of it.
func shotsFuncs(t *testing.T, names ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "screenshots.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	var b strings.Builder
	for _, name := range names {
		start := strings.Index(script, "\n"+name+"() {")
		if start < 0 {
			t.Fatalf("no %s() in scripts/screenshots.sh", name)
		}
		end := strings.Index(script[start:], "\n}\n")
		if end < 0 {
			t.Fatalf("%s() in scripts/screenshots.sh never closes", name)
		}
		b.WriteString(script[start : start+end+3])
	}
	return b.String()
}

// runShots runs body under bash after the script's jira functions, with the
// environment they read. WaitDelay bounds how long a grandchild may hold
// stdout open after bash exits: ErrWaitDelay is that hold.
func runShots(t *testing.T, shots, fakejira, body string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH")
	}
	src := "set -euo pipefail\n" +
		"SHOTS=" + strconv.Quote(shots) + "\n" +
		"FAKEJIRA=" + strconv.Quote(fakejira) + "\n" +
		"JIRA_SHOTS_USER=u\nJIRA_SHOTS_TOKEN=t\n" +
		shotsFuncs(t, "fail", "jira_up", "jira_is_ours", "jira_down") + "\n" + body
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(60*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "-c", src)
	cmd.WaitDelay = wait.Timeout(5 * time.Second)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("bash: %v\n%s", err, stderr.String())
	}
	return string(out), err
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// TestScreenshotsJiraLifecycle holds jira_up and jira_down in
// scripts/screenshots.sh to the two properties review F1 and F2 on #951
// found missing: the fake left up by `seed` must not keep the seed's stdout
// open (F2), and jira_down must never signal a process the stale pid file
// merely names (F1).
func TestScreenshotsJiraLifecycle(t *testing.T) {
	for _, tool := range []string{"ps", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}
	fake := jiratest.BuildFakeJira(t)
	// A path with a space, as $SHOTS may be.
	shots := filepath.Join(t.TempDir(), "shots dir")
	if err := os.MkdirAll(shots, 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(shots, "fakejira.pid")
	readPid := func(t *testing.T) int {
		t.Helper()
		b, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			t.Fatal(err)
		}
		return pid
	}

	t.Run("the fake left up holds no pipe of the script's", func(t *testing.T) {
		out, err := runShots(t, shots, fake, `
"$FAKEJIRA" seed --corpus "$SHOTS/jira-corpus.json" >/dev/null
jira_up
echo "$JBASE"`)
		pid := readPid(t)
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		if errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("bash exited but stdout stayed open: the backgrounded fake (pid %d) inherited it", pid)
		}
		if !strings.HasPrefix(strings.TrimSpace(out), "http://127.0.0.1:") {
			t.Fatalf("jira_up printed %q, want the fake's base URL", out)
		}
		if !alive(pid) {
			t.Fatal("the fake did not outlive the script, as seed mode needs")
		}

		if _, err := runShots(t, shots, fake, "jira_down"); err != nil {
			t.Fatal(err)
		}
		wait.Until(t, "jira_down to stop the fake", func() bool { return !alive(pid) })
		if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
			t.Errorf("jira_down left the pid file: %v", err)
		}
	})

	t.Run("a stale pid file names someone else", func(t *testing.T) {
		other := exec.Command("sleep", "60")
		if err := other.Start(); err != nil {
			t.Fatal(err)
		}
		// Reaped here, so a SIGKILL shows as an exit rather than as a
		// zombie that kill -0 would still call alive.
		exited := make(chan struct{})
		go func() { _ = other.Wait(); close(exited) }()
		t.Cleanup(func() { _ = other.Process.Kill(); <-exited })
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(other.Process.Pid)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		begin := time.Now()
		if _, err := runShots(t, shots, fake, "jira_down"); err != nil {
			t.Fatal(err)
		}
		if took := time.Since(begin); took > wait.Timeout(5*time.Second) {
			t.Errorf("jira_down waited %s on a process that is not the fake", took)
		}
		select {
		case <-exited:
			t.Fatal("jira_down killed an unrelated process its stale pid file named")
		default:
		}
		if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
			t.Errorf("jira_down kept a stale pid file: %v", err)
		}
	})
}
