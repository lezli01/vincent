package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/taskrun"
)

// containerMCPHost is the name a containerized agent step dials the daemon by.
// Every task container is created with it mapped to the host gateway (task 061
// decision 1).
const containerMCPHost = "host.docker.internal"

// stepBridge serves §13.4's per-step endpoint on container network gateways
// (task 062.2 decision 1). The daemon's own listener is loopback-only (§13.1),
// and on native Docker Engine `host-gateway` resolves to the bridge's gateway
// IP — docker0's 172.17.0.1, say — where a 127.0.0.1 listener cannot be
// reached. So a containerized step gets a second listener, bound on that
// gateway, that answers `/mcp/step/{run_id}` and nothing else.
//
// Listeners are reference counted per gateway IP: one lives while any
// containerized step needs it and closes after the last releases it, and at
// shutdown.
type stepBridge struct {
	handler func() http.Handler
	log     *slog.Logger

	mu        sync.Mutex
	closed    bool
	listeners map[string]*bridgeListener
}

type bridgeListener struct {
	srv  *http.Server
	port int
	refs int
}

func newStepBridge(handler func() http.Handler, log *slog.Logger) *stepBridge {
	return &stepBridge{handler: handler, log: log, listeners: map[string]*bridgeListener{}}
}

// endpoint returns the per-step endpoint URL for a step reaching the daemon
// over route, and the release for any bridge listener it took. loopback is the
// daemon's own listener address; path is the session's URL path.
//
// A host step dials loopback, as it always has. A containerized one dials
// host.docker.internal on the bridge listener's port when one could be bound
// on its gateway, and on the loopback port otherwise: a gateway IP that is not
// a local address — Docker Desktop (including Docker Desktop for Linux), a
// rootless podman — fails the bind, and those runtimes forward
// host.docker.internal to the host's loopback, which is the path left.
func (b *stepBridge) endpoint(route taskrun.MCPRoute, loopback, path string) (string, func()) {
	if !route.Container {
		return "http://" + loopback + path, func() {}
	}
	_, port, err := net.SplitHostPort(loopback)
	if err != nil {
		port = loopback
	}
	release := func() {}
	if route.Gateway != nil {
		if gw := route.Gateway(); gw != "" {
			if p, rel, err := b.acquire(gw); err == nil {
				port, release = strconv.Itoa(p), rel
			} else {
				b.log.Debug("step bridge not bound; using the loopback port",
					"gateway", gw, "error", err)
			}
		}
	}
	return "http://" + net.JoinHostPort(containerMCPHost, port) + path, release
}

// errBridgeClosed refuses a listener after shutdown began.
var errBridgeClosed = errors.New("step bridge closed")

// acquire takes a reference on the listener for ip, binding it on first use.
func (b *stepBridge) acquire(ip string) (int, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, nil, errBridgeClosed
	}
	l, ok := b.listeners[ip]
	if !ok {
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
		if err != nil {
			return 0, nil, fmt.Errorf("listen on gateway %s: %w", ip, err)
		}
		srv := &http.Server{Handler: b.handler(), ReadHeaderTimeout: 10 * time.Second}
		l = &bridgeListener{srv: srv, port: ln.Addr().(*net.TCPAddr).Port}
		b.listeners[ip] = l
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				b.log.Warn("step bridge stopped", "gateway", ip, "error", err)
			}
		}()
		b.log.Info("step bridge listening", "addr", ln.Addr().String())
	}
	l.refs++
	var once sync.Once
	return l.port, func() { once.Do(func() { b.release(ip, l) }) }, nil
}

func (b *stepBridge) release(ip string, l *bridgeListener) {
	b.mu.Lock()
	l.refs--
	last := l.refs == 0 && b.listeners[ip] == l
	if last {
		delete(b.listeners, ip)
	}
	b.mu.Unlock()
	if last {
		// Close, not Shutdown: the step this served is over, and its secret
		// with it, so nothing on this listener has anything left to finish.
		_ = l.srv.Close()
	}
}

// close shuts every bridge listener and refuses new ones.
func (b *stepBridge) close(ctx context.Context) {
	b.mu.Lock()
	b.closed = true
	ls := b.listeners
	b.listeners = map[string]*bridgeListener{}
	b.mu.Unlock()
	for _, l := range ls {
		_ = l.srv.Shutdown(ctx)
	}
}
