package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// ServerName is the MCP server's advertised name. It is load-bearing beyond
// display: claude derives a tool's namespaced name from it, so every tool here
// is `mcp__vincent__<tool>` to a claude step — which is what §9.4's restricted
// allow-list matches on.
const ServerName = "vincent"

// Deps are what the MCP server needs from the daemon.
type Deps struct {
	// Handler is the `/v1` route handler — the mux *before* internal/api's
	// auth middleware. Every tool call is replayed against it (decision 3).
	Handler http.Handler
	// Logger receives tool-call and session logs.
	Logger *slog.Logger
	// Broker feeds the wait tool. Nil is tolerated: the wait tool then
	// refuses rather than blocking on a channel nothing publishes to.
	Broker *events.Broker
	// Store answers the wait tool's state reads and its deadlock check. Nil
	// is tolerated on the same terms as Broker.
	Store *store.Store
	// Config returns the current effective configuration, for the §11 caps
	// the deadlock check consults.
	Config func() config.Config
	// Version is the daemon version, advertised to clients.
	Version string
	// Now is the clock, injectable for tests. Nil means time.Now.
	Now func() time.Time
}

// Server serves MCP over streamable HTTP. It owns no listener: internal/api
// mounts its handlers on the one that already exists.
type Server struct {
	deps    Deps
	mcp     *sdk.Server
	http    http.Handler
	steps   *stepRegistry
	stepMCP http.Handler
	// granted holds the step servers of granted handoffs, one per set of
	// granted pull tools, keyed by their tool names; built on first use.
	grantedMu sync.Mutex
	granted   map[string]*sdk.Server
}

// New builds the MCP server and registers every tool.
func New(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.Config == nil {
		deps.Config = config.Default
	}
	s := &Server{deps: deps, steps: newStepRegistry(), granted: map[string]*sdk.Server{}}
	s.mcp = s.newSDKServer(nil)

	getServer := func(*http.Request) *sdk.Server { return s.mcp }
	s.http = sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{Logger: deps.Logger})
	// The SDK's DNS-rebinding guard refuses a request that reached a loopback
	// listener under a non-loopback Host. A containerized step on Docker
	// Desktop does exactly that: it dials host.docker.internal, which Desktop
	// forwards to the daemon's loopback port (task 062.2 decision 1), and the
	// guard answered 403. The guard is redundant here — StepHandler has
	// already authenticated the run's own secret, which no rebinding page
	// holds — so it is off for this endpoint and stays on for `/mcp`.
	s.stepMCP = sdk.NewStreamableHTTPHandler(s.stepServer, &sdk.StreamableHTTPOptions{
		Logger:                     deps.Logger,
		DisableLocalhostProtection: true,
	})
	return s
}

// newSDKServer builds one MCP server: every tool in routes, the wait tool,
// and extra — the granted pull writes of a handoff's step endpoint.
func (s *Server) newSDKServer(extra []Route) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{
		Name:    ServerName,
		Title:   "vincent",
		Version: s.deps.Version,
		Description: "Drive the vincent daemon: projects, workflows, tasks, " +
			"transcripts, diffs, and the human actions a task can be waiting on.",
	}, nil)
	for _, r := range append(slices.Clone(routes), extra...) {
		srv.AddTool(&sdk.Tool{
			Name:        r.Tool,
			Description: r.Description,
			InputSchema: inputSchema(r),
		}, s.routeHandler(r))
	}
	srv.AddTool(&sdk.Tool{
		Name:        WaitTool,
		Description: waitDescription,
		InputSchema: waitSchema(),
	}, s.handleWait)
	return srv
}

// stepServer picks the server a step endpoint's session talks to (task
// 137.7): the shared tool set, plus — when the step's task is a live
// handoff with a non-empty grant — each Granted pull write whose capability
// the grant names, so tools/list shows what the guard will honour. It is
// read when a session opens; a grant cannot change after its handoff is
// created, and a handoff that settles mid-session is refused by the guard.
func (s *Server) stepServer(r *http.Request) *sdk.Server {
	extra := s.grantedRoutes(r.Context())
	if len(extra) == 0 {
		return s.mcp
	}
	names := make([]string, len(extra))
	for i, g := range extra {
		names[i] = g.Tool
	}
	key := strings.Join(names, ",")
	s.grantedMu.Lock()
	defer s.grantedMu.Unlock()
	srv, ok := s.granted[key]
	if !ok {
		srv = s.newSDKServer(extra)
		s.granted[key] = srv
	}
	return srv
}

// grantedRoutes is the Granted pull writes the calling step's handoff was
// granted: none on a step whose task is no handoff's, or whose handoff is
// not live or was granted nothing.
func (s *Server) grantedRoutes(ctx context.Context) []Route {
	sess, ok := stepFrom(ctx)
	if !ok || sess.TaskID == 0 || s.deps.Store == nil {
		return nil
	}
	h, err := s.deps.Store.GetHandoffByTask(ctx, sess.TaskID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.deps.Logger.Warn("mcp: step handoff not read", "task", sess.TaskID, "error", err)
		}
		return nil
	}
	if h.State != store.HandoffLive {
		return nil
	}
	grant := h.Grant()
	var out []Route
	for _, g := range Granted {
		if slices.Contains(grant, g.Capability) {
			out = append(out, g.Route)
		}
	}
	return out
}

// routeHandler adapts one route to a tool handler.
func (s *Server) routeHandler(r Route) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var raw json.RawMessage
		if req != nil && req.Params != nil {
			raw = req.Params.Arguments
		}
		return s.dispatch(ctx, r, raw)
	}
}

func (s *Server) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

// Handler serves the shared `/mcp` endpoint. Authentication has already
// happened in internal/api's chain, with the same §13.1 bearer token every
// other client presents.
func (s *Server) Handler() http.Handler { return s.http }

// StepHandler serves `/mcp/step/{run_id}`: the per-step endpoint the daemon
// wires an agent step to (decision 6). It authenticates the run's own secret
// rather than the daemon token, and tags the request context with the calling
// step so the wait tool can refuse a self-blocking wait.
//
// It is not a security boundary (doc.go, spec §16). A full-auto agent can read
// `{data_dir}/token` and reach `/mcp` directly; this exists to make the wait
// tool correct, and to scope what a step's tool calls are attributed to.
func (s *Server) StepHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.steps.authenticate(r)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(
				`{"error":{"code":"unauthorized","message":"missing or invalid step token"}}`))
			return
		}
		s.stepMCP.ServeHTTP(w, r.WithContext(withStep(r.Context(), sess)))
	})
}

// OpenStep mints a session for one step run and returns it. Close it with
// CloseStep when the step ends: the secret dies with the step run, so a
// leaked endpoint is inert as soon as the step it belonged to is over.
func (s *Server) OpenStep(runID, taskID int64, stepID string) (StepSession, error) {
	return s.steps.open(runID, taskID, stepID)
}

// CloseStep retires a step run's session. Idempotent.
func (s *Server) CloseStep(runID int64) { s.steps.close(runID) }
