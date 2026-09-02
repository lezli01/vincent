package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/testrepo"
)

// vincentBin is the real binary under test, built once in TestMain: the
// auto-start path must be proven against an actual daemon process.
var vincentBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "vincent-tui-e2e-")
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

// TestAutoStartRealDaemon is the T3.1 acceptance against a real daemon: no
// daemon → the shell starts one, connects, and re-renders on a state change
// made by an external API client.
func TestAutoStartRealDaemon(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	env := append(os.Environ(),
		config.EnvDataDir+"="+dataDir,
		config.EnvConfigDir+"="+cfgDir,
	)
	t.Cleanup(func() {
		// Never leak a daemon, even when an assertion fails.
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = env
		_, _ = cmd.CombinedOutput()
	})

	cn := connector{
		resolveDataDir: func() (string, error) { return dataDir, nil },
		readRuntime:    daemon.ReadRuntimeInfo,
		checkHealth:    daemon.CheckHealth,
		newClient:      defaultConnector().newClient,
		// The TUI's real startDetached self-execs os.Executable(), which in
		// tests is the test binary; substitute the built vincent. Detaching
		// is `vincent daemon start`'s concern, proven in the cli e2e.
		startDetached: func() (int, error) {
			cmd := exec.Command(vincentBin, "daemon")
			cmd.Env = env
			if err := cmd.Start(); err != nil {
				return 0, err
			}
			go func() { _ = cmd.Wait() }()
			return cmd.Process.Pid, nil
		},
		startTimeout: 30 * time.Second,
		pollInterval: 100 * time.Millisecond,
	}

	m := newRoot(testCtx(t), cn, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	msg := runCmd(t, m.Init(), 10*time.Second)
	if _, ok := msg.(probeFailedMsg); !ok {
		t.Fatalf("probe with no daemon = %T, want probeFailedMsg", msg)
	}
	_, cmd := m.Update(msg)
	if !strings.Contains(content(m), "starting daemon") {
		t.Errorf("starting view lacks 'starting daemon': %q", content(m))
	}

	msg = runCmd(t, cmd, 60*time.Second)
	conn, ok := msg.(connectedMsg)
	if !ok {
		t.Fatalf("auto-start = %#v, want connectedMsg", msg)
	}
	if !conn.autoStarted {
		t.Error("connectedMsg.autoStarted = false, want true")
	}
	_, cmd = m.Update(msg)

	// Wait for the subscription itself: a stream with no Last-Event-ID
	// starts live at the next committed event (§13.3), so registering the
	// project before it is established would lose the event.
	pmp := newPump(t, m, cmd)
	pmp.until(30*time.Second, "the event stream to go live", func() bool { return m.streamLive })

	// Externally-made change: a plain HTTP client registers a project while
	// the shell watches the event stream.
	ri, err := daemon.ReadRuntimeInfo(dataDir)
	if err != nil {
		t.Fatalf("ReadRuntimeInfo: %v", err)
	}
	token, err := daemon.ReadToken(dataDir)
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	repo := testrepo.Init(t, "main")
	body, _ := json.Marshal(map[string]string{"path": repo})
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/projects", ri.Port), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build register request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	respBody := new(bytes.Buffer)
	_, _ = respBody.ReadFrom(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register project: status %s: %s", resp.Status, respBody)
	}
	var project struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(respBody.Bytes(), &project); err != nil || project.ID == 0 {
		t.Fatalf("register project: no id in %s", respBody)
	}

	// A second external change the screen can prove: a task created over
	// plain HTTP has to appear on the board without the TUI being told —
	// the event stream driving the refresh is the whole point (§13.3).
	body, _ = json.Marshal(map[string]any{
		"project_id": project.ID, "title": "e2e live proof",
		"description": "created behind the TUI's back",
	})
	req, err = http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/tasks", ri.Port), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build task request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task: status %s", resp.Status)
	}

	pmp.until(30*time.Second, "the externally created task to render", func() bool {
		return strings.Contains(content(m), "e2e live proof")
	})

	// Graceful stop through the real CLI; the daemon must not linger.
	stop := exec.Command(vincentBin, "daemon", "stop")
	stop.Env = env
	if out, err := stop.CombinedOutput(); err != nil || !strings.Contains(string(out), "daemon stopped") {
		t.Fatalf("daemon stop: %v\n%s", err, out)
	}
}

// §16's account of the status-line write rests on a fact nobody would notice
// breaking: the daemon never touches Claude Code's settings file. Only the
// TUI's `i` does, and only after showing what it would write. Booting a real
// daemon and looking is the only way to know that, so this asserts it rather
// than assuming it.
//
// HOME and USERPROFILE point at a temp dir — os.UserHomeDir reads the first on
// POSIX and the second on Windows — so the test never reads or writes the
// developer's real ~/.claude either.
func TestDaemonNeverTouchesClaudeSettings(t *testing.T) {
	dataDir, cfgDir, home := t.TempDir(), t.TempDir(), t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	settings := filepath.Join(claudeDir, "settings.json")
	const body = `{"model":"opus","statusLine":{"type":"command","command":"~/bin/mine.sh"}}`
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	env := append(os.Environ(),
		config.EnvDataDir+"="+dataDir,
		config.EnvConfigDir+"="+cfgDir,
		"HOME="+home,
		"USERPROFILE="+home,
	)
	start := exec.Command(vincentBin, "daemon")
	start.Env = env
	if err := start.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	go func() { _ = start.Wait() }()
	t.Cleanup(func() {
		stop := exec.Command(vincentBin, "daemon", "stop", "--force")
		stop.Env = env
		_, _ = stop.CombinedOutput()
	})

	deadline := time.Now().Add(60 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("the daemon never became healthy")
		}
		ri, err := daemon.ReadRuntimeInfo(dataDir)
		if err == nil {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			_, err = daemon.CheckHealth(ctx, ri.Port)
			cancel()
			if err == nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	stop := exec.Command(vincentBin, "daemon", "stop")
	stop.Env = env
	if out, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("daemon stop: %v\n%s", err, out)
	}

	got, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read settings back: %v", err)
	}
	if string(got) != body {
		t.Errorf("a daemon run rewrote claude's settings:\n got %s\nwant %s", got, body)
	}
	entries, err := os.ReadDir(claudeDir)
	if err != nil {
		t.Fatalf("read %s: %v", claudeDir, err)
	}
	if len(entries) != 1 {
		t.Errorf("a daemon run left %d files in ~/.claude, want only the settings", len(entries))
	}
}
