package apiclient_test

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/release"
)

// The wire-agreement test for GET /v1/update: the client driven against the
// **real** handler over httptest, which is what keeps client and server types
// from drifting — the server DTO is unexported, so nothing else catches a
// field renamed on one side.
func TestUpdateStatusRoundTrip(t *testing.T) {
	checked := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	published := time.Date(2026, 8, 21, 9, 31, 7, 0, time.UTC)
	status := release.Status{
		Enabled:   true,
		CheckedAt: checked,
		Latest: release.Release{
			Version:     "v99.0.0",
			PublishedAt: published,
			URL:         "https://github.com/lezli01/vincent/releases/tag/v99.0.0",
		},
	}
	c := newUpdateClient(t, func() release.Status { return status })

	got, err := c.Update(t.Context())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !got.Enabled {
		t.Error("enabled did not survive the round trip")
	}
	if got.LatestVersion != "v99.0.0" {
		t.Errorf("latest_version = %q, want v99.0.0", got.LatestVersion)
	}
	// The verdict is the daemon's, computed server-side, and it must agree
	// with the one rule: `dev` is never behind. The test binary reports
	// "dev", so this is false here — and asserting agreement rather than a
	// constant is what makes the same test correct in a release build, where
	// v99.0.0 is genuinely newer.
	if want := release.IsNewer(got.CurrentVersion, got.LatestVersion); got.UpdateAvailable != want {
		t.Errorf("update_available = %v for %s against %s, want %v",
			got.UpdateAvailable, got.LatestVersion, got.CurrentVersion, want)
	}
	if got.CurrentVersion == "dev" && got.UpdateAvailable {
		t.Error("a dev build was reported as behind a release")
	}
	if got.CurrentVersion == "" {
		t.Error("current_version is empty; a client cannot render the comparison")
	}
	if got.CheckedAt == nil || !got.CheckedAt.Equal(checked) {
		t.Errorf("checked_at = %v, want %s", got.CheckedAt, checked)
	}
	if got.PublishedAt == nil || !got.PublishedAt.Equal(published) {
		t.Errorf("published_at = %v, want %s", got.PublishedAt, published)
	}
	if !got.Checked() {
		t.Error("Checked() is false for a status that carries a checked_at")
	}
}

// The never-polled state is a distinct answer from "no update available", and
// it has to survive the wire as one: checked_at null, no version, and
// update_available false.
func TestUpdateStatusNeverPolled(t *testing.T) {
	c := newUpdateClient(t, func() release.Status { return release.Status{Enabled: true} })

	got, err := c.Update(t.Context())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Checked() {
		t.Errorf("checked_at = %v for a daemon that has never polled", got.CheckedAt)
	}
	if got.LatestVersion != "" || got.UpdateAvailable {
		t.Errorf("a never-polled daemon reported latest=%q available=%v",
			got.LatestVersion, got.UpdateAvailable)
	}
	if !got.Enabled {
		t.Error("enabled = false for a daemon whose check is on but has not polled yet")
	}
}

// A daemon wired with no poller at all — every test harness in this
// repository, and any build that never wires one — must answer rather than
// 500, and must say the check is off rather than inventing one.
func TestUpdateStatusWithoutPoller(t *testing.T) {
	c := newUpdateClient(t, nil)

	got, err := c.Update(t.Context())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Enabled || got.Checked() || got.UpdateAvailable {
		t.Errorf("a daemon with no poller reported %+v", got)
	}
}

func newUpdateClient(t *testing.T, status func() release.Status) *apiclient.Client {
	t.Helper()
	s := api.New(api.Deps{
		Token:        testToken,
		Config:       config.Default,
		StartedAt:    time.Now(),
		ListenAddr:   "127.0.0.1:0",
		RequestStop:  func() {},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		UpdateStatus: status,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, testToken)
}
