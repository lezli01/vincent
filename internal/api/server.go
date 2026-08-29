package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/version"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// Deps are the daemon facilities the API serves from.
type Deps struct {
	// Token is the bearer token every non-health request must present.
	Token string
	// Config returns the current effective configuration (hot-reloaded).
	Config func() config.Config
	// StartedAt is the daemon start time, for uptime reporting.
	StartedAt time.Time
	// ListenAddr is the effective bound address.
	ListenAddr string
	// Dirs are the resolved §12.2 directories, reported by /v1/doctor.
	Dirs config.Dirs
	// LogPath is the daemon log file, likewise for /v1/doctor. It is passed
	// in rather than derived because internal/daemon owns that path and
	// nothing here may import it (task 005 decision 6).
	LogPath string
	// TailLog reads the daemon log's trailing lines for /v1/doctor; nil means
	// the report carries the log's stat without a tail.
	TailLog func(path string, n int) ([]string, error)
	// RequestStop triggers a graceful daemon shutdown; it must not block.
	RequestStop func()
	// Logger receives request and panic logs.
	Logger *slog.Logger
	// Store is the persistence layer.
	Store *store.Store
	// Git runs git commands for registration validation.
	Git *gitx.Git
	// GitHub reads GitHub issues for the §13.2 issue endpoints and the
	// `github_issue` field on POST /v1/tasks (task 035). Nil is tolerated
	// (tests without it, and any build that never wires one) — the capability
	// probe then answers "no credential" and the issue endpoints refuse,
	// which is exactly what a daemon with no reachable GitHub reports.
	GitHub *github.Client
	// Worktrees removes task worktrees during forced project deletion.
	Worktrees *worktree.Manager
	// Agents is the adapter registry, for task-override validation.
	Agents *agent.Registry
	// Workflows is the workflow registry (§5.2), serving /v1/workflows.
	Workflows *workflow.Registry
	// Runner applies the §6 human actions. The engine owns them because it
	// owns the live runs they reach and the step_run rows they close; this
	// package only maps them onto HTTP.
	Runner *taskrun.Runner
	// Catalog serves /v1/agents and /v1/info agent availability from the
	// §9.6 binary-identity cache: fresh by construction, stat-cheap per
	// request. Nil is tolerated (tests without adapters) — /v1/info then
	// reports no agents and /v1/agents answers 500.
	Catalog *agent.CatalogCache
	// WakeRunner nudges task admission after a task is created; it must not
	// block. Nil is tolerated (tests without a runner).
	WakeRunner func()
	// OnProjectsChanged is called after a project is registered, re-pointed,
	// or deleted, so the workflow registry can follow the project scopes
	// (§5.2). Nil is tolerated.
	OnProjectsChanged func()
	// Broker feeds the SSE endpoints (§13.3). Nil is tolerated (tests
	// without streaming); the endpoints then answer 500.
	Broker *events.Broker
	// Reclaimer serves /v1/maintenance and the /v1/info orphan count
	// (task 005, §10). Nil is tolerated (tests without a data dir) — the
	// maintenance endpoints then answer 500 and /v1/info reports no orphans.
	Reclaimer *taskrun.Reclaimer
}

// AgentStatus is one adapter's availability as reported by /v1/info
// (spec §9.5).
type AgentStatus struct {
	Name          string `json:"name"`
	Available     bool   `json:"available"`
	Path          string `json:"path,omitempty"`
	Version       string `json:"version,omitempty"`
	SupportsInput bool   `json:"supports_input"`
	// VersionVerdict, TestedVersions and RestrictedVerdict are the two health
	// facets task 041 added beside the three /v1/info already carried
	// (installed, authenticated, and the §7.4 protocol half of
	// protocol-compatible). Both are advisory here; the restricted one is
	// what task creation refuses on (§9.4).
	VersionVerdict    string `json:"version_verdict"`
	TestedVersions    string `json:"tested_versions,omitempty"`
	RestrictedVerdict string `json:"restricted_verdict"`
	// LoggedIn is null where the adapter has no cheap authentication probe
	// (claude, whose CLI exposes no non-interactive auth surface) and a
	// definite boolean where it has one (codex's `login status`, cursor's
	// `status`). The distinction carries weight: an
	// installed-but-unauthenticated CLI probes as available and then fails
	// every run (spec §9.5).
	LoggedIn *bool  `json:"logged_in"`
	Error    string `json:"error,omitempty"`
	// Quota is this adapter's usage window as the daemon last observed it
	// (task 026); null when nothing has been observed. It is the same block
	// GET /v1/agents carries, and is served here so the board header — which
	// reads /v1/info — needs no second fetch to render a badge.
	Quota *quotaResponse `json:"quota"`
}

// Server is the vincent HTTP API server.
type Server struct {
	deps    Deps
	handler http.Handler
	httpSrv *http.Server
	// snaps memoizes parsed workflow snapshots for the task list's step
	// columns; entries are immutable, so it needs no invalidation (§18).
	snaps *snapshotCache
}

// New assembles the server: routes wrapped in recover → log → auth
// middleware (outermost first; phase 1 decision).
func New(deps Deps) *Server {
	if deps.Workflows == nil {
		// Tests and early boot get a registry with no scopes on disk: the
		// built-in ad-hoc workflow is still served.
		deps.Workflows = workflow.NewRegistry("", workflow.Options{}, deps.Logger)
	}
	s := &Server{deps: deps, snaps: newSnapshotCache(func() int {
		if deps.Config == nil {
			return 0
		}
		return deps.Config().Loop.MaxIterations
	})}
	s.handler = s.buildHandler()
	s.httpSrv = &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout bounds reading a whole request, headers and body. Without
		// it a client that sends headers and then dribbles a body holds a
		// connection and a goroutine for as long as it likes (§13.1, amended
		// 2026-08-25). It does not reach §13.3's streams: the deadline covers
		// reading the *request*, and an SSE response outlives it — a client
		// disconnect is still noticed, by the write that then fails.
		ReadTimeout: 30 * time.Second,
		// IdleTimeout closes a kept-alive connection nobody is using; without
		// one an idle connection lives as long as the daemon.
		IdleTimeout: 120 * time.Second,
		// No WriteTimeout, deliberately. §13.3's state and per-task streams are
		// long-lived by contract, and a server-wide write deadline would sever
		// every one of them at the deadline.
	}
	return s
}

// Handler exposes the fully-wrapped handler for tests.
func (s *Server) Handler() http.Handler { return s.handler }

// Serve serves on ln until Shutdown; it returns http.ErrServerClosed then.
func (s *Server) Serve(ln net.Listener) error {
	if err := s.httpSrv.Serve(ln); err != nil {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}

// Shutdown gracefully drains in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.httpSrv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown http: %w", err)
	}
	return nil
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	rt := &router{mux: mux, allowed: map[string][]string{}}
	rt.handle(http.MethodGet, "/v1/health", s.handleHealth)
	rt.handle(http.MethodGet, "/v1/info", s.handleInfo)
	rt.handle(http.MethodGet, "/v1/config", s.handleConfig)
	rt.handle(http.MethodGet, "/v1/agents", s.handleAgents)
	rt.handle(http.MethodGet, "/v1/doctor", s.handleDoctor)
	rt.handle(http.MethodPost, "/v1/doctor/fix", s.handleDoctorFix)
	rt.handle(http.MethodPost, "/v1/daemon/stop", s.handleStop)
	rt.handle(http.MethodPost, "/v1/daemon/backup", s.handleBackup)
	rt.handle(http.MethodGet, "/v1/maintenance/orphans", s.handleOrphans)
	rt.handle(http.MethodPost, "/v1/maintenance/gc", s.handleGC)
	rt.handle(http.MethodGet, "/v1/projects", s.handleProjectList)
	rt.handle(http.MethodPost, "/v1/projects", s.handleProjectCreate)
	rt.handle(http.MethodGet, "/v1/projects/{id}", s.handleProjectGet)
	rt.handle(http.MethodPatch, "/v1/projects/{id}", s.handleProjectPatch)
	rt.handle(http.MethodDelete, "/v1/projects/{id}", s.handleProjectDelete)
	rt.handle(http.MethodGet, "/v1/projects/{id}/github", s.handleProjectGitHub)
	rt.handle(http.MethodGet, "/v1/projects/{id}/github/issues", s.handleProjectGitHubIssues)
	rt.handle(http.MethodGet, "/v1/workflows", s.handleWorkflowList)
	rt.handle(http.MethodPost, "/v1/workflows/validate", s.handleWorkflowValidate)
	rt.handle(http.MethodGet, "/v1/workflows/definition", s.handleWorkflowDefinition)
	rt.handle(http.MethodPost, "/v1/resolve", s.handleResolve)
	rt.handle(http.MethodGet, "/v1/tasks", s.handleTaskList)
	rt.handle(http.MethodPost, "/v1/tasks", s.handleTaskCreate)
	rt.handle(http.MethodGet, "/v1/tasks/{id}", s.handleTaskGet)
	rt.handle(http.MethodPatch, "/v1/tasks/{id}", s.handleTaskPatch)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/cancel", s.handleTaskCancel)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/pause", s.handleTaskPause)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/resume", s.handleTaskResume)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/retry", s.handleTaskRetry)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/repair", s.handleTaskRepair)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/skip", s.handleTaskSkip)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/approve", s.handleTaskApprove)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/reject", s.handleTaskReject)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/answer", s.handleTaskAnswer)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/archive", s.handleTaskArchive)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/follow_up", s.handleTaskFollowUp)
	rt.handle(http.MethodGet, "/v1/tasks/{id}/workflow", s.handleTaskWorkflow)
	rt.handle(http.MethodGet, "/v1/tasks/{id}/steps", s.handleTaskSteps)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/steps/{step_id}/status", s.handleStepStatus)
	rt.handle(http.MethodGet, "/v1/tasks/{id}/steps/{run_id}/transcript", s.handleTranscript)
	rt.handle(http.MethodGet, "/v1/tasks/{id}/diff", s.handleTaskDiff)
	rt.handle(http.MethodGet, "/v1/events", s.handleEvents)
	rt.handle(http.MethodGet, "/v1/tasks/{id}/events", s.handleTaskEvents)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such endpoint")
	})
	var h http.Handler = mux
	h = s.authMiddleware(h)
	h = s.logMiddleware(h)
	h = s.recoverMiddleware(h)
	return h
}

// router registers method-qualified patterns plus one path-level fallback per
// path, so requests with a wrong method get the §13.1 envelope (with Allow)
// instead of ServeMux's plain-text 405.
type router struct {
	mux     *http.ServeMux
	allowed map[string][]string
}

func (rt *router) handle(method, path string, h http.HandlerFunc) {
	if len(rt.allowed[path]) == 0 {
		rt.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			allow := strings.Join(rt.allowed[path], ", ")
			w.Header().Set("Allow", allow)
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
				fmt.Sprintf("%s is not allowed on %s (allowed: %s)", r.Method, path, allow))
		})
	}
	rt.allowed[path] = append(rt.allowed[path], method)
	rt.mux.HandleFunc(method+" "+path, h)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": version.Version(),
	})
}

// infoResponse is the GET /v1/info body (initial shape, phase 1 decision;
// agent availability joined in T1.7).
type infoResponse struct {
	Version          string        `json:"version"`
	Commit           string        `json:"commit"`
	Built            string        `json:"built"`
	PID              int           `json:"pid"`
	StartedAt        string        `json:"started_at"`
	UptimeSeconds    int64         `json:"uptime_seconds"`
	Listen           string        `json:"listen"`
	MaxParallelTasks int           `json:"max_parallel_tasks"`
	Agents           []AgentStatus `json:"agents"`
	// Orphans is how many data-root directories no task row claims right now
	// (task 005, §10). It rides /v1/info rather than /v1/health because
	// health is deliberately {status, version} and is the one unauthenticated
	// endpoint (§13.1) — the shape of a user's disk does not belong on it.
	// Computed per request from a readdir and the id queries, with no size
	// walk, so it is never stale after a gc run.
	Orphans int `json:"orphans"`
	// Database is the store's on-disk footprint (task 029, §17).
	Database infoDatabase `json:"database"`
}

// infoDatabase is the byte half of §17's database figures: the file plus the
// two sidecars WAL mode keeps beside it.
//
// **Only** the byte figures ride here. Row counts, the retention span and the
// workflow-snapshot total are scans, and this endpoint is polled by the board,
// the projects view and the daemon view on every debounced refresh — the same
// rule that admitted `orphans` ("a readdir plus the id queries — no size walk,
// no git — so it is cheap") is what keeps a COUNT(*) over a multi-million-row
// events table off it. Three os.Stat calls are cheap in that sense; a scan is
// not. The scans live on GET /v1/doctor, which is deliberately cold and needs
// no cache anywhere (task 029 decision 1).
type infoDatabase struct {
	Path       string `json:"path"`
	SizeBytes  int64  `json:"size_bytes"`
	WALBytes   int64  `json:"wal_bytes"`
	SHMBytes   int64  `json:"shm_bytes"`
	TotalBytes int64  `json:"total_bytes"`
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	cfg := s.deps.Config()
	// Availability rides the same binary-identity cache as /v1/agents
	// (T2.11): a CLI installed or upgraded after daemon start is visible on
	// the next request, and the two endpoints can never disagree.
	agents := []AgentStatus{}
	if s.deps.Catalog != nil {
		quotas := s.agentQuotas(r.Context())
		for _, name := range s.deps.Catalog.Names() {
			e, ok := s.deps.Catalog.Entry(r.Context(), name, false)
			if !ok {
				continue
			}
			agents = append(agents, AgentStatus{
				Name:              name,
				Available:         e.Availability.Found,
				Path:              e.Availability.Path,
				Version:           e.Availability.Version,
				SupportsInput:     e.Availability.SupportsInput,
				VersionVerdict:    string(e.Availability.VersionVerdict),
				TestedVersions:    e.Availability.TestedVersions,
				RestrictedVerdict: string(e.RestrictedVerdict()),
				LoggedIn:          e.Availability.LoggedIn,
				Error:             e.Availability.Error,
				Quota:             quotas[name],
			})
		}
	}
	// A failed count degrades to zero with a log line: /v1/info is what every
	// client polls for daemon identity, and a readdir problem must not take
	// the whole payload down with it.
	orphans := 0
	if s.deps.Reclaimer != nil {
		n, err := s.deps.Reclaimer.Count(r.Context())
		if err != nil {
			s.deps.Logger.Warn("count orphans", "error", err)
		} else {
			orphans = n
		}
	}
	writeJSON(w, http.StatusOK, infoResponse{
		Version:          version.Version(),
		Commit:           version.Commit(),
		Built:            version.Date(),
		PID:              os.Getpid(),
		StartedAt:        s.deps.StartedAt.UTC().Format(time.RFC3339),
		UptimeSeconds:    int64(time.Since(s.deps.StartedAt).Seconds()),
		Listen:           s.deps.ListenAddr,
		MaxParallelTasks: cfg.MaxParallelTasks,
		Agents:           agents,
		Orphans:          orphans,
		Database:         s.infoDatabase(),
	})
}

// infoDatabase stats the store's files. A failed stat degrades to zeros with a
// log line, for the same reason the orphan count does: identity is what every
// client polls this endpoint for, and a stat problem must not take the payload
// down with it.
func (s *Server) infoDatabase() infoDatabase {
	if s.deps.Store == nil {
		return infoDatabase{}
	}
	out := infoDatabase{Path: s.deps.Store.Path()}
	sizes, err := s.deps.Store.FileSizes()
	if err != nil {
		s.deps.Logger.Warn("stat database", "error", err)
		return out
	}
	out.SizeBytes, out.WALBytes = sizes.MainBytes, sizes.WALBytes
	out.SHMBytes, out.TotalBytes = sizes.SHMBytes, sizes.TotalBytes
	return out
}

// configResponse mirrors config.yaml as snake_case JSON; durations render as
// Go duration strings (phase 1 decision).
type configResponse struct {
	Listen           string         `json:"listen"`
	MaxParallelTasks int            `json:"max_parallel_tasks"`
	Defaults         configDefaults `json:"defaults"`
	// The §10 branch-cleanup pair (task 008). Both are reported because the
	// remote one is inert without the local one, and a client showing only the
	// key that is on would describe a policy that cannot run.
	DeleteEmptyBranchOnArchive  bool  `json:"delete_empty_branch_on_archive"`
	DeleteRemoteBranchOnArchive bool  `json:"delete_remote_branch_on_archive"`
	TranscriptRetentionDays     int   `json:"transcript_retention_days"`
	TranscriptMaxBytes          int64 `json:"transcript_max_bytes"`
	// MaxTaskCostUSD is the per-task spend ceiling; 0 is no cap (task 033).
	// Served for the same reason every other key is: the TUI reads no
	// configuration from disk (§15).
	MaxTaskCostUSD    float64              `json:"max_task_cost_usd"`
	UsageLimitRecheck string               `json:"usage_limit_recheck_interval"`
	LogLevel          string               `json:"log_level"`
	Agents            map[string]agentPath `json:"agents"`
	// TUI is view preference the daemon only relays (§15): it is in the file
	// the daemon owns and hot-reloads, so this endpoint is how the TUI — which
	// reads no configuration of its own — gets it.
	TUI configTUI `json:"tui"`
}

type configTUI struct {
	Board configBoard `json:"board"`
}

type configBoard struct {
	// GroupBy is always present, empty list included: `null` would make a
	// flat table indistinguishable from a client's own default.
	GroupBy []string `json:"group_by"`
}

type configDefaults struct {
	AgentTimeout   string `json:"agent_timeout"`
	CommandTimeout string `json:"command_timeout"`
	InputTimeout   string `json:"input_timeout"`
}

type agentPath struct {
	Path string `json:"path"`
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := s.deps.Config()
	writeJSON(w, http.StatusOK, configResponse{
		Listen:           cfg.Listen,
		MaxParallelTasks: cfg.MaxParallelTasks,
		Defaults: configDefaults{
			AgentTimeout:   cfg.Defaults.AgentTimeout.String(),
			CommandTimeout: cfg.Defaults.CommandTimeout.String(),
			InputTimeout:   cfg.Defaults.InputTimeout.String(),
		},
		DeleteEmptyBranchOnArchive:  cfg.DeleteEmptyBranchOnArchive,
		DeleteRemoteBranchOnArchive: cfg.DeleteRemoteBranchOnArchive,
		TranscriptRetentionDays:     cfg.TranscriptRetentionDays,
		TranscriptMaxBytes:          cfg.TranscriptMaxBytes.Bytes(),
		MaxTaskCostUSD:              cfg.MaxTaskCostUSD,
		UsageLimitRecheck:           cfg.UsageLimitRecheckInterval.String(),
		LogLevel:                    cfg.LogLevel,
		Agents: map[string]agentPath{
			"claude": {Path: cfg.Agents.Claude.Path},
			"codex":  {Path: cfg.Agents.Codex.Path},
			"cursor": {Path: cfg.Agents.Cursor.Path},
		},
		TUI: configTUI{Board: configBoard{GroupBy: boardGroupBy(cfg.TUI.Board.GroupBy)}},
	})
}

// boardGroupBy renders the grouping levels as strings, never as JSON null: a
// flat table is a configured choice and has to read as `[]`.
func boardGroupBy(levels []config.BoardGroup) []string {
	out := make([]string, 0, len(levels))
	for _, l := range levels {
		out = append(out, string(l))
	}
	return out
}

func (s *Server) handleStop(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]bool{"stopping": true})
	s.deps.RequestStop()
}
