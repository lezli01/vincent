package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
)

func TestEnsureTokenCreatesAndReuses(t *testing.T) {
	dir := t.TempDir()
	tok, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	if len(tok) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(tok))
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(TokenPath(dir))
		if err != nil {
			t.Fatalf("stat token: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("token permissions = %o, want 600", perm)
		}
	} else if _, err := os.Stat(TokenPath(dir)); err != nil {
		t.Fatalf("token file missing: %v", err)
	}
	again, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("EnsureToken (second): %v", err)
	}
	if again != tok {
		t.Error("second EnsureToken generated a new token; want reuse")
	}
	read, err := ReadToken(dir)
	if err != nil || read != tok {
		t.Errorf("ReadToken = %q, %v; want the ensured token", read, err)
	}
}

func TestEnsureTokenRegeneratesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(TokenPath(dir), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64", len(tok))
	}
}

func TestRuntimeInfoRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadRuntimeInfo(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read missing daemon.json: err = %v, want ErrNotExist", err)
	}
	want := RuntimeInfo{Port: 4242, PID: 999, StartedAt: time.Now().UTC().Truncate(time.Second)}
	if err := WriteRuntimeInfo(dir, want); err != nil {
		t.Fatalf("WriteRuntimeInfo: %v", err)
	}
	got, err := ReadRuntimeInfo(dir)
	if err != nil {
		t.Fatalf("ReadRuntimeInfo: %v", err)
	}
	if got.Port != want.Port || got.PID != want.PID || !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("round-trip = %+v, want %+v", got, want)
	}
	if err := RemoveRuntimeInfo(dir); err != nil {
		t.Fatalf("RemoveRuntimeInfo: %v", err)
	}
	if err := RemoveRuntimeInfo(dir); err != nil {
		t.Errorf("RemoveRuntimeInfo (already gone): %v, want nil", err)
	}
}

func TestProbeRunningNoDaemon(t *testing.T) {
	running, err := ProbeRunning(t.TempDir())
	if err != nil || running {
		t.Errorf("ProbeRunning = %v, %v; want false, nil", running, err)
	}
	running, err = ProbeRunning(filepath.Join(t.TempDir(), "does", "not", "exist"))
	if err != nil || running {
		t.Errorf("ProbeRunning (missing dir) = %v, %v; want false, nil", running, err)
	}
}

func TestLogTail(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		b.WriteString("line ")
		b.WriteString(string(rune('0' + i%10)))
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "daemon.log"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tail := LogTail(dir, 5)
	if got := strings.Count(tail, "\n") + 1; got != 5 {
		t.Errorf("tail has %d lines, want 5", got)
	}
}

// startTestDaemon runs Run in-process against temp dirs and returns the
// discovered runtime info plus the Run result channel.
func startTestDaemon(ctx context.Context, t *testing.T) (dataDir string, ri RuntimeInfo, done <-chan error) {
	return startTestDaemonWithAgents(ctx, t, nil)
}

func startTestDaemonWithAgents(ctx context.Context, t *testing.T, agents *agent.Registry) (dataDir string, ri RuntimeInfo, done <-chan error) {
	t.Helper()
	dataDir = t.TempDir()
	t.Setenv(config.EnvDataDir, dataDir)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	ch := make(chan error, 1)
	go func() { ch <- runWithAgents(ctx, Options{}, agents) }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		if ri, err = ReadRuntimeInfo(dataDir); err == nil {
			if _, err := CheckHealth(ctx, ri.Port); err == nil {
				return dataDir, ri, ch
			}
		}
		select {
		case err := <-ch:
			t.Fatalf("daemon exited during startup: %v", err)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("daemon did not become healthy within 15s")
	return "", RuntimeInfo{}, nil
}

type blockingCatalogAdapter struct {
	started  chan struct{}
	finished chan struct{}
}

func (a *blockingCatalogAdapter) Name() string { return "blocking" }

func (a *blockingCatalogAdapter) Detect(ctx context.Context) (agent.Availability, error) {
	close(a.started)
	<-ctx.Done()
	return agent.Availability{Error: ctx.Err().Error()}, nil
}

func (a *blockingCatalogAdapter) Options(ctx context.Context) (agent.Options, error) {
	<-ctx.Done()
	// Model a canceled subprocess that takes a moment to be reaped. Run must
	// still join the prime before the logger and its file are closed.
	time.Sleep(250 * time.Millisecond)
	close(a.finished)
	return agent.Options{}, nil
}

func (a *blockingCatalogAdapter) Path() (string, error) {
	return "", errors.New("blocking adapter has no binary")
}

func (a *blockingCatalogAdapter) Curated() agent.Options { return agent.Options{} }

func (a *blockingCatalogAdapter) Start(context.Context, agent.RunSpec) (agent.RunHandle, error) {
	return nil, errors.New("blocking adapter cannot run")
}

func TestRunJoinsAgentCatalogPrimeBeforeReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adapter := &blockingCatalogAdapter{
		started:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	dataDir, ri, done := startTestDaemonWithAgents(ctx, t, agent.NewRegistry(adapter))

	select {
	case <-adapter.started:
	case <-time.After(5 * time.Second):
		t.Fatal("agent catalog prime did not start")
	}
	token, err := ReadToken(dataDir)
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if err := RequestStop(ctx, ri.Port, token); err != nil {
		t.Fatalf("RequestStop: %v", err)
	}
	if err := waitDone(t, done); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	select {
	case <-adapter.finished:
		// The prime stopped before Run returned, as required.
	default:
		cancel()
		select {
		case <-adapter.finished:
		case <-time.After(5 * time.Second):
			t.Fatal("agent catalog prime ignored context cancellation")
		}
		t.Fatal("Run returned before the agent catalog prime stopped")
	}
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not exit within 15s")
		return nil
	}
}

func TestRunLifecycleStopViaAPI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dataDir, ri, done := startTestDaemon(ctx, t)

	if ri.PID != os.Getpid() {
		t.Errorf("daemon.json pid = %d, want %d", ri.PID, os.Getpid())
	}
	if running, err := ProbeRunning(dataDir); err != nil || !running {
		t.Errorf("ProbeRunning = %v, %v; want true, nil", running, err)
	}
	token, err := ReadToken(dataDir)
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}

	// A second instance must refuse to start while the lock is held.
	if err := Run(ctx, Options{}); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Run: err = %v, want ErrAlreadyRunning", err)
	}

	// A stop request with a bad token must be rejected.
	if err := RequestStop(ctx, ri.Port, "wrong-token"); err == nil {
		t.Error("RequestStop with wrong token succeeded; want rejection")
	}

	if err := RequestStop(ctx, ri.Port, token); err != nil {
		t.Fatalf("RequestStop: %v", err)
	}
	if err := waitDone(t, done); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, err := ReadRuntimeInfo(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("daemon.json still present after graceful stop: %v", err)
	}
	if running, _ := ProbeRunning(dataDir); running {
		t.Error("lock still held after graceful stop")
	}
}

func TestRunContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dataDir, _, done := startTestDaemon(ctx, t)
	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, err := ReadRuntimeInfo(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Error("daemon.json still present after context cancel")
	}
}

func TestRunFatalOnInvalidConfig(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvConfigDir, cfgDir)
	bad := "log_level: bogus\n"
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "log_level") {
		t.Fatalf("Run with invalid config: err = %v, want log_level validation error", err)
	}
}
