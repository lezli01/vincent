package trigger

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// The `type: http` source (096.5, decision 31G). A pushed event reaches
// POST /v1/triggers/{id}/events, which stays behind §13.1's bearer token like
// every other route and additionally verifies the trigger's own signature, so
// only a caller on this machine that can read {data_dir}/token *and* holds the
// shared secret can deliver. A GitHub.com webhook through a bare tunnel cannot
// add the bearer header and so cannot deliver; that is the design, not a gap.

// Headers the GitHub scheme reads.
const (
	HeaderGitHubSignature = "X-Hub-Signature-256"
	HeaderGitHubDelivery  = "X-GitHub-Delivery"
)

// Ingress refusals. The API maps each to its status; none writes a ledger
// row, because none reached the pipeline.
var (
	// ErrNotHTTP is a push to a trigger whose source is not `type: http`.
	ErrNotHTTP = errors.New("the trigger's source is not type: http")
	// ErrSignature is a missing or wrong signature, or a secret variable the
	// daemon's environment does not set — indistinguishable to the caller on
	// purpose.
	ErrSignature = errors.New("the event's signature did not verify")
)

// DisarmedError is a push to a trigger that is not armed.
type DisarmedError struct{ Reason string }

func (e *DisarmedError) Error() string { return "the trigger is not armed: " + e.Reason }

// EventError is a pushed body that is not an event.
type EventError struct{ Message string }

func (e *EventError) Error() string { return e.Message }

// VerifySignature checks a pushed body against the source's scheme. The
// comparison is constant-time, and the secret is read from the daemon's
// environment at the moment of use, never stored (§2).
func VerifySignature(sig *Signature, header http.Header, body []byte, getenv func(string) string) error {
	if sig == nil {
		return ErrSignature
	}
	switch sig.Scheme {
	case SignatureGitHubHMACSHA256:
		secret := getenv(sig.SecretEnv)
		if secret == "" {
			return ErrSignature
		}
		got, ok := strings.CutPrefix(header.Get(HeaderGitHubSignature), "sha256=")
		if !ok {
			return ErrSignature
		}
		gotSum, err := hex.DecodeString(got)
		if err != nil {
			return ErrSignature
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		if !hmac.Equal(gotSum, mac.Sum(nil)) {
			return ErrSignature
		}
		return nil
	default:
		return ErrSignature
	}
}

// SignGitHub is the `X-Hub-Signature-256` value for body under secret: what
// a sender computes, and what the gate and tests sign with.
func SignGitHub(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Ingest delivers one pushed event. The order of refusals is the route's:
// unknown id, not armed, not http, bad signature, not an event — then the
// same pipeline a poll runs, with no catch-up cap for a single push.
func (m *Manager) Ingest(ctx context.Context, id string, header http.Header, body []byte) (*Delivery, error) {
	e, err := m.entry(id)
	if err != nil {
		return nil, err
	}
	if e.Valid() && e.Def.Source.Type != SourceHTTP {
		return nil, ErrNotHTTP
	}
	if reason := m.DisarmedReason(e); reason != "" {
		return nil, &DisarmedError{Reason: reason}
	}
	d := e.Def
	if err := VerifySignature(d.Source.Signature, header, body, m.deps.Getenv); err != nil {
		return nil, err
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil || ev == nil {
		return nil, &EventError{Message: "the body must be a JSON object"}
	}
	// The reserved id (appendix A) comes from the payload, or else from the
	// delivery header GitHub sets. A non-string `id` in a vendor payload is
	// that vendor's field, not an event identity, so the header replaces it.
	if ev.ID() == "" {
		delivery := strings.TrimSpace(header.Get(HeaderGitHubDelivery))
		if delivery == "" {
			return nil, &EventError{Message: fmt.Sprintf(
				"the event has no string id and the request carries no %s header", HeaderGitHubDelivery)}
		}
		ev["id"] = delivery
	}
	unlock := m.lockTrigger(d.ID)
	defer unlock()
	return fire(ctx, m.deps.Store, m.deps.Handler, d, ev, m.deps.Now())
}
