package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AgentStatus is one adapter's availability from GET /v1/info.
type AgentStatus struct {
	Name          string `json:"name"`
	Available     bool   `json:"available"`
	Path          string `json:"path,omitempty"`
	Version       string `json:"version,omitempty"`
	SupportsInput bool   `json:"supports_input"`
	// VersionVerdict, TestedVersions and RestrictedVerdict are the task-041
	// health facets: what vincent knows about this build, the builds it was
	// judged against, and whether this adapter can restrict on this host.
	// Empty means a daemon that predates them, which renders as no judgement.
	VersionVerdict    string `json:"version_verdict,omitempty"`
	TestedVersions    string `json:"tested_versions,omitempty"`
	RestrictedVerdict string `json:"restricted_verdict,omitempty"`
	// LoggedIn is nil when the adapter cannot cheaply tell (§9.5). Renderers
	// must distinguish nil from false: "unknown" is the normal state for
	// claude and codex, while false means every run will fail at the API.
	LoggedIn *bool  `json:"logged_in"`
	Error    string `json:"error,omitempty"`
	// Quota is the adapter's usage window as last observed (task 026), or
	// nil when nothing has been observed. It is the same block /v1/agents
	// carries: the board header renders from /v1/info and must not need a
	// second fetch to do it.
	Quota *AgentQuota `json:"quota"`
}

// QuotaSpent reports an adapter whose usage window is still shut as of now.
// Nothing observed answers false — see Agent.QuotaSpent.
func (a AgentStatus) QuotaSpent(now time.Time) bool { return a.Quota.SpentAt(now) }

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
	// Database is the store's on-disk footprint (task 029). Byte figures
	// only: the row counts and the retention span are scans and ride
	// GET /v1/doctor instead, which is the cold path.
	Database InfoDatabase `json:"database"`
}

// InfoDatabase is the database footprint carried on GET /v1/info: the main
// file, the two WAL-mode sidecars, and their total. The total is the figure
// worth rendering — between checkpoints the main file alone understates it.
type InfoDatabase struct {
	Path       string `json:"path"`
	SizeBytes  int64  `json:"size_bytes"`
	WALBytes   int64  `json:"wal_bytes"`
	SHMBytes   int64  `json:"shm_bytes"`
	TotalBytes int64  `json:"total_bytes"`
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

// Config is the GET /v1/config body: config.yaml as the daemon currently has
// it loaded, durations rendered as Go duration strings. Every key in the file
// is here — a key this type omits is one no client can see, which is the
// defect task 060 fixed.
type Config struct {
	Listen           string `json:"listen"`
	MaxParallelTasks int    `json:"max_parallel_tasks"`
	// MaxParallelChats bounds chats holding a live agent process, counted
	// separately from MaxParallelTasks (§11, task 063 decision 1).
	MaxParallelChats int `json:"max_parallel_chats"`
	// BranchTemplate is empty when the file pins none; the built-in fallback
	// lives in the daemon, and a project may override it either way.
	BranchTemplate string         `json:"branch_template"`
	Defaults       ConfigDefaults `json:"defaults"`
	// DeleteEmptyBranchOnArchive deletes a task's branch at archive time when
	// it has no commits past its base (§10). DeleteRemoteBranchOnArchive does
	// the same to its upstream counterpart, and is honoured only on an
	// attended archive — it is inert while the local one is off.
	DeleteEmptyBranchOnArchive  bool  `json:"delete_empty_branch_on_archive"`
	DeleteRemoteBranchOnArchive bool  `json:"delete_remote_branch_on_archive"`
	FetchBaseBranch             bool  `json:"fetch_base_branch"`
	TranscriptRetentionDays     int   `json:"transcript_retention_days"`
	TranscriptMaxBytes          int64 `json:"transcript_max_bytes"`
	// MaxTaskCostUSD caps what one task may spend across every attempt of
	// every step, in US dollars; 0 is no cap (§12.3, task 033).
	MaxTaskCostUSD float64 `json:"max_task_cost_usd"`
	// UsageLimitRecheck is how long a quota-held task waits before the
	// scheduler tries again, when the agent CLI reported no reset time (§11).
	UsageLimitRecheck string            `json:"usage_limit_recheck_interval"`
	LogLevel          string            `json:"log_level"`
	Debug             bool              `json:"debug"`
	Environment       ConfigEnvironment `json:"environment"`
	Agents            ConfigAgents      `json:"agents"`
	Parallel          ConfigParallel    `json:"parallel"`
	FanOut            ConfigFanOut      `json:"fan_out"`
	Loop              ConfigLoop        `json:"loop"`
	Include           ConfigInclude     `json:"include"`
	MCP               ConfigMCP         `json:"mcp"`
	GitHub            ConfigGitHub      `json:"github"`
	Update            ConfigUpdate      `json:"update"`
	Notify            ConfigNotify      `json:"notify"`
	// Container is §16's container execution mode (task 061). Image empty is
	// the default and means the steps run on this host.
	Container ConfigContainer `json:"container"`
	// TUI is the view preference the daemon only relays (§15). It is served
	// here rather than read from disk because the TUI is a pure API client;
	// a client that cannot reach the daemon renders its own defaults.
	TUI ConfigTUI `json:"tui"`
}

// ConfigContainer is the `container` section of config.yaml as served (§16,
// task 061). Runtime is the docker-CLI-compatible binary; ExtraMounts are
// `host:container[:ro]` bind mounts beyond the repository and worktree the
// daemon mounts on its own.
type ConfigContainer struct {
	Image            string   `json:"image"`
	Runtime          string   `json:"runtime"`
	MountAgentConfig bool     `json:"mount_agent_config"`
	Network          bool     `json:"network"`
	ExtraMounts      []string `json:"extra_mounts"`
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

// ConfigAgents are the three §9 adapters' configured binaries.
type ConfigAgents struct {
	Claude AgentPath `json:"claude"`
	Codex  AgentPath `json:"codex"`
	Cursor AgentPath `json:"cursor"`
}

// AgentPath is a configured adapter binary. An empty Path means the config
// pinned nothing and the adapter is resolved from PATH.
type AgentPath struct {
	Path string `json:"path"`
}

// ConfigEnvironment is what the daemon's child processes inherit (§12.3).
type ConfigEnvironment struct {
	Inherit ConfigInherit     `json:"inherit"`
	Unset   []string          `json:"unset"`
	Set     map[string]string `json:"set"`
}

// ConfigInherit is the `environment.inherit` union: the word "all" or "none",
// or an explicit list of names. It is a union on the wire because it is one in
// the file, and flattening it here would make `inherit: []` — which means
// nothing at all — indistinguishable from the default, which means everything.
type ConfigInherit struct {
	// Mode is "all", "none" or "list".
	Mode  string
	Names []string
}

// MarshalJSON writes the union back in the shape the daemon reads.
func (i ConfigInherit) MarshalJSON() ([]byte, error) {
	if i.Mode == "list" {
		names := i.Names
		if names == nil {
			names = []string{}
		}
		return json.Marshal(names)
	}
	if i.Mode == "" {
		return json.Marshal("all")
	}
	return json.Marshal(i.Mode)
}

// UnmarshalJSON reads either form.
func (i *ConfigInherit) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*i = ConfigInherit{Mode: s}
		return nil
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return fmt.Errorf("environment.inherit: want \"all\", \"none\", or a list of names")
	}
	*i = ConfigInherit{Mode: "list", Names: names}
	return nil
}

// String renders the union the way config.yaml spells it.
func (i ConfigInherit) String() string {
	if i.Mode == "list" {
		return "[" + strings.Join(i.Names, ", ") + "]"
	}
	if i.Mode == "" {
		return "all"
	}
	return i.Mode
}

// ConfigParallel bounds one `parallel` step's concurrent lanes (§7.5).
type ConfigParallel struct {
	MaxParallel int `json:"max_parallel"`
}

// ConfigFanOut bounds a `fan_out` tree (§7.6).
type ConfigFanOut struct {
	MaxDepth int `json:"max_depth"`
	MaxTasks int `json:"max_tasks"`
}

// ConfigLoop bounds one `loop` step's iterations (§7.8).
type ConfigLoop struct {
	MaxIterations int `json:"max_iterations"`
}

// ConfigInclude bounds `include` nesting (§7.9).
type ConfigInclude struct {
	MaxDepth int `json:"max_depth"`
}

// ConfigMCP is the daemon's MCP server policy (§13.4).
type ConfigMCP struct {
	WireSteps bool `json:"wire_steps"`
	MaxDepth  int  `json:"max_depth"`
	MaxTasks  int  `json:"max_tasks"`
}

// ConfigGitHub is the GitHub integration policy (task 035).
type ConfigGitHub struct {
	Enabled      bool   `json:"enabled"`
	PollInterval string `json:"poll_interval"`
}

// ConfigUpdate is the release-check policy (task 055) — not the cached answer,
// which is what GET /v1/update serves.
type ConfigUpdate struct {
	Check        bool   `json:"check"`
	PollInterval string `json:"poll_interval"`
}

// ConfigNotify is the §12.3 outward signal (task 046). Command is argv.
type ConfigNotify struct {
	On      []string `json:"on"`
	Command []string `json:"command"`
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

// PatchConfig applies a partial edit to config.yaml and returns the
// configuration in force afterwards (§13.2, task 060). The daemon validates
// the whole candidate file before writing: an invalid patch changes nothing,
// and the error is the standard envelope.
//
// The returned body is what the daemon is actually running, which is not
// always what was written — `listen` is pinned until the next restart, so a
// patch that moves it answers 200 with the old address still in force.
func (c *Client) PatchConfig(ctx context.Context, req ConfigPatch) (Config, error) {
	var out Config
	if err := c.send(ctx, http.MethodPatch, "/v1/config", req, &out); err != nil {
		return Config{}, err
	}
	return out, nil
}

// ConfigPatch is the PATCH /v1/config body: the read shape with every key
// optional. A nil field is a key the file keeps.
//
// It is a distinct type from Config rather than Config with pointers because
// the two say different things: Config is "what is in force", and every field
// of it is populated; ConfigPatch is "what to change", and a zero value has to
// mean *do not touch this* rather than "set it to zero".
type ConfigPatch struct {
	Listen                      *string                 `json:"listen,omitempty"`
	MaxParallelTasks            *int                    `json:"max_parallel_tasks,omitempty"`
	MaxParallelChats            *int                    `json:"max_parallel_chats,omitempty"`
	BranchTemplate              *string                 `json:"branch_template,omitempty"`
	Defaults                    *ConfigDefaultsPatch    `json:"defaults,omitempty"`
	DeleteEmptyBranchOnArchive  *bool                   `json:"delete_empty_branch_on_archive,omitempty"`
	DeleteRemoteBranchOnArchive *bool                   `json:"delete_remote_branch_on_archive,omitempty"`
	FetchBaseBranch             *bool                   `json:"fetch_base_branch,omitempty"`
	TranscriptRetentionDays     *int                    `json:"transcript_retention_days,omitempty"`
	TranscriptMaxBytes          *int64                  `json:"transcript_max_bytes,omitempty"`
	MaxTaskCostUSD              *float64                `json:"max_task_cost_usd,omitempty"`
	UsageLimitRecheck           *string                 `json:"usage_limit_recheck_interval,omitempty"`
	LogLevel                    *string                 `json:"log_level,omitempty"`
	Debug                       *bool                   `json:"debug,omitempty"`
	Environment                 *ConfigEnvironmentPatch `json:"environment,omitempty"`
	Agents                      *ConfigAgentsPatch      `json:"agents,omitempty"`
	Parallel                    *ConfigParallel         `json:"parallel,omitempty"`
	FanOut                      *ConfigFanOut           `json:"fan_out,omitempty"`
	Loop                        *ConfigLoop             `json:"loop,omitempty"`
	Include                     *ConfigInclude          `json:"include,omitempty"`
	MCP                         *ConfigMCPPatch         `json:"mcp,omitempty"`
	GitHub                      *ConfigGitHubPatch      `json:"github,omitempty"`
	Update                      *ConfigUpdatePatch      `json:"update,omitempty"`
	Notify                      *ConfigNotifyPatch      `json:"notify,omitempty"`
	Container                   *ConfigContainerPatch   `json:"container,omitempty"`
	TUI                         *ConfigTUIPatch         `json:"tui,omitempty"`
}

// ConfigDefaultsPatch is the optional half of ConfigDefaults.
type ConfigDefaultsPatch struct {
	AgentTimeout   *string `json:"agent_timeout,omitempty"`
	CommandTimeout *string `json:"command_timeout,omitempty"`
	InputTimeout   *string `json:"input_timeout,omitempty"`
}

// ConfigEnvironmentPatch is the optional half of ConfigEnvironment.
type ConfigEnvironmentPatch struct {
	Inherit *ConfigInherit     `json:"inherit,omitempty"`
	Unset   *[]string          `json:"unset,omitempty"`
	Set     *map[string]string `json:"set,omitempty"`
}

// ConfigAgentsPatch names the adapters whose paths are being changed.
type ConfigAgentsPatch struct {
	Claude *AgentPathPatch `json:"claude,omitempty"`
	Codex  *AgentPathPatch `json:"codex,omitempty"`
	Cursor *AgentPathPatch `json:"cursor,omitempty"`
}

// AgentPathPatch is one adapter's binary. An empty string clears the pin.
type AgentPathPatch struct {
	Path *string `json:"path,omitempty"`
}

// ConfigMCPPatch is the optional half of ConfigMCP.
type ConfigMCPPatch struct {
	WireSteps *bool `json:"wire_steps,omitempty"`
	MaxDepth  *int  `json:"max_depth,omitempty"`
	MaxTasks  *int  `json:"max_tasks,omitempty"`
}

// ConfigGitHubPatch is the optional half of ConfigGitHub.
type ConfigGitHubPatch struct {
	Enabled      *bool   `json:"enabled,omitempty"`
	PollInterval *string `json:"poll_interval,omitempty"`
}

// ConfigUpdatePatch is the optional half of ConfigUpdate.
type ConfigUpdatePatch struct {
	Check        *bool   `json:"check,omitempty"`
	PollInterval *string `json:"poll_interval,omitempty"`
}

// ConfigNotifyPatch is the optional half of ConfigNotify.
type ConfigNotifyPatch struct {
	On      *[]string `json:"on,omitempty"`
	Command *[]string `json:"command,omitempty"`
}

// ConfigContainerPatch is the optional half of ConfigContainer.
type ConfigContainerPatch struct {
	Image            *string   `json:"image,omitempty"`
	Runtime          *string   `json:"runtime,omitempty"`
	MountAgentConfig *bool     `json:"mount_agent_config,omitempty"`
	Network          *bool     `json:"network,omitempty"`
	ExtraMounts      *[]string `json:"extra_mounts,omitempty"`
}

// ConfigTUIPatch is the optional half of ConfigTUI.
type ConfigTUIPatch struct {
	Board *ConfigBoardPatch `json:"board,omitempty"`
}

// ConfigBoardPatch is the optional half of ConfigBoard.
type ConfigBoardPatch struct {
	GroupBy *[]string `json:"group_by,omitempty"`
}
