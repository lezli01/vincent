package api

import (
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
// `POST /v1/tasks` is the only route in §13.2 where a replayed request produces
// a second side effect. Everything else is already safe: the §6 action routes
// are a compare-and-swap on the state the request read (amended 2026-08-24),
// `POST /v1/projects` refuses an already-registered path, and the PATCH and
// DELETE routes are desired-state operations. So this is one header on one
// route, even though the table is keyed to take more later.
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
// mutation of the decoded value — the GitHub issue prefill in particular reads
// a live issue (§13.2, task 035), and hashing after it would turn an edited
// issue title into a spurious 409 on a request the caller sent identically
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
	rec, err := s.deps.Store.GetIdempotencyKey(r.Context(), r.Method, idempotencyRoute, key)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		s.internalError(w, "get idempotency key", err)
		return true
	}
	if rec.RequestSHA != sha {
		writeConflict(w,
			fmt.Sprintf("%s %q was already used for a different request",
				idempotencyHeader, key),
			map[string]string{"reason": idempotencyReasonReused})
		return true
	}
	task, err := s.deps.Store.GetTask(r.Context(), rec.TaskID)
	if err != nil {
		// Unreachable in a consistent database: the key row carries
		// ON DELETE CASCADE, so a destroyed task takes its key with it and
		// this lookup is never reached for a task that is gone.
		s.internalError(w, "get task for idempotent replay", err)
		return true
	}
	writeJSON(w, http.StatusCreated, toTaskResponse(task, s.snaps.get(task.ID, task.WorkflowSnapshot)))
	return true
}
