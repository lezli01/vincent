package api

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/container"
	"github.com/lezli01/vincent/internal/workflow"
)

// containerMismatch is the creation-time half of the container gate (§16,
// task 061 decision 3). It refuses only what is cheap and local, which is
// task 041's actual shape: 041's `restricted` verdict is judgeable with no
// binary installed at all, because it depends on adapter identity and GOOS.
// Verifying a container *image* needs a registry pull and an exec, and task
// 041 decision 4 re-affirms task 003 decision 4 — there is no pre-flight
// refusal on an unhealthy environment. So a missing or unpullable image is an
// admission block (`container_image_unavailable`), not a 400.
//
// Three things are refused here:
//
//   - a Windows daemon, because paths are identical inside and out (decision
//     2) and `C:\...` cannot exist in a Linux container;
//   - a missing or unusable runtime binary, which is local and costs one
//     `docker version`;
//   - `container.network: false` together with `mcp.wire_steps: true`
//     (decision 1), which is a contradiction: a container with no network
//     cannot reach the daemon's per-step MCP endpoint, and §9.1's rule is
//     that a missing MCP channel fails loudly rather than running a prompt
//     premised on tools that are not there.
//
// It also carries decision 8's second `shell:` refusal. A workflow that pins
// its own image is refused at load; every other case can only be judged here,
// where the config-level and workflow-level images resolve together.
//
// An empty string means the task may be created.
func (s *Server) containerMismatch(ctx context.Context, wf *workflow.Workflow) string {
	cfg := s.deps.Config()
	c := cfg.Container.Merge(wf.Defaults.Container)
	if !c.Enabled() {
		return ""
	}
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("workflow %q runs in container image %q, which this daemon cannot do on "+
			"windows: a containerized task mounts its worktree and repository at their own absolute "+
			"paths, and a windows path cannot exist inside a linux container", wf.Name, c.Image)
	}
	if !c.Network && cfg.MCP.WireSteps {
		return fmt.Sprintf("workflow %q asks for container.network: false while mcp.wire_steps is true: "+
			"a container with no network cannot reach the daemon's per-step MCP endpoint. "+
			"Set mcp.wire_steps: false, or leave container.network on", wf.Name)
	}
	if conflicts := workflow.ContainerShellConflicts(wf); len(conflicts) > 0 {
		return fmt.Sprintf("workflow %q runs in container image %q: %s",
			wf.Name, c.Image, conflicts[0].Message)
	}
	if msg := s.containerRuntimeMissing(ctx, c); msg != "" {
		return msg
	}
	return ""
}

// containerRuntimeMissing probes the configured runtime. It is the one part of
// this gate that touches the world, and it is bounded: `docker version`
// against a local socket, not a registry.
func (s *Server) containerRuntimeMissing(ctx context.Context, c config.Container) string {
	rt := s.containerRuntime(c.RuntimeBinary())
	if err := rt.Available(ctx); err != nil {
		return fmt.Sprintf("container runtime %q is not usable: %v. "+
			"A containerized task is never silently run on the host instead", c.RuntimeBinary(), err)
	}
	return ""
}

// containerRuntime resolves a runtime binary through the injectable factory,
// so the API's tests never need docker.
func (s *Server) containerRuntime(binary string) container.Runtime {
	if s.deps.Containers != nil {
		return s.deps.Containers(binary)
	}
	return container.New(binary)
}

// containerInfo is what `GET /v1/info` reports about container execution, the
// way it reports adapters. Presence, never a verdict on the image: the image
// is a per-workflow fact and probing every configured one on an info request
// is a registry pull behind a health endpoint.
func (s *Server) containerInfo(ctx context.Context) map[string]any {
	c := s.deps.Config().Container
	info := map[string]any{
		"enabled": c.Enabled(),
		"runtime": c.RuntimeBinary(),
		"image":   strings.TrimSpace(c.Image),
	}
	if runtime.GOOS == "windows" {
		info["available"] = false
		info["error"] = "containerized tasks are not supported on a windows daemon"
		return info
	}
	if err := s.containerRuntime(c.RuntimeBinary()).Available(ctx); err != nil {
		info["available"] = false
		info["error"] = err.Error()
		return info
	}
	info["available"] = true
	return info
}
