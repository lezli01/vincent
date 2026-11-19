package claude

import "github.com/lezli01/vincent/internal/agent"

var _ agent.Compactor = (*Adapter)(nil)

// CompactCommand implements agent.Compactor (task 157 decision 4, §9.2):
// claude compacts its session's context when `/compact` arrives as a message.
func (a *Adapter) CompactCommand() string { return "/compact" }
