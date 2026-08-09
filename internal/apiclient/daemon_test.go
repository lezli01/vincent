package apiclient_test

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

// cfgDefault is what the harness serves, so the assertions name the same
// source the handler read rather than repeating its numbers.
var cfgDefault = config.Default()

// The daemon view renders these fields directly, so the client struct has to
// pick up every one the handler sends — a field the client silently drops
// renders as an empty row that looks like a daemon problem.
func TestInfoCarriesTheIdentityTheDaemonViewRenders(t *testing.T) {
	h := newHarness(t)

	info, err := h.client().Info(t.Context())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Version == "" {
		t.Error("Version is empty")
	}
	if info.PID == 0 {
		t.Error("PID = 0")
	}
	if info.Listen == "" {
		t.Error("Listen is empty")
	}
	if info.StartedAt.IsZero() {
		t.Error("StartedAt is zero; uptime is ticked from it")
	}
	if info.MaxParallelTasks != cfgDefault.MaxParallelTasks {
		t.Errorf("MaxParallelTasks = %d, want %d",
			info.MaxParallelTasks, cfgDefault.MaxParallelTasks)
	}
}

// Uptime ticks locally between refetches, so it comes off StartedAt rather
// than off the fetched second count, which is stale the moment it lands.
func TestInfoUptimeComesFromStartedAt(t *testing.T) {
	start := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	info := apiclient.Info{StartedAt: start, UptimeSeconds: 1}

	if got := info.Uptime(start.Add(90 * time.Second)); got != 90*time.Second {
		t.Errorf("Uptime = %v, want 90s (not the fetched 1s)", got)
	}
	// A clock that disagrees with the daemon's must not render as negative.
	if got := info.Uptime(start.Add(-time.Minute)); got != 0 {
		t.Errorf("Uptime = %v, want 0 for a now before StartedAt", got)
	}
	// Without a StartedAt there is nothing to tick from; the fetched figure
	// is all there is.
	bare := apiclient.Info{UptimeSeconds: 42}
	if got := bare.Uptime(time.Now()); got != 42*time.Second {
		t.Errorf("Uptime = %v, want the fetched 42s", got)
	}
}

func TestConfigCarriesTheSettingsInEffect(t *testing.T) {
	h := newHarness(t)

	cfg, err := h.client().Config(t.Context())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.MaxParallelTasks != cfgDefault.MaxParallelTasks {
		t.Errorf("MaxParallelTasks = %d, want %d",
			cfg.MaxParallelTasks, cfgDefault.MaxParallelTasks)
	}
	if cfg.LogLevel != cfgDefault.LogLevel {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, cfgDefault.LogLevel)
	}
	if cfg.TranscriptRetentionDays != cfgDefault.TranscriptRetentionDays {
		t.Errorf("TranscriptRetentionDays = %d, want %d",
			cfg.TranscriptRetentionDays, cfgDefault.TranscriptRetentionDays)
	}
	want := cfgDefault.Defaults.AgentTimeout.String()
	if cfg.Defaults.AgentTimeout != want {
		t.Errorf("Defaults.AgentTimeout = %q, want %q", cfg.Defaults.AgentTimeout, want)
	}
	if _, ok := cfg.Agents["claude"]; !ok {
		t.Errorf("Agents = %v, want a claude entry", cfg.Agents)
	}
}
