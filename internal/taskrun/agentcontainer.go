package taskrun

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/container"
)

// containerResolveTimeout bounds resolving an agent CLI on the image's PATH.
// It is a `docker exec` of `command -v`, so the bound is mostly the runtime's
// own exec latency, which is well under a second on a warm daemon.
const containerResolveTimeout = 30 * time.Second

// containerSignalTimeout bounds one in-container signal. Each gets a context of
// its own: a stop arrives after the run context is already canceled, and a
// signal that inherited it would be dead on arrival.
const containerSignalTimeout = 15 * time.Second

// agentLauncher is where an agent step's process runs (task 062). It is the
// one place the engine chooses, so the three adapters never learn whether they
// are contained: a task with no active container gets HostLauncher exactly as
// before task 062.2, and a containerized one gets a launcher that resolves,
// probes and runs the CLI inside the task's container.
func agentLauncher(tc taskContainer, runID int64, log *slog.Logger) agent.Launcher {
	if !tc.active() {
		return agent.HostLauncher{}
	}
	return &containerLauncher{tc: tc, key: execKey(runID), user: container.HostUser(), log: log}
}

// inputVerdict is the §7.4 `require` pre-flight's answer for this run. A host
// run reads the catalog, as it always has. A containerized run is judged
// against the image's CLI when the adapter can say (decision 2): the host
// catalog describes a binary that is not the one about to run. An adapter that
// cannot probe through a launcher keeps the catalog's verdict, whose only
// positive "cannot" for such an adapter is the static one — true in any image.
func (r *Runner) inputVerdict(
	ctx context.Context, adapter agent.Adapter, l agent.Launcher, tc taskContainer, name string,
) agent.InputVerdict {
	if tc.active() {
		if p, ok := adapter.(agent.InputProber); ok {
			return p.InputVerdictWith(ctx, l)
		}
	}
	return r.deps.Catalog.InputVerdict(ctx, name)
}

// mcpRouteFor says where a step's process reaches the §13.4 per-step endpoint
// from. A host step's route is the zero value. A containerized step's carries
// the container network's gateway, looked up only if wiring is on (decision
// 1).
func mcpRouteFor(tc taskContainer, log *slog.Logger) MCPRoute {
	if !tc.active() {
		return MCPRoute{}
	}
	return MCPRoute{
		Container: true,
		Gateway: func() string {
			ctx, cancel := context.WithTimeout(context.Background(), containerResolveTimeout)
			defer cancel()
			gw, err := tc.rt.Gateway(ctx, tc.id)
			if err != nil && log != nil {
				log.Warn("container gateway lookup", "container", tc.id, "error", err)
			}
			return gw
		},
	}
}

// containerLauncher runs an agent Command inside its task's container (task
// 062.2). The host process is the runtime client, spawned through
// HostLauncher, so the transcript, §17 parsing and exit paths are the host's
// own code reading the same bytes (061 decision 10: `-i`, never `-t`).
type containerLauncher struct {
	tc   taskContainer
	key  string
	user string
	log  *slog.Logger
}

// probeEnv is the environment a resolve or probe exec runs with: only the
// vincent home, when mounts put the agent configuration beneath it, so a CLI
// that reads its config to answer `--version` reads the mounted one.
func (l *containerLauncher) probeEnv() []string {
	if !l.tc.settings.MountAgentConfig {
		return nil
	}
	return []string{"HOME=" + container.HomeDir}
}

// Resolve implements agent.Launcher against the image's PATH (decision 2).
// The configured `agents.*.path` is a host path and means nothing in the image,
// so it is ignored. A CLI the image lacks fails the step's Start, which the
// engine reports as agent_unavailable — the host's own missing-CLI outcome.
func (l *containerLauncher) Resolve(_, _, binary string) (string, error) {
	argv := l.tc.rt.ExecDirect(l.tc.id, container.ExecSpec{
		// `$1` rather than interpolation: the name is passed as argv, never
		// re-parsed as shell text.
		Argv: []string{"/bin/sh", "-c", `command -v "$1"`, "vincent", binary},
		Env:  l.probeEnv(),
		User: l.user,
	})
	out, _, err := agent.Probe(context.Background(), containerResolveTimeout, argv[0], argv[1:]...)
	path := strings.TrimSpace(firstLineOf(string(out)))
	if err != nil || path == "" {
		if err == nil {
			err = fmt.Errorf("no output")
		}
		return "", fmt.Errorf("%s not found on the PATH of container image %s: %w",
			binary, l.tc.settings.Image, err)
	}
	return path, nil
}

// Probe implements agent.Launcher: the probe runs inside the container,
// unwrapped, so what it reports is the image's CLI (decision 2).
func (l *containerLauncher) Probe(
	ctx context.Context, timeout time.Duration, path string, args ...string,
) (stdout, stderr []byte, err error) {
	argv := l.tc.rt.ExecDirect(l.tc.id, container.ExecSpec{
		Argv: append([]string{path}, args...),
		Env:  l.probeEnv(),
		User: l.user,
	})
	return agent.Probe(ctx, timeout, argv[0], argv[1:]...)
}

// Launch implements agent.Launcher. Every step variable goes onto the host
// argv by name only, its value in the runtime client's own environment
// (container.SplitEnv): codex's MCP token rides the environment precisely so
// it is not on an argv (task 057 decision 8), and a `docker exec --env K=V`
// would put it back on one.
func (l *containerLauncher) Launch(cmd agent.Command) (agent.Process, error) {
	flags, values := container.SplitEnv(cmd.Env)
	argv := l.tc.rt.Exec(l.tc.id, container.ExecSpec{
		Key:     l.key,
		Argv:    append([]string{cmd.Path}, cmd.Args...),
		Env:     flags,
		WorkDir: cmd.Dir,
		User:    l.user,
	})
	// The client needs the daemon's environment to find its own daemon; the
	// step's values layer over it, and the resolution variables SplitEnv kept
	// literal never reach this list.
	clientEnv := append(os.Environ(), values...)
	p, err := agent.HostLauncher{}.Launch(agent.Command{
		Path:      argv[0],
		Args:      argv[1:],
		Env:       clientEnv,
		Stdin:     cmd.Stdin,
		StdinPipe: cmd.StdinPipe,
		Stderr:    cmd.Stderr,
	})
	if err != nil {
		return nil, err
	}
	return &containerProcess{Process: p, tc: l.tc, key: l.key, log: l.log}, nil
}

// containerProcess is a containerized agent run. Its host half is the runtime
// client; its stop primitives reach the process inside through 061 decision
// 9's pid file, because killing only the client leaves the agent running in a
// container that survives the step.
type containerProcess struct {
	agent.Process
	tc  taskContainer
	key string
	log *slog.Logger

	signaled atomic.Bool
	killOnce sync.Once
}

// Terminate implements agent.Process: TERM to the pid file's process group.
func (p *containerProcess) Terminate() error {
	p.signaled.Store(true)
	return p.signal("TERM")
}

// Kill implements agent.Process: KILL inside the container, then the host
// client. The in-container signal is sent once; the client kill is itself
// idempotent.
func (p *containerProcess) Kill() error {
	p.signaled.Store(true)
	p.killOnce.Do(func() {
		if err := p.signal("KILL"); err != nil && p.log != nil {
			p.log.Warn("signal agent in container", "container", p.tc.id, "signal", "KILL", "error", err)
		}
	})
	return p.Process.Kill()
}

func (p *containerProcess) signal(sig string) error {
	ctx, cancel := context.WithTimeout(context.Background(), containerSignalTimeout)
	defer cancel()
	return p.tc.rt.Signal(ctx, p.tc.id, p.key, sig)
}

// Wait implements agent.Process. A signaled exit that `docker exec` reports as
// 128+n is the host's -1 when vincent sent the signal, so step_runs.exit_code
// and classifyAgent see what a host run would have produced. An exit vincent
// did not cause keeps its code: that is the process's own answer.
func (p *containerProcess) Wait() (int, error) {
	code, err := p.Process.Wait()
	return containerExitCode(code, p.signaled.Load()), err
}

func containerExitCode(code int, signaled bool) int {
	if signaled && code > 128 && code <= 128+64 {
		return -1
	}
	return code
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
