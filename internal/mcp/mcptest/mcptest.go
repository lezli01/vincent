// Package mcptest serves the real §13.4 per-step MCP endpoint over a stub
// `/v1` mux, so an adapter test can start an agent process wired to it and
// see what the step called back with (task 057 decision 6, task 057.9).
//
// The MCP server is internal/mcp itself rather than a hand-written fake: a
// fake server would prove cmd/fakeagent's client only against its own reading
// of the protocol. What is stubbed is the route handler the server replays tool
// calls against, and only step_status's route answers — the callback the fake
// agent's `mcp-callback` scenario makes.
//
// It lives under internal/mcp rather than in internal/agent/agenttest so that
// agenttest stays light and the helper stays with the package it exercises.
// internal/mcp imports nothing under internal/agent, so the adapter tests can
// import this without a cycle. It is imported only from _test files.
package mcptest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/mcp"
)

// The step the session is opened for. A test puts TaskID and StepID in the
// agent's environment (Env) the way the engine does (§8.5).
const (
	RunID  int64 = 7
	TaskID int64 = 42
	StepID       = "build"
)

// StatusCall is one POST /v1/tasks/{id}/steps/{step_id}/status the stub mux
// received, as the route saw it.
type StatusCall struct {
	TaskID  string
	StepID  string
	Message string
}

// Server is one per-step session behind a live listener.
type Server struct {
	// MCP is what an adapter is handed in RunSpec.MCP: the `vincent` server at
	// the per-step URL, with the session's own secret.
	MCP *agent.MCPServer

	mu    sync.Mutex
	calls []StatusCall
}

// New starts the server and opens the step session; both are torn down when
// the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{}
	stub := http.NewServeMux()
	stub.HandleFunc("POST /v1/tasks/{id}/steps/{step_id}/status", s.handleStatus)
	stub.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{
			"code": "not_found", "message": "mcptest stubs step_status only",
		}})
	})
	srv := mcp.New(mcp.Deps{Handler: stub})
	sess, err := srv.OpenStep(RunID, TaskID, StepID)
	if err != nil {
		t.Fatalf("open step session: %v", err)
	}
	mux := http.NewServeMux()
	// Mounted exactly as internal/api's server.go mounts it.
	mux.Handle(mcp.StepPathPrefix+"{run_id}", srv.StepHandler())
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.CloseStep(RunID)
		ts.Close()
	})
	s.MCP = &agent.MCPServer{Name: mcp.ServerName, URL: ts.URL + sess.URLPath(), Token: sess.Secret}
	return s
}

// Env is the §8.5 addressing an agent step's environment carries.
func (s *Server) Env() []string {
	return []string{
		"VINCENT_TASK_ID=" + strconv.FormatInt(TaskID, 10),
		"VINCENT_STEP_ID=" + StepID,
	}
}

// StatusCalls returns every step_status call received so far, in order.
func (s *Server) StatusCalls() []StatusCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{
			"code": "validation_failed", "message": err.Error(),
		}})
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, StatusCall{
		TaskID:  r.PathValue("id"),
		StepID:  r.PathValue("step_id"),
		Message: body.Message,
	})
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"message": body.Message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
