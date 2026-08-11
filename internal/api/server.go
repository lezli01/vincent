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
	// RequestStop triggers a graceful daemon shutdown; it must not block.
	RequestStop func()
	// Logger receives request and panic logs.
	Logger *slog.Logger
	// Store is the persistence layer.
	Store *store.Store
	// Git runs git commands for registration validation.
	Git *gitx.Git
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
}

// AgentStatus is one adapter's availability as reported by /v1/info
// (spec §9.5).
type AgentStatus struct {
	Name          string `json:"name"`
	Available     bool   `json:"available"`
	Path          string `json:"path,omitempty"`
	Version       string `json:"version,omitempty"`
	SupportsInput bool   `json:"supports_input"`
	// LoggedIn is null where the adapter has no cheap authentication probe
	// (claude, codex) and a definite boolean where it has one (cursor). The
	// distinction carries weight: an installed-but-unauthenticated CLI probes
	// as available and then fails every run (spec §9.5).
	LoggedIn *bool  `json:"logged_in"`
	Error    string `json:"error,omitempty"`
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
	s := &Server{deps: deps, snaps: newSnapshotCache()}
	s.handler = s.buildHandler()
	s.httpSrv = &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
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
	rt.handle(http.MethodPost, "/v1/daemon/stop", s.handleStop)
	rt.handle(http.MethodGet, "/v1/projects", s.handleProjectList)
	rt.handle(http.MethodPost, "/v1/projects", s.handleProjectCreate)
	rt.handle(http.MethodGet, "/v1/projects/{id}", s.handleProjectGet)
	rt.handle(http.MethodPatch, "/v1/projects/{id}", s.handleProjectPatch)
	rt.handle(http.MethodDelete, "/v1/projects/{id}", s.handleProjectDelete)
	rt.handle(http.MethodGet, "/v1/workflows", s.handleWorkflowList)
	rt.handle(http.MethodPost, "/v1/workflows/validate", s.handleWorkflowValidate)
	rt.handle(http.MethodPost, "/v1/resolve", s.handleResolve)
	rt.handle(http.MethodGet, "/v1/tasks", s.handleTaskList)
	rt.handle(http.MethodPost, "/v1/tasks", s.handleTaskCreate)
	rt.handle(http.MethodGet, "/v1/tasks/{id}", s.handleTaskGet)
	rt.handle(http.MethodPatch, "/v1/tasks/{id}", s.handleTaskPatch)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/cancel", s.handleTaskCancel)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/pause", s.handleTaskPause)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/resume", s.handleTaskResume)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/retry", s.handleTaskRetry)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/skip", s.handleTaskSkip)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/approve", s.handleTaskApprove)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/reject", s.handleTaskReject)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/answer", s.handleTaskAnswer)
	rt.handle(http.MethodPost, "/v1/tasks/{id}/archive", s.handleTaskArchive)
	rt.handle(http.MethodGet, "/v1/tasks/{id}/steps", s.handleTaskSteps)
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
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	cfg := s.deps.Config()
	// Availability rides the same binary-identity cache as /v1/agents
	// (T2.11): a CLI installed or upgraded after daemon start is visible on
	// the next request, and the two endpoints can never disagree.
	agents := []AgentStatus{}
	if s.deps.Catalog != nil {
		for _, name := range s.deps.Catalog.Names() {
			e, ok := s.deps.Catalog.Entry(r.Context(), name, false)
			if !ok {
				continue
			}
			agents = append(agents, AgentStatus{
				Name:          name,
				Available:     e.Availability.Found,
				Path:          e.Availability.Path,
				Version:       e.Availability.Version,
				SupportsInput: e.Availability.SupportsInput,
				LoggedIn:      e.Availability.LoggedIn,
				Error:         e.Availability.Error,
			})
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
	})
}

// configResponse mirrors config.yaml as snake_case JSON; durations render as
// Go duration strings (phase 1 decision).
type configResponse struct {
	Listen                  string               `json:"listen"`
	MaxParallelTasks        int                  `json:"max_parallel_tasks"`
	Defaults                configDefaults       `json:"defaults"`
	TranscriptRetentionDays int                  `json:"transcript_retention_days"`
	TranscriptMaxBytes      int64                `json:"transcript_max_bytes"`
	LogLevel                string               `json:"log_level"`
	Agents                  map[string]agentPath `json:"agents"`
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
		TranscriptRetentionDays: cfg.TranscriptRetentionDays,
		TranscriptMaxBytes:      cfg.TranscriptMaxBytes.Bytes(),
		LogLevel:                cfg.LogLevel,
		Agents: map[string]agentPath{
			"claude": {Path: cfg.Agents.Claude.Path},
			"codex":  {Path: cfg.Agents.Codex.Path},
			"cursor": {Path: cfg.Agents.Cursor.Path},
		},
	})
}

func (s *Server) handleStop(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]bool{"stopping": true})
	s.deps.RequestStop()
}
