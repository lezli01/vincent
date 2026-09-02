package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// quotaStub is a stubAdapter that also satisfies QuotaReporter, so a test can
// count how often the cache asks and what it does with the answer.
type quotaStub struct {
	stubAdapter
	quota  *ReportedQuota
	err    error
	quotas int
}

func (q *quotaStub) Quota(context.Context) (*ReportedQuota, error) {
	q.quotas++
	return q.quota, q.err
}

func reading(source string, pct float64) *ReportedQuota {
	return &ReportedQuota{
		Source:     source,
		ReportedAt: time.Now(),
		Windows:    []ReportedWindow{{Name: "primary", UsedPercent: pct, Window: "5h"}},
	}
}

// newQuotaStub is an installed reporter whose binary is a real file, so the
// identity check exercises a real stat.
func newQuotaStub(t *testing.T, q *ReportedQuota) *quotaStub {
	t.Helper()
	bin := fakeBinary(t)
	return &quotaStub{stubAdapter: stubAdapter{
		name: "codex", path: bin,
		av:   Availability{Found: true, Path: bin, Version: "1"},
		opts: cachedOpts("gpt-5"),
	}, quota: q}
}

// TestQuotaProbedOncePerTTL pins the cost of the feature: a board, a detail
// view and a new-task form asking in the same second must spawn one probe, and
// the reading must expire on its own — binary identity says nothing about
// quota, so nothing else can expire it.
func TestQuotaProbedOncePerTTL(t *testing.T) {
	stub := newQuotaStub(t, reading(QuotaSourceCodexAppServer, 28))
	c := NewCatalogCache(NewRegistry(stub))
	now := time.Now()
	c.now = func() time.Time { return now }

	for range 3 {
		e, _ := c.Entry(t.Context(), "codex", false)
		if e.Quota == nil || e.Quota.Source != QuotaSourceCodexAppServer {
			t.Fatalf("Quota = %+v, want a codex_app_server reading", e.Quota)
		}
	}
	if stub.quotas != 1 {
		t.Errorf("asked %d times within the TTL, want 1", stub.quotas)
	}

	now = now.Add(quotaTTL)
	stub.quota = reading(QuotaSourceCodexAppServer, 53)
	e, _ := c.Entry(t.Context(), "codex", false)
	if stub.quotas != 2 {
		t.Errorf("asked %d times across the TTL, want 2", stub.quotas)
	}
	if got := e.Quota.Windows[0].UsedPercent; got != 53 {
		t.Errorf("used_percent = %v after the TTL, want the fresh 53", got)
	}
}

// TestQuotaRefreshForces pins the seam doctor rides: `GET /v1/doctor` already
// calls Entry with probe=true, and that must be the whole knob.
func TestQuotaRefreshForces(t *testing.T) {
	stub := newQuotaStub(t, reading(QuotaSourceCodexAppServer, 28))
	c := NewCatalogCache(NewRegistry(stub))

	c.Entry(t.Context(), "codex", false)
	c.Entry(t.Context(), "codex", true)
	if stub.quotas != 2 {
		t.Errorf("asked %d times, want 2 — refresh must force the reading", stub.quotas)
	}
}

// TestQuotaOnlyReportersAreAsked is the §9.7 rule as a test: an adapter with
// no quota surface grows no stub and costs no subprocess. It cannot be
// asserted by counting calls on something that has no method, so it is
// asserted where it shows: the entry stays empty and nothing fails.
func TestQuotaOnlyReportersAreAsked(t *testing.T) {
	bin := fakeBinary(t)
	stub := &stubAdapter{
		name: "cursor", path: bin,
		av:   Availability{Found: true, Path: bin, Version: "1"},
		opts: cachedOpts("auto"),
	}
	c := NewCatalogCache(NewRegistry(stub))

	e, ok := c.Entry(t.Context(), "cursor", true)
	switch {
	case !ok:
		t.Fatal("Entry: adapter unknown")
	case e.Quota != nil:
		t.Errorf("Quota = %+v, want nil for an adapter with no quota surface", e.Quota)
	case e.QuotaError != "":
		t.Errorf("QuotaError = %q, want empty — nothing was asked", e.QuotaError)
	case !e.QuotaCheckedAt.IsZero():
		t.Error("QuotaCheckedAt stamped for an adapter that was never asked")
	case e.ProbeError != "":
		t.Errorf("ProbeError = %q, want empty", e.ProbeError)
	}
}

// TestQuotaNotAskedWhenAbsent: an uninstalled binary has no surface to answer
// with, so asking is pure cost. staleQuota is what refuses.
func TestQuotaNotAskedWhenAbsent(t *testing.T) {
	stub := newQuotaStub(t, reading(QuotaSourceCodexAppServer, 28))
	stub.av = Availability{Error: "not found"}
	stub.path, stub.pathErr = "", errors.New("not in PATH")
	c := NewCatalogCache(NewRegistry(stub))

	if e, _ := c.Entry(t.Context(), "codex", true); e.Quota != nil {
		t.Errorf("Quota = %+v, want nil for an uninstalled adapter", e.Quota)
	}
	if stub.quotas != 0 {
		t.Errorf("asked %d times, want 0 — nothing is installed to ask", stub.quotas)
	}
}

// TestQuotaFailureKeepsPreviousReading is redetect's rule applied here: a
// reading that did not arrive is not a reading of zero. Every degradation path
// keeps what we had, leaves probe_error alone and fails no probe — and the
// clock is stamped anyway, which is what bounds a broken reporter to one
// subprocess per TTL.
func TestQuotaFailureKeepsPreviousReading(t *testing.T) {
	stub := newQuotaStub(t, reading(QuotaSourceCodexAppServer, 28))
	c := NewCatalogCache(NewRegistry(stub))
	now := time.Now()
	c.now = func() time.Time { return now }
	c.Entry(t.Context(), "codex", false)

	for _, tc := range []struct {
		name    string
		quota   *ReportedQuota
		err     error
		wantErr string
	}{
		{"errored", nil, errors.New("app-server timed out"), "app-server timed out"},
		{"declined", nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub.quota, stub.err = tc.quota, tc.err
			now = now.Add(quotaTTL)
			asked := stub.quotas
			e, _ := c.Entry(t.Context(), "codex", false)
			switch {
			case stub.quotas != asked+1:
				t.Fatalf("asked %d times, want %d", stub.quotas, asked+1)
			case e.Quota == nil || e.Quota.Windows[0].UsedPercent != 28:
				t.Errorf("Quota = %+v, want the previous 28%% reading kept", e.Quota)
			case e.QuotaError != tc.wantErr:
				t.Errorf("QuotaError = %q, want %q", e.QuotaError, tc.wantErr)
			case e.ProbeError != "":
				t.Errorf("ProbeError = %q — a quota failure must never touch it", e.ProbeError)
			case !e.Availability.Found:
				t.Error("a quota failure marked the adapter unavailable")
			case !e.QuotaCheckedAt.Equal(now):
				t.Error("QuotaCheckedAt not stamped on failure; a broken reporter would be asked every request")
			}
		})
	}
}

// TestSetQuotaChangeDetection: the push seam reports whether anything a client
// renders moved, which is what keeps a status line re-rendering on every
// prompt from waking every SSE subscriber with news they already have.
func TestSetQuotaChangeDetection(t *testing.T) {
	bin := fakeBinary(t)
	stub := &stubAdapter{
		name: "claude", path: bin,
		av: Availability{Found: true, Path: bin, Version: "1"},
	}
	c := NewCatalogCache(NewRegistry(stub))
	reset := time.Now().Add(time.Hour).UTC()
	first := &ReportedQuota{
		Source: QuotaSourceClaudeStatusLine, ReportedAt: time.Now(),
		Windows: []ReportedWindow{{Name: "five_hour", UsedPercent: 28.5, Window: "5h", ResetsAt: reset}},
	}
	if !c.SetQuota("claude", first) {
		t.Error("SetQuota on an empty entry = false, want true")
	}
	same := *first
	same.ReportedAt = first.ReportedAt.Add(time.Minute)
	if c.SetQuota("claude", &same) {
		t.Error("SetQuota with only ReportedAt moved = true; an unchanged reading must emit nothing")
	}
	moved := *first
	moved.Windows = []ReportedWindow{{Name: "five_hour", UsedPercent: 31, Window: "5h", ResetsAt: reset}}
	if !c.SetQuota("claude", &moved) {
		t.Error("SetQuota with a new percentage = false, want true")
	}
	if c.SetQuota("nope", first) {
		t.Error("SetQuota on an unknown adapter = true, want false")
	}

	e, _ := c.Entry(t.Context(), "claude", false)
	if e.Quota == nil || e.Quota.Windows[0].UsedPercent != 31 {
		t.Fatalf("Quota = %+v, want the pushed 31%% reading", e.Quota)
	}
}

// TestSetQuotaSurvivesReprobe: a pushed reading has no probe behind it to
// re-run, so a re-probe that dropped it would blank the board every time a
// binary changed or `?refresh=true` arrived.
func TestSetQuotaSurvivesReprobe(t *testing.T) {
	bin := fakeBinary(t)
	stub := &stubAdapter{
		name: "claude", path: bin,
		av: Availability{Found: true, Path: bin, Version: "1"},
	}
	c := NewCatalogCache(NewRegistry(stub))
	c.Entry(t.Context(), "claude", false)
	c.SetQuota("claude", reading(QuotaSourceClaudeStatusLine, 42))

	e, _ := c.Entry(t.Context(), "claude", true)
	if e.Quota == nil || e.Quota.Windows[0].UsedPercent != 42 {
		t.Errorf("Quota = %+v after a forced re-probe, want the pushed reading intact", e.Quota)
	}
	if stub.detects != 2 {
		t.Errorf("detects = %d, want 2 — the refresh must still re-probe the catalog", stub.detects)
	}
}
