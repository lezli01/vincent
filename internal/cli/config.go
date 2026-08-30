package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `vincent config` (task 060, §12.1).
//
// The daemon owns config.yaml and hot-reloads it; this is a client of
// PATCH /v1/config like every other command here, not a second editor. That
// is what makes `vincent config set` and the TUI's editor the same operation
// with the same validation, and why a set takes effect without a restart.
//
// Keys are the dotted paths config.yaml carries, which are also the paths the
// configuration reference documents and the TUI's editor names — one spelling
// everywhere. `vincent config get` with no key prints them all.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and change the daemon configuration",
		Long: "Read and change config.yaml through the daemon (§12.3).\n\n" +
			"The daemon validates the whole file before writing it and applies the " +
			"result before answering, so a set takes effect without a restart — " +
			"except `listen`, which the running daemon keeps until it is restarted. " +
			"An invalid value changes nothing.\n\n" +
			"Comments and key order in config.yaml survive a set: the daemon edits " +
			"the key in place rather than regenerating the file.",
	}
	cmd.AddCommand(newConfigGetCmd(), newConfigSetCmd())
	return cmd
}

func newConfigGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get [key]",
		Short: "Print the configuration in effect, or one key of it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				cfg, err := c.Config(ctx)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if len(args) == 0 {
					if wantJSON(cmd) {
						return emitJSON(cmd.OutOrStdout(), cfg)
					}
					return renderConfig(cmd.OutOrStdout(), cfg)
				}
				value, ok := configValue(cfg, args[0])
				if !ok {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Error: no such configuration key %q\n", args[0])
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), map[string]string{args[0]: value})
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), value)
				return err
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change one configuration key",
		Long: "Change one key in config.yaml and apply it.\n\n" +
			"Lists and argv are whitespace-separated in one argument: " +
			`vincent config set notify.on "blocked awaiting_gate". ` +
			"environment.set takes NAME=VALUE pairs the same way. An argument " +
			"containing a space cannot be written this way and has to be edited " +
			"in the file.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			patch, err := configPatchFor(key, value)
			if err != nil {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
				return exitError{code: 1}
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				cfg, err := c.PatchConfig(ctx, patch)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), cfg)
				}
				now, _ := configValue(cfg, key)
				if key == "listen" {
					// The write happened; the running daemon did not move. Say
					// so rather than printing an address that is not bound.
					_, err = fmt.Fprintf(cmd.OutOrStdout(),
						"listen written to config.yaml; the running daemon keeps %s until it is restarted\n", now)
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", key, now)
				return err
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// renderConfig prints every key as `path = value`, in the order config.yaml
// carries them, which is the order the reference documents them in.
func renderConfig(w io.Writer, cfg apiclient.Config) error {
	for _, k := range configPaths() {
		v, _ := configValue(cfg, k)
		if _, err := fmt.Fprintf(w, "%s = %s\n", k, v); err != nil {
			return err
		}
	}
	return nil
}

// configFields is the one table this command reads and writes through: a
// dotted path, how to render it, and how to spell a change.
//
// It is deliberately not derived from apiclient.Config by reflection. A path
// like `agents.claude.path` is not a field name, the two list kinds render
// differently, and a table a reader can diff against the configuration
// reference is worth more here than a mechanism.
type configField struct {
	read  func(apiclient.Config) string
	write func(string) (apiclient.ConfigPatch, error)
}

func configFields() map[string]configField {
	str := func(read func(apiclient.Config) string, patch func(*string) apiclient.ConfigPatch) configField {
		return configField{
			read: read,
			write: func(s string) (apiclient.ConfigPatch, error) {
				v := strings.TrimSpace(s)
				return patch(&v), nil
			},
		}
	}
	return map[string]configField{
		"listen": str(func(c apiclient.Config) string { return c.Listen },
			func(v *string) apiclient.ConfigPatch { return apiclient.ConfigPatch{Listen: v} }),
		"max_parallel_tasks": intField(
			func(c apiclient.Config) int { return c.MaxParallelTasks },
			func(n *int) apiclient.ConfigPatch { return apiclient.ConfigPatch{MaxParallelTasks: n} }),
		"branch_template": {
			read: func(c apiclient.Config) string { return c.BranchTemplate },
			// Not trimmed: a template is written verbatim into a branch name,
			// and trimming one would be this command editing the value.
			write: func(s string) (apiclient.ConfigPatch, error) {
				return apiclient.ConfigPatch{BranchTemplate: &s}, nil
			},
		},
		"defaults.agent_timeout": str(func(c apiclient.Config) string { return c.Defaults.AgentTimeout },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Defaults: &apiclient.ConfigDefaultsPatch{AgentTimeout: v}}
			}),
		"defaults.command_timeout": str(func(c apiclient.Config) string { return c.Defaults.CommandTimeout },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Defaults: &apiclient.ConfigDefaultsPatch{CommandTimeout: v}}
			}),
		"defaults.input_timeout": str(func(c apiclient.Config) string { return c.Defaults.InputTimeout },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Defaults: &apiclient.ConfigDefaultsPatch{InputTimeout: v}}
			}),
		"delete_empty_branch_on_archive": boolField(
			func(c apiclient.Config) bool { return c.DeleteEmptyBranchOnArchive },
			func(b *bool) apiclient.ConfigPatch { return apiclient.ConfigPatch{DeleteEmptyBranchOnArchive: b} }),
		"delete_remote_branch_on_archive": boolField(
			func(c apiclient.Config) bool { return c.DeleteRemoteBranchOnArchive },
			func(b *bool) apiclient.ConfigPatch { return apiclient.ConfigPatch{DeleteRemoteBranchOnArchive: b} }),
		"fetch_base_branch": boolField(
			func(c apiclient.Config) bool { return c.FetchBaseBranch },
			func(b *bool) apiclient.ConfigPatch { return apiclient.ConfigPatch{FetchBaseBranch: b} }),
		"transcript_retention_days": intField(
			func(c apiclient.Config) int { return c.TranscriptRetentionDays },
			func(n *int) apiclient.ConfigPatch { return apiclient.ConfigPatch{TranscriptRetentionDays: n} }),
		"transcript_max_bytes": {
			read: func(c apiclient.Config) string { return byteSizeText(c.TranscriptMaxBytes) },
			write: func(s string) (apiclient.ConfigPatch, error) {
				n, err := parseByteSizeArg(s)
				if err != nil {
					return apiclient.ConfigPatch{}, err
				}
				return apiclient.ConfigPatch{TranscriptMaxBytes: &n}, nil
			},
		},
		"max_task_cost_usd": {
			read: func(c apiclient.Config) string { return floatText(c.MaxTaskCostUSD) },
			write: func(s string) (apiclient.ConfigPatch, error) {
				f, err := parseFloatArg(s)
				if err != nil {
					return apiclient.ConfigPatch{}, err
				}
				return apiclient.ConfigPatch{MaxTaskCostUSD: &f}, nil
			},
		},
		"usage_limit_recheck_interval": str(func(c apiclient.Config) string { return c.UsageLimitRecheck },
			func(v *string) apiclient.ConfigPatch { return apiclient.ConfigPatch{UsageLimitRecheck: v} }),
		"log_level": str(func(c apiclient.Config) string { return c.LogLevel },
			func(v *string) apiclient.ConfigPatch { return apiclient.ConfigPatch{LogLevel: v} }),
		"debug": boolField(func(c apiclient.Config) bool { return c.Debug },
			func(b *bool) apiclient.ConfigPatch { return apiclient.ConfigPatch{Debug: b} }),
		"environment.inherit": {
			read: func(c apiclient.Config) string { return c.Environment.Inherit.String() },
			write: func(s string) (apiclient.ConfigPatch, error) {
				v := parseInheritArg(s)
				return apiclient.ConfigPatch{Environment: &apiclient.ConfigEnvironmentPatch{Inherit: &v}}, nil
			},
		},
		"environment.unset": listField(
			func(c apiclient.Config) []string { return c.Environment.Unset },
			func(v *[]string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Environment: &apiclient.ConfigEnvironmentPatch{Unset: v}}
			}),
		"environment.set": {
			read: func(c apiclient.Config) string { return pairsText(c.Environment.Set) },
			write: func(s string) (apiclient.ConfigPatch, error) {
				m, err := parsePairsArg(s)
				if err != nil {
					return apiclient.ConfigPatch{}, err
				}
				return apiclient.ConfigPatch{Environment: &apiclient.ConfigEnvironmentPatch{Set: &m}}, nil
			},
		},
		"agents.claude.path": str(func(c apiclient.Config) string { return c.Agents.Claude.Path },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Agents: &apiclient.ConfigAgentsPatch{Claude: &apiclient.AgentPathPatch{Path: v}}}
			}),
		"agents.codex.path": str(func(c apiclient.Config) string { return c.Agents.Codex.Path },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Agents: &apiclient.ConfigAgentsPatch{Codex: &apiclient.AgentPathPatch{Path: v}}}
			}),
		"agents.cursor.path": str(func(c apiclient.Config) string { return c.Agents.Cursor.Path },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Agents: &apiclient.ConfigAgentsPatch{Cursor: &apiclient.AgentPathPatch{Path: v}}}
			}),
		"parallel.max_parallel": intField(
			func(c apiclient.Config) int { return c.Parallel.MaxParallel },
			func(n *int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Parallel: &apiclient.ConfigParallel{MaxParallel: *n}}
			}),
		"fan_out.max_depth": intField(
			func(c apiclient.Config) int { return c.FanOut.MaxDepth },
			func(n *int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{FanOut: &apiclient.ConfigFanOut{MaxDepth: *n}}
			}),
		"fan_out.max_tasks": intField(
			func(c apiclient.Config) int { return c.FanOut.MaxTasks },
			func(n *int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{FanOut: &apiclient.ConfigFanOut{MaxTasks: *n}}
			}),
		"loop.max_iterations": intField(
			func(c apiclient.Config) int { return c.Loop.MaxIterations },
			func(n *int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Loop: &apiclient.ConfigLoop{MaxIterations: *n}}
			}),
		"include.max_depth": intField(
			func(c apiclient.Config) int { return c.Include.MaxDepth },
			func(n *int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Include: &apiclient.ConfigInclude{MaxDepth: *n}}
			}),
		"mcp.wire_steps": boolField(func(c apiclient.Config) bool { return c.MCP.WireSteps },
			func(b *bool) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{MCP: &apiclient.ConfigMCPPatch{WireSteps: b}}
			}),
		"mcp.max_depth": intField(func(c apiclient.Config) int { return c.MCP.MaxDepth },
			func(n *int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{MCP: &apiclient.ConfigMCPPatch{MaxDepth: n}}
			}),
		"mcp.max_tasks": intField(func(c apiclient.Config) int { return c.MCP.MaxTasks },
			func(n *int) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{MCP: &apiclient.ConfigMCPPatch{MaxTasks: n}}
			}),
		"github.enabled": boolField(func(c apiclient.Config) bool { return c.GitHub.Enabled },
			func(b *bool) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{GitHub: &apiclient.ConfigGitHubPatch{Enabled: b}}
			}),
		"github.poll_interval": str(func(c apiclient.Config) string { return c.GitHub.PollInterval },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{GitHub: &apiclient.ConfigGitHubPatch{PollInterval: v}}
			}),
		"update.check": boolField(func(c apiclient.Config) bool { return c.Update.Check },
			func(b *bool) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Update: &apiclient.ConfigUpdatePatch{Check: b}}
			}),
		"update.poll_interval": str(func(c apiclient.Config) string { return c.Update.PollInterval },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Update: &apiclient.ConfigUpdatePatch{PollInterval: v}}
			}),
		"notify.on": listField(func(c apiclient.Config) []string { return c.Notify.On },
			func(v *[]string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Notify: &apiclient.ConfigNotifyPatch{On: v}}
			}),
		"notify.command": listField(func(c apiclient.Config) []string { return c.Notify.Command },
			func(v *[]string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Notify: &apiclient.ConfigNotifyPatch{Command: v}}
			}),
		"container.image": str(func(c apiclient.Config) string { return c.Container.Image },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Container: &apiclient.ConfigContainerPatch{Image: v}}
			}),
		"container.runtime": str(func(c apiclient.Config) string { return c.Container.Runtime },
			func(v *string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Container: &apiclient.ConfigContainerPatch{Runtime: v}}
			}),
		"container.mount_agent_config": boolField(
			func(c apiclient.Config) bool { return c.Container.MountAgentConfig },
			func(b *bool) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Container: &apiclient.ConfigContainerPatch{MountAgentConfig: b}}
			}),
		"container.network": boolField(
			func(c apiclient.Config) bool { return c.Container.Network },
			func(b *bool) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Container: &apiclient.ConfigContainerPatch{Network: b}}
			}),
		"container.extra_mounts": listField(func(c apiclient.Config) []string { return c.Container.ExtraMounts },
			func(v *[]string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{Container: &apiclient.ConfigContainerPatch{ExtraMounts: v}}
			}),
		"tui.board.group_by": listField(func(c apiclient.Config) []string { return c.TUI.Board.GroupBy },
			func(v *[]string) apiclient.ConfigPatch {
				return apiclient.ConfigPatch{TUI: &apiclient.ConfigTUIPatch{Board: &apiclient.ConfigBoardPatch{GroupBy: v}}}
			}),
	}
}

// configPaths is the key list, sorted so `vincent config get` prints the same
// order on every run — a map's is not an order.
func configPaths() []string {
	fields := configFields()
	out := make([]string, 0, len(fields))
	for k := range fields {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func configValue(cfg apiclient.Config, key string) (string, bool) {
	f, ok := configFields()[key]
	if !ok {
		return "", false
	}
	return f.read(cfg), true
}

func configPatchFor(key, value string) (apiclient.ConfigPatch, error) {
	f, ok := configFields()[key]
	if !ok {
		return apiclient.ConfigPatch{}, fmt.Errorf(
			"no such configuration key %q; `vincent config get` lists them", key)
	}
	return f.write(value)
}
