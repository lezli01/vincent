package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/release"
)

// The release check (task 055, spec §12.3).
//
// It is modelled on PullReconciler, which is modelled on internal/notify: it
// owns one goroutine, reads the config per tick so a hot reload governs the
// next one, and its failure policy is quiet. Offline, rate-limited and
// malformed all degrade to "no new answer this tick" at debug level, and the
// previous cached answer survives — a check that forgets what it knew because
// a laptop was on a train is worse than one that says nothing.
//
// The cache is in memory rather than in SQLite. There is no migration and a
// restart re-polls, which is the right trade for a fact whose whole value is
// that it is recent: §12.4's "persist before acting" governs task
// transitions, and this is not one.

// UpdateCheck polls for the latest stable release and caches the answer.
// The cached value is a release.Status because internal/api serves it and
// this package fills it, and neither imports the other.
type UpdateCheck struct {
	cfg    func() config.Config
	client *release.Client
	logger *slog.Logger
	now    func() time.Time

	mu     sync.Mutex
	result release.Status
}

// NewUpdateCheck builds the poller. It performs no I/O.
func NewUpdateCheck(cfg func() config.Config, client *release.Client, logger *slog.Logger) *UpdateCheck {
	return &UpdateCheck{cfg: cfg, client: client, logger: logger, now: time.Now}
}

// Run ticks until ctx is done. The interval is re-read every tick, so a hot
// reload that changes `update.poll_interval` — including to 0 — reaches the
// next one. A disabled checker still ticks on a slow heartbeat and makes no
// request, which is how it notices being switched back on without a restart.
func (u *UpdateCheck) Run(ctx context.Context) {
	const idle = time.Minute
	for {
		wait := idle
		if cfg := u.cfg(); cfg.Update.Polls() {
			u.Tick(ctx)
			wait = cfg.Update.PollInterval.Std()
		} else {
			u.setDisabled()
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Tick performs one check. It is exported so a test can drive a single pass
// without a timer, which is also what makes the "no outbound request at all"
// property assertable on this path rather than inferred from a duration.
func (u *UpdateCheck) Tick(ctx context.Context) {
	// The gate first, and it stops at the first "no" — the same shape the
	// pull reconciler's gate has. A disabled check makes no call.
	if !u.cfg().Update.Polls() || u.client == nil {
		u.setDisabled()
		return
	}
	rctx, cancel := context.WithTimeout(ctx, release.Timeout)
	defer cancel()
	rel, err := u.client.Latest(rctx)

	u.mu.Lock()
	defer u.mu.Unlock()
	u.result.Enabled = true
	if err != nil {
		// Debug, not warn: a daemon on a laptop that sleeps overnight would
		// otherwise file a warning every morning about a check nothing waits
		// on. The reason is kept on the cached result, where a human asking
		// `vincent doctor` will find it.
		u.logf("release check failed", "error", err)
		u.result.Error = err.Error()
		return
	}
	u.result.Error = ""
	u.result.Latest = rel
	u.result.CheckedAt = u.now().UTC()
}

// Result returns the cached answer.
func (u *UpdateCheck) Result() release.Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.result
}

func (u *UpdateCheck) setDisabled() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.result.Enabled = false
}

func (u *UpdateCheck) logf(msg string, args ...any) {
	if u.logger != nil {
		u.logger.Debug(msg, args...)
	}
}
