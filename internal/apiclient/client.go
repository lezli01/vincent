package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/daemon"
)

// requestTimeout bounds plain REST calls. Everything here talks to loopback,
// so a slow response means a wedged daemon, not a slow network.
const requestTimeout = 10 * time.Second

// probeTimeout bounds the two calls whose server-side work is not loopback at
// all: GET /v1/doctor with probe=true and GET /v1/agents?refresh=true both ask
// the daemon to spawn an agent CLI per adapter (§9.5, §9.6). The daemon walks
// the adapters serially and each subprocess carries the adapter's own deadline
// — 45 s for claude (--version plus --help), 40 s for codex (--version plus
// `login status`), 60 s for cursor, whose model catalog is an authenticated
// network call — so the honest ceiling for these two is that sum, not loopback
// latency.
//
// It has to *exceed* the sum rather than merely be generous. The report is the
// thing that names which adapter hung; a client that gives up first replaces
// that diagnosis with "context deadline exceeded", which is the one answer
// `vincent doctor` must never give. Every other call keeps requestTimeout: a
// wedged daemon is still caught in ten seconds everywhere it means anything.
const probeTimeout = 3 * time.Minute

// EnvTimeoutScale names the factor the test suite sets on a loaded CI leg
// (#731, internal/testutil/wait) so the budgets its tests inherit from here —
// requestTimeout, and `vincent daemon start`'s health poll — stretch with
// every other wait in the suite. The e2e tests drive the real binary, so the
// factor has to be read by production code to reach them at all. Nothing
// outside the test suite sets it; unset, every default stands.
const EnvTimeoutScale = "VINCENT_TEST_TIMEOUT_SCALE"

// MaxTimeoutScale caps EnvTimeoutScale's factor: past it a budget stops
// meaning anything, and a large enough factor overflows a Duration.
const MaxTimeoutScale = 100

// ScaleTimeout is d multiplied by EnvTimeoutScale's factor, capped at
// MaxTimeoutScale, or d itself when the variable is unset or not a positive
// number. NaN is not one: it passes every ordered comparison's negation and
// would turn d into zero, which is no timeout at all.
func ScaleTimeout(d time.Duration) time.Duration {
	f, err := strconv.ParseFloat(os.Getenv(EnvTimeoutScale), 64)
	if err != nil || math.IsNaN(f) || f <= 0 {
		return d
	}
	return time.Duration(float64(d) * min(f, MaxTimeoutScale))
}

// Client talks to one vincent daemon. It is safe for concurrent use.
type Client struct {
	baseURL string
	token   string
	// rest carries request/response calls and enforces requestTimeout;
	// probes carries the adapter-probing calls and enforces probeTimeout;
	// stream has no timeout because SSE responses never end on their own.
	rest   *http.Client
	probes *http.Client
	stream *http.Client
}

// New returns a client for the daemon at baseURL (e.g. "http://127.0.0.1:7777")
// authenticating with token.
//
// When the process runs as a workflow step or a chat agent — VINCENT_TASK_ID
// or VINCENT_CHAT_ID is in its environment — every request carries the
// matching marker header, and the daemon treats it as an agent's (task
// 130.10 decisions 1-3): it may not change the state of an issue that
// writes back to GitHub. The marker never makes a caller more than it is.
func New(baseURL, token string) *Client {
	rt := http.DefaultTransport
	if h := markerHeaders(os.Getenv); len(h) > 0 {
		rt = markerTransport{base: rt, headers: h}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		rest:    &http.Client{Timeout: ScaleTimeout(requestTimeout), Transport: rt},
		probes:  &http.Client{Timeout: probeTimeout, Transport: rt},
		stream:  &http.Client{Transport: rt},
	}
}

// The agent marker headers and the environment variables they come from.
// internal/api spells the header names too; the live test holds the two
// together.
const (
	HeaderTaskMarker = "X-Vincent-Task-Id"
	HeaderChatMarker = "X-Vincent-Chat-Id"
	// EnvChatID is what internal/chatrun puts in a chat agent's
	// environment; VINCENT_TASK_ID is §8.5's, in every step's.
	EnvChatID = "VINCENT_CHAT_ID"
	envTaskID = "VINCENT_TASK_ID"
)

// markerHeaders is the marker headers getenv's environment calls for.
func markerHeaders(getenv func(string) string) map[string]string {
	h := map[string]string{}
	if v := strings.TrimSpace(getenv(envTaskID)); v != "" {
		h[HeaderTaskMarker] = v
	}
	if v := strings.TrimSpace(getenv(EnvChatID)); v != "" {
		h[HeaderChatMarker] = v
	}
	return h
}

// markerTransport adds the marker headers to every request, on a clone: a
// RoundTripper must not modify the request it is handed.
type markerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t markerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}

// Discover builds a client from the daemon's on-disk discovery records:
// daemon.json for the port and the token file for auth (§12.2). It does not
// verify the daemon is actually reachable — callers health-check separately.
func Discover(dataDir string) (*Client, error) {
	ri, err := daemon.ReadRuntimeInfo(dataDir)
	if err != nil {
		return nil, fmt.Errorf("discover daemon: %w", err)
	}
	token, err := daemon.ReadToken(dataDir)
	if err != nil {
		return nil, fmt.Errorf("read api token: %w", err)
	}
	return New(fmt.Sprintf("http://127.0.0.1:%d", ri.Port), token), nil
}

// BaseURL reports the daemon address this client targets.
func (c *Client) BaseURL() string { return c.baseURL }

// Error is the §13.1 error envelope as a Go error. Status is the HTTP
// status; Code is the stable snake_case code clients may branch on.
type Error struct {
	Status  int
	Code    string
	Message string
	// Details carries the envelope's string-valued details — the ones a
	// client branches on (`state`, `reason`).
	Details map[string]string
	// RawDetails is every details value verbatim, objects included: an
	// issue_changed 409 carries the current issue there (task 130.3).
	RawDetails map[string]json.RawMessage
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s (%s, http %d)", e.Message, e.Code, e.Status)
}

// Health is the GET /v1/health response body.
type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// Health calls the daemon's unauthenticated health endpoint.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	if err := c.get(ctx, "/v1/health", &h); err != nil {
		return Health{}, err
	}
	return h, nil
}

// get performs an authenticated GET and decodes the JSON response into out.
// Non-2xx responses come back as *Error.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.getVia(ctx, c.rest, path, out)
}

// getVia is get over a caller-chosen http.Client, so a request whose cost is
// the daemon's adapter probes can be held to probeTimeout instead of the
// loopback deadline the rest of the surface is held to.
func (c *Client) getVia(ctx context.Context, hc *http.Client, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// decodeError maps a non-2xx response onto *Error, falling back to a bare
// status when the body is not the §13.1 envelope.
func decodeError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	var envelope struct {
		Error struct {
			Code    string                     `json:"code"`
			Message string                     `json:"message"`
			Details map[string]json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != "" {
		e := &Error{
			Status:     resp.StatusCode,
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
			RawDetails: envelope.Error.Details,
		}
		for k, raw := range envelope.Error.Details {
			var v string
			if json.Unmarshal(raw, &v) == nil {
				if e.Details == nil {
					e.Details = map[string]string{}
				}
				e.Details[k] = v
			}
		}
		return e
	}
	return &Error{
		Status:  resp.StatusCode,
		Code:    "unexpected_response",
		Message: fmt.Sprintf("unexpected status %s", resp.Status),
	}
}

// maxErrorBytes bounds an error body's read. It is not 4 KiB because a 409
// may carry the current issue in its details, body and all (task 130.3), and
// an issue's body is bounded at 64 KiB before JSON escaping.
const maxErrorBytes = 1 << 20

// probeClient picks the deadline a request is held to: probeTimeout when the
// daemon will spawn an agent CLI per adapter to answer it, requestTimeout when
// it answers from its §9.6 cache and the call really is loopback-fast.
func (c *Client) probeClient(probing bool) *http.Client {
	if probing {
		return c.probes
	}
	return c.rest
}
