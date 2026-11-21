package tui

// The Settings screen's catalogue (task 152): every key configKeys edits, sorted
// into the categories a person looks for it under and described in plain words.
// configKeys stays the one table of what a key is and how it is written; this
// says only where it lives on the screen. A test holds the two to each other, so
// a key added there cannot go missing here.

// settingsItem places one configuration key on the screen.
type settingsItem struct {
	path  string
	label string
	// desc is the line under the setting.
	desc string
	// presets are the durations ←/→ walk, for a key whose value is a duration.
	presets []string
}

// settingsSection is one titled box of settings.
type settingsSection struct {
	title, note string
	items       []settingsItem
}

// settingsCategory is one entry of the list on the left.
type settingsCategory struct {
	glyph, name string
	sections    []settingsSection
}

// The duration presets. "0s" is drawn as off.
var (
	syncPresets   = []string{"0s", "5m", "15m", "1h"}
	updatePresets = []string{"0s", "1h", "12h", "24h"}
	backupPresets = []string{"0s", "6h", "24h"}
	// archivePresets is task 155's clock: off, a working day, a day, a week.
	archivePresets = []string{"0s", "1h", "12h", "24h", "168h"}
)

// settingsCatalog is the whole screen, in display order.
var settingsCatalog = []settingsCategory{
	{"▶", "Work & limits", []settingsSection{
		{"▶ Running at once", "how much vincent does in parallel", []settingsItem{
			{path: "max_parallel_tasks", label: "Tasks at once", desc: "The most tasks running together, across every project"},
			{path: "max_parallel_chats", label: "Chats at once", desc: "Counted apart from tasks, so a chat never waits behind them"},
			{path: "parallel.max_parallel", label: "Lanes in a parallel step", desc: "Checks one parallel step runs side by side"},
		}},
		{"⑂ Workflow bounds", "these stop a runaway workflow", []settingsItem{
			{path: "fan_out.max_depth", label: "Fan-out depth", desc: "How many generations of lanes one task may create"},
			{path: "fan_out.max_tasks", label: "Fan-out tasks", desc: "How many tasks one root task may create in total"},
			{path: "loop.max_iterations", label: "Loop iterations", desc: "The most times one loop step may repeat"},
			{path: "include.max_depth", label: "Include depth", desc: "How many levels of include a workflow may nest"},
		}},
		{"◈ Tasks agents can create", "through vincent's MCP tools", []settingsItem{
			{path: "mcp.wire_steps", label: "Give agent steps the tools", desc: "Let an agent step call vincent's own tools"},
			{path: "mcp.max_depth", label: "Generations", desc: "How deep a chain of agent-created tasks may go"},
			{path: "mcp.max_tasks", label: "Tasks in one chain", desc: "How many tasks one chain of agents may create"},
		}},
	}},
	{"◷", "Timeouts", []settingsSection{
		{"◷ When a workflow sets no limit", "", []settingsItem{
			{path: "defaults.agent_timeout", label: "Agent step", desc: "The longest one agent step or chat turn may run", presets: []string{"30m", "1h", "2h", "4h"}},
			{path: "defaults.command_timeout", label: "Command step", desc: "The longest one command step may run", presets: []string{"5m", "15m", "30m", "1h"}},
			{path: "defaults.input_timeout", label: "Waiting for your answer", desc: "How long an agent's question stays open", presets: []string{"1h", "8h", "24h", "72h"}},
			{path: "defaults.background_wait", label: "Background work", desc: "How long a run stays open for work the agent left running", presets: []string{"0s", "15m", "30m", "1h"}},
		}},
	}},
	{"⎇", "Git & branches", []settingsSection{
		{"⎇ Branch names", "", []settingsItem{
			{path: "branch_template", label: "Branch template", desc: "How each new task's branch is named; empty uses the built-in name"},
		}},
		{"↓ Starting a task", "", []settingsItem{
			{path: "fetch_base_branch", label: "Fetch the base branch first", desc: "Start from the remote's latest commit; your checkout is not touched"},
		}},
		{"▣ Archiving a task", "", []settingsItem{
			{path: "delete_empty_branch_on_archive", label: "Delete empty branches", desc: "Remove a task's branch when it has no commits of its own"},
			{path: "delete_remote_branch_on_archive", label: "Delete on the remote too", desc: "Also remove its upstream branch; this cannot be undone"},
			{path: "archive_tasks_on_issue_close", label: "Archive when an issue closes", desc: "Tidy up a closed issue's finished tasks automatically"},
			{path: "archive_finished_tasks_after", label: "Archive finished tasks after", desc: "Tidy up a task left done or aborted this long; 0 never does", presets: archivePresets},
		}},
	}},
	{"$", "Cost & quota", []settingsSection{
		{"$ Spending limits", "only agents that report cost count", []settingsItem{
			{path: "max_task_cost_usd", label: "Most one task may spend", desc: "In US dollars; 0 means no limit"},
			{path: "max_tree_cost_usd", label: "Most one fan-out may spend", desc: "A root task and all its lanes, in US dollars; 0 means no limit"},
		}},
		{"⧖ When an agent hits its usage limit", "", []settingsItem{
			{path: "usage_limit_auto_continue", label: "Wait and continue", desc: "Whether a task waits for the limit to reset or stops for you"},
			{path: "usage_limit_recheck_interval", label: "Check again after", desc: "How long a task waits when its agent named no reset time", presets: []string{"5m", "15m", "30m", "1h"}},
		}},
	}},
	{"◈", "Agents & environment", []settingsSection{
		{"◈ Agent programs", "empty finds it on your PATH", []settingsItem{
			{path: "agents.claude.path", label: "claude", desc: "The program vincent starts for claude"},
			{path: "agents.codex.path", label: "codex", desc: "The program vincent starts for codex"},
			{path: "agents.cursor.path", label: "cursor", desc: "The program vincent starts for cursor"},
			{path: "agents.copilot.path", label: "copilot", desc: "The program vincent starts for copilot"},
		}},
		{"≡ What agents inherit", "from the daemon's environment", []settingsItem{
			{path: "environment.inherit", label: "Inherit", desc: "all, none, or the names of the variables to pass on"},
			{path: "environment.unset", label: "Remove", desc: "Variables taken away after that"},
			{path: "environment.set", label: "Set", desc: "NAME=VALUE pairs added last"},
		}},
	}},
	{"⇅", "Integrations", []settingsSection{
		{"⇅ GitHub", "", []settingsItem{
			{path: "github.enabled", label: "Use GitHub", desc: "Let the daemon talk to GitHub at all"},
			{path: "github.poll_interval", label: "GitHub sync", desc: "How often issues import and pull requests link", presets: syncPresets},
		}},
		{"▦ Jira", "", []settingsItem{
			{path: "jira.enabled", label: "Use Jira", desc: "Let the daemon sync projects bound to Jira"},
			{path: "jira.poll_interval", label: "Jira sync", desc: "How often bound projects refresh their issues", presets: syncPresets},
			{path: "jira.user_env", label: "E-mail variable", desc: "The name of the variable holding your Jira e-mail, never the value"},
			{path: "jira.token_env", label: "Token variable", desc: "The name of the variable holding your Jira token, never the value"},
		}},
		{"↑ Updates", "", []settingsItem{
			{path: "update.check", label: "Check for updates", desc: "Ask GitHub whether a newer vincent exists; nothing is installed"},
			{path: "update.poll_interval", label: "Update check", desc: "How often vincent looks for a new release", presets: updatePresets},
		}},
	}},
	{"!", "Notifications", []settingsSection{
		{"! Tell me when", "", []settingsItem{
			{path: "notify.on", label: "Task states", desc: "The states that run the command below"},
			{path: "notify.chat_on", label: "Chat moments", desc: "A chat's question, or its turn finishing or failing, runs the command below"},
			{path: "notify.command", label: "Command", desc: "The program vincent runs, as you, for each of them"},
		}},
		{"⚡ Triggers", "", []settingsItem{
			{path: "triggers.enabled", label: "Run triggers", desc: "Let trigger files poll and start work on their own"},
		}},
	}},
	{"▣", "Storage & backups", []settingsSection{
		{"≡ Transcripts", "", []settingsItem{
			{path: "transcript_retention_days", label: "Keep for", desc: "Days an archived task's transcripts are kept"},
			{path: "transcript_max_bytes", label: "Largest transcript", desc: "A step that writes more than this fails, e.g. 512MB"},
		}},
		{"▣ Backups", "", []settingsItem{
			{path: "backup.interval", label: "Backup", desc: "How often the daemon takes a scheduled backup", presets: backupPresets},
			{path: "backup.keep", label: "Backups to keep", desc: "Older scheduled backups are removed; 0 keeps them all"},
			{path: "backup.dir", label: "Backup folder", desc: "Empty keeps them beside the database, on the same disk"},
		}},
	}},
	{"⬡", "Containers", []settingsSection{
		{"⬡ Run steps in a container", "", []settingsItem{
			{path: "container.image", label: "Image", desc: "Empty runs every step on this machine"},
			{path: "container.runtime", label: "Runtime", desc: "The docker-compatible program to use"},
			{path: "container.mount_agent_config", label: "Share agent settings", desc: "Give the container the agent CLI's own configuration"},
			{path: "container.network", label: "Network access", desc: "Leave the container on the network"},
			{path: "container.extra_mounts", label: "Extra folders", desc: "host:container[:ro] mounts, separated by spaces"},
		}},
	}},
	{"▦", "Interface", []settingsSection{
		{"▦ Screens", "", []settingsItem{
			{path: "tui.tabs", label: "Tabs", desc: "The screens tab cycles through; the first is where vincent opens"},
			{path: "tui.default_project", label: "Default project", desc: "The project to open on when nothing else picks one"},
			{path: "tui.board.group_by", label: "Group tasks by", desc: "issue, workflow, both, or empty for one flat list"},
			{path: "tui.output.level", label: "Output detail", desc: "How much of a run the output pane shows when it opens"},
		}},
		{"◆ Appearance", "", []settingsItem{
			{path: "tui.icons", label: "Icons", desc: "nerd needs a Nerd Font in your terminal"},
			{path: "tui.hyperlinks", label: "Clickable links", desc: "Only if your terminal supports them"},
		}},
		{"◌ Chats", "", []settingsItem{
			{path: "tui.chat.status_line", label: "Status line", desc: "A template for the chat header, such as {{.Model}} · {{.ContextPercent}}%"},
			{path: "tui.chat.vim", label: "Vim keys", desc: "Insert and normal modes in the composer; enter still sends"},
		}},
		{"⌨ Keys", "", []settingsItem{
			{path: "tui.keys", label: "Key bindings", desc: "operation=key pairs, such as refresh=f5"},
		}},
	}},
	{"⚙", "Daemon", []settingsSection{
		{"⚙ The background process", "", []settingsItem{
			{path: "listen", label: "Address", desc: "Where the API listens; loopback only, port 0 picks a free one"},
			{path: "log_level", label: "Log detail", desc: "How much the daemon writes to its log"},
			{path: "debug", label: "Record how steps were run", desc: "Adds each step's command and prompt to its transcript"},
		}},
	}},
}
