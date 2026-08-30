package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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
}

// New builds the MCP server and registers every tool.
func New(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	if deps.Config == nil {
		deps.Config = config.Default
	}
	s := &Server{deps: deps, steps: newStepRegistry()}
	s.mcp = sdk.NewServer(&sdk.Implementation{
		Name:    ServerName,
		Title:   "vincent",
		Version: deps.Version,
		Description: "Drive the vincent daemon: projects, workflows, tasks, " +
			"transcripts, diffs, and the human actions a task can be waiting on.",
	}, nil)
	for _, r := range routes {
		s.mcp.AddTool(&sdk.Tool{
			Name:        r.Tool,
			Description: r.Description,
			InputSchema: inputSchema(r),
		}, s.routeHandler(r))
	}
	s.mcp.AddTool(&sdk.Tool{
		Name:        WaitTool,
		Description: waitDescription,
		InputSchema: waitSchema(),
	}, s.handleWait)

	getServer := func(*http.Request) *sdk.Server { return s.mcp }
	s.http = sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{Logger: deps.Logger})
	s.stepMCP = sdk.NewStreamableHTTPHandler(getServer, &sdk.StreamableHTTPOptions{Logger: deps.Logger})
	return s
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
