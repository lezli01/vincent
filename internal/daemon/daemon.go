package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/version"
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

	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	stopRequested := make(chan struct{})
	var stopOnce sync.Once
	requestStop := func() { stopOnce.Do(func() { close(stopRequested) }) }

	startedAt := time.Now()
	srv := api.New(api.Deps{
		Token:       token,
		Config:      func() config.Config { return *current.Load() },
		StartedAt:   startedAt,
		ListenAddr:  ln.Addr().String(),
		RequestStop: requestStop,
		Logger:      logger,
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
		return fmt.Errorf("http server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown incomplete", "error", err)
	}
	logger.Info("daemon stopped")
	return nil
}
