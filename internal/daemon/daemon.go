package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/version"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// ErrAlreadyRunning is returned when another daemon holds the single-instance
// lock (spec §12.1).
var ErrAlreadyRunning = errors.New("another vincent daemon is already running")

// shutdownTimeout bounds the HTTP server drain on graceful shutdown.
const shutdownTimeout = 5 * time.Second

// Options configures Run.
type Options struct {
	// Foreground mirrors logs to stderr in addition to the log file.
	Foreground bool
}

// Run runs the daemon until the context is canceled, a termination signal
// arrives, or a stop is requested via POST /v1/daemon/stop. Startup order:
// dirs → lock → config (invalid = fatal) → store/migrations → token →
// listener → daemon.json; teardown reverses it, removing daemon.json only on
// this graceful path (spec §12.2, §12.4).
func Run(ctx context.Context, opts Options) error {
	dirs, err := config.ResolveDirs()
	if err != nil {
		return err
	}
	logsDir := filepath.Join(dirs.Data, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	logger, level, closeLog := newLogger(logsDir, opts.Foreground)
	defer func() { _ = closeLog() }()

	lock := flock.New(LockPath(dirs.Data))
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("acquire daemon lock: %w", err)
	}
	if !locked {
		if ri, err := ReadRuntimeInfo(dirs.Data); err == nil {
			return fmt.Errorf("%w (pid %d, port %d)", ErrAlreadyRunning, ri.PID, ri.Port)
		}
		return ErrAlreadyRunning
	}
	defer func() { _ = lock.Unlock() }()

	if _, err := config.EnsureDefaultFile(dirs.Config); err != nil {
		logger.Error("startup failed", "error", err)
		return err
	}
	cfg, err := config.Load(filepath.Join(dirs.Config, config.FileName))
	if err != nil {
		logger.Error("startup failed: invalid config", "error", err)
		return err
	}
	if lvl, err := parseLevel(cfg.LogLevel); err == nil {
		level.Set(lvl)
	}
	logger.Info("starting vincent daemon",
		"version", version.Version(), "pid", os.Getpid(), "data_dir", dirs.Data)

	st, err := store.Open(filepath.Join(dirs.Data, "vincent.db"))
	if err != nil {
		logger.Error("startup failed: store", "error", err)
		return err
	}
	defer func() { _ = st.Close() }()

	// git availability is probed once and logged; a missing or old git warns
	// rather than fails — the daemon still serves (phase 1 decision).
	git := gitx.New()
	if raw, major, minor, err := git.Version(ctx); err != nil {
		logger.Warn("git not detected; project registration and worktrees will fail", "error", err)
	} else {
		logger.Info("git detected", "version", raw)
		if major < gitx.MinMajor || (major == gitx.MinMajor && minor < gitx.MinMinor) {
			logger.Warn("git is older than recommended",
				"version", raw, "recommended", fmt.Sprintf("%d.%d+", gitx.MinMajor, gitx.MinMinor))
		}
	}

	token, err := EnsureToken(dirs.Data)
	if err != nil {
		logger.Error("startup failed: token", "error", err)
		return err
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("startup failed: listen", "addr", cfg.Listen, "error", err)
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}

	// current is the effective configuration, swapped whole on hot-reload.
	var current atomic.Pointer[config.Config]
	current.Store(&cfg)
	currentConfig := func() config.Config { return *current.Load() }

	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	stopRequested := make(chan struct{})
	var stopOnce sync.Once
	requestStop := func() { stopOnce.Do(func() { close(stopRequested) }) }

	// Agent adapters: paths ride the live config so hot-reloaded paths reach
	// future runs; availability is probed once at startup and served from
	// /v1/info (on-demand re-probing arrives with /v1/agents, T2.11).
	agents := agent.NewRegistry(
		claude.New(func() string { return currentConfig().Agents.Claude.Path }),
	)
	agentStatus := probeAgents(ctx, logger, agents)

	// Workflow registry: global scope from {config_dir}/workflows, project
	// scopes from every registered repo's .vincent/workflows (§5.2). Both
	// are watched; a repo that has no such directory is picked up if one
	// appears later.
	workflows := workflow.NewRegistry(
		filepath.Join(dirs.Config, workflow.GlobalDirName),
		workflow.Options{KnownAgents: agents.Names()},
		logger,
	)
	workflows.ReloadGlobal()
	if err := syncWorkflowProjects(ctx, st, workflows); err != nil {
		logger.Warn("project workflow scopes unavailable", "error", err)
	}
	if err := workflows.Watch(ctx); err != nil {
		logger.Warn("workflow hot-reload unavailable", "error", err)
	}

	worktrees := worktree.NewManager(git, dirs.Data)
	runner := taskrun.New(taskrun.Deps{
		Store:     st,
		Config:    currentConfig,
		Worktrees: worktrees,
		Agents:    agents,
		DataDir:   dirs.Data,
		Logger:    logger,
	})

	startedAt := time.Now()
	srv := api.New(api.Deps{
		Token:       token,
		Config:      currentConfig,
		StartedAt:   startedAt,
		ListenAddr:  ln.Addr().String(),
		RequestStop: requestStop,
		Logger:      logger,
		Store:       st,
		Git:         git,
		Worktrees:   worktrees,
		Agents:      agents,
		AgentStatus: agentStatus,
		Workflows:   workflows,
		WakeRunner:  runner.Wake,
		OnProjectsChanged: func() {
			pctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := syncWorkflowProjects(pctx, st, workflows); err != nil {
				logger.Warn("project workflow scopes not refreshed", "error", err)
			}
		},
	})

	if err := config.Watch(ctx, logger, dirs.Config, func(next config.Config) {
		prev := current.Load()
		if next.Listen != prev.Listen {
			logger.Warn("listen change ignored until restart",
				"effective", prev.Listen, "requested", next.Listen)
			next.Listen = prev.Listen
		}
		if lvl, err := parseLevel(next.LogLevel); err == nil {
			level.Set(lvl)
		}
		current.Store(&next)
	}); err != nil {
		logger.Warn("config hot-reload unavailable", "error", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	ri := RuntimeInfo{Port: port, PID: os.Getpid(), StartedAt: startedAt}
	if err := WriteRuntimeInfo(dirs.Data, ri); err != nil {
		logger.Error("startup failed: daemon.json", "error", err)
		return err
	}
	defer func() { _ = RemoveRuntimeInfo(dirs.Data) }()

	runner.Start(ctx)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	logger.Info("daemon ready", "addr", ln.Addr().String())

	select {
	case <-ctx.Done():
		logger.Info("shutting down: signal or parent context")
	case <-stopRequested:
		logger.Info("shutting down: stop requested via API")
	case err := <-serveErr:
		logger.Error("http server failed", "error", err)
		runner.Stop()
		return fmt.Errorf("http server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown incomplete", "error", err)
	}
	// After the HTTP drain: kill in-flight agent runs (tree-kill) and wait
	// for their interrupted state to be persisted (T1.7–T1.9 decision).
	runner.Stop()
	logger.Info("daemon stopped")
	return nil
}

// syncWorkflowProjects points the registry at the current set of registered
// repos, so their .vincent/workflows scopes are loaded and watched.
func syncWorkflowProjects(ctx context.Context, st *store.Store, reg *workflow.Registry) error {
	projects, err := st.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	roots := make(map[int64]string, len(projects))
	for i := range projects {
		roots[projects[i].ID] = projects[i].Path
	}
	reg.SetProjects(roots)
	return nil
}

// probeAgents runs Detect on every registered adapter and logs the outcome.
func probeAgents(ctx context.Context, logger *slog.Logger, agents *agent.Registry) []api.AgentStatus {
	out := make([]api.AgentStatus, 0, len(agents.Names()))
	for _, a := range agents.All() {
		av, err := a.Detect(ctx)
		if err != nil {
			av = agent.Availability{Error: err.Error()}
		}
		if av.Found {
			logger.Info("agent detected", "agent", a.Name(), "path", av.Path, "version", av.Version)
		} else {
			logger.Warn("agent not available", "agent", a.Name(), "error", av.Error)
		}
		out = append(out, api.AgentStatus{
			Name:          a.Name(),
			Available:     av.Found,
			Path:          av.Path,
			Version:       av.Version,
			SupportsInput: av.SupportsInput,
			Error:         av.Error,
		})
	}
	return out
}
