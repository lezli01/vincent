package builtin

import (
	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/copilot"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/config"
)

// New returns a registry of every shipped adapter: claude, codex, cursor,
// copilot — the order every caller lists and probes them in. Each adapter's
// path accessor asks path for its own name on every read, so an accessor over
// a hot-reloading configuration reaches future runs without a rebuild.
func New(path func(name string) string) *agent.Registry {
	return agent.NewRegistry(
		claude.New(func() string { return path("claude") }),
		codex.New(func() string { return path("codex") }),
		cursor.New(func() string { return path("cursor") }),
		copilot.New(func() string { return path("copilot") }),
	)
}

// ConfigPaths maps an adapter name to its configured binary path, reading cfg
// on every call. An unknown name has no configured path and yields "".
func ConfigPaths(cfg func() config.Config) func(name string) string {
	return func(name string) string {
		agents := cfg().Agents
		switch name {
		case "claude":
			return agents.Claude.Path
		case "codex":
			return agents.Codex.Path
		case "cursor":
			return agents.Cursor.Path
		case "copilot":
			return agents.Copilot.Path
		}
		return ""
	}
}
