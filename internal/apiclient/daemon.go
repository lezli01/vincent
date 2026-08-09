package apiclient

import (
	"context"
	"time"
)

// AgentStatus is one adapter's availability from GET /v1/info.
type AgentStatus struct {
	Name          string `json:"name"`
	Available     bool   `json:"available"`
	Path          string `json:"path,omitempty"`
	Version       string `json:"version,omitempty"`
	SupportsInput bool   `json:"supports_input"`
	Error         string `json:"error,omitempty"`
}

// Info is the GET /v1/info body: the daemon's own identity, its global cap
// and adapter availability.
type Info struct {
	Version          string        `json:"version"`
	Commit           string        `json:"commit"`
	Built            string        `json:"built"`
	PID              int           `json:"pid"`
	StartedAt        time.Time     `json:"started_at"`
	UptimeSeconds    int64         `json:"uptime_seconds"`
	Listen           string        `json:"listen"`
	MaxParallelTasks int           `json:"max_parallel_tasks"`
	Agents           []AgentStatus `json:"agents"`
}

// Uptime is how long the daemon has been up as of now. It is derived from
// StartedAt rather than from UptimeSeconds so a view can tick it locally
// between fetches without drifting away from the daemon's own clock.
func (i Info) Uptime(now time.Time) time.Duration {
	if i.StartedAt.IsZero() {
		return time.Duration(i.UptimeSeconds) * time.Second
	}
	d := now.Sub(i.StartedAt)
	if d < 0 {
		return 0
	}
	return d
}

// Info fetches daemon identity, caps and adapter availability. Availability
// rides a daemon-side cache that only moves when a CLI is installed
// mid-session, so callers refetch on demand rather than on a timer.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	if err := c.get(ctx, "/v1/info", &out); err != nil {
		return Info{}, err
	}
	return out, nil
}

// Config is the GET /v1/config body: config.yaml as the daemon currently
// has it loaded, durations rendered as Go duration strings. It is read-only
// here — the file is authoritative and the daemon hot-reloads it (§12.3).
type Config struct {
	Listen                  string               `json:"listen"`
	MaxParallelTasks        int                  `json:"max_parallel_tasks"`
	Defaults                ConfigDefaults       `json:"defaults"`
	TranscriptRetentionDays int                  `json:"transcript_retention_days"`
	LogLevel                string               `json:"log_level"`
	Agents                  map[string]AgentPath `json:"agents"`
}

// ConfigDefaults are the §12.3 default timeouts, as duration strings.
type ConfigDefaults struct {
	AgentTimeout   string `json:"agent_timeout"`
	CommandTimeout string `json:"command_timeout"`
	InputTimeout   string `json:"input_timeout"`
}

// AgentPath is a configured adapter binary. An empty Path means the config
// pinned nothing and the adapter is resolved from PATH.
type AgentPath struct {
	Path string `json:"path"`
}

// Config fetches the configuration in effect. There is no event for a
// config reload, so callers refetch rather than subscribe.
func (c *Client) Config(ctx context.Context) (Config, error) {
	var out Config
	if err := c.get(ctx, "/v1/config", &out); err != nil {
		return Config{}, err
	}
	return out, nil
}
