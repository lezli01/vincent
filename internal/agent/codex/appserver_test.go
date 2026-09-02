package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// fixtureResult returns the `result` payload of the captured 0.150.1
// `account/rateLimits/read` response, optionally rewritten.
//
// The file on disk is the verbatim response line, so every variant below is
// derived from real bytes rather than typed out: a test that hand-wrote the
// shape would keep passing after the vendor changed it.
func fixtureResult(t *testing.T, rewrite func(map[string]json.RawMessage)) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "app_server_ratelimits_0.150.1.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatalf("parse fixture envelope: %v", err)
	}
	if rewrite == nil {
		return envelope.Result
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Result, &fields); err != nil {
		t.Fatalf("parse fixture result: %v", err)
	}
	rewrite(fields)
	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("re-encode fixture result: %v", err)
	}
	return out
}

// swapUsedPercent rewrites every `"usedPercent":28` in raw, so a variant can
// tell the two duplicate shapes apart by their numbers.
func swapUsedPercent(raw json.RawMessage, to string) json.RawMessage {
	return json.RawMessage(strings.ReplaceAll(string(raw), `"usedPercent":28`, `"usedPercent":`+to))
}

func TestParseRateLimits(t *testing.T) {
	reported := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	// The captured epochs, converted: `resetsAt` is UNIX seconds, and a
	// reader that took it for an RFC3339 string would report windows that
	// never reset.
	primaryReset := time.Date(2026, 9, 2, 17, 49, 23, 0, time.UTC)
	secondaryReset := time.Date(2026, 9, 7, 15, 27, 18, 0, time.UTC)

	tests := []struct {
		name    string
		rewrite func(map[string]json.RawMessage)
		want    []agent.ReportedWindow
		wantErr string
	}{
		{
			name: "captured response verbatim",
			want: []agent.ReportedWindow{
				{Name: "primary", UsedPercent: 28, Window: "5h", ResetsAt: primaryReset},
				{Name: "secondary", UsedPercent: 53, Window: "7d", ResetsAt: secondaryReset},
			},
		},
		{
			// 0.150.1 sends both shapes with identical numbers, so the
			// preference is only observable once they disagree.
			name: "rateLimitsByLimitId wins over rateLimits",
			rewrite: func(f map[string]json.RawMessage) {
				f["rateLimits"] = swapUsedPercent(f["rateLimits"], "99")
			},
			want: []agent.ReportedWindow{
				{Name: "primary", UsedPercent: 28, Window: "5h", ResetsAt: primaryReset},
				{Name: "secondary", UsedPercent: 53, Window: "7d", ResetsAt: secondaryReset},
			},
		},
		{
			name: "rateLimits is the fallback for an older build",
			rewrite: func(f map[string]json.RawMessage) {
				f["rateLimits"] = swapUsedPercent(f["rateLimits"], "99")
				delete(f, "rateLimitsByLimitId")
			},
			want: []agent.ReportedWindow{
				{Name: "primary", UsedPercent: 99, Window: "5h", ResetsAt: primaryReset},
				{Name: "secondary", UsedPercent: 53, Window: "7d", ResetsAt: secondaryReset},
			},
		},
		{
			name: "a map keyed by another product falls back too",
			rewrite: func(f map[string]json.RawMessage) {
				f["rateLimitsByLimitId"] = json.RawMessage(`{"something-else":{"primary":{"usedPercent":1,"windowDurationMins":60,"resetsAt":1}}}`)
				f["rateLimits"] = swapUsedPercent(f["rateLimits"], "99")
			},
			want: []agent.ReportedWindow{
				{Name: "primary", UsedPercent: 99, Window: "5h", ResetsAt: primaryReset},
				{Name: "secondary", UsedPercent: 53, Window: "7d", ResetsAt: secondaryReset},
			},
		},
		{
			name: "a window with no reset is reported without one",
			rewrite: func(f map[string]json.RawMessage) {
				delete(f, "rateLimitsByLimitId")
				f["rateLimits"] = json.RawMessage(`{"primary":{"usedPercent":4.5,"windowDurationMins":90,"resetsAt":0}}`)
			},
			want: []agent.ReportedWindow{
				{Name: "primary", UsedPercent: 4.5, Window: "90m"},
			},
		},
		{
			name: "malformed windows are not a reading",
			rewrite: func(f map[string]json.RawMessage) {
				delete(f, "rateLimitsByLimitId")
				f["rateLimits"] = json.RawMessage(`"nonsense"`)
			},
			wantErr: "parse codex rate limits",
		},
		{
			name: "a response naming no window is not a reading",
			rewrite: func(f map[string]json.RawMessage) {
				delete(f, "rateLimitsByLimitId")
				delete(f, "rateLimits")
			},
			wantErr: "no windows",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRateLimits(fixtureResult(t, tc.rewrite), reported)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got reading %+v", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				if got != nil {
					t.Fatalf("a failed parse must yield no reading, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Source != agent.QuotaSourceCodexAppServer {
				t.Fatalf("source = %q, want %q", got.Source, agent.QuotaSourceCodexAppServer)
			}
			if !got.ReportedAt.Equal(reported) {
				t.Fatalf("reported_at = %s, want %s", got.ReportedAt, reported)
			}
			if len(got.Windows) != len(tc.want) {
				t.Fatalf("windows = %+v, want %+v", got.Windows, tc.want)
			}
			for i, w := range tc.want {
				g := got.Windows[i]
				if g.Name != w.Name || g.UsedPercent != w.UsedPercent || g.Window != w.Window || !g.ResetsAt.Equal(w.ResetsAt) {
					t.Fatalf("window %d = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

func TestWindowLabel(t *testing.T) {
	// The two the vendor actually sends, plus the arithmetic either side of
	// them: the label is the largest unit that divides exactly, and minutes
	// when none does.
	for _, tc := range []struct {
		mins int
		want string
	}{
		{300, "5h"},
		{10080, "7d"},
		{60, "1h"},
		{1440, "1d"},
		{90, "90m"},
		{1, "1m"},
		{0, ""},
		{-5, ""},
	} {
		if got := windowLabel(tc.mins); got != tc.want {
			t.Errorf("windowLabel(%d) = %q, want %q", tc.mins, got, tc.want)
		}
	}
}

func TestQuotaAgainstFakeAgent(t *testing.T) {
	bin := agenttest.BuildFakeAgent(t)

	t.Run("healthy", func(t *testing.T) {
		t.Setenv("FAKEAGENT_CODEX_APP_SERVER", "healthy")
		q, err := New(func() string { return bin }).Quota(t.Context())
		if err != nil {
			t.Fatalf("quota: %v", err)
		}
		if q.Source != agent.QuotaSourceCodexAppServer {
			t.Fatalf("source = %q", q.Source)
		}
		if len(q.Windows) != 2 {
			t.Fatalf("windows = %+v, want two", q.Windows)
		}
		if q.Windows[0].Name != "primary" || q.Windows[0].UsedPercent != 28 || q.Windows[0].Window != "5h" {
			t.Fatalf("primary = %+v", q.Windows[0])
		}
		if q.Windows[1].Name != "secondary" || q.Windows[1].UsedPercent != 53 || q.Windows[1].Window != "7d" {
			t.Fatalf("secondary = %+v", q.Windows[1])
		}
		// The stand-in dates its windows from its own clock, exactly as a
		// real CLI does, so both must still be open.
		for _, w := range q.Windows {
			if !w.ResetsAt.After(time.Now()) {
				t.Fatalf("window %s resets at %s, which is not in the future", w.Name, w.ResetsAt)
			}
		}
	})

	// Every one of these is a degradation the daemon must survive silently:
	// no reading, an error recorded off the wire, and nothing said about the
	// option probe. TestQuotaFailureLeavesProbeErrorAlone proves the last
	// half against the cache that records it.
	for _, tc := range []struct {
		name    string
		env     string
		path    func(t *testing.T) string
		timeout time.Duration
		wantErr string
	}{
		{
			name:    "an unauthenticated account",
			env:     "unauthenticated",
			wantErr: "not logged in",
		},
		{
			name:    "an answer that does not parse",
			env:     "malformed",
			wantErr: "parse codex rate limits",
		},
		{
			name: "a handshake that never completes",
			env:  "hang",
			// Shorter than appServerTimeout on purpose: the adapter's own
			// bound is the ceiling, and a caller's deadline still cuts in
			// under it.
			timeout: 2 * time.Second,
			wantErr: "context deadline exceeded",
		},
		{
			name:    "a binary that is not there",
			path:    func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-such-codex") },
			wantErr: "configured codex path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKEAGENT_CODEX_APP_SERVER", tc.env)
			path := bin
			if tc.path != nil {
				path = tc.path(t)
			}
			ctx := t.Context()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			q, err := New(func() string { return path }).Quota(ctx)
			if q != nil {
				t.Fatalf("a degraded probe must report no reading, got %+v", q)
			}
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestQuotaSpawnFailure covers the one degradation path resolvePath cannot
// produce: a path that resolves and then will not start. It goes through the
// unexported reader directly, because LookPath is what stands between the
// adapter and this case on every platform.
func TestQuotaSpawnFailure(t *testing.T) {
	_, err := readRateLimits(t.Context(), filepath.Join(t.TempDir(), "not-an-executable"))
	if err == nil {
		t.Fatal("want an error starting a binary that is not there")
	}
	if !strings.Contains(err.Error(), "start codex app-server") {
		t.Fatalf("error = %v, want the spawn failure", err)
	}
}

// TestQuotaFailureLeavesProbeErrorAlone is (d): a reporter that cannot answer
// must cost the entry nothing a client renders. `probe_error` keeps meaning
// only "the option probe failed", the adapter stays available, and the
// reading stays nil rather than becoming a zero.
func TestQuotaFailureLeavesProbeErrorAlone(t *testing.T) {
	bin := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_CODEX_APP_SERVER", "unauthenticated")

	cache := agent.NewCatalogCache(agent.NewRegistry(New(func() string { return bin })))
	e, ok := cache.Entry(t.Context(), "codex", false)
	if !ok {
		t.Fatal("codex is not in the cache")
	}
	if !e.Availability.Found {
		t.Fatalf("the adapter must still be available: %+v", e.Availability)
	}
	if e.ProbeError != "" {
		t.Fatalf("probe_error = %q; a quota failure must not touch it", e.ProbeError)
	}
	if e.Quota != nil {
		t.Fatalf("quota = %+v, want none", e.Quota)
	}
	if e.QuotaError == "" {
		t.Fatal("the failure must be recorded off the wire in QuotaError")
	}

	// And the healthy path through the same seam, so the assertions above are
	// known to be about the failure rather than about the cache never asking.
	t.Setenv("FAKEAGENT_CODEX_APP_SERVER", "healthy")
	cache = agent.NewCatalogCache(agent.NewRegistry(New(func() string { return bin })))
	e, _ = cache.Entry(t.Context(), "codex", false)
	if e.Quota == nil || len(e.Quota.Windows) != 2 {
		t.Fatalf("quota = %+v, want a two-window reading", e.Quota)
	}
	if e.QuotaError != "" {
		t.Fatalf("quota_error = %q on a healthy read", e.QuotaError)
	}
}
