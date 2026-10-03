package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/lezli01/vincent/internal/store"
)

// Idempotency keys (§13.1, amended 2026-08-28, task 040, issue #146).
//
// `POST /v1/tasks` was the only route in §13.2 where a replayed request
// produced a second side effect, until `POST /v1/issues` joined it (task
// 130.3). Everything else is already safe: the §6 action routes are a
// compare-and-swap on the state the request read (amended 2026-08-24),
// `POST /v1/projects` refuses an already-registered path, and the PATCH and
// DELETE routes are desired-state operations. So this is one header on two
// routes, sharing every rule below.
const (
	// idempotencyHeader is the request header a client sets to make a create
	// replayable. It is optional: a request without it behaves exactly as it
	// did before this existed.
	idempotencyHeader = "Idempotency-Key"
	// idempotencyReasonReused is the `details.reason` of the 409 a key reused
	// with a different body gets. It is not a new error *code*: §13.1 fixes
	// every 409 at CodeInvalidState with the specific reason in details, and
	// docs/reference/api.md publishes that rule, so the reason travels where
	// every other 409 reason travels.
	idempotencyReasonReused = "idempotency_key_reused"
)

// idempotencyRoute is the `path` half of the `(method, path, key)` scope. It is
// the route, not r.URL.Path, so the scope cannot be widened by a client
// appending a trailing slash or a query string.
const idempotencyRoute = "/v1/tasks"

// idempotencyIssueRoute is the second route that honours the header
// (task 130.3): POST /v1/issues, whose keys live in their own table (0037)
// under the same digest, window and replay rule.
const idempotencyIssueRoute = "/v1/issues"

// readIdempotencyKey returns the request's Idempotency-Key, "" when it carries
// none, and false when it carries one that is not usable — in which case the
// 400 has already been written.
//
// The key is bounded and required to be printable ASCII for the reason every
// §13.1 field bound exists: it is persisted, and it is compared byte for byte
// on a later request, so a control character or a truncated multi-byte rune in
// it is a client bug that would silently never match again.
func readIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(idempotencyHeader)
	if key == "" {
		return "", true
	}
	if msg := boundString(idempotencyHeader, key, maxIdempotencyKeyBytes); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return "", false
	}
	for i := range len(key) {
		if key[i] < 0x20 || key[i] > 0x7e {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("%s must be printable ASCII (byte %d is 0x%02x)",
					idempotencyHeader, i, key[i]))
			return "", false
		}
	}
	return key, true
}

// idempotencyDigest is the canonical digest of a decoded request.
//
// It hashes the **decoded struct re-marshalled**, not the bytes as they
// arrived, so whitespace and JSON key order cannot manufacture a conflict out
// of two sends of the same request. Callers take it *before* any server-side
// mutation of the decoded value — the pull-request prefill in particular reads
// a live pull request (§13.2, task 064), and hashing after it would turn an
// edited title into a spurious 409 on a request the caller sent identically
// twice.
func idempotencyDigest(v any) (string, error) {
	// Go marshals map keys in sorted order, so the `fields` map is canonical
	// here without any help.
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("digest request: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// replayTaskCreate answers a create whose key has already been recorded, and
// reports whether it did. false means the key is new and the caller should do
// the work.
//
// A key recorded against a *different* request digest is a 409 — the client
// reused a key for a second operation, which is a client bug the daemon must
// not paper over by creating a task under a key that names another one.
//
// A key recorded against the same digest replays the task it names. The body is
// rendered from the task **as it is now**, not from a stored copy of the
// original response: storing the rendered JSON would put a task's workflow
// snapshot under a 4 MiB body bound (§13.1) into a table that grows with every
// create. The consequence is visible and deliberate — a task the scheduler has
// since admitted replays as `state: running` under a `201`. That is the honest
// answer: the task exists, and this is it.
func (s *Server) replayTaskCreate(w http.ResponseWriter, r *http.Request, key, sha string) bool {
	rec, done := s.recordedKey(w, r, key, sha, idempotencyRoute, s.deps.Store.GetIdempotencyKey)
	if rec == nil {
		return done
	}
	task, err := s.deps.Store.GetTask(r.Context(), rec.TaskID)
	if err != nil {
		// Unreachable in a consistent database: the key row carries
		// ON DELETE CASCADE, so a destroyed task takes its key with it and
		// this lookup is never reached for a task that is gone.
		s.internalError(w, "get task for idempotent replay", err)
		return true
	}
	resp := toTaskResponse(task, s.snaps.get(task.ID, task.WorkflowSnapshot))
	s.overlayLiveIssue(r.Context(), &resp, task)
	writeJSON(w, http.StatusCreated, resp)
	return true
}

// replayIssueCreate is replayTaskCreate for POST /v1/issues: the issue is
// rendered as it is now, under a 201.
func (s *Server) replayIssueCreate(w http.ResponseWriter, r *http.Request, key, sha string) bool {
	rec, done := s.recordedKey(w, r, key, sha, idempotencyIssueRoute, s.deps.Store.GetIssueIdempotencyKey)
	if rec == nil {
		return done
	}
	iss, err := s.deps.Store.GetIssue(r.Context(), rec.IssueID)
	if err != nil {
		// Unreachable for the same reason: the key cascades with its issue.
		s.internalError(w, "get issue for idempotent replay", err)
		return true
	}
	writeJSON(w, http.StatusCreated, s.renderIssue(r.Context(), iss))
	return true
}

// recordedKey is the lookup both replays share. It returns the recorded key
// when it matches sha and the caller should render a replay. Otherwise rec is
// nil and done says whether a response was already written — the 409 for a
// key reused with a different body, or a 500 — or, false, that the key is
// new and the caller should do the work.
func (s *Server) recordedKey(w http.ResponseWriter, r *http.Request, key, sha, route string,
	get func(ctx context.Context, method, path, key string) (*store.IdempotencyKey, error),
) (rec *store.IdempotencyKey, done bool) {
	rec, err := get(r.Context(), r.Method, route, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false
	}
	if err != nil {
		s.internalError(w, "get idempotency key", err)
		return nil, true
	}
	if rec.RequestSHA != sha {
		writeConflict(w,
			fmt.Sprintf("%s %q was already used for a different request",
				idempotencyHeader, key),
			map[string]string{"reason": idempotencyReasonReused})
		return nil, true
	}
	return rec, true
}
