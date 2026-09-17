package daemon

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/taskrun"
)

func testBridge() *stepBridge {
	return newStepBridge(func() http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestStepBridgeHostRouteIsUnchanged: a step on the host dials the daemon's
// own loopback listener, exactly as before task 062.2.
func TestStepBridgeHostRouteIsUnchanged(t *testing.T) {
	b := testBridge()
	url, release := b.endpoint(taskrun.MCPRoute{}, "127.0.0.1:7777", "/mcp/step/1")
	defer release()
	if url != "http://127.0.0.1:7777/mcp/step/1" {
		t.Errorf("url = %q", url)
	}
}

// TestStepBridgeBindsOnALocalGateway is the native-Linux path of decision 1: a
// gateway IP that is a local address gets a listener of its own, the URL names
// host.docker.internal on that listener's port, and the listener closes when
// the last step releases it.
func TestStepBridgeBindsOnALocalGateway(t *testing.T) {
	b := testBridge()
	defer b.close(t.Context())
	route := taskrun.MCPRoute{Container: true, Gateway: func() string { return "127.0.0.1" }}
	url, release := b.endpoint(route, "127.0.0.1:7777", "/mcp/step/1")
	_, release2 := b.endpoint(route, "127.0.0.1:7777", "/mcp/step/2")
	rest, ok := strings.CutPrefix(url, "http://host.docker.internal:")
	if !ok || strings.HasPrefix(rest, "7777") {
		t.Fatalf("url = %q, want host.docker.internal on the bridge's own port", url)
	}
	port, _, _ := strings.Cut(rest, "/")
	resp, err := http.Get("http://127.0.0.1:" + port + "/mcp/step/1")
	if err != nil {
		t.Fatalf("bridge not serving: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("bridge status = %d, want the step handler's", resp.StatusCode)
	}
	release()
	if _, err := net.Dial("tcp", "127.0.0.1:"+port); err != nil {
		t.Fatalf("bridge closed while a step still holds it: %v", err)
	}
	release2()
	release2() // idempotent
	b.mu.Lock()
	n := len(b.listeners)
	b.mu.Unlock()
	if n != 0 {
		t.Errorf("listeners after the last release = %d, want 0", n)
	}
}

// TestStepBridgeFallsBackToLoopback is the Docker Desktop path: a gateway IP
// that is not a local address cannot be bound, and the step dials
// host.docker.internal on the daemon's own port, which Desktop forwards to the
// host's loopback.
func TestStepBridgeFallsBackToLoopback(t *testing.T) {
	b := testBridge()
	defer b.close(t.Context())
	for _, gw := range []string{"192.0.2.1", ""} {
		route := taskrun.MCPRoute{Container: true, Gateway: func() string { return gw }}
		url, release := b.endpoint(route, "127.0.0.1:7777", "/mcp/step/1")
		release()
		if url != "http://host.docker.internal:7777/mcp/step/1" {
			t.Errorf("gateway %q: url = %q, want the loopback port", gw, url)
		}
	}
}
