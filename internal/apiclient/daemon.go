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
	// LoggedIn is nil when the adapter cannot cheaply tell (§9.5). Renderers
	// must distinguish nil from false: "unknown" is the normal state for
	// claude and codex, while false means every run will fail at the API.
	LoggedIn *bool  `json:"logged_in"`
	Error    string `json:"error,omitempty"`
}

// NotAuthenticated reports an adapter that is installed and probes cleanly
// but will fail every run for lack of a login. It is deliberately false when
// LoggedIn is nil: an adapter that cannot answer must never be accused.
func (a AgentStatus) NotAuthenticated() bool {
	return a.Available && a.LoggedIn != nil && !*a.LoggedIn
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
	// Orphans counts data-root directories no task row claims (task 005).
	// It is a pointer to a leak a human clears with `vincent gc`, not
	// something a client acts on by itself.
	Orphans int `json:"orphans"`
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
	Listen           string         `json:"listen"`
	MaxParallelTasks int            `json:"max_parallel_tasks"`
	Defaults         ConfigDefaults `json:"defaults"`
	// DeleteEmptyBranchOnArchive deletes a task's branch at archive time when
	// it has no commits past its base (§10). DeleteRemoteBranchOnArchive does
	// the same to its upstream counterpart, and is honoured only on an
	// attended archive — it is inert while the local one is off.
	DeleteEmptyBranchOnArchive  bool  `json:"delete_empty_branch_on_archive"`
	DeleteRemoteBranchOnArchive bool  `json:"delete_remote_branch_on_archive"`
	TranscriptRetentionDays     int   `json:"transcript_retention_days"`
	TranscriptMaxBytes          int64 `json:"transcript_max_bytes"`
	// UsageLimitRecheck is how long a quota-held task waits before the
	// scheduler tries again, when the agent CLI reported no reset time (§11).
	UsageLimitRecheck string               `json:"usage_limit_recheck_interval"`
	LogLevel          string               `json:"log_level"`
	Agents            map[string]AgentPath `json:"agents"`
	// TUI is the view preference the daemon only relays (§15). It is served
	// here rather than read from disk because the TUI is a pure API client;
	// a client that cannot reach the daemon renders its own defaults.
	TUI ConfigTUI `json:"tui"`
}

// ConfigTUI is the `tui` section of config.yaml as served.
type ConfigTUI struct {
	Board ConfigBoard `json:"board"`
}

// ConfigBoard configures the task table. GroupBy names the grouping levels,
// outermost first; an empty list is a flat table. Unknown levels are the
// caller's to ignore — a newer daemon may serve one this client predates.
type ConfigBoard struct {
	GroupBy []string `json:"group_by"`
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
