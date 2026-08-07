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

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/version"
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
}

// Server is the vincent HTTP API server.
type Server struct {
	deps    Deps
	handler http.Handler
	httpSrv *http.Server
}

// New assembles the server: routes wrapped in recover → log → auth
// middleware (outermost first; phase 1 decision).
func New(deps Deps) *Server {
	s := &Server{deps: deps}
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
	rt.handle(http.MethodPost, "/v1/daemon/stop", s.handleStop)
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

// infoResponse is the GET /v1/info body (initial shape, phase 1 decision);
// agent availability joins in T1.7 additively.
type infoResponse struct {
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	Built            string `json:"built"`
	PID              int    `json:"pid"`
	StartedAt        string `json:"started_at"`
	UptimeSeconds    int64  `json:"uptime_seconds"`
	Listen           string `json:"listen"`
	MaxParallelTasks int    `json:"max_parallel_tasks"`
}

func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	cfg := s.deps.Config()
	writeJSON(w, http.StatusOK, infoResponse{
		Version:          version.Version(),
		Commit:           version.Commit(),
		Built:            version.Date(),
		PID:              os.Getpid(),
		StartedAt:        s.deps.StartedAt.UTC().Format(time.RFC3339),
		UptimeSeconds:    int64(time.Since(s.deps.StartedAt).Seconds()),
		Listen:           s.deps.ListenAddr,
		MaxParallelTasks: cfg.MaxParallelTasks,
	})
}

// configResponse mirrors config.yaml as snake_case JSON; durations render as
// Go duration strings (phase 1 decision).
type configResponse struct {
	Listen                  string               `json:"listen"`
	MaxParallelTasks        int                  `json:"max_parallel_tasks"`
	Defaults                configDefaults       `json:"defaults"`
	TranscriptRetentionDays int                  `json:"transcript_retention_days"`
	LogLevel                string               `json:"log_level"`
	Agents                  map[string]agentPath `json:"agents"`
}

type configDefaults struct {
	AgentTimeout   string `json:"agent_timeout"`
	CommandTimeout string `json:"command_timeout"`
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
		},
		TranscriptRetentionDays: cfg.TranscriptRetentionDays,
		LogLevel:                cfg.LogLevel,
		Agents: map[string]agentPath{
			"claude": {Path: cfg.Agents.Claude.Path},
			"codex":  {Path: cfg.Agents.Codex.Path},
		},
	})
}

func (s *Server) handleStop(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]bool{"stopping": true})
	s.deps.RequestStop()
}
