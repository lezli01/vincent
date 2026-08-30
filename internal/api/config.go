package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/worktree"
)

// GET and PATCH /v1/config (§12.3, §13.2; task 060).
//
// The read serves every key in config.Config, not a subset. It used to serve
// eleven of them, which is how `branch_template`, `environment`, `notify` and
// six others became invisible from every client: the TUI reads no
// configuration of its own (§15), so a key this endpoint omits is a key nobody
// but a text editor can see. A drift test in this package fails when a field
// is added to config.Config and not here.
//
// Values are served in full, including `environment.set` and `notify.command`
// (task 060 decision 2). The endpoint is loopback-only behind an 0600 bearer
// token, which is the same trust boundary as the 0600 file — anyone who can
// call it can already read config.yaml. §12.3's "names, never the values" rule
// still governs the log and step transcripts, which is where it was earned.
// The MCP rendering of this route is redacted; see internal/mcp.

// configResponse mirrors config.yaml as snake_case JSON; durations render as
// Go duration strings (phase 1 decision).
type configResponse struct {
	Listen           string `json:"listen"`
	MaxParallelTasks int    `json:"max_parallel_tasks"`
	// BranchTemplate is empty when the file pins none, which is not the same
	// as the built-in: a project may override it, and the fallback lives in
	// internal/worktree rather than in a default here.
	BranchTemplate string         `json:"branch_template"`
	Defaults       configDefaults `json:"defaults"`
	// The §10 branch-cleanup pair (task 008). Both are reported because the
	// remote one is inert without the local one, and a client showing only the
	// key that is on would describe a policy that cannot run.
	DeleteEmptyBranchOnArchive  bool  `json:"delete_empty_branch_on_archive"`
	DeleteRemoteBranchOnArchive bool  `json:"delete_remote_branch_on_archive"`
	FetchBaseBranch             bool  `json:"fetch_base_branch"`
	TranscriptRetentionDays     int   `json:"transcript_retention_days"`
	TranscriptMaxBytes          int64 `json:"transcript_max_bytes"`
	// MaxTaskCostUSD is the per-task spend ceiling; 0 is no cap (task 033).
	// Served for the same reason every other key is: the TUI reads no
	// configuration from disk (§15).
	MaxTaskCostUSD    float64            `json:"max_task_cost_usd"`
	UsageLimitRecheck string             `json:"usage_limit_recheck_interval"`
	LogLevel          string             `json:"log_level"`
	Debug             bool               `json:"debug"`
	Environment       configEnvironment  `json:"environment"`
	Agents            configAgents       `json:"agents"`
	Parallel          configParallel     `json:"parallel"`
	FanOut            configFanOut       `json:"fan_out"`
	Loop              configLoop         `json:"loop"`
	Include           configInclude      `json:"include"`
	MCP               configMCP          `json:"mcp"`
	GitHub            configGitHub       `json:"github"`
	Update            configUpdateStatus `json:"update"`
	Notify            configNotify       `json:"notify"`
	// Container is §16's container execution mode (task 061). Served like
	// every other key: `image: ""` — the default — is what says the steps run
	// on the host, and a client that could not see it could not tell a
	// containerized installation from a plain one.
	Container configContainer `json:"container"`
	// TUI is view preference the daemon only relays (§15): it is in the file
	// the daemon owns and hot-reloads, so this endpoint is how the TUI — which
	// reads no configuration of its own — gets it.
	TUI configTUI `json:"tui"`
}

// configContainer is §16's block. ExtraMounts is always a list, empty
// included, for the same reason group_by is: `null` would be indistinguishable
// from a client's own default.
type configContainer struct {
	Image            string   `json:"image"`
	Runtime          string   `json:"runtime"`
	MountAgentConfig bool     `json:"mount_agent_config"`
	Network          bool     `json:"network"`
	ExtraMounts      []string `json:"extra_mounts"`
}

type configTUI struct {
	Board configBoard `json:"board"`
}

type configBoard struct {
	// GroupBy is always present, empty list included: `null` would make a
	// flat table indistinguishable from a client's own default.
	GroupBy []string `json:"group_by"`
}

type configDefaults struct {
	AgentTimeout   string `json:"agent_timeout"`
	CommandTimeout string `json:"command_timeout"`
	InputTimeout   string `json:"input_timeout"`
}

type agentPath struct {
	Path string `json:"path"`
}

// configAgents is a struct rather than the map the endpoint used to serve: the
// adapter set is fixed by §9, and a map made "which adapters exist" look like
// a runtime answer while also giving PATCH no shape to validate against.
type configAgents struct {
	Claude agentPath `json:"claude"`
	Codex  agentPath `json:"codex"`
	Cursor agentPath `json:"cursor"`
}

// configEnvironment is §12.3's child-process policy. Inherit is a union in
// JSON because it is a union in YAML: "all", "none", or a list of names.
type configEnvironment struct {
	Inherit configInherit     `json:"inherit"`
	Unset   []string          `json:"unset"`
	Set     map[string]string `json:"set"`
}

// configInherit renders as the string "all"/"none" or as an array of names.
// Spelling the two words as one-element arrays would read worse than the
// union does, and the config file already made this choice.
type configInherit struct {
	Mode  string
	Names []string
}

func (i configInherit) MarshalJSON() ([]byte, error) {
	if i.Mode == "list" {
		names := i.Names
		if names == nil {
			names = []string{}
		}
		return json.Marshal(names)
	}
	return json.Marshal(i.Mode)
}

func (i *configInherit) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "all", "none":
			*i = configInherit{Mode: strings.ToLower(strings.TrimSpace(s))}
			return nil
		default:
			return fmt.Errorf("environment.inherit: want \"all\", \"none\", or a list of names; got %q", s)
		}
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return errors.New("environment.inherit: want \"all\", \"none\", or a list of names")
	}
	*i = configInherit{Mode: "list", Names: names}
	return nil
}

// yaml renders the union back into the one line config.yaml carries.
func (i configInherit) yaml() string {
	switch i.Mode {
	case "none":
		return "none"
	case "list":
		return config.RenderList(i.Names)
	default:
		return "all"
	}
}

type configParallel struct {
	MaxParallel int `json:"max_parallel"`
}

type configFanOut struct {
	MaxDepth int `json:"max_depth"`
	MaxTasks int `json:"max_tasks"`
}

type configLoop struct {
	MaxIterations int `json:"max_iterations"`
}

type configInclude struct {
	MaxDepth int `json:"max_depth"`
}

type configMCP struct {
	WireSteps bool `json:"wire_steps"`
	MaxDepth  int  `json:"max_depth"`
	MaxTasks  int  `json:"max_tasks"`
}

type configGitHub struct {
	Enabled      bool   `json:"enabled"`
	PollInterval string `json:"poll_interval"`
}

// configUpdateStatus is the `update` block of config.yaml — the release-check
// policy, not GET /v1/update's cached answer.
type configUpdateStatus struct {
	Check        bool   `json:"check"`
	PollInterval string `json:"poll_interval"`
}

type configNotify struct {
	On []string `json:"on"`
	// Command is argv, served in full. It can carry a secret, and that is why
	// config.yaml is 0600 — not why this endpoint would hide it (decision 2).
	Command []string `json:"command"`
}

// configBody renders the effective configuration as the wire shape.
func configBody(cfg config.Config) configResponse {
	return configResponse{
		Listen:           cfg.Listen,
		MaxParallelTasks: cfg.MaxParallelTasks,
		BranchTemplate:   cfg.BranchTemplate,
		Defaults: configDefaults{
			AgentTimeout:   cfg.Defaults.AgentTimeout.String(),
			CommandTimeout: cfg.Defaults.CommandTimeout.String(),
			InputTimeout:   cfg.Defaults.InputTimeout.String(),
		},
		DeleteEmptyBranchOnArchive:  cfg.DeleteEmptyBranchOnArchive,
		DeleteRemoteBranchOnArchive: cfg.DeleteRemoteBranchOnArchive,
		FetchBaseBranch:             cfg.FetchBaseBranch,
		TranscriptRetentionDays:     cfg.TranscriptRetentionDays,
		TranscriptMaxBytes:          cfg.TranscriptMaxBytes.Bytes(),
		MaxTaskCostUSD:              cfg.MaxTaskCostUSD,
		UsageLimitRecheck:           cfg.UsageLimitRecheckInterval.String(),
		LogLevel:                    cfg.LogLevel,
		Debug:                       cfg.Debug,
		Environment: configEnvironment{
			Inherit: inheritBody(cfg.Environment.Inherit),
			Unset:   stringList(cfg.Environment.Unset),
			Set:     stringMap(cfg.Environment.Set),
		},
		Agents: configAgents{
			Claude: agentPath{Path: cfg.Agents.Claude.Path},
			Codex:  agentPath{Path: cfg.Agents.Codex.Path},
			Cursor: agentPath{Path: cfg.Agents.Cursor.Path},
		},
		Parallel: configParallel{MaxParallel: cfg.Parallel.MaxParallel},
		FanOut:   configFanOut{MaxDepth: cfg.FanOut.MaxDepth, MaxTasks: cfg.FanOut.MaxTasks},
		Loop:     configLoop{MaxIterations: cfg.Loop.MaxIterations},
		Include:  configInclude{MaxDepth: cfg.Include.MaxDepth},
		MCP: configMCP{
			WireSteps: cfg.MCP.WireSteps,
			MaxDepth:  cfg.MCP.MaxDepth,
			MaxTasks:  cfg.MCP.MaxTasks,
		},
		GitHub: configGitHub{
			Enabled:      cfg.GitHub.Enabled,
			PollInterval: cfg.GitHub.PollInterval.String(),
		},
		Update: configUpdateStatus{
			Check:        cfg.Update.Check,
			PollInterval: cfg.Update.PollInterval.String(),
		},
		Notify: configNotify{On: notifyStates(cfg.Notify.On), Command: stringList(cfg.Notify.Command)},
		Container: configContainer{
			Image:            cfg.Container.Image,
			Runtime:          cfg.Container.Runtime,
			MountAgentConfig: cfg.Container.MountAgentConfig,
			Network:          cfg.Container.Network,
			ExtraMounts:      stringList(cfg.Container.ExtraMounts),
		},
		TUI: configTUI{Board: configBoard{GroupBy: boardGroupBy(cfg.TUI.Board.GroupBy)}},
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, configBody(s.deps.Config()))
}

// boardGroupBy renders the grouping levels as strings, never as JSON null: a
// flat table is a configured choice and has to read as `[]`.
func boardGroupBy(levels []config.BoardGroup) []string {
	out := make([]string, 0, len(levels))
	for _, l := range levels {
		out = append(out, string(l))
	}
	return out
}

// notifyStates renders the §6 states notify fires on, `[]` when none.
func notifyStates(states []taskstate.State) []string {
	out := make([]string, 0, len(states))
	for _, st := range states {
		out = append(out, string(st))
	}
	return out
}

// ftoa renders a dollar ceiling the way a human writes one: no exponent and
// no trailing zeros, so 0 stays "0" rather than becoming "0.000000".
func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// stringList renders a list as `[]` rather than `null`, on the same rule
// boardGroupBy follows: an empty list is a configured choice.
func stringList(items []string) []string {
	out := make([]string, 0, len(items))
	out = append(out, items...)
	return out
}

func stringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func inheritBody(i config.Inherit) configInherit {
	switch i.Mode {
	case config.InheritNoneMode:
		return configInherit{Mode: "none"}
	case config.InheritListMode:
		return configInherit{Mode: "list", Names: stringList(i.Names)}
	default:
		return configInherit{Mode: "all"}
	}
}

// configPatch is the PATCH body: the read shape with every key optional, on
// the pattern PATCH /v1/projects/{id} and PATCH /v1/tasks/{id} set. A key
// absent from the body is a key the file keeps.
type configPatch struct {
	Listen                      *string            `json:"listen"`
	MaxParallelTasks            *int               `json:"max_parallel_tasks"`
	BranchTemplate              *string            `json:"branch_template"`
	Defaults                    *defaultsPatch     `json:"defaults"`
	DeleteEmptyBranchOnArchive  *bool              `json:"delete_empty_branch_on_archive"`
	DeleteRemoteBranchOnArchive *bool              `json:"delete_remote_branch_on_archive"`
	FetchBaseBranch             *bool              `json:"fetch_base_branch"`
	TranscriptRetentionDays     *int               `json:"transcript_retention_days"`
	TranscriptMaxBytes          *int64             `json:"transcript_max_bytes"`
	MaxTaskCostUSD              *float64           `json:"max_task_cost_usd"`
	UsageLimitRecheck           *string            `json:"usage_limit_recheck_interval"`
	LogLevel                    *string            `json:"log_level"`
	Debug                       *bool              `json:"debug"`
	Environment                 *environmentPatch  `json:"environment"`
	Agents                      *agentsPatch       `json:"agents"`
	Parallel                    *parallelPatch     `json:"parallel"`
	FanOut                      *fanOutPatch       `json:"fan_out"`
	Loop                        *loopPatch         `json:"loop"`
	Include                     *includePatch      `json:"include"`
	MCP                         *mcpPatch          `json:"mcp"`
	GitHub                      *githubPatch       `json:"github"`
	Update                      *updatePolicyPatch `json:"update"`
	Notify                      *notifyPatch       `json:"notify"`
	Container                   *containerPatch    `json:"container"`
	TUI                         *tuiPatch          `json:"tui"`
}

type defaultsPatch struct {
	AgentTimeout   *string `json:"agent_timeout"`
	CommandTimeout *string `json:"command_timeout"`
	InputTimeout   *string `json:"input_timeout"`
}

type environmentPatch struct {
	Inherit *configInherit     `json:"inherit"`
	Unset   *[]string          `json:"unset"`
	Set     *map[string]string `json:"set"`
}

type agentsPatch struct {
	Claude *agentPatch `json:"claude"`
	Codex  *agentPatch `json:"codex"`
	Cursor *agentPatch `json:"cursor"`
}

type agentPatch struct {
	Path *string `json:"path"`
}

type parallelPatch struct {
	MaxParallel *int `json:"max_parallel"`
}

type fanOutPatch struct {
	MaxDepth *int `json:"max_depth"`
	MaxTasks *int `json:"max_tasks"`
}

type loopPatch struct {
	MaxIterations *int `json:"max_iterations"`
}

type includePatch struct {
	MaxDepth *int `json:"max_depth"`
}

type mcpPatch struct {
	WireSteps *bool `json:"wire_steps"`
	MaxDepth  *int  `json:"max_depth"`
	MaxTasks  *int  `json:"max_tasks"`
}

type githubPatch struct {
	Enabled      *bool   `json:"enabled"`
	PollInterval *string `json:"poll_interval"`
}

type updatePolicyPatch struct {
	Check        *bool   `json:"check"`
	PollInterval *string `json:"poll_interval"`
}

type notifyPatch struct {
	On      *[]string `json:"on"`
	Command *[]string `json:"command"`
}

type containerPatch struct {
	Image            *string   `json:"image"`
	Runtime          *string   `json:"runtime"`
	MountAgentConfig *bool     `json:"mount_agent_config"`
	Network          *bool     `json:"network"`
	ExtraMounts      *[]string `json:"extra_mounts"`
}

type tuiPatch struct {
	Board *boardPatch `json:"board"`
}

type boardPatch struct {
	GroupBy *[]string `json:"group_by"`
}

// sets turns the patch into the dotted-path assignments the config editor
// applies. Nothing is type-checked here beyond what JSON already decided:
// every value is rendered as YAML and the candidate file is decoded through
// config.Decode, so the rule that rejects a bad value is the same rule the
// next daemon start would apply.
func (p configPatch) sets() []config.Set {
	var out []config.Set
	add := func(path, value string) { out = append(out, config.Set{Path: path, Value: value}) }
	if p.Listen != nil {
		add("listen", config.RenderString(*p.Listen))
	}
	if p.MaxParallelTasks != nil {
		add("max_parallel_tasks", strconv.Itoa(*p.MaxParallelTasks))
	}
	if p.BranchTemplate != nil {
		add("branch_template", config.RenderString(*p.BranchTemplate))
	}
	if d := p.Defaults; d != nil {
		addIfString(add, "defaults.agent_timeout", d.AgentTimeout)
		addIfString(add, "defaults.command_timeout", d.CommandTimeout)
		addIfString(add, "defaults.input_timeout", d.InputTimeout)
	}
	addIfBool(add, "delete_empty_branch_on_archive", p.DeleteEmptyBranchOnArchive)
	addIfBool(add, "delete_remote_branch_on_archive", p.DeleteRemoteBranchOnArchive)
	addIfBool(add, "fetch_base_branch", p.FetchBaseBranch)
	if p.TranscriptRetentionDays != nil {
		add("transcript_retention_days", strconv.Itoa(*p.TranscriptRetentionDays))
	}
	if p.TranscriptMaxBytes != nil {
		// Rendered in the unit a human would have written: ByteSize.String
		// picks the largest unit that divides exactly, so 512MB stays 512MB.
		add("transcript_max_bytes", config.RenderString(config.ByteSize(*p.TranscriptMaxBytes).String()))
	}
	if p.MaxTaskCostUSD != nil {
		add("max_task_cost_usd", ftoa(*p.MaxTaskCostUSD))
	}
	addIfString(add, "usage_limit_recheck_interval", p.UsageLimitRecheck)
	addIfString(add, "log_level", p.LogLevel)
	addIfBool(add, "debug", p.Debug)
	if e := p.Environment; e != nil {
		if e.Inherit != nil {
			add("environment.inherit", e.Inherit.yaml())
		}
		if e.Unset != nil {
			add("environment.unset", config.RenderList(*e.Unset))
		}
		if e.Set != nil {
			add("environment.set", config.RenderMap(*e.Set))
		}
	}
	if a := p.Agents; a != nil {
		for name, ap := range map[string]*agentPatch{"claude": a.Claude, "codex": a.Codex, "cursor": a.Cursor} {
			if ap != nil {
				addIfString(add, "agents."+name+".path", ap.Path)
			}
		}
	}
	if v := p.Parallel; v != nil {
		addIfInt(add, "parallel.max_parallel", v.MaxParallel)
	}
	if v := p.FanOut; v != nil {
		addIfInt(add, "fan_out.max_depth", v.MaxDepth)
		addIfInt(add, "fan_out.max_tasks", v.MaxTasks)
	}
	if v := p.Loop; v != nil {
		addIfInt(add, "loop.max_iterations", v.MaxIterations)
	}
	if v := p.Include; v != nil {
		addIfInt(add, "include.max_depth", v.MaxDepth)
	}
	if v := p.MCP; v != nil {
		addIfBool(add, "mcp.wire_steps", v.WireSteps)
		addIfInt(add, "mcp.max_depth", v.MaxDepth)
		addIfInt(add, "mcp.max_tasks", v.MaxTasks)
	}
	if v := p.GitHub; v != nil {
		addIfBool(add, "github.enabled", v.Enabled)
		addIfString(add, "github.poll_interval", v.PollInterval)
	}
	if v := p.Update; v != nil {
		addIfBool(add, "update.check", v.Check)
		addIfString(add, "update.poll_interval", v.PollInterval)
	}
	if v := p.Notify; v != nil {
		if v.On != nil {
			add("notify.on", config.RenderList(*v.On))
		}
		if v.Command != nil {
			add("notify.command", config.RenderList(*v.Command))
		}
	}
	if v := p.Container; v != nil {
		addIfString(add, "container.image", v.Image)
		addIfString(add, "container.runtime", v.Runtime)
		addIfBool(add, "container.mount_agent_config", v.MountAgentConfig)
		addIfBool(add, "container.network", v.Network)
		if v.ExtraMounts != nil {
			add("container.extra_mounts", config.RenderList(*v.ExtraMounts))
		}
	}
	if v := p.TUI; v != nil && v.Board != nil && v.Board.GroupBy != nil {
		add("tui.board.group_by", config.RenderList(*v.Board.GroupBy))
	}
	// Order is the struct's, which is config.yaml's, so two clients sending
	// the same patch produce the same bytes.
	return out
}

func addIfString(add func(string, string), path string, v *string) {
	if v != nil {
		add(path, config.RenderString(*v))
	}
}

func addIfBool(add func(string, string), path string, v *bool) {
	if v != nil {
		if *v {
			add(path, "true")
		} else {
			add(path, "false")
		}
	}
}

func addIfInt(add func(string, string), path string, v *int) {
	if v != nil {
		add(path, strconv.Itoa(*v))
	}
}

// handleConfigPatch applies a partial edit to config.yaml and puts it into
// force before answering (task 060 decision 5).
//
// The order is the whole contract: read the file fresh, apply the sets to its
// bytes, decode the candidate, refuse the request without touching the disk if
// it does not hold, write atomically at 0600, then apply synchronously. A GET
// issued the instant a 200 lands reads the new values, with no sleep — the
// fsnotify watcher's later fire re-reads identical bytes and is a no-op.
//
// One mutex serializes the read-modify-write (decision 6). A hand-edit racing
// a patch is last-writer-wins and undetected, which is the posture PATCH
// /v1/projects/{id} already has; there is no ETag, because a precondition
// concept no other endpoint carries would exist for a race between a human and
// themselves.
func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	var patch configPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	sets := patch.sets()
	if len(sets) == 0 {
		// Nothing to write is not an error: it is the configuration unchanged.
		writeJSON(w, http.StatusOK, configBody(s.deps.Config()))
		return
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()

	path := s.configPath()
	if path == "" {
		s.internalError(w, "config path", errors.New("no config directory is wired"))
		return
	}
	//nolint:gosec // G304: the path is the daemon's own resolved config dir
	src, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.internalError(w, "read config", err)
		return
	}
	next, err := config.Apply(src, sets)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
		return
	}
	cfg, err := config.Decode(next)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
		return
	}
	// The same check daemon.Run's reload path applies. config cannot make it
	// itself: it is a leaf package and the template context lives in
	// internal/worktree.
	if err := worktree.ValidateBranchTemplate(cfg.BranchTemplate); err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"branch_template does not compile: "+err.Error())
		return
	}
	if err := config.WriteFile(path, next); err != nil {
		s.internalError(w, "write config", err)
		return
	}
	if s.deps.ApplyConfig != nil {
		s.deps.ApplyConfig(cfg)
	}
	// Served from the applier's own view rather than from cfg: `listen` is
	// pinned until restart, and reporting the value the daemon is actually
	// running on is the honest answer to "what is in force now".
	writeJSON(w, http.StatusOK, configBody(s.deps.Config()))
}

// configPath is the config.yaml this daemon owns.
func (s *Server) configPath() string {
	if s.deps.Dirs.Config == "" {
		return ""
	}
	return filepath.Join(s.deps.Dirs.Config, config.FileName)
}
