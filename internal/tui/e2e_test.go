package tui

import (
	"bytes"
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

	pmp.until(30*time.Second, "project.created to render", func() bool {
		return strings.Contains(content(m), "project.created")
	})

	// Graceful stop through the real CLI; the daemon must not linger.
	stop := exec.Command(vincentBin, "daemon", "stop")
	stop.Env = env
	if out, err := stop.CombinedOutput(); err != nil || !strings.Contains(string(out), "daemon stopped") {
		t.Fatalf("daemon stop: %v\n%s", err, out)
	}
}
