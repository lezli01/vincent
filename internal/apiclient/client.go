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

// Client talks to one vincent daemon. It is safe for concurrent use.
type Client struct {
	baseURL string
	token   string
	// rest carries request/response calls and enforces requestTimeout;
	// stream has no timeout because SSE responses never end on their own.
	rest   *http.Client
	stream *http.Client
}

// New returns a client for the daemon at baseURL (e.g. "http://127.0.0.1:7777")
// authenticating with token.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		rest:    &http.Client{Timeout: requestTimeout},
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.rest.Do(req)
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
