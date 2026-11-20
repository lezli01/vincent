package api

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/copilot"
	"github.com/lezli01/vincent/internal/agent/cursor"
)

// TestAgentsReportCompactCommand pins task 157 decision 4's capability on
// GET /v1/agents: `/compact` for claude, "" for every other shipped adapter,
// and null — "nobody can say" — with no registry to ask.
func TestAgentsReportCompactCommand(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	newReg := func() *agent.Registry {
		return agent.NewRegistry(
			claude.New(func() string { return fake }),
			codex.New(func() string { return "/nonexistent/codex-not-here" }),
			cursor.New(func() string { return "/nonexistent/cursor-agent-not-here" }),
			copilot.New(func() string { return "/nonexistent/copilot-not-here" }),
		)
	}
	want := map[string]string{"claude": "/compact", "codex": "", "cursor": "", "copilot": ""}
	for _, a := range servedAgents(t, newReg(), true) {
		w, ok := want[a.Name]
		if !ok {
			continue
		}
		if a.CompactCommand == nil || *a.CompactCommand != w {
			t.Errorf("%s compact_command = %v, want %q", a.Name, a.CompactCommand, w)
		}
	}
	for _, a := range servedAgentsFrom(t, newReg(), nil) {
		if a.CompactCommand != nil {
			t.Errorf("%s compact_command = %q with no registry, want null", a.Name, *a.CompactCommand)
		}
	}
}
