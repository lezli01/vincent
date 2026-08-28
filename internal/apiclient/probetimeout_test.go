package apiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The two adapter-probing calls are not loopback calls, and holding them to
// the loopback deadline is what made `vincent doctor` answer "context deadline
// exceeded" on a machine whose `cursor-agent` needs a few seconds to answer:
// GET /v1/doctor?probe=true and GET /v1/agents?refresh=true both make the
// daemon spawn an agent CLI per adapter (§9.5, §9.6), and the adapters' own
// deadlines already sum to more than two minutes.
//
// The three legs below are the whole contract: the two probing calls survive a
// response that takes longer than requestTimeout, and the same call *without*
// probing does not — because that one really is served from the cache, and a
// daemon that cannot answer it in ten seconds is wedged.
func TestProbingCallsOutliveTheLoopbackDeadline(t *testing.T) {
	// Comfortably past requestTimeout, comfortably short of probeTimeout: the
	// test is over in about this long, not in three minutes.
	const slack = 2 * time.Second
	delay := requestTimeout + slack

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			// The client gave up. Returning promptly keeps Close from
			// waiting out the delay on a request nobody is reading.
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/agents" {
			_, _ = io.WriteString(w, `{"agents":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer ts.Close()

	c := New(ts.URL, "test-token")
	ctx := context.Background()

	var (
		wg                             sync.WaitGroup
		doctorErr, agentsErr, cacheErr error
	)
	wg.Add(3)
	go func() { defer wg.Done(); _, doctorErr = c.Doctor(ctx, true) }()
	go func() { defer wg.Done(); _, agentsErr = c.ListAgents(ctx, true) }()
	go func() { defer wg.Done(); _, cacheErr = c.Doctor(ctx, false) }()
	wg.Wait()

	if doctorErr != nil {
		t.Errorf("Doctor(probe=true) after %s: %v, want it to wait for the probes", delay, doctorErr)
	}
	if agentsErr != nil {
		t.Errorf("ListAgents(refresh=true) after %s: %v, want it to wait for the probes", delay, agentsErr)
	}
	if !errors.Is(cacheErr, context.DeadlineExceeded) {
		t.Errorf("Doctor(probe=false) after %s: %v, want the loopback deadline to still apply", delay, cacheErr)
	}
}
