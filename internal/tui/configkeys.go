package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/taskstate"
)

// The editable surface of config.yaml, as the daemon view presents it
// (task 060).
//
// One table, because every row needs the same four things and a switch
// statement per column would put them in four places: what the key is called,
// what shape its value has, how to read the current one out of GET /v1/config,
// and how to spell a change as a PATCH body. A key the daemon serves and this
// table omits is a key the TUI shows and cannot edit, which is the state this
// task was opened to end — a test asserts the table covers the served shape.
//
// internal/config is imported for Default() alone. It is a leaf package, so
// the dependency direction stays one-way, and reading the built-in defaults
// from the package that defines them beats copying them into a second list
// that would drift. It is *not* the TUI reading configuration from disk (§15):
// the values shown are the daemon's, off the API; only the defaults compared
// against are compiled in.

// configKind is how a row is edited: a free-text field, a chooser, or a list.
type configKind int

const (
	// kindText is a typed value the daemon parses — a duration, a size, a
	// template, a path. Its rules live in internal/config, so the field
	// accepts anything and the daemon's rejection is what renders.
	kindText configKind = iota
	// kindInt and kindFloat are checked here as well as server-side, so a
	// typo does not cost a round trip.
	kindInt
	kindFloat
	// kindBool and kindEnum are chosen from a fixed vocabulary rather than
	// typed: there is no reason to let someone spell "ture".
	kindBool
	kindEnum
	// kindList is whitespace-separated tokens: argv, variable names, or
	// grouping levels. An argument containing a space cannot be written this
	// way and has to be edited in the file; the modal says so.
	kindList
	// kindMap is whitespace-separated NAME=VALUE pairs (environment.set).
	kindMap
)

// configKey is one editable key.
type configKey struct {
	// path is the dotted path, and is also what the confirmation names: it is
	// the spelling that appears in config.yaml and on the config reference.
	path  string
	label string
	kind  configKind
	// choices is the vocabulary for kindBool, kindEnum and kindList.
	choices []string
	// dangerous marks a key that decides what the daemon executes or exposes.
	// The issue names four — notify.command, environment, agents.*.path and
	// listen — and every one of them is a key where a stray keystroke changes
	// the argv the daemon spawns as you, or the address it binds. Agents run
	// full-auto by default (§16); these ask before they apply.
	dangerous bool
	// restart marks a key the running daemon keeps until it is restarted. Only
	// `listen` is one: the reload path pins it deliberately, so PATCH writes
	// the file and GET keeps reporting the address actually bound.
	restart bool
	// help is the one line the modal prints under the field.
	help string
	// read renders the value in force. write turns edited text into a patch,
	// or refuses it with the message the modal shows against the field.
	read  func(apiclient.Config) string
	write func(string) (apiclient.ConfigPatch, error)
	// show is the block's rendering when the raw value is not the honest one
	// to read: a zero cost cap is "off", not "0", and a retention of 90 is
	// "90 days". It is never what the editor seeds — that is always read, so
	// what you edit is what the file will carry. Nil means read.
	show func(apiclient.Config) string
}

// display is what the config block prints for this key.
func (k configKey) display(c apiclient.Config) string {
	if k.show != nil {
		return k.show(c)
	}
	return k.read(c)
}

// def is the built-in default for this key, rendered the way read renders a
// value, so a row can say what it would be if the file said nothing.
func (k configKey) def() string { return k.read(defaultClientConfig()) }

// boolChoices and the enum vocabularies. group_by and notify.on are lists
// drawn from a vocabulary rather than free text, so the modal can offer them.
var (
	boolChoices     = []string{"false", "true"}
	logLevelChoices = []string{"debug", "info", "warn", "error"}
	groupByChoices  = []string{"project", "workflow"}
	inheritChoices  = []string{"all", "none"}
)

// configKeys is the editable table, in the order config.yaml carries the keys
// so the block reads like the file.
func configKeys() []configKey {
	keys := []configKey{
		{
			path: "listen", label: "listen", kind: kindText, dangerous: true, restart: true,
			help:  "loopback address the API binds to; port 0 picks an ephemeral one",
			read:  func(c apiclient.Config) string { return c.Listen },
			write: func(s string) (apiclient.ConfigPatch, error) { return apiclient.ConfigPatch{Listen: &s}, nil },
		},
		{
			path: "max_parallel_tasks", label: "max parallel tasks", kind: kindInt,
			help: "global cap on concurrently running tasks",
			read: func(c apiclient.Config) string { return strconv.Itoa(c.MaxParallelTasks) },
			write: func(s string) (apiclient.ConfigPatch, error) {
				n, err := parseInt(s)
				return apiclient.ConfigPatch{MaxParallelTasks: &n}, err
			},
		},
		{
			path: "branch_template", label: "branch template", kind: kindText,
			help: "Go template for a task's branch name; empty uses the built-in",
			read: func(c apiclient.Config) string { return c.BranchTemplate },
			write: func(s string) (apiclient.ConfigPatch, error) {
				return apiclient.ConfigPatch{BranchTemplate: &s}, nil
			},
		},
		durationKey("defaults.agent_timeout", "agent timeout", "fallback timeout for an agent step",
			func(c apiclient.Config) string { return c.Defaults.AgentTimeout },
			func(s string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Defaults: &apiclient.ConfigDefaultsPatch{AgentTimeout: &s}}
			}),
		durationKey("defaults.command_timeout", "command timeout", "fallback timeout for a command step",
			func(c apiclient.Config) string { return c.Defaults.CommandTimeout },
			func(s string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Defaults: &apiclient.ConfigDefaultsPatch{CommandTimeout: &s}}
			}),
		durationKey("defaults.input_timeout", "input timeout", "how long a step waits for an answer (§7.4)",
			func(c apiclient.Config) string { return c.Defaults.InputTimeout },
			func(s string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Defaults: &apiclient.ConfigDefaultsPatch{InputTimeout: &s}}
			}),
		boolKey("delete_empty_branch_on_archive", "delete empty branch",
			"delete an archived task's branch when it carries no commits (§10)",
			func(c apiclient.Config) bool { return c.DeleteEmptyBranchOnArchive },
			func(b bool) apiclient.ConfigPatch { return apiclient.ConfigPatch{DeleteEmptyBranchOnArchive: &b} }),
		boolKey("delete_remote_branch_on_archive", "delete remote branch",
			"also delete its upstream counterpart; inert while the local one is off",
			func(c apiclient.Config) bool { return c.DeleteRemoteBranchOnArchive },
			func(b bool) apiclient.ConfigPatch { return apiclient.ConfigPatch{DeleteRemoteBranchOnArchive: &b} }),
		boolKey("fetch_base_branch", "fetch base branch",
			"refresh a task's base from its remote before the worktree is cut",
			func(c apiclient.Config) bool { return c.FetchBaseBranch },
			func(b bool) apiclient.ConfigPatch { return apiclient.ConfigPatch{FetchBaseBranch: &b} }),
		{
			path: "transcript_retention_days", label: "transcript retention", kind: kindInt,
			help: "days an archived task's transcripts are kept",
			read: func(c apiclient.Config) string { return strconv.Itoa(c.TranscriptRetentionDays) },
			show: func(c apiclient.Config) string { return strconv.Itoa(c.TranscriptRetentionDays) + " days" },
			write: func(s string) (apiclient.ConfigPatch, error) {
				n, err := parseInt(s)
				return apiclient.ConfigPatch{TranscriptRetentionDays: &n}, err
			},
		},
		{
			path: "transcript_max_bytes", label: "transcript cap", kind: kindText,
			help: "per-attempt transcript ceiling, e.g. 512MB",
			read: func(c apiclient.Config) string { return config.ByteSize(c.TranscriptMaxBytes).String() },
			show: func(c apiclient.Config) string { return humanBytes(c.TranscriptMaxBytes) },
			write: func(s string) (apiclient.ConfigPatch, error) {
				// Parsed here as well as server-side so the accepted spellings
				// are one implementation: config.ParseByteSize is exported for
				// exactly this.
				v, err := config.ParseByteSize(strings.TrimSpace(s))
				n := v.Bytes()
				return apiclient.ConfigPatch{TranscriptMaxBytes: &n}, err
			},
		},
		{
			path: "max_task_cost_usd", label: "max task cost", kind: kindFloat,
			help: "per-task spend ceiling in US dollars; 0 is no cap",
			read: func(c apiclient.Config) string { return strconv.FormatFloat(c.MaxTaskCostUSD, 'f', -1, 64) },
			// task 033: an unset cap reads "off", never "$0.00" — that
			// spelling is reserved for a task whose adapters reported nothing.
			show: costCapText,
			write: func(s string) (apiclient.ConfigPatch, error) {
				f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
				if err != nil {
					return apiclient.ConfigPatch{}, fmt.Errorf("want a number, got %q", s)
				}
				return apiclient.ConfigPatch{MaxTaskCostUSD: &f}, nil
			},
		},
		durationKey("usage_limit_recheck_interval", "usage limit recheck",
			"how long a quota-held task waits when the CLI named no reset time",
			func(c apiclient.Config) string { return c.UsageLimitRecheck },
			func(s string) apiclient.ConfigPatch { return apiclient.ConfigPatch{UsageLimitRecheck: &s} }),
		{
			path: "log_level", label: "log level", kind: kindEnum, choices: logLevelChoices,
			help:  "daemon log verbosity",
			read:  func(c apiclient.Config) string { return c.LogLevel },
			write: func(s string) (apiclient.ConfigPatch, error) { return apiclient.ConfigPatch{LogLevel: &s}, nil },
		},
		boolKey("debug", "debug", "record resolved argv and prompt in every transcript",
			func(c apiclient.Config) bool { return c.Debug },
			func(b bool) apiclient.ConfigPatch { return apiclient.ConfigPatch{Debug: &b} }),
		{
			path: "environment.inherit", label: "environment inherit", kind: kindText, dangerous: true,
			choices: inheritChoices,
			help:    "\"all\", \"none\", or whitespace-separated names to inherit",
			read:    func(c apiclient.Config) string { return c.Environment.Inherit.String() },
			write: func(s string) (apiclient.ConfigPatch, error) {
				v := parseInherit(s)
				return apiclient.ConfigPatch{Environment: &apiclient.ConfigEnvironmentPatch{Inherit: &v}}, nil
			},
		},
		{
			path: "environment.unset", label: "environment unset", kind: kindList, dangerous: true,
			help: "variable names dropped after inherit",
			read: func(c apiclient.Config) string { return strings.Join(c.Environment.Unset, " ") },
			write: func(s string) (apiclient.ConfigPatch, error) {
				v := strings.Fields(s)
				return apiclient.ConfigPatch{Environment: &apiclient.ConfigEnvironmentPatch{Unset: &v}}, nil
			},
		},
		{
			path: "environment.set", label: "environment set", kind: kindMap, dangerous: true,
			help: "NAME=VALUE pairs, applied last; values are literal",
			read: func(c apiclient.Config) string { return joinPairs(c.Environment.Set) },
			write: func(s string) (apiclient.ConfigPatch, error) {
				m, err := parsePairs(s)
				return apiclient.ConfigPatch{Environment: &apiclient.ConfigEnvironmentPatch{Set: &m}}, err
			},
		},
		agentPathKey("claude", func(c apiclient.Config) string { return c.Agents.Claude.Path },
			func(p *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Agents: &apiclient.ConfigAgentsPatch{Claude: &apiclient.AgentPathPatch{Path: p}}}
			}),
		agentPathKey("codex", func(c apiclient.Config) string { return c.Agents.Codex.Path },
			func(p *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Agents: &apiclient.ConfigAgentsPatch{Codex: &apiclient.AgentPathPatch{Path: p}}}
			}),
		agentPathKey("cursor", func(c apiclient.Config) string { return c.Agents.Cursor.Path },
			func(p *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Agents: &apiclient.ConfigAgentsPatch{Cursor: &apiclient.AgentPathPatch{Path: p}}}
			}),
		intKey("parallel.max_parallel", "parallel lanes", "concurrent verifications inside one parallel step (§7.5)",
			func(c apiclient.Config) int { return c.Parallel.MaxParallel },
			func(n int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Parallel: &apiclient.ConfigParallel{MaxParallel: n}}
			}),
		intKey("fan_out.max_depth", "fan-out depth", "generations a fan_out tree may go deep (§7.6)",
			func(c apiclient.Config) int { return c.FanOut.MaxDepth },
			func(n int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{FanOut: &apiclient.ConfigFanOut{MaxDepth: n}}
			}),
		intKey("fan_out.max_tasks", "fan-out tasks", "descendants one root may create (§7.6)",
			func(c apiclient.Config) int { return c.FanOut.MaxTasks },
			func(n int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{FanOut: &apiclient.ConfigFanOut{MaxTasks: n}}
			}),
		intKey("loop.max_iterations", "loop iterations", "ceiling on one loop step's iterations (§7.8)",
			func(c apiclient.Config) int { return c.Loop.MaxIterations },
			func(n int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Loop: &apiclient.ConfigLoop{MaxIterations: n}}
			}),
		intKey("include.max_depth", "include depth", "levels of include a workflow may nest (§7.9)",
			func(c apiclient.Config) int { return c.Include.MaxDepth },
			func(n int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Include: &apiclient.ConfigInclude{MaxDepth: n}}
			}),
		boolKey("mcp.wire_steps", "mcp wire steps", "hand agent steps their own /mcp endpoint (§13.4)",
			func(c apiclient.Config) bool { return c.MCP.WireSteps },
			func(b bool) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{MCP: &apiclient.ConfigMCPPatch{WireSteps: &b}}
			}),
		intKey("mcp.max_depth", "mcp depth", "generations of tasks an agent's tools may create",
			func(c apiclient.Config) int { return c.MCP.MaxDepth },
			func(n int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{MCP: &apiclient.ConfigMCPPatch{MaxDepth: &n}}
			}),
		intKey("mcp.max_tasks", "mcp tasks", "tasks an agent's tools may create in one chain",
			func(c apiclient.Config) int { return c.MCP.MaxTasks },
			func(n int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{MCP: &apiclient.ConfigMCPPatch{MaxTasks: &n}}
			}),
		boolKey("github.enabled", "github enabled", "let the daemon read GitHub at all (task 035)",
			func(c apiclient.Config) bool { return c.GitHub.Enabled },
			func(b bool) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{GitHub: &apiclient.ConfigGitHubPatch{Enabled: &b}}
			}),
		durationKey("github.poll_interval", "github poll", "how often the pull-request reconciler runs; 0 is off",
			func(c apiclient.Config) string { return c.GitHub.PollInterval },
			func(s string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{GitHub: &apiclient.ConfigGitHubPatch{PollInterval: &s}}
			}),
		boolKey("update.check", "update check", "ask GitHub once a day whether a newer vincent exists",
			func(c apiclient.Config) bool { return c.Update.Check },
			func(b bool) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Update: &apiclient.ConfigUpdatePatch{Check: &b}}
			}),
		durationKey("update.poll_interval", "update poll", "how often that check runs; 0 is off",
			func(c apiclient.Config) string { return c.Update.PollInterval },
			func(s string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Update: &apiclient.ConfigUpdatePatch{PollInterval: &s}}
			}),
		{
			path: "notify.on", label: "notify on", kind: kindList, choices: stateNames(),
			help: "states that fire the notify hook, whitespace-separated",
			read: func(c apiclient.Config) string { return strings.Join(c.Notify.On, " ") },
			write: func(s string) (apiclient.ConfigPatch, error) {
				v := strings.Fields(s)
				return apiclient.ConfigPatch{Notify: &apiclient.ConfigNotifyPatch{On: &v}}, nil
			},
		},
		{
			path: "notify.command", label: "notify command", kind: kindList, dangerous: true,
			help: "argv the daemon runs as you; one argument per whitespace-separated word",
			read: func(c apiclient.Config) string { return strings.Join(c.Notify.Command, " ") },
			write: func(s string) (apiclient.ConfigPatch, error) {
				v := strings.Fields(s)
				return apiclient.ConfigPatch{Notify: &apiclient.ConfigNotifyPatch{Command: &v}}, nil
			},
		},
		{
			path: "tui.board.group_by", label: "task grouping", kind: kindList, choices: groupByChoices,
			help: "grouping levels for the board, outermost first; empty is a flat table",
			read: func(c apiclient.Config) string { return strings.Join(c.TUI.Board.GroupBy, " ") },
			show: func(c apiclient.Config) string { return groupSummary(c.TUI.Board.GroupBy) },
			write: func(s string) (apiclient.ConfigPatch, error) {
				v := strings.Fields(s)
				return apiclient.ConfigPatch{TUI: &apiclient.ConfigTUIPatch{Board: &apiclient.ConfigBoardPatch{GroupBy: &v}}}, nil
			},
		},
	}
	return keys
}

// agentPathKey is the §9 adapter binary rows. All three are dangerous for one
// reason: the path is the program the daemon execs for every agent step.
func agentPathKey(name string, read func(apiclient.Config) string,
	patch func(*string) apiclient.ConfigPatch,
) configKey {
	return configKey{
		path: "agents." + name + ".path", label: name + " path", kind: kindText, dangerous: true,
		help: "binary vincent runs for " + name + "; empty resolves it from PATH",
		read: read,
		write: func(s string) (apiclient.ConfigPatch, error) {
			v := strings.TrimSpace(s)
			return patch(&v), nil
		},
	}
}

func durationKey(path, label, help string, read func(apiclient.Config) string,
	patch func(string) apiclient.ConfigPatch,
) configKey {
	return configKey{
		path: path, label: label, kind: kindText, help: help + " — a duration like 60m or 24h",
		read: read,
		write: func(s string) (apiclient.ConfigPatch, error) {
			return patch(strings.TrimSpace(s)), nil
		},
	}
}

func boolKey(path, label, help string, read func(apiclient.Config) bool,
	patch func(bool) apiclient.ConfigPatch,
) configKey {
	return configKey{
		path: path, label: label, kind: kindBool, choices: boolChoices, help: help,
		read: func(c apiclient.Config) string { return strconv.FormatBool(read(c)) },
		write: func(s string) (apiclient.ConfigPatch, error) {
			return patch(s == "true"), nil
		},
	}
}

func intKey(path, label, help string, read func(apiclient.Config) int,
	patch func(int) apiclient.ConfigPatch,
) configKey {
	return configKey{
		path: path, label: label, kind: kindInt, help: help,
		read: func(c apiclient.Config) string { return strconv.Itoa(read(c)) },
		write: func(s string) (apiclient.ConfigPatch, error) {
			n, err := parseInt(s)
			if err != nil {
				return apiclient.ConfigPatch{}, err
			}
			return patch(n), nil
		},
	}
}

func parseInt(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("want a whole number, got %q", s)
	}
	return n, nil
}

// parseInherit reads the union the way config.yaml spells it: the two words,
// or a list of names. A bare list is accepted with or without brackets, since
// the row renders the bracketed form and someone editing it will leave them.
func parseInherit(s string) apiclient.ConfigInherit {
	t := strings.TrimSpace(s)
	switch strings.ToLower(t) {
	case "all", "none":
		return apiclient.ConfigInherit{Mode: strings.ToLower(t)}
	}
	t = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	names := strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	return apiclient.ConfigInherit{Mode: "list", Names: names}
}

// joinPairs and parsePairs are environment.set's one-line form. A value
// containing a space cannot be written here and has to be edited in the file;
// the modal's help line says so rather than silently truncating one.
func joinPairs(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

func parsePairs(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, tok := range strings.Fields(s) {
		name, value, ok := strings.Cut(tok, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("want NAME=VALUE pairs, got %q", tok)
		}
		out[name] = value
	}
	return out, nil
}

// stateNames is the §6 vocabulary notify.on draws from, offered as choices so
// the modal can list them rather than making someone remember them.
func stateNames() []string {
	states := taskstate.All
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, string(s))
	}
	return out
}

// defaultClientConfig renders config.Default() in the wire shape, so a row can
// print what a key would be if the file said nothing about it.
//
// GET /v1/config serves effective values and carries no provenance: it cannot
// say whether a value came from the file or from the built-in. So the block
// says "differs from the default", which is what it actually knows, rather
// than "set in the file", which it does not.
func defaultClientConfig() apiclient.Config {
	d := config.Default()
	return apiclient.Config{
		Listen:           d.Listen,
		MaxParallelTasks: d.MaxParallelTasks,
		BranchTemplate:   d.BranchTemplate,
		Defaults: apiclient.ConfigDefaults{
			AgentTimeout:   d.Defaults.AgentTimeout.String(),
			CommandTimeout: d.Defaults.CommandTimeout.String(),
			InputTimeout:   d.Defaults.InputTimeout.String(),
		},
		DeleteEmptyBranchOnArchive:  d.DeleteEmptyBranchOnArchive,
		DeleteRemoteBranchOnArchive: d.DeleteRemoteBranchOnArchive,
		FetchBaseBranch:             d.FetchBaseBranch,
		TranscriptRetentionDays:     d.TranscriptRetentionDays,
		TranscriptMaxBytes:          d.TranscriptMaxBytes.Bytes(),
		MaxTaskCostUSD:              d.MaxTaskCostUSD,
		UsageLimitRecheck:           d.UsageLimitRecheckInterval.String(),
		LogLevel:                    d.LogLevel,
		Debug:                       d.Debug,
		Environment: apiclient.ConfigEnvironment{
			Inherit: apiclient.ConfigInherit{Mode: inheritMode(d.Environment.Inherit), Names: d.Environment.Inherit.Names},
			Unset:   d.Environment.Unset,
			Set:     d.Environment.Set,
		},
		Agents: apiclient.ConfigAgents{
			Claude: apiclient.AgentPath{Path: d.Agents.Claude.Path},
			Codex:  apiclient.AgentPath{Path: d.Agents.Codex.Path},
			Cursor: apiclient.AgentPath{Path: d.Agents.Cursor.Path},
		},
		Parallel: apiclient.ConfigParallel{MaxParallel: d.Parallel.MaxParallel},
		FanOut:   apiclient.ConfigFanOut{MaxDepth: d.FanOut.MaxDepth, MaxTasks: d.FanOut.MaxTasks},
		Loop:     apiclient.ConfigLoop{MaxIterations: d.Loop.MaxIterations},
		Include:  apiclient.ConfigInclude{MaxDepth: d.Include.MaxDepth},
		MCP: apiclient.ConfigMCP{
			WireSteps: d.MCP.WireSteps, MaxDepth: d.MCP.MaxDepth, MaxTasks: d.MCP.MaxTasks,
		},
		GitHub: apiclient.ConfigGitHub{Enabled: d.GitHub.Enabled, PollInterval: d.GitHub.PollInterval.String()},
		Update: apiclient.ConfigUpdate{Check: d.Update.Check, PollInterval: d.Update.PollInterval.String()},
		Notify: apiclient.ConfigNotify{On: notifyStateNames(d), Command: d.Notify.Command},
		TUI:    apiclient.ConfigTUI{Board: apiclient.ConfigBoard{GroupBy: boardGroupNames(d)}},
	}
}

func inheritMode(i config.Inherit) string {
	switch i.Mode {
	case config.InheritNoneMode:
		return "none"
	case config.InheritListMode:
		return "list"
	default:
		return "all"
	}
}

func notifyStateNames(c config.Config) []string {
	out := make([]string, 0, len(c.Notify.On))
	for _, s := range c.Notify.On {
		out = append(out, string(s))
	}
	return out
}

func boardGroupNames(c config.Config) []string {
	out := make([]string, 0, len(c.TUI.Board.GroupBy))
	for _, g := range c.TUI.Board.GroupBy {
		out = append(out, string(g))
	}
	return out
}
