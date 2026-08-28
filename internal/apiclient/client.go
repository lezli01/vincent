package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		rest:    &http.Client{Timeout: requestTimeout},
		probes:  &http.Client{Timeout: probeTimeout},
		stream:  &http.Client{},
	}
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
	Details map[string]string
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var envelope struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != "" {
		return &Error{
			Status:  resp.StatusCode,
			Code:    envelope.Error.Code,
			Message: envelope.Error.Message,
			Details: envelope.Error.Details,
		}
	}
	return &Error{
		Status:  resp.StatusCode,
		Code:    "unexpected_response",
		Message: fmt.Sprintf("unexpected status %s", resp.Status),
	}
}

// probeClient picks the deadline a request is held to: probeTimeout when the
// daemon will spawn an agent CLI per adapter to answer it, requestTimeout when
// it answers from its §9.6 cache and the call really is loopback-fast.
func (c *Client) probeClient(probing bool) *http.Client {
	if probing {
		return c.probes
	}
	return c.rest
}
