package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
)

const (
	// startTimeout bounds the auto-start health poll — same bound as
	// `vincent daemon start` (phase 1 decision).
	startTimeout = 10 * time.Second
	// pollInterval paces the auto-start poll loop.
	pollInterval = 100 * time.Millisecond
	// probeTimeout bounds one reachability check against a daemon that may
	// be wedged; CheckHealth's own client timeout is the real ceiling.
	probeTimeout = 3 * time.Second
)

// connector is the daemon discovery/auto-start seam (§12.1). Fields default
// to the real implementations; tests inject fakes or a built binary.
type connector struct {
	resolveDataDir func() (string, error)
	readRuntime    func(dataDir string) (daemon.RuntimeInfo, error)
	checkHealth    func(ctx context.Context, port int) (daemon.HealthInfo, error)
	newClient      func(dataDir string) (*apiclient.Client, error)
	startDetached  func() (int, error)
	startTimeout   time.Duration
	pollInterval   time.Duration
}

func defaultConnector() connector {
	return connector{
		resolveDataDir: func() (string, error) {
			dirs, err := config.ResolveDirs()
			if err != nil {
				return "", fmt.Errorf("resolve data dir: %w", err)
			}
			return dirs.Data, nil
		},
		readRuntime:   daemon.ReadRuntimeInfo,
		checkHealth:   daemon.CheckHealth,
		newClient:     apiclient.Discover,
		startDetached: daemon.StartDetached,
		startTimeout:  startTimeout,
		pollInterval:  pollInterval,
	}
}

// connectedMsg reports a healthy daemon and a ready client.
type connectedMsg struct {
	client      *apiclient.Client
	health      daemon.HealthInfo
	dataDir     string
	autoStarted bool
}

// probeFailedMsg reports an unreachable daemon; the root switches to the
// "starting daemon…" phase and issues startCmd.
type probeFailedMsg struct{ dataDir string }

// connectFailedMsg reports a connect flow that ended without a healthy
// daemon; the root shows the error screen (log path, retry).
type connectFailedMsg struct {
	err     error
	logPath string
}

// probeCmd checks for a reachable daemon via daemon.json + /v1/health. It
// resolves fast: either the daemon is there, or auto-start takes over.
func (cn connector) probeCmd() tea.Cmd {
	return func() tea.Msg {
		dataDir, err := cn.resolveDataDir()
		if err != nil {
			return connectFailedMsg{err: err}
		}
		ri, err := cn.readRuntime(dataDir)
		if err != nil {
			return probeFailedMsg{dataDir: dataDir}
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		h, err := cn.checkHealth(ctx, ri.Port)
		if err != nil {
			return probeFailedMsg{dataDir: dataDir}
		}
		return cn.finish(dataDir, h, false)
	}
}

// startCmd auto-starts the daemon (§12.1) and polls it healthy, matching
// the spawned pid so a stale daemon.json can't be mistaken for the child.
func (cn connector) startCmd(dataDir string) tea.Cmd {
	return func() tea.Msg {
		pid, err := cn.startDetached()
		if err != nil {
			return connectFailedMsg{
				err:     fmt.Errorf("start daemon: %w", err),
				logPath: daemonLogPath(dataDir),
			}
		}
		deadline := time.Now().Add(cn.startTimeout)
		for time.Now().Before(deadline) {
			ri, err := cn.readRuntime(dataDir)
			if err == nil && ri.PID == pid {
				ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
				h, err := cn.checkHealth(ctx, ri.Port)
				cancel()
				if err == nil {
					return cn.finish(dataDir, h, true)
				}
			}
			time.Sleep(cn.pollInterval)
		}
		return connectFailedMsg{
			err:     fmt.Errorf("daemon did not become healthy within %s", cn.startTimeout),
			logPath: daemonLogPath(dataDir),
		}
	}
}

// finish builds the API client once the daemon is provably healthy.
func (cn connector) finish(dataDir string, h daemon.HealthInfo, autoStarted bool) tea.Msg {
	client, err := cn.newClient(dataDir)
	if err != nil {
		return connectFailedMsg{err: err, logPath: daemonLogPath(dataDir)}
	}
	return connectedMsg{client: client, health: h, dataDir: dataDir, autoStarted: autoStarted}
}

func daemonLogPath(dataDir string) string {
	return filepath.Join(dataDir, "logs", "daemon.log")
}
