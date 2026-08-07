package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// HealthInfo is the /v1/health response body.
type HealthInfo struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// httpClient keeps lifecycle requests snappy: everything here talks to
// loopback, so a slow response means a wedged daemon, not a slow network.
var httpClient = &http.Client{Timeout: 2 * time.Second}

// CheckHealth calls the daemon's unauthenticated health endpoint.
func CheckHealth(ctx context.Context, port int) (HealthInfo, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/health", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return HealthInfo{}, fmt.Errorf("build health request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return HealthInfo{}, fmt.Errorf("health check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return HealthInfo{}, fmt.Errorf("health check: unexpected status %s", resp.Status)
	}
	var h HealthInfo
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return HealthInfo{}, fmt.Errorf("decode health response: %w", err)
	}
	return h, nil
}

// RequestStop asks the daemon to shut down gracefully via
// POST /v1/daemon/stop (phase 1 spec addition). It returns once the daemon
// has accepted the request; the caller waits for the lock to release.
func RequestStop(ctx context.Context, port int, token string) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/daemon/stop", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("build stop request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stop request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("stop request: unexpected status %s: %s", resp.Status, body)
	}
	return nil
}

// KillPID force-kills a daemon process, the `daemon stop --force` fallback.
func KillPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := p.Kill(); err != nil {
		return fmt.Errorf("kill process %d: %w", pid, err)
	}
	return nil
}
