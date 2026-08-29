package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// StepSession identifies the agent step that opened an MCP connection
// (decision 6). It travels out of band — in the URL the daemon wired into the
// agent's own MCP configuration — so the agent does not have to cooperate to
// be identified, and cannot claim to be a different step by asking.
type StepSession struct {
	RunID  int64
	TaskID int64
	StepID string
	// Secret is the bearer token for this run's endpoint. It exists for one
	// step run and is forgotten when the step ends.
	Secret string
}

// URLPath is the endpoint this session is reached at, relative to the API
// root: `/mcp/step/{run_id}`.
func (s StepSession) URLPath() string {
	return StepPathPrefix + strconv.FormatInt(s.RunID, 10)
}

// StepPathPrefix is the per-step endpoint's path prefix. internal/api mounts
// `{run_id}` under it.
const StepPathPrefix = "/mcp/step/"

type stepRegistry struct {
	mu   sync.RWMutex
	byID map[int64]StepSession
}

func newStepRegistry() *stepRegistry {
	return &stepRegistry{byID: map[int64]StepSession{}}
}

func (reg *stepRegistry) open(runID, taskID int64, stepID string) (StepSession, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return StepSession{}, fmt.Errorf("mint step token: %w", err)
	}
	sess := StepSession{
		RunID:  runID,
		TaskID: taskID,
		StepID: stepID,
		Secret: base64.RawURLEncoding.EncodeToString(buf),
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.byID[runID] = sess
	return sess, nil
}

func (reg *stepRegistry) close(runID int64) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	delete(reg.byID, runID)
}

// authenticate resolves a request on the per-step endpoint to its session.
// The run id in the path picks the session; the bearer token proves it.
func (reg *stepRegistry) authenticate(r *http.Request) (StepSession, bool) {
	rest := strings.TrimPrefix(r.URL.Path, StepPathPrefix)
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" || strings.Contains(rest, "/") {
		return StepSession{}, false
	}
	runID, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return StepSession{}, false
	}
	reg.mu.RLock()
	sess, ok := reg.byID[runID]
	reg.mu.RUnlock()
	if !ok {
		return StepSession{}, false
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return StepSession{}, false
	}
	if subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(sess.Secret)) != 1 {
		return StepSession{}, false
	}
	return sess, true
}

type stepCtxKey struct{}

func withStep(ctx context.Context, sess StepSession) context.Context {
	return context.WithValue(ctx, stepCtxKey{}, sess)
}

// stepFrom returns the step session a request arrived on, if it arrived on the
// per-step endpoint at all. A call on the shared `/mcp` endpoint has none, and
// the wait tool's deadlock refusal does not apply to it: nothing there holds a
// §11 slot.
func stepFrom(ctx context.Context) (StepSession, bool) {
	sess, ok := ctx.Value(stepCtxKey{}).(StepSession)
	return sess, ok
}

// CreatorTaskID returns the task whose agent step is making this call, when
// the call arrived on the per-step endpoint (§13.4, task 057 decision 7).
//
// It is how internal/api learns a task's MCP provenance without a header a
// client could forge: a tool call is replayed against the mux with the *same*
// context the MCP session put the step identity on, so the value cannot be
// reached from a real HTTP request at all.
func CreatorTaskID(ctx context.Context) (int64, bool) {
	sess, ok := stepFrom(ctx)
	if !ok || sess.TaskID == 0 {
		return 0, false
	}
	return sess.TaskID, true
}
