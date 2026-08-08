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
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/scheduler"
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

// processGrace is §12.4's window between asking running step processes to
// exit and killing them on graceful shutdown.
const processGrace = 15 * time.Second

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
	return runWithAgents(ctx, opts, nil)
}

// runWithAgents is Run with an injectable registry for lifecycle tests. A nil
// registry builds the production adapters from the live configuration.
func runWithAgents(ctx context.Context, opts Options, agents *agent.Registry) error {
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
	// future runs. Availability and option catalogs are served from the
	// §9.6 binary-identity cache — primed asynchronously here, stat-checked
	// per request, so a CLI installed after daemon start is visible without
	// a restart (T2.11).
	if agents == nil {
		agents = agent.NewRegistry(
			claude.New(func() string { return currentConfig().Agents.Claude.Path }),
			codex.New(func() string { return currentConfig().Agents.Codex.Path }),
		)
	}
	catalog := agent.NewCatalogCache(agents)
	stopCatalogPrime := startAgentCatalogPrime(ctx, logger, catalog)
	defer stopCatalogPrime()

	// Workflow registry: global scope from {config_dir}/workflows, project
	// scopes from every registered repo's .vincent/workflows (§5.2). Both
	// are watched; a repo that has no such directory is picked up if one
	// appears later. Catalog checks read the cache's primed-or-curated view
	// and never probe (§8.2).
	workflows := workflow.NewRegistry(
		filepath.Join(dirs.Config, workflow.GlobalDirName),
		workflow.Options{KnownAgents: agents.Names(), Catalogs: catalog.Catalogs},
		logger,
	)
	workflows.ReloadGlobal()
	if err := syncWorkflowProjects(ctx, st, workflows); err != nil {
		logger.Warn("project workflow scopes unavailable", "error", err)
	}
	if err := workflows.Watch(ctx); err != nil {
		logger.Warn("workflow hot-reload unavailable", "error", err)
	}

	// The command-step shell is probed at startup and re-probed on config
	// reload; a step's `shell:` pin is resolved per use and fails the step
	// when missing (§8.3).
	shells := taskrun.NewShells(logger)
	if _, err := shells.Default(); err != nil {
		logger.Warn("command steps will fail until a shell is available", "error", err)
	}

	// The broker is the post-commit fan-out (§13.3): SSE subscribers and the
	// scheduler's wake all hang off the store's single event hook through it.
	broker := events.New()
	st.SetEventHook(broker.Publish)

	// Crash recovery runs before anything can admit or execute (§12.4): it
	// belongs to neither the scheduler nor the runner. Orphans are killed
	// only when PID and start time both match the journal; the owning tasks
	// re-queue through the FSM, consuming no retries.
	if n, err := taskrun.Recover(ctx, st, logger); err != nil {
		logger.Error("startup crash recovery failed", "error", err)
	} else if n > 0 {
		logger.Warn("recovered tasks from a previous run", "tasks", n)
	}

	worktrees := worktree.NewManager(git, dirs.Data)
	runner := taskrun.New(taskrun.Deps{
		Store:     st,
		Config:    currentConfig,
		Worktrees: worktrees,
		Agents:    agents,
		Shells:    shells,
		DataDir:   dirs.Data,
		Logger:    logger,
		Events:    broker,
	})
	sched := scheduler.New(scheduler.Deps{
		Store:    st,
		Config:   currentConfig,
		Admitter: runner,
		Logger:   logger,
	})
	// The scheduler re-evaluates on the two event types that change what may
	// be admitted (§11) — an internal broker subscriber, one hop downstream
	// of the store's post-commit hook.
	broker.OnEvent(func(e *store.Event) {
		if scheduler.WakeOn(e) {
			sched.Wake()
		}
	})
	// Registry reloads become durable workflow.registry_changed events
	// (§13.3). Registered after the initial loads so boot churn stays out of
	// the event log.
	workflows.OnChange(func() {
		if err := st.AppendEvent(context.Background(),
			&store.Event{Type: store.EventWorkflowRegistryChanged}); err != nil {
			logger.Warn("workflow.registry_changed event not recorded", "error", err)
		}
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
		Catalog:     catalog,
		Workflows:   workflows,
		Runner:      runner,
		WakeRunner:  sched.Wake,
		Broker:      broker,
		OnProjectsChanged: func() {
			pctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := syncWorkflowProjects(pctx, st, workflows); err != nil {
				logger.Warn("project workflow scopes not refreshed", "error", err)
			}
			// A project's max_parallel_tasks may have changed (§11).
			sched.Wake()
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
		// A reload may follow a shell install; pick it up without a restart
		// (§8.3).
		shells.Reprobe()
		// max_parallel_tasks may have changed (§11).
		sched.Wake()
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
	sched.Start(ctx)

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
		sched.Stop()
		runner.Stop()
		broker.Close()
		return fmt.Errorf("http server: %w", err)
	}

	// Graceful order (§12.4, PR D decision): announce, stop admitting, give
	// running processes the grace to exit while the API — SSE included —
	// stays up so clients watch the wind-down, then end the streams and
	// drain HTTP.
	if err := st.AppendEvent(context.Background(),
		&store.Event{Type: store.EventDaemonShuttingDown}); err != nil {
		logger.Warn("daemon.shutting_down event not recorded", "error", err)
	}
	sched.Stop()
	runner.StopGraceful(processGrace)
	broker.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown incomplete", "error", err)
	}
	logger.Info("daemon stopped")
	return nil
}

// startAgentCatalogPrime starts the asynchronous startup probe and returns a
// stop function that cancels and joins it. Run defers the stop before closing
// the logger so a canceled probe cannot write through lumberjack afterward and
// reopen daemon.log during shutdown (notably breaking TempDir cleanup on
// Windows).
func startAgentCatalogPrime(ctx context.Context, logger *slog.Logger, catalog *agent.CatalogCache) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		primeAgentCatalog(ctx, logger, catalog)
	}()
	return func() {
		cancel()
		<-done
	}
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

// primeAgentCatalog fills the §9.6 cache at startup and logs each adapter's
// availability. Runs asynchronously: a slow probe never delays daemon
// readiness, and a request racing the prime waits behind the same
// per-adapter probe instead of double-spawning.
func primeAgentCatalog(ctx context.Context, logger *slog.Logger, catalog *agent.CatalogCache) {
	for _, name := range catalog.Names() {
		e, ok := catalog.Entry(ctx, name, false)
		if !ok {
			continue
		}
		av := e.Availability
		if av.Found {
			logger.Info("agent detected", "agent", name, "path", av.Path, "version", av.Version)
		} else {
			logger.Warn("agent not available", "agent", name, "error", av.Error)
		}
	}
}
