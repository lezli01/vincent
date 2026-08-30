package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/container"
	"github.com/lezli01/vincent/internal/workflow"
)

// gateRuntime stands in for docker. No `go test` in this repository may need a
// container daemon (the testing conventions), so the creation gate's one call
// into the world is faked and the real argv is pinned in internal/container.
type gateRuntime struct{ err error }

func (g gateRuntime) Name() string                              { return "fake" }
func (g gateRuntime) Available(context.Context) error           { return g.err }
func (g gateRuntime) EnsureImage(context.Context, string) error { return nil }

func (g gateRuntime) Create(context.Context, container.CreateSpec) (string, error) {
	return "cid", nil
}
func (g gateRuntime) Exec(string, container.ExecSpec) []string             { return nil }
func (g gateRuntime) Signal(context.Context, string, string, string) error { return nil }
func (g gateRuntime) Remove(context.Context, string) error                 { return nil }
func (g gateRuntime) Lookup(context.Context, string) (string, error)       { return "", nil }
func (g gateRuntime) TaskLabel(context.Context, string) (string, error)    { return "", nil }

// gateServer is a Server with nothing wired but what containerMismatch reads:
// the config and the runtime factory. The gate is deliberately cheap and local
// (task 061 decision 3), which is exactly what makes it testable this way.
func gateServer(t *testing.T, cfg config.Config, rt container.Runtime) *Server {
	t.Helper()
	return New(Deps{
		Token:       testToken,
		Config:      func() config.Config { return cfg },
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Containers:  func(string) container.Runtime { return rt },
	})
}

// testImage is any image name at all: nothing in the creation gate reads it
// beyond "is it empty", which is decision 3's whole point — the image's
// *contents* are an admission-time question.
const testImage = "ghcr.io/example/dev:latest"

func containerConfig() config.Config {
	cfg := config.Default()
	cfg.Container = config.Container{
		Image: testImage, Runtime: "docker", MountAgentConfig: true, Network: true,
	}
	return cfg
}

func parseWorkflow(t *testing.T, src string) *workflow.Workflow {
	t.Helper()
	wf, errs, err := workflow.Parse([]byte(src), workflow.Options{KnownAgents: []string{"claude"}})
	if err != nil || len(errs) > 0 {
		t.Fatalf("parse workflow: %v %v", err, errs)
	}
	return wf
}

const gateWorkflowYAML = `name: build
description: Build it.
steps:
  - {id: compile, type: command, run: "go build ./..."}
`

// TestContainerGatePassesWhenTheRuntimeAnswers is the baseline the three
// refusals below are measured against: an image, a usable runtime, a network,
// and the task may be created.
func TestContainerGatePassesWhenTheRuntimeAnswers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a windows daemon refuses every containerized task (decision 2)")
	}
	s := gateServer(t, containerConfig(), gateRuntime{})
	if msg := s.containerMismatch(t.Context(), parseWorkflow(t, gateWorkflowYAML)); msg != "" {
		t.Fatalf("refused a runnable containerized task: %s", msg)
	}
}

// TestContainerGateIgnoresAnUncontainerizedTask is the regression that matters
// most: `container.image: ""` is the default, and an installation that never
// sets one must not learn that docker exists. The runtime here always fails,
// and the gate must never ask it.
func TestContainerGateIgnoresAnUncontainerizedTask(t *testing.T) {
	s := gateServer(t, config.Default(), gateRuntime{err: errors.New("docker: not found")})
	if msg := s.containerMismatch(t.Context(), parseWorkflow(t, gateWorkflowYAML)); msg != "" {
		t.Fatalf("a host task was refused over the container runtime: %s", msg)
	}
}

// TestContainerGateRefusesAnUnusableRuntime: the cheap, local half of decision
// 3. A containerized task is never silently run on the host instead.
func TestContainerGateRefusesAnUnusableRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the windows refusal fires first (decision 2)")
	}
	s := gateServer(t, containerConfig(),
		gateRuntime{err: errors.New("docker: command not found")})
	msg := s.containerMismatch(t.Context(), parseWorkflow(t, gateWorkflowYAML))
	if !strings.Contains(msg, "docker") || !strings.Contains(msg, "not usable") {
		t.Fatalf("refusal does not name the runtime: %q", msg)
	}
}

// TestContainerGateRefusesNoNetworkWithWiredMCP pins decision 1's
// contradiction. A container with no network cannot reach the daemon's
// per-step MCP endpoint, and §9.1's rule is that a missing MCP channel fails
// loudly rather than running a prompt premised on tools that are not there.
func TestContainerGateRefusesNoNetworkWithWiredMCP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the windows refusal fires first (decision 2)")
	}
	cfg := containerConfig()
	cfg.Container.Network = false
	cfg.MCP.WireSteps = true
	s := gateServer(t, cfg, gateRuntime{})
	msg := s.containerMismatch(t.Context(), parseWorkflow(t, gateWorkflowYAML))
	if !strings.Contains(msg, "container.network") || !strings.Contains(msg, "mcp.wire_steps") {
		t.Fatalf("refusal does not name both sides of the contradiction: %q", msg)
	}

	// Turning the other knob off resolves it, rather than the pair being
	// permanently forbidden: a no-network container is a legitimate ask.
	cfg.MCP.WireSteps = false
	if msg := gateServer(t, cfg, gateRuntime{}).containerMismatch(
		t.Context(), parseWorkflow(t, gateWorkflowYAML)); msg != "" {
		t.Fatalf("a no-network task with mcp.wire_steps off was still refused: %s", msg)
	}
}

// TestContainerGateRefusesPwshAndNamesTheStep is decision 8's second half.
// The workflow pins no image of its own, so load-time validation could not
// have judged it — this is the first moment the config-level and
// workflow-level images resolve together, and the message must name the step
// the human has to change.
func TestContainerGateRefusesPwshAndNamesTheStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the windows refusal fires first (decision 2)")
	}
	const src = `name: build
description: Build it.
steps:
  - {id: compile, type: command, run: "go build ./..."}
  - {id: sign, type: command, shell: pwsh, run: "Write-Host hi"}
`
	s := gateServer(t, containerConfig(), gateRuntime{})
	msg := s.containerMismatch(t.Context(), parseWorkflow(t, src))
	if !strings.Contains(msg, `"sign"`) || !strings.Contains(msg, "pwsh") {
		t.Fatalf("refusal does not name the offending step: %q", msg)
	}

	// The same workflow off the container path keeps working: §8.3 still
	// resolves pwsh on a host that has it, and this gate does not touch it.
	if msg := gateServer(t, config.Default(), gateRuntime{}).containerMismatch(
		t.Context(), parseWorkflow(t, src)); msg != "" {
		t.Fatalf("a host task with a pwsh step was refused: %s", msg)
	}
}

// TestContainerInfoReportsPresenceNotAVerdict pins what `GET /v1/info` says.
// It is adapter-shaped: whether the runtime answered, never whether the image
// exists — probing every configured image behind a health endpoint is a
// registry pull.
func TestContainerInfoReportsPresenceNotAVerdict(t *testing.T) {
	s := gateServer(t, containerConfig(), gateRuntime{})
	info := s.containerInfo(t.Context())
	if info["enabled"] != true || info["runtime"] != "docker" {
		t.Fatalf("info = %+v", info)
	}
	if _, ok := info["image_available"]; ok {
		t.Errorf("info claims a verdict on the image: %+v", info)
	}
	want := runtime.GOOS != "windows"
	if info["available"] != want {
		t.Errorf("available = %v, want %v (%+v)", info["available"], want, info)
	}
}
