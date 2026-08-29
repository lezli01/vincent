package release

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Parsing is table-driven against a captured real `releases/latest` body,
// named with the date it was captured — the adapter-parsing convention
// (CLAUDE.md). The point is that the fields this reads survive a payload with
// everything else on it.
func TestLatestParsesCapturedBody(t *testing.T) {
	srv := serveFile(t, http.StatusOK, "latest_2026-08-29.json")
	rel, err := New(Options{BaseURL: srv.URL}).Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "v0.4.1" {
		t.Errorf("version = %q, want v0.4.1", rel.Version)
	}
	if got := rel.URL; got != "https://github.com/lezli01/vincent/releases/tag/v0.4.1" {
		t.Errorf("url = %q", got)
	}
	want := time.Date(2026, 8, 21, 9, 31, 7, 0, time.UTC)
	if !rel.PublishedAt.Equal(want) {
		t.Errorf("published_at = %s, want %s", rel.PublishedAt, want)
	}
}

// A prerelease is never an available update — the acceptance criterion, at the
// layer that can enforce it. `releases/latest` excludes them server-side, so
// this asserts the client-side backstop the design settled on rather than
// trusting one API's documented behaviour.
func TestLatestRejectsPrerelease(t *testing.T) {
	srv := serveFile(t, http.StatusOK, "prerelease_2026-08-29.json")
	if _, err := New(Options{BaseURL: srv.URL}).Latest(t.Context()); err == nil {
		t.Fatal("a prerelease tag was accepted as the latest stable release")
	}
}

// A 403 rate-limit body is the expected failure for an unauthenticated call.
// It must degrade to "unknown" — an error the caller may ignore — and never
// to a Release.
func TestLatestRateLimitDegrades(t *testing.T) {
	srv := serveFile(t, http.StatusForbidden, "ratelimit_2026-08-29.json")
	rel, err := New(Options{BaseURL: srv.URL}).Latest(t.Context())
	if err == nil {
		t.Fatal("a 403 was accepted as an answer")
	}
	if rel != (Release{}) {
		t.Errorf("a failed call returned %+v, want the zero Release", rel)
	}
}

// A timeout is the offline case. Nothing waits on this answer, so it must
// return rather than park.
func TestLatestTimeoutDegrades(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(block); srv.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := New(Options{BaseURL: srv.URL}).Latest(ctx); err == nil {
		t.Fatal("a hung server was accepted as an answer")
	}
}

func TestLatestRejectsMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	t.Cleanup(srv.Close)
	if _, err := New(Options{BaseURL: srv.URL}).Latest(t.Context()); err == nil {
		t.Fatal("a malformed body was accepted as an answer")
	}
}

// The comparison, including the two cases that make it worth having: a `dev`
// build is never behind, and goreleaser's missing "v" prefix must not make
// every release look newer than every binary forever.
func TestIsNewer(t *testing.T) {
	for _, tc := range []struct {
		name            string
		current, latest string
		want            bool
	}{
		{"older binary", "v0.3.0", "v0.4.1", true},
		{"missing v prefix on the binary", "0.3.0", "v0.4.1", true},
		{"missing v prefix on the tag", "v0.3.0", "0.4.1", true},
		{"equal", "v0.4.1", "v0.4.1", false},
		{"newer binary", "v0.5.0", "v0.4.1", false},
		{"dev build is never behind", "dev", "v0.4.1", false},
		{"empty current", "", "v0.4.1", false},
		{"empty latest", "v0.3.0", "", false},
		{"prerelease is never an update", "v0.4.1", "v0.5.0-rc.1", false},
		{"patch ordering", "v0.4.1", "v0.4.10", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNewer(tc.current, tc.latest); got != tc.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

// The zero Status is the never-polled state, and it must not read as "up to
// date" — a client that renders the two the same way claims a check that
// never happened.
func TestZeroStatusIsNotUpToDate(t *testing.T) {
	var s Status
	if s.UpdateAvailable("v0.1.0") {
		t.Error("an unpolled Status reported an update available")
	}
	if s.Enabled || !s.CheckedAt.IsZero() || s.Latest.Version != "" {
		t.Errorf("the zero Status is not the never-polled state: %+v", s)
	}
}

// The one header the request carries is Accept. No Authorization, no
// identifying user agent: §16's promise is that the check sends nothing about
// this machine, and that is only true if nothing puts a header on it.
func TestLatestSendsNothingIdentifying(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.4.1", "published_at": "2026-08-21T09:31:07Z",
		})
	}))
	t.Cleanup(srv.Close)
	if _, err := New(Options{BaseURL: srv.URL}).Latest(t.Context()); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	for _, h := range []string{"Authorization", "Cookie", "X-Github-Api-Version"} {
		if got.Get(h) != "" {
			t.Errorf("request carried %s: %q", h, got.Get(h))
		}
	}
	if got.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got.Get("Accept"))
	}
}

func serveFile(t *testing.T, status int, name string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}
