package daemon

import (
	"context"
	"encoding/json"
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
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/github"
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
	// Console records what `--hide-console` did with the console this process
	// was handed, for the startup log record: "detached", "shared", "none",
	// "kept", "attached", or "" when the flag was not passed. A window left on
	// the desktop at logon is then a log line rather than a screenshot
	// (T4.21); see DetachConsole for what each word means.
	Console string
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

	// Observed before EnsureDefaultFile, because that is what tightens them and
	// afterwards there is nothing left to report. Never silent: the mode of a
	// config file is something a user could have widened deliberately, so the
	// change says which path it touched and what it found (§12.2).
	if issues, err := config.CheckPermissions(dirs.Config); err != nil {
		logger.Warn("cannot inspect config permissions", "error", err)
	} else {
		for _, issue := range issues {
			logger.Warn("config permissions tightened to owner-only",
				"path", issue.Path,
				"mode", fmt.Sprintf("%04o", issue.Mode.Perm()),
				"want", fmt.Sprintf("%04o", issue.Want.Perm()))
		}
	}
	if _, err := config.EnsureDefaultFile(dirs.Config); err != nil {
		logger.Error("startup failed", "error", err)
		return err
	}
	cfg, err := config.Load(filepath.Join(dirs.Config, config.FileName))
	if err != nil {
		logger.Error("startup failed: invalid config", "error", err)
		return err
	}
	// Checked here rather than in config.validate: internal/config is a leaf
	// package and the branch template's context lives in internal/worktree
	// (task 001). A hard failure, not a warning — a daemon that ignored the
	// configured convention would create every branch under the wrong name, and
	// the user would not find out until they looked at their repository.
	if err := worktree.ValidateBranchTemplate(cfg.BranchTemplate); err != nil {
		logger.Error("startup failed: branch_template does not compile",
			"branch_template", cfg.BranchTemplate, "error", err)
		return fmt.Errorf("invalid branch_template: %w", err)
	}
	// Settings that load and take effect but cannot do what they ask for
	// (§12.3). They never refuse the file, so the log is the only place they
	// are ever visible; the reload path re-reports them when they change.
	prevWarnings := cfg.Warnings()
	for _, warning := range prevWarnings {
		logger.Warn("config warning", "warning", warning)
	}
	if lvl, err := parseLevel(cfg.LogLevel); err == nil {
		level.Set(lvl)
	}
	startup := []any{
		"version", version.Version(), "pid", os.Getpid(), "data_dir", dirs.Data,
	}
	if opts.Console != "" {
		startup = append(startup, "console", opts.Console)
	}
	logger.Info("starting vincent daemon", startup...)

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

	// What every child of this daemon will inherit (§12.3, T4.23). Logged
	// before anything is spawned, so the record precedes the runs it explains.
	envNames := logEnvironment(logger, cfg.Environment)

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
			cursor.New(func() string { return currentConfig().Agents.Cursor.Path }),
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
		workflow.Options{
			KnownAgents:   agents.Names(),
			Catalogs:      catalog.Catalogs,
			MaxIterations: func() int { return currentConfig().Loop.MaxIterations },
		},
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
	// Fan-out ancestors are notified through the same hook (§13.3, task 014
	// decision 14). The relay runs post-commit like every other subscriber,
	// and the events it appends come back through this hook themselves — no
	// recursion, because children_changed is not one of the types it reacts
	// to.
	st.SetEventHook(func(e *store.Event) {
		broker.Publish(e)
		if e == nil || e.TaskID == nil {
			return
		}
		if e.Type != store.EventTaskStateChanged && e.Type != store.EventTaskCreated {
			return
		}
		if err := st.EmitChildrenChanged(ctx, *e.TaskID, store.TaskState(stateOfEvent(e))); err != nil {
			logger.Error("notify fan-out ancestors", "task", *e.TaskID, "error", err)
		}
	})

	// Crash recovery runs before anything can admit or execute (§12.4): it
	// belongs to neither the scheduler nor the runner. Orphans are killed only
	// when the PID still holds the process the journal names — an exact native
	// identity, or the legacy start-time tolerance for a row that carries none
	// (issue #149); the owning tasks re-queue through the FSM, consuming no
	// retries.
	//
	// A failure here stops the daemon (issue #142). Recovery reconciles each
	// task atomically or not at all, so what it leaves behind on failure is
	// the recoverable state a later start can retry — and starting the
	// scheduler over rows it could not reconcile is how a second attempt
	// gets admitted against a first one the database still calls `running`.
	if n, err := taskrun.Recover(ctx, st, logger); err != nil {
		logger.Error("startup failed: crash recovery", "error", err)
		return fmt.Errorf("crash recovery: %w", err)
	} else if n > 0 {
		logger.Warn("recovered tasks from a previous run", "tasks", n)
	}

	worktrees := worktree.NewManager(git, dirs.Data)
	runnerDeps := taskrun.Deps{
		Store:     st,
		Config:    currentConfig,
		Worktrees: worktrees,
		Agents:    agents,
		Catalog:   catalog,
		Shells:    shells,
		DataDir:   dirs.Data,
		Logger:    logger,
		Events:    broker,
	}
	runner := taskrun.New(runnerDeps)
	// Transcript retention (§17): once at startup and every 24 h after. The
	// ticker is what makes retention work on a daemon that survives reboots
	// (T4.1) rather than only on the restarts it no longer has.
	go taskrun.NewTranscriptPruner(runnerDeps).Run(ctx)
	// Orphan reconcile (task 005, §10). It runs after recovery, which
	// reconciles rows and processes but never directories, and it **reports
	// only**: silently deleting a directory that may hold an agent's
	// uncommitted work is what §18's "never auto-deletes" posture rejects.
	// A human runs `vincent gc`; the count also rides /v1/info so a log line
	// nobody tails is not the whole report.
	reclaimer := taskrun.NewReclaimer(runnerDeps)
	reportOrphans(ctx, reclaimer, logger)
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
	// The GitHub issue reader (task 035). It is built once and reads the
	// config toggle per call through the API's own gate, so a hot reload
	// governs the next request rather than requiring a restart.
	githubClient := github.New(github.Options{Logger: logger})

	srv := api.New(api.Deps{
		Token:       token,
		Config:      currentConfig,
		StartedAt:   startedAt,
		ListenAddr:  ln.Addr().String(),
		Dirs:        dirs,
		LogPath:     LogPath(dirs.Data),
		TailLog:     TailFile,
		RequestStop: requestStop,
		Logger:      logger,
		Store:       st,
		Git:         git,
		GitHub:      githubClient,
		Worktrees:   worktrees,
		Agents:      agents,
		Catalog:     catalog,
		Workflows:   workflows,
		Runner:      runner,
		WakeRunner:  sched.Wake,
		Broker:      broker,
		Reclaimer:   reclaimer,
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
		// A branch template that does not compile is refused on its own rather
		// than dropping the whole reload: a typo here should not also revert an
		// unrelated log_level edit in the same save. Keeping the previous value is
		// the honest fallback — silently falling back to the built-in name would
		// create every branch under a convention the user did not ask for
		// (task 001). config cannot check this itself; it is a leaf package and
		// the template context lives in internal/worktree.
		if next.BranchTemplate != prev.BranchTemplate {
			if err := worktree.ValidateBranchTemplate(next.BranchTemplate); err != nil {
				logger.Warn("branch_template change ignored: it does not compile",
					"effective", prev.BranchTemplate, "requested", next.BranchTemplate, "error", err)
				next.BranchTemplate = prev.BranchTemplate
			}
		}
		// Re-warned on every reload that introduces one: a warning is about the
		// configuration now in force, and startup's copy says nothing about an
		// edit made an hour later.
		if warnings := next.Warnings(); !sameNames(prevWarnings, warnings) {
			for _, warning := range warnings {
				logger.Warn("config warning", "warning", warning)
			}
			prevWarnings = warnings
		}
		if lvl, err := parseLevel(next.LogLevel); err == nil {
			level.Set(lvl)
		}
		current.Store(&next)
		// Re-report the environment when a reload actually moves it. "Once at
		// startup" would go stale the moment someone edits the policy, and a
		// log that quietly describes a set no longer in force is worse than
		// one that never spoke (T4.23).
		if names := next.Environment.ResolveProcess(); !sameNames(envNames, config.Names(names)) {
			envNames = logEnvironment(logger, next.Environment)
		}
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

// reportOrphans logs the data-root directories no task row claims, one line
// each plus a summary naming the command that clears them (task 005, §10).
//
// It deletes nothing, ever. The asymmetry with `vincent gc` — which deletes
// by default — is deliberate: the explicit command has a human behind it, a
// dirty check, a containment rule and a printed byte report, and the
// unattended path at startup has none of those.
//
// A failure here is logged and dropped. Reconciling directories is
// housekeeping, and a daemon that refuses to serve because it could not read
// a directory has its priorities backwards.
func reportOrphans(ctx context.Context, rc *taskrun.Reclaimer, logger *slog.Logger) {
	rep, err := rc.Scan(ctx)
	if err != nil {
		logger.Error("scan for orphaned directories failed", "error", err)
		return
	}
	for _, o := range rep.Orphans {
		logger.Warn("orphaned directory: no task claims it",
			"path", o.Path, "kind", o.Kind, "bytes", o.Bytes, "skip_reason", o.Skip)
	}
	for _, m := range rep.Mismatches {
		logger.Warn("task points at a worktree that is gone",
			"task", m.TaskID, "state", m.State, "path", m.Path)
	}
	if len(rep.Orphans) > 0 {
		logger.Warn("orphaned directories found; reclaim them with `vincent gc`",
			"orphans", len(rep.Orphans), "bytes", rep.Bytes)
	}
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

// stateOfEvent reads the `to` field a task.state_changed event carries, so
// the children_changed relay can pass it on. A task.created event has none —
// the task is queued by definition — and an unreadable payload yields "",
// which a client treats as "re-fetch the rollup", which it does anyway.
func stateOfEvent(e *store.Event) string {
	var payload struct {
		To string `json:"to"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return ""
	}
	if payload.To == "" && e.Type == store.EventTaskCreated {
		return string(store.TaskQueued)
	}
	return payload.To
}
