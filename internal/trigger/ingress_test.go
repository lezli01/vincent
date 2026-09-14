package trigger

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

const hookSecret = "s3cret"

// httpDoc is the `hook` trigger: an http source signed with HOOK_SECRET.
func httpDoc(enabled bool) string {
	return fmt.Sprintf(`id: hook
enabled: %t
source:
  type: http
  project: 1
  signature:
    scheme: github_hmac_sha256
    secret_env: HOOK_SECRET
action:
  type: create_task
  title: 'push {{ .Event.id }}'
`, enabled)
}

// signed is the header a sender holding secret sets, plus a delivery id when
// one is given.
func signed(secret string, body []byte, delivery string) http.Header {
	h := http.Header{}
	h.Set(HeaderGitHubSignature, SignGitHub(secret, body))
	if delivery != "" {
		h.Set(HeaderGitHubDelivery, delivery)
	}
	return h
}

// TestIngest drives Manager.Ingest: a valid push fires through the pipeline,
// and every refusal returns its error without writing a ledger row.
func TestIngest(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.env.Store("HOOK_SECRET", hookSecret)
	h.write("hook", httpDoc(true))
	h.write("poll", commandDoc(t, "poll", true, filepath.Join(t.TempDir(), "unused.out")))

	body := []byte(`{"id":"d1","ref":"main"}`)
	del, err := h.m.Ingest(ctx, "hook", signed(hookSecret, body, ""), body)
	if err != nil || del.Outcome != store.DeliveryFired || del.EventID != "d1" || del.TaskID == nil {
		t.Fatalf("Ingest = %+v, %v", del, err)
	}
	if reqs := h.api.requests(); len(reqs) != 1 || reqs[0].body.Title != "push d1" {
		t.Errorf("replays = %+v", reqs)
	}
	if evs := h.events(store.EventTriggerFired); len(evs) != 1 {
		t.Errorf("trigger.fired = %+v", evs)
	}

	// A vendor's non-string id is its own field; the delivery header names
	// the event instead, and the same delivery again is deduped.
	vendor := []byte(`{"id":5,"action":"completed"}`)
	del, err = h.m.Ingest(ctx, "hook", signed(hookSecret, vendor, "guid-1"), vendor)
	if err != nil || del.Outcome != store.DeliveryFired || del.EventID != "guid-1" || del.DedupeKey != "guid-1" {
		t.Fatalf("Ingest(header id) = %+v, %v", del, err)
	}
	del, err = h.m.Ingest(ctx, "hook", signed(hookSecret, vendor, "guid-1"), vendor)
	if err != nil || del.Outcome != store.DeliveryDeduped {
		t.Errorf("Ingest(same delivery) = %+v, %v", del, err)
	}

	rows := len(h.ledger("hook"))
	noRow := func(t *testing.T) {
		t.Helper()
		if n := len(h.ledger("hook")); n != rows {
			t.Errorf("a refusal wrote %d ledger rows", n-rows)
		}
	}
	isSignature := func(err error) bool { return errors.Is(err, ErrSignature) }
	isEvent := func(err error) bool {
		var ee *EventError
		return errors.As(err, &ee)
	}
	isDisarmed := func(reason string) func(error) bool {
		return func(err error) bool {
			var de *DisarmedError
			return errors.As(err, &de) && strings.Contains(de.Reason, reason)
		}
	}
	malformed := http.Header{}
	malformed.Set(HeaderGitHubSignature, "sha256=not-hex")
	unprefixed := http.Header{}
	unprefixed.Set(HeaderGitHubSignature, strings.TrimPrefix(SignGitHub(hookSecret, body), "sha256="))
	noID := []byte(`{"action":"completed"}`)
	list := []byte(`[1,2]`)

	for _, tc := range []struct {
		name   string
		setup  func() (undo func())
		id     string
		header http.Header
		body   []byte
		want   func(error) bool
	}{
		{"wrong secret", nil, "hook", signed("wrong", body, ""), body, isSignature},
		{"missing header", nil, "hook", http.Header{}, body, isSignature},
		{"signature not hex", nil, "hook", malformed, body, isSignature},
		{"signature without its scheme prefix", nil, "hook", unprefixed, body, isSignature},
		{"body changed after signing", nil, "hook", signed(hookSecret, body, ""), []byte(`{"id":"d2","ref":"main"}`), isSignature},
		{
			"secret variable unset",
			func() func() { h.env.Delete("HOOK_SECRET"); return func() { h.env.Store("HOOK_SECRET", hookSecret) } },
			"hook", signed(hookSecret, body, ""), body, isSignature,
		},
		{"no string id and no delivery header", nil, "hook", signed(hookSecret, noID, ""), noID, isEvent},
		{"not a JSON object", nil, "hook", signed(hookSecret, list, ""), list, isEvent},
		{"unknown trigger", nil, "nope", signed(hookSecret, body, ""), body, func(err error) bool { return errors.Is(err, ErrNotFound) }},
		{"not type: http", nil, "poll", signed(hookSecret, body, ""), body, func(err error) bool { return errors.Is(err, ErrNotHTTP) }},
		{
			"trigger disabled, checked before the signature",
			func() func() {
				h.write("hook", httpDoc(false))
				return func() { h.write("hook", httpDoc(true)) }
			},
			"hook", signed("wrong", body, ""), body, isDisarmed("the trigger is disabled"),
		},
		{
			"triggers.enabled off",
			func() func() { h.enabled.Store(false); return func() { h.enabled.Store(true) } },
			"hook", signed(hookSecret, body, ""), body, isDisarmed("triggers.enabled"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				defer tc.setup()()
			}
			del, err := h.m.Ingest(ctx, tc.id, tc.header, tc.body)
			if del != nil || !tc.want(err) {
				t.Errorf("Ingest = %+v, %v", del, err)
			}
			noRow(t)
		})
	}
}
